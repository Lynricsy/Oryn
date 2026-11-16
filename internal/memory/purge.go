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
// 也删掉行，由墓碑接管。返回删除数、进入队列数，以及删除成功的 document 所在范围的标签（供刷新 mental model）。
func (s *Service) deleteDocuments(ctx context.Context, docs []tombstoneTarget) (int, int, map[string]bool, error) {
	c, cerr := s.admin(ctx)
	hctx, cancel := context.WithTimeout(ctx, purgeTimeout)
	defer cancel()
	deleted, pending := 0, 0
	tags := map[string]bool{}
	for _, d := range docs {
		err := cerr
		if err == nil {
			err = c.deleteDocument(hctx, d.id)
		}
		if err != nil {
			if err := s.tombstone(ctx, d, err); err != nil {
				return deleted, pending, tags, err
			}
			pending++
		} else {
			deleted++
			tags[scopeTag(d.projectID)] = true
		}
		if _, err := s.cfg.Q.DeleteMemoryDocument(context.WithoutCancel(ctx), d.id); err != nil {
			return deleted, pending, tags, err
		}
	}
	return deleted, pending, tags, nil
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
	if _, _, _, err := s.deleteDocuments(ctx, docs); err != nil {
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

// RetryTombstones 重试到期的删除：成功的移出队列，并刷新仍存在的范围（全局或未删除的项目）里的 mental model；
// 失败的按退避推迟。未配置时不重试、不计次数。返回本轮成功的条数。
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
	done := 0
	tags := map[string]bool{}
	for _, t := range due {
		target := tombstoneTarget{id: t.DocumentID, projectID: t.ProjectID}
		var derr error
		if strings.HasPrefix(t.DocumentID, mentalModelTombstonePrefix) {
			derr = c.deleteOwnedModel(hctx, target)
		} else {
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
		if strings.HasPrefix(t.DocumentID, mentalModelTombstonePrefix) {
			continue
		}
		if t.ProjectID == nil {
			tags[GlobalTag] = true
		} else if _, err := s.cfg.Q.GetProject(ctx, *t.ProjectID); err == nil {
			tags[ProjectTag(*t.ProjectID)] = true
		}
	}
	s.refreshScopes(hctx, c, tags, true)
	return done, nil
}

// RunTombstones 是后台循环：每分钟重试到期的删除，并在记忆启用时保证 user-preferences 存在。ctx 取消时返回。
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
		if err := s.EnsureUserPrefs(ctx); err != nil && ctx.Err() == nil {
			// Hindsight 停机时每分钟都会失败，只记调试日志；状态页会显示不可达
			slog.Debug("创建 user-preferences 失败", "err", err)
		}
	}
}
