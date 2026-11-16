package github

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 管理接口（`/api/v1/github/hooks|audit|deliveries`）用到的只读视图与投递重放（M15 步骤 7.1）。

const (
	// replayWindow 是可重放投递的最长时间：只接受这段时间内收到的投递（R24）。
	replayWindow = 7 * 24 * time.Hour
	// auditDefaultLimit / auditMaxLimit 是审计分页的默认与最大条数（与 OpenAPI 一致）。
	auditDefaultLimit = 50
	auditMaxLimit     = 200
)

// Hooks 列出 Oryn 管理的仓库 hook 及其状态（`GET /api/v1/github/hooks`），按仓库名排序。
func (s *Service) Hooks(ctx context.Context) ([]apigen.GitHubHook, error) {
	rows, err := s.st.Q.ListGithubHooks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.GitHubHook, 0, len(rows))
	for _, r := range rows {
		h := apigen.GitHubHook{
			RepoID:         r.RepoID,
			FullName:       r.FullName,
			HookID:         r.HookID,
			Status:         apigen.GitHubHookStatus(r.Status),
			Error:          r.Error,
			LastDeliveryAt: r.LastDeliveryAt,
			UpdatedAt:      r.UpdatedAt,
		}
		if r.LastDeliverySrc != nil {
			src := apigen.GitHubHookLastDeliverySource(*r.LastDeliverySrc)
			h.LastDeliverySource = &src
		}
		out = append(out, h)
	}
	return out, nil
}

// AuditQuery 是审计列表的过滤与分页条件；零值字段表示不过滤 / 取默认。
type AuditQuery struct {
	ThreadID string
	Repo     string // owner/repo，不区分大小写
	Before   int64  // 只取 id 小于它的记录；0 = 从最新开始
	Limit    int    // 1–200；0 = 50
}

// Audit 按时间倒序列出写操作审计（`GET /api/v1/github/audit`）。多取一条判断是否还有更早的记录，
// 有则以本页最后一条的 id 作为 nextBefore。
func (s *Service) Audit(ctx context.Context, q AuditQuery) (apigen.GitHubAuditList, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = auditDefaultLimit
	}
	if limit > auditMaxLimit {
		return apigen.GitHubAuditList{}, errf("invalid_request", "limit must be at most 200")
	}
	p := db.ListGithubAuditParams{Before: q.Before, Limit: int64(limit) + 1}
	if p.Before <= 0 {
		p.Before = 1<<63 - 1
	}
	if q.ThreadID != "" {
		p.ThreadID = &q.ThreadID
	}
	if v := strings.ToLower(strings.TrimSpace(q.Repo)); v != "" {
		// 出站代理记录的仓库是小写的 owner/repo
		p.Repo = &v
	}
	rows, err := s.st.Q.ListGithubAudit(ctx, p)
	if err != nil {
		return apigen.GitHubAuditList{}, err
	}
	out := apigen.GitHubAuditList{Entries: make([]apigen.GitHubAuditEntry, 0, min(len(rows), limit))}
	for i, r := range rows {
		if i == limit {
			next := out.Entries[limit-1].ID
			out.NextBefore = &next
			break
		}
		out.Entries = append(out.Entries, auditView(r))
	}
	return out, nil
}

// auditView 把审计行转成 API 形态：空串的仓库与门禁结论省略。
func auditView(r db.GithubAudit) apigen.GitHubAuditEntry {
	e := apigen.GitHubAuditEntry{
		ID:         r.ID,
		At:         r.At,
		ThreadID:   r.ThreadID,
		Slot:       apigen.GitHubIdentitySlot(r.Slot),
		Method:     r.Method,
		Op:         r.Op,
		Status:     int(r.Status),
		BodySha256: r.BodySha256,
	}
	if r.OrbID != "" {
		orb := r.OrbID
		e.OrbID = &orb
	}
	if r.Repo != "" {
		repo := r.Repo
		e.Repo = &repo
	}
	if r.Gate != "" {
		gate := r.Gate
		e.Gate = &gate
	}
	return e
}

// ReplayDelivery 把一条已结束的投递重新置为 pending 并立即入队（`POST /api/v1/github/deliveries/{guid}/replay`，R24）。
// 只接受 replayWindow 内收到、结果已定（不是 pending）且载荷仍在的投递：不存在 → 404；仍在处理、过旧或载荷已清 → 409。
func (s *Service) ReplayDelivery(ctx context.Context, guid string) error {
	n, err := s.st.Q.ReplayGithubDelivery(ctx, db.ReplayGithubDeliveryParams{
		Guid: guid, Since: s.now().Add(-replayWindow).UnixMilli(),
	})
	if err != nil {
		return err
	}
	row, err := s.st.Q.GetGithubDelivery(ctx, guid)
	if err != nil {
		if isNoRows(err) {
			return errf("not_found", "delivery not found")
		}
		return err
	}
	if n == 0 {
		if row.Outcome == "pending" {
			return &Error{Status: http.StatusConflict, Code: "conflict", Message: "delivery is still being processed"}
		}
		return &Error{Status: http.StatusConflict, Code: "conflict", Message: "only deliveries received within the last 7 days can be replayed"}
	}
	s.enqueueDelivery(row.Guid, row.Event, row.Payload)
	return nil
}
