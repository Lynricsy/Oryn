package memory

// stale.go：遗忘之后的 mental model。Hindsight 删除 document 后只把受影响的 mental model 标 is_stale（列表接口
// 不观测删除），旧摘要照常返回；Core 只要 content 非空就把它注入 `<mental_models>`，reflect 默认也会读它，
// 已遗忘的内容会经旧摘要继续进入 agent。所以凡是带遗忘语义的操作，在调用 Hindsight 删除之前先把受影响的模型
// 记进 memory_stale_models：网关对 orb 隐藏这些模型的内容、在 reflect 里排除它们，直到 orynd 在删除之后提交的
// 那次全量刷新（refresh_op）报告完成，或模型已被删除。不按 last_full_refreshed_at 判断：删除之前已开始的刷新
// 可能在删除之后才完成，时间戳变了，内容却仍来自删除之前。
// 并发：每次遗忘对每个模型持有一条 hold（memory_stale_holds），删除结束后释放；模型还有任何 hold 时不提交刷新、
// 不写回、不清除，所以重叠的删除乱序结束时，只有最后一个释放后才刷新。每次标记写入新的随机令牌，写回与清除都
// 以读到的令牌为条件，旧的请求晚到也碰不到新遗忘的记录（包括记录清除后又重新插入的情形）。
// 只新增内容的操作（批准候选、新建全局记忆）不标记。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// staleMark 是一次遗忘对一个模型的标记：hold 是这次删除持有的 hold_id，删除结束后按它精确释放。
type staleMark struct {
	id, hold string
}

// randomToken 生成从不复用的随机令牌（16 字节十六进制），用作标记令牌与 hold_id。
func randomToken() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// markStale 在删除之前把范围内的模型记为待刷新：每个模型先插入这次删除的 hold（模型有 hold 时不许提交刷新、
// 写回或清除：刷新会读到尚未删除的数据），再写入这次遗忘的新令牌并清掉之前提交的刷新。先 hold 后令牌，任何一步
// 之后模型都不可刷新，所以不需要事务。返回这次的标记；出错时调用方不得删除（失败按拒绝处理，否则删除成功后旧摘要
// 无人隐藏），已持有的 hold 在返回前释放。调用方在删除结束后（成功或失败）必须调用 releaseStale。
func (s *Service) markStale(ctx context.Context, c adminClient, scopes forgetScopes) ([]staleMark, error) {
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	var marks []staleMark
	fail := func(err error) ([]staleMark, error) {
		s.releaseStale(ctx, marks)
		return nil, err
	}
	lctx := context.WithoutCancel(ctx)
	now := s.cfg.Now().Unix()
	for tag, projectID := range scopes {
		models, err := c.scopeModels(ctx, tag)
		if err != nil {
			return fail(err)
		}
		for _, m := range models {
			hold, err := randomToken()
			if err != nil {
				return fail(err)
			}
			if err := s.cfg.Q.InsertMemoryStaleHold(lctx, db.InsertMemoryStaleHoldParams{HoldID: hold, ModelID: m.ID, CreatedAt: now}); err != nil {
				return fail(err)
			}
			marks = append(marks, staleMark{id: m.ID, hold: hold})
			if err := s.cfg.Q.UpsertMemoryStaleModel(lctx, db.UpsertMemoryStaleModelParams{
				ModelID: m.ID, ProjectID: projectID, Now: now, Token: token,
			}); err != nil {
				return fail(err)
			}
		}
	}
	return marks, nil
}

// releaseStale 在删除结束后（成功或失败）释放这次持有的 hold。按 hold_id 精确删除，可重复调用，碰不到别的遗忘的
// hold；失败时刷新只是按现有数据重新提炼一次，之后的墓碑重试会再标记。调用方用 defer 兜底，保证每条路径都释放。
func (s *Service) releaseStale(ctx context.Context, marks []staleMark) {
	for _, m := range marks {
		if err := s.cfg.Q.ReleaseMemoryStaleHold(context.WithoutCancel(ctx), m.hold); err != nil {
			slog.Warn("释放 mental model 删除保持失败", "model", m.id, "err", err)
		}
	}
}

// refreshStale 在 releaseStale 之后对标记的模型立即提交一次全量刷新：重新读记录，模型还有别的 hold（另一个遗忘的
// 删除仍在进行）时不提交，等最后一个释放者或重试循环提交。失败只记次数与原因，由重试循环按退避重发。
func (s *Service) refreshStale(ctx context.Context, c adminClient, marks []staleMark) {
	seen := map[string]bool{}
	for _, m := range marks {
		if seen[m.id] {
			continue
		}
		seen[m.id] = true
		row, err := s.cfg.Q.GetRefreshableMemoryStaleModel(ctx, m.id)
		if err != nil {
			continue
		}
		s.submitStaleRefresh(ctx, c, row)
	}
}

