package memory

// purge.go：删除联动（步骤 2.6、R23）。项目删除后按 memory_documents 逐个删除该项目的 document，并删除项目的
// mental model；失败的进入 memory_tombstones，由后台循环按退避重试，成功后移除。记忆未配置或 Hindsight 不可用都
// 不阻塞项目删除：失败一律落进重试队列。

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	// mentalModelTombstonePrefix 标记重试队列里的 mental model 删除：document_id 列写 `mental-model:<模型 id>`。
	mentalModelTombstonePrefix = "mental-model:"
	// purgeTimeout 是一次项目级联删除访问 Hindsight 的总时限；超时剩下的进入重试队列。
	purgeTimeout = 30 * time.Second
	// tombstoneInterval 是重试循环的间隔。
	tombstoneInterval = time.Minute
	// tombstoneBatch 是每轮最多重试的条目数。
	tombstoneBatch = 100
	// 退避：30 秒起，每次翻倍，最多 6 小时。
	tombstoneBackoffBase = 30 * time.Second
	tombstoneBackoffMax  = 6 * time.Hour
)

// tombstoneBackoff 是第 attempts 次失败后到下次重试的间隔。
func tombstoneBackoff(attempts int64) time.Duration {
	d := tombstoneBackoffBase
	for i := int64(0); i < attempts && d < tombstoneBackoffMax; i++ {
		d *= 2
	}
	return min(d, tombstoneBackoffMax)
}

// tombstoneTarget 是一个待删除的 document（或 mental model 标记）及其项目。
type tombstoneTarget struct {
	id        string
	projectID *string
}

// tombstone 把删除失败的条目写进重试队列（已在队列里的保持原样）。用不受调用方超时影响的 context 落库。
func (s *Service) tombstone(ctx context.Context, t tombstoneTarget, cause error) error {
	msg := truncate(cause.Error(), 500)
	now := s.cfg.Now()
	return s.cfg.Q.InsertMemoryTombstone(context.WithoutCancel(ctx), db.InsertMemoryTombstoneParams{
		DocumentID: t.id, ProjectID: t.projectID, NextAttemptAt: now.Add(tombstoneBackoff(0)).Unix(),
		LastError: &msg, CreatedAt: now.Unix(),
	})
}

// deleteDocuments 逐个删除 document：成功的删掉 memory_documents 行；失败的（含未配置、Hindsight 不可用）写进重试队列后
// 也删掉行，由墓碑接管。删除之前先把受影响范围的 mental model 标为待刷新，标记失败时一个都不删（全部进重试队列，
// 重试时再标记）。refresh 为真时，有删除成功就立即对标记的模型提交全量刷新。返回删除数与进入队列数。
func (s *Service) deleteDocuments(ctx context.Context, docs []tombstoneTarget, refresh bool) (int, int, error) {
	if len(docs) == 0 {
		return 0, 0, nil
	}
	c, cerr := s.admin(ctx)
	hctx, cancel := context.WithTimeout(ctx, purgeTimeout)
	defer cancel()
	var stale []string
	if cerr == nil {
		scopes := forgetScopes{}
		for _, d := range docs {
			scopes.add(d.projectID)
		}
		stale, cerr = s.markStale(hctx, c, scopes)
	}
	deleted, pending := 0, 0
	for _, d := range docs {
		err := cerr
		if err == nil {
			err = c.deleteDocument(hctx, d.id)
		}
		if err != nil {
			if err := s.tombstone(ctx, d, err); err != nil {
				return deleted, pending, err
			}
			pending++
		} else {
			deleted++
		}
		if _, err := s.cfg.Q.DeleteMemoryDocument(context.WithoutCancel(ctx), d.id); err != nil {
			return deleted, pending, err
		}
	}
	if refresh && deleted > 0 {
		s.refreshStale(hctx, c, stale)
	}
	return deleted, pending, nil
}

