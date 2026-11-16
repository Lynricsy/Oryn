package memory

// stale.go：遗忘之后的 mental model。Hindsight 删除 document 后只把受影响的 mental model 标 is_stale（列表接口
// 不观测删除），旧摘要照常返回；Core 只要 content 非空就把它注入 `<mental_models>`，reflect 默认也会读它，
// 已遗忘的内容会经旧摘要继续进入 agent。所以凡是带遗忘语义的操作，在调用 Hindsight 删除之前先把受影响的模型
// 记进 memory_stale_models：网关对 orb 隐藏这些模型的内容、在 reflect 里排除它们，直到 orynd 在删除之后提交的
// 那次全量刷新（refresh_op）报告完成，或模型已被删除。不按 last_full_refreshed_at 判断：删除之前已开始的刷新
// 可能在删除之后才完成，时间戳变了，内容却仍来自删除之前。
// 只新增内容的操作（批准候选、新建全局记忆）不标记。

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	// staleRefreshPrefix 是等待刷新的模型在 pending-deletes 里的 documentId 前缀。
	staleRefreshPrefix = "mental-model-refresh:"
	// staleReason 是 pending-deletes 里等待刷新的模型的说明。
	staleReason = "遗忘后等待 mental model 刷新"
	// 刷新退避：2 分钟起，每次翻倍，最多 1 小时（异步刷新需要时间，不每分钟重发或查询）。
	staleBackoffBase = 2 * time.Minute
	staleBackoffMax  = time.Hour
)

// staleBackoff 是第 attempts 次提交或查询刷新后到下一次的间隔。
func staleBackoff(attempts int64) time.Duration {
	d := staleBackoffBase
	for i := int64(0); i < attempts && d < staleBackoffMax; i++ {
		d *= 2
	}
	return min(d, staleBackoffMax)
}

// forgetScopes 是一次遗忘影响的范围：键是范围标签（`project:<P>` 或 `oryn:global`），值是项目 ID
// （全局或无法确定项目时为 nil，只用于展示）。
type forgetScopes map[string]*string

func (fs forgetScopes) add(projectID *string) { fs[scopeTag(projectID)] = projectID }

// scopeModels 列出范围内内容可能含有被遗忘事实的模型：项目范围是带 `project:<P>` 的模型；
// 全局范围是带 `oryn:global` 的模型，再加上 user-preferences（即使它的标签不对）。
func (c adminClient) scopeModels(ctx context.Context, tag string) ([]hsMentalModel, error) {
	models, err := c.listMentalModels(ctx, tag, "metadata")
	if err != nil || tag != GlobalTag {
		return models, err
	}
	for _, m := range models {
		if m.ID == UserPrefsModelID {
			return models, nil
		}
	}
	var m hsMentalModel
	err = c.call(ctx, http.MethodGet, mmPath(UserPrefsModelID), nil, &m)
	switch {
	case statusOf(err) == http.StatusNotFound:
		return models, nil
	case err != nil:
		return nil, err
	}
	m.ID = UserPrefsModelID
	return append(models, m), nil
}