// submitStaleRefresh 提交一次全量刷新，并以读到的令牌为条件记下 operation id；返回的 id 无效按提交失败处理。
// 模型已不存在时清除记录（同样以令牌为条件）。
func (s *Service) submitStaleRefresh(ctx context.Context, c adminClient, row db.MemoryStaleModel) {
	op, err := c.refreshMentalModel(ctx, row.ModelID, true)
	if statusOf(err) == http.StatusNotFound {
		s.clearStale(ctx, row)
		return
	}
	if err == nil && !exportIDRe.MatchString(op) {
		err = errors.New("Hindsight 返回了无效的 operation id")
	}
	if err != nil {
		slog.Warn("提交 mental model 全量刷新失败", "model", row.ModelID, "err", err)
		s.deferStale(ctx, row, nil, err)
		return
	}
	s.deferStale(ctx, row, &op, nil)
}

// deferStale 写回这次提交或查询的结果（refresh_op、失败原因），并按退避推迟下一次。条件是令牌仍是读到的那一个且
// 模型没有 hold：期间又有遗忘标记了它时什么也不写，这次的结果作废。
func (s *Service) deferStale(ctx context.Context, row db.MemoryStaleModel, op *string, cause error) {
	var msg *string
	if cause != nil {
		v := truncate(cause.Error(), 500)
		msg = &v
	}
	if _, err := s.cfg.Q.RetryMemoryStaleModel(context.WithoutCancel(ctx), db.RetryMemoryStaleModelParams{
		NextAttemptAt: s.cfg.Now().Add(staleBackoff(row.Attempts)).Unix(), RefreshOp: op, LastError: msg,
		ModelID: row.ModelID, Token: row.Token,
	}); err != nil {
		slog.Warn("记录 mental model 刷新状态失败", "model", row.ModelID, "err", err)
	}
}

// clearStale 清除记录（遗忘已完成），条件同 deferStale；返回是否真的清除了。
func (s *Service) clearStale(ctx context.Context, row db.MemoryStaleModel) bool {
	n, err := s.cfg.Q.ClearMemoryStaleModel(context.WithoutCancel(ctx), db.ClearMemoryStaleModelParams{ModelID: row.ModelID, Token: row.Token})
	if err != nil {
		slog.Warn("清除 mental model 待刷新记录失败", "model", row.ModelID, "err", err)
		return false
	}
	return n > 0
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

// RecoverStaleModels 清空全部 hold：进行中的删除随上个进程结束（只有一个 orynd 实例）。New 构造服务时调用一次，
// 早于本进程的任何遗忘操作。
func (s *Service) RecoverStaleModels(ctx context.Context) error {
	return s.cfg.Q.DeleteAllMemoryStaleHolds(ctx)
}

// RetryStaleModels 处理可刷新（没有 hold）且到期的待刷新模型：模型已删除（404）或我们提交的刷新操作已完成时清除
// 记录；刷新仍在排队或执行时继续隐藏、不重发，按退避再查；刷新失败、取消或查不到时清掉 refresh_op，到期重新提交；
// 还没提交成功的到期提交。每次写回、清除都以读到的令牌且没有 hold 为条件，期间有新的遗忘标记时这轮结果作废。
// 未配置时不动。返回本轮清除的记录数。
func (s *Service) RetryStaleModels(ctx context.Context) (int, error) {
	rows, err := s.cfg.Q.ListDueMemoryStaleModels(ctx, s.cfg.Now().Unix())
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
	cleared := 0
	for _, row := range rows {
		var hm hsMentalModel
		err := c.call(hctx, http.MethodGet, mmPath(row.ModelID), nil, &hm)
		if statusOf(err) == http.StatusNotFound {
			if s.clearStale(hctx, row) {
				cleared++
			}
			continue
		}
		if err != nil {
			// 读不到就不知道模型是否还在：保持隐藏，按退避再看
			s.deferStale(hctx, row, row.RefreshOp, err)
			continue
		}
		if row.RefreshOp == nil {
			s.submitStaleRefresh(hctx, c, row)
			continue
		}
		status, detail, err := c.refreshOpStatus(hctx, *row.RefreshOp)
		switch {
		case err != nil:
			s.deferStale(hctx, row, row.RefreshOp, err)
		case status == "completed":
			if s.clearStale(hctx, row) {
				cleared++
			}
		case status == "failed" || status == "cancelled" || status == "not_found":
			cause := "刷新操作 " + *row.RefreshOp + " 状态为 " + status
			if detail != "" {
				cause += "：" + detail
			}
			s.deferStale(hctx, row, nil, errors.New(cause))
		default:
			// pending、processing 或未知状态：刷新还没完成，继续隐藏，不重发
			s.deferStale(hctx, row, row.RefreshOp, nil)
		}
	}
	return cleared, nil
}

// StaleModelIDs 返回仍在等待遗忘后刷新的模型 id（不论删除是否已结束），网关据此隐藏内容、在 reflect 里排除。
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
