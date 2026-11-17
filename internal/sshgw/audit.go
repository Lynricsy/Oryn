package sshgw

import (
	"context"
	"log/slog"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	auditDefaultLimit = 50
	auditMaxLimit     = 200
	maxAuditDetail    = 4096
)

// 审计来源。
const (
	srcOrb   = "orb"
	srcOwner = "owner"
	srcTool  = "tool"
)

// auditRow 是一行审计；空字符串的 orbID / threadID 存 NULL。
type auditRow struct {
	source     string
	orbID      string
	threadID   string
	host       db.SshHost // 只取 id 与名称快照
	hostID     string     // host 为空时用（如按名拒绝、主机未登记）
	hostName   string
	kind       string
	detail     string
	exitCode   *int64
	durationMs *int64
	ok         bool
}

// audit 同步写一行审计；失败只记日志。
func (s *Service) audit(ctx context.Context, r auditRow) {
	id, name := r.host.ID, r.host.Name
	if id == "" {
		id, name = r.hostID, r.hostName
	}
	p := db.InsertSshAuditParams{
		At: s.now(), Source: r.source, HostID: id, HostName: name, Kind: r.kind, Detail: truncate(r.detail, maxAuditDetail),
		ExitCode: r.exitCode, DurationMs: r.durationMs, Ok: r.ok,
	}
	if r.orbID != "" {
		p.OrbID = &r.orbID
	}
	if r.threadID != "" {
		p.ThreadID = &r.threadID
	}
	if err := s.st.Q.InsertSshAudit(context.WithoutCancel(ctx), p); err != nil {
		slog.Warn("写 SSH 审计失败", "host", name, "kind", r.kind, "err", err)
	}
}

// truncate 把 v 截到至多 n 字节（不切断 UTF-8 字符），截断时加「…」。
func truncate(v string, n int) string {
	if len(v) <= n {
		return v
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(v[cut]) {
		cut--
	}
	return v[:cut] + "…"
}

// AuditQuery 是审计列表的过滤与分页参数。
type AuditQuery struct {
	HostID, ThreadID string
	Before           int64 // ≤0 = 从最新开始
	Limit            int   // ≤0 = 50
}

// Audit 按时间倒序列出审计，多取一条判断是否还有下一页。
func (s *Service) Audit(ctx context.Context, q AuditQuery) (apigen.SSHAuditList, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = auditDefaultLimit
	}
	if limit > auditMaxLimit {
		return apigen.SSHAuditList{}, invalid("limit 最大 200")
	}
	p := db.ListSshAuditParams{Before: q.Before, Limit: int64(limit) + 1}
	if p.Before <= 0 {
		p.Before = 1<<63 - 1
	}
	if q.HostID != "" {
		p.HostID = &q.HostID
	}
	if q.ThreadID != "" {
		p.ThreadID = &q.ThreadID
	}
	rows, err := s.st.Q.ListSshAudit(ctx, p)
	if err != nil {
		return apigen.SSHAuditList{}, err
	}
	out := apigen.SSHAuditList{Entries: make([]apigen.SSHAuditEntry, 0, min(len(rows), limit))}
	for i, r := range rows {
		if i == limit {
			next := out.Entries[limit-1].ID
			out.NextBefore = &next
			break
		}
		e := apigen.SSHAuditEntry{
			ID: r.ID, At: r.At, Source: apigen.SSHAuditEntrySource(r.Source), OrbID: r.OrbID, ThreadID: r.ThreadID,
			HostID: r.HostID, HostName: r.HostName, Kind: apigen.SSHAuditEntryKind(r.Kind), Detail: r.Detail,
			DurationMs: r.DurationMs, Ok: r.Ok,
		}
		if r.ExitCode != nil {
			e.ExitCode = new(int(*r.ExitCode))
		}
		out.Entries = append(out.Entries, e)
	}
	return out, nil
}

// threadOf 返回 orb 上恰好一条线程时它的 id，否则空。
func (s *Service) threadOf(ctx context.Context, orbID string) string {
	ts, err := s.st.Q.ListThreadsByOrb(ctx, &orbID)
	if err != nil || len(ts) != 1 {
		return ""
	}
	return ts[0].ID
}