// markStale 在删除之前把范围内的模型记为待刷新（已有记录时清掉之前提交的刷新：它可能早于这次删除）。
// 返回被标记的模型 id；出错时调用方不得删除（失败按拒绝处理，否则删除成功后旧摘要无人隐藏）。
func (s *Service) markStale(ctx context.Context, c adminClient, scopes forgetScopes) ([]string, error) {
	var ids []string
	now := s.cfg.Now().Unix()
	for tag, projectID := range scopes {
		models, err := c.scopeModels(ctx, tag)
		if err != nil {
			return nil, err
		}
		for _, m := range models {
			if err := s.cfg.Q.UpsertMemoryStaleModel(context.WithoutCancel(ctx), db.UpsertMemoryStaleModelParams{
				ModelID: m.ID, ProjectID: projectID, Now: now,
			}); err != nil {
				return nil, err
			}
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// refreshStale 对删除之后的模型立即提交一次全量刷新；失败只记次数与原因，由重试循环按退避重发。
func (s *Service) refreshStale(ctx context.Context, c adminClient, ids []string) {
	for _, id := range ids {
		s.submitStaleRefresh(ctx, c, id, 0)
	}
}

// submitStaleRefresh 提交一次全量刷新并记下 operation id；返回的 id 无效按提交失败处理。模型已不存在时直接移除记录。
func (s *Service) submitStaleRefresh(ctx context.Context, c adminClient, id string, attempts int64) {
	op, err := c.refreshMentalModel(ctx, id, true)
	if statusOf(err) == http.StatusNotFound {
		if err := s.cfg.Q.DeleteMemoryStaleModel(context.WithoutCancel(ctx), id); err != nil {
			slog.Warn("移除已删除 mental model 的待刷新记录失败", "model", id, "err", err)
		}
		return
	}
	if err == nil && !exportIDRe.MatchString(op) {
		err = errors.New("Hindsight 返回了无效的 operation id")
	}
	if err != nil {
		slog.Warn("提交 mental model 全量刷新失败", "model", id, "err", err)
		s.deferStale(ctx, id, attempts, nil, err)
		return
	}
	s.deferStale(ctx, id, attempts, &op, nil)
}

// deferStale 记下这次提交或查询的结果（refresh_op、失败原因），并按退避推迟下一次。
func (s *Service) deferStale(ctx context.Context, id string, attempts int64, op *string, cause error) {
	var msg *string
	if cause != nil {
		m := truncate(cause.Error(), 500)
		msg = &m
	}
	if err := s.cfg.Q.RetryMemoryStaleModel(context.WithoutCancel(ctx), db.RetryMemoryStaleModelParams{
		NextAttemptAt: s.cfg.Now().Add(staleBackoff(attempts)).Unix(), RefreshOp: op, LastError: msg, ModelID: id,
	}); err != nil {
		slog.Warn("记录 mental model 刷新状态失败", "model", id, "err", err)
	}
}

// refreshOpStatus 读刷新操作的状态；HTTP 404 视为 not_found。
func (c adminClient) refreshOpStatus(ctx context.Context, op string) (string, string, error) {
	var resp struct {
		Status       string  `json:"status"`
		ErrorMessage *string `json:"error_message"`
	}
	err := c.call(ctx, http.MethodGet, adminBank+"/operations/"+url.PathEscape(op), nil, &resp)
	if statusOf(err) == http.StatusNotFound {
		return "not_found", "", nil
	}
	msg := ""
	if resp.ErrorMessage != nil {
		msg = *resp.ErrorMessage
	}
	return resp.Status, msg, err
}

// RetryStaleModels 处理到期的待刷新模型：模型已删除（404）或我们提交的刷新操作已完成时移除记录；
// 刷新仍在排队或执行时继续隐藏、不重发，按退避再查；刷新失败、取消或查不到时清掉 refresh_op，到期重新提交；
// 还没提交成功的到期提交。未配置时不动。返回本轮移除的记录数。
func (s *Service) RetryStaleModels(ctx context.Context) (int, error) {
	rows, err := s.cfg.Q.ListMemoryStaleModels(ctx)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	c, err := s.admin(ctx)
	if err != nil {
		if errors.Is(err, ErrNotConfigured) {
			return 0, nil
		}
		return 0, err
	}
	hctx, cancel := context.WithTimeout(ctx, purgeTimeout)
	defer cancel()
	now := s.cfg.Now().Unix()
	cleared := 0
	clear := func(id string) error {
		if err := s.cfg.Q.DeleteMemoryStaleModel(ctx, id); err != nil {
			return err
		}
		cleared++
		return nil
	}
	for _, row := range rows {
		if row.NextAttemptAt > now {
			continue
		}
		var m hsMentalModel
		err := c.call(hctx, http.MethodGet, mmPath(row.ModelID), nil, &m)
		if statusOf(err) == http.StatusNotFound {
			if err := clear(row.ModelID); err != nil {
				return cleared, err
			}
			continue
		}
		if err != nil {
			// 读不到就不知道模型是否还在：保持隐藏，按退避再看
			s.deferStale(hctx, row.ModelID, row.Attempts, row.RefreshOp, err)
			continue
		}
		if row.RefreshOp == nil {
			s.submitStaleRefresh(hctx, c, row.ModelID, row.Attempts)
			continue
		}
		status, detail, err := c.refreshOpStatus(hctx, *row.RefreshOp)
		switch {
		case err != nil:
			s.deferStale(hctx, row.ModelID, row.Attempts, row.RefreshOp, err)
		case status == "completed":
			if err := clear(row.ModelID); err != nil {
				return cleared, err
			}
		case status == "failed" || status == "cancelled" || status == "not_found":
			cause := "刷新操作 " + *row.RefreshOp + " 状态为 " + status
			if detail != "" {
				cause += "：" + detail
			}
			s.deferStale(hctx, row.ModelID, row.Attempts, nil, errors.New(cause))
		default:
			// pending、processing 或未知状态：刷新还没完成，继续隐藏，不重发
			s.deferStale(hctx, row.ModelID, row.Attempts, row.RefreshOp, nil)
		}
	}
	return cleared, nil
}

// StaleModelIDs 返回仍在等待遗忘后刷新的模型 id（网关据此隐藏内容、在 reflect 里排除）。
func (s *Service) StaleModelIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.cfg.Q.ListMemoryStaleModels(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[r.ModelID] = true
	}
	return out, nil
}