// PurgeProject 在项目删除成功后清掉它在 Hindsight 里的全部记忆（R23）：memory_documents 里记到该项目的 document，
// 以及带 `project:<P>` 标签的 mental model（列不出来时按 Core 的两个固定 id 删除）。
// 失败的进入重试队列；返回的错误只来自本地数据库，调用方记日志即可，不影响项目删除。
func (s *Service) PurgeProject(ctx context.Context, projectID string) error {
	ids, err := s.cfg.Q.ListAllMemoryDocumentIDsByProject(ctx, &projectID)
	if err != nil {
		return err
	}
	c, cerr := s.admin(ctx)
	if errors.Is(cerr, ErrNotConfigured) && len(ids) == 0 {
		// 从未配置过记忆（或已清空设置且没有记录）：没有可清的东西
		return nil
	}
	pid := projectID
	docs := make([]tombstoneTarget, 0, len(ids))
	for _, id := range ids {
		docs = append(docs, tombstoneTarget{id: id, projectID: &pid})
	}
	// 项目的 mental model 随后整个删除，不必提交刷新；删除前它们已被标记，网关不会再给出旧内容
	if _, _, err := s.deleteDocuments(ctx, docs, false); err != nil {
		return err
	}
	// mental model：先按标签列出，再并上 Core 的固定 id（列表失败或模型尚未被列出时兜底）
	models := ProjectModelIDs(projectID)
	hctx, cancel := context.WithTimeout(ctx, purgeTimeout)
	defer cancel()
	if cerr == nil {
		if listed, err := c.listMentalModels(hctx, ProjectTag(projectID), "metadata"); err == nil {
			for _, m := range listed {
				if m.ID != UserPrefsModelID && !containsStr(models, m.ID) {
					models = append(models, m.ID)
				}
			}
		}
	}
	for _, id := range models {
		err := cerr
		if err == nil {
			err = c.deleteMentalModel(hctx, id)
		}
		if err != nil {
			if err := s.tombstone(ctx, tombstoneTarget{id: mentalModelTombstonePrefix + id, projectID: &pid}, err); err != nil {
				return err
			}
			continue
		}
		// 模型已删除，不再需要等待刷新
		if err := s.cfg.Q.DeleteMemoryStaleModel(context.WithoutCancel(ctx), id); err != nil {
			return err
		}
	}
	return nil
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// deleteOwnedModel 删除重试队列里记下的项目 mental model：只删标签含 `project:<P>` 且不含 `oryn:global` 的模型，
// 不属于该项目（或已不存在）时视为完成。document id 恰好以前缀开头的情况也一并按 document 删除。
func (c adminClient) deleteOwnedModel(ctx context.Context, t tombstoneTarget) error {
	if err := c.deleteDocument(ctx, t.id); err != nil {
		return err
	}
	if t.projectID == nil {
		return nil
	}
	id := strings.TrimPrefix(t.id, mentalModelTombstonePrefix)
	var m hsMentalModel
	err := c.call(ctx, http.MethodGet, mmPath(id), nil, &m)
	if statusOf(err) == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if !hasTag(m.Tags, ProjectTag(*t.projectID)) || hasTag(m.Tags, GlobalTag) {
		return nil
	}
	return c.deleteMentalModel(ctx, id)
}

// RetryTombstones 重试到期的删除：成功的移出队列，失败的按退避推迟。document 删除之前同样先把受影响范围的
// mental model 标为待刷新（标记失败时本轮不删），删除成功后提交全量刷新。未配置时不重试、不计次数。返回本轮成功的条数。
func (s *Service) RetryTombstones(ctx context.Context) (int, error) {
	c, err := s.admin(ctx)
	if errors.Is(err, ErrNotConfigured) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	now := s.cfg.Now()
	due, err := s.cfg.Q.ListDueMemoryTombstones(ctx, db.ListDueMemoryTombstonesParams{Now: now.Unix(), Lim: tombstoneBatch})
	if err != nil {
		return 0, err
	}
	if len(due) == 0 {
		return 0, nil
	}
	hctx, cancel := context.WithTimeout(ctx, purgeTimeout)
	defer cancel()
	scopes := forgetScopes{}
	for _, t := range due {
		if !strings.HasPrefix(t.DocumentID, mentalModelTombstonePrefix) {
			scopes.add(t.ProjectID)
		}
	}
	var stale []string
	var markErr error
	if len(scopes) > 0 {
		stale, markErr = s.markStale(hctx, c, scopes)
	}
	done, docsDeleted := 0, 0
	for _, t := range due {
		target := tombstoneTarget{id: t.DocumentID, projectID: t.ProjectID}
		isModel := strings.HasPrefix(t.DocumentID, mentalModelTombstonePrefix)
		var derr error
		switch {
		case isModel:
			derr = c.deleteOwnedModel(hctx, target)
		case markErr != nil:
			derr = markErr
		default:
			derr = c.deleteDocument(hctx, t.DocumentID)
		}
		if derr != nil {
			msg := truncate(derr.Error(), 500)
			if err := s.cfg.Q.RetryMemoryTombstone(ctx, db.RetryMemoryTombstoneParams{
				NextAttemptAt: now.Add(tombstoneBackoff(t.Attempts + 1)).Unix(), LastError: &msg, DocumentID: t.DocumentID,
			}); err != nil {
				return done, err
			}
			continue
		}
		if err := s.cfg.Q.DeleteMemoryTombstone(ctx, t.DocumentID); err != nil {
			return done, err
		}
		if _, err := s.cfg.Q.DeleteMemoryDocument(ctx, t.DocumentID); err != nil {
			return done, err
		}
		done++
		if !isModel {
			docsDeleted++
		}
	}
	if docsDeleted > 0 {
		s.refreshStale(hctx, c, stale)
	}
	return done, nil
}

// RunTombstones 是后台循环：每分钟重试到期的删除、检查遗忘后待刷新的 mental model，并在记忆启用时保证
// user-preferences 存在。ctx 取消时返回。
func (s *Service) RunTombstones(ctx context.Context) {
	t := time.NewTicker(tombstoneInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := s.RetryTombstones(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("重试删除 Hindsight 记忆失败", "err", err)
		}
		if _, err := s.RetryStaleModels(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("检查待刷新的 mental model 失败", "err", err)
		}
		if err := s.EnsureUserPrefs(ctx); err != nil && ctx.Err() == nil {
			// Hindsight 停机时每分钟都会失败，只记调试日志；状态页会显示不可达
			slog.Debug("创建 user-preferences 失败", "err", err)
		}
	}
}
