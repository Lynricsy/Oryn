package threads

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/portalhost"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// Entry 是条目的线路形态（REST `GET threads/{id}/entries` 与 WS `entries` 共用）。
// 与 apigen.Entry 同形，但 payload 保持原始字节，避免把每行 JSON 解码成 map 再编码。
type Entry struct {
	Seq      int64           `json:"seq"`
	File     string          `json:"file"`
	EntryID  string          `json:"entryId"`
	ParentID *string         `json:"parentId,omitempty"`
	Type     string          `json:"type"`
	TS       *int64          `json:"ts,omitempty"`
	Payload  json.RawMessage `json:"payload"`
}

func entryFromDB(e db.Entry) Entry {
	return Entry{Seq: e.Seq, File: e.File, EntryID: e.EntryID, ParentID: e.ParentID, Type: e.Type, TS: e.Ts, Payload: e.Payload}
}

// modelString / parseModel 在 threads.model 列（`<provider>/<id>`）与 ModelRef 之间转换。
func modelString(m *apigen.ModelRef) *string {
	if m == nil || m.Provider == "" || m.ID == "" {
		return nil
	}
	s := m.Provider + "/" + m.ID
	return &s
}

func parseModel(s *string) *apigen.ModelRef {
	if s == nil || *s == "" {
		return nil
	}
	p, id, ok := strings.Cut(*s, "/")
	if !ok {
		return nil
	}
	return &apigen.ModelRef{Provider: p, ID: id}
}

// ThreadToAPI 把线程行转换成 API 形态。
func ThreadToAPI(t db.Thread, labels []string) apigen.Thread {
	if labels == nil {
		labels = []string{}
	}
	return apigen.Thread{
		ID:                t.ID,
		ProjectID:         t.ProjectID,
		OrbID:             t.OrbID,
		Title:             t.Title,
		TitleSource:       apigen.ThreadTitleSource(t.TitleSource),
		State:             apigen.ThreadState(t.State),
		StateChangedAt:    t.StateChangedAt,
		ParentThreadID:    t.ParentThreadID,
		ForkedFromEntryID: t.ForkedFromEntryID,
		LeafEntryID:       t.LeafEntryID,
		Model:             parseModel(t.Model),
		ThinkingLevel:     t.ThinkingLevel,
		Color:             t.Color,
		Pinned:            t.Pinned,
		SnoozedUntil:      t.SnoozedUntil,
		ArchivedAt:        t.ArchivedAt,
		Unread:            t.Unread,
		Labels:            labels,
		CreatedAt:         t.CreatedAt,
		UpdatedAt:         t.UpdatedAt,
		LastMessageAt:     t.LastMessageAt,
	}
}

// orbAPI 把 orb 行转换成 API 形态（orbs.View）；读库失败时记录并返回已有字段。
func (s *Service) orbAPI(ctx context.Context, o db.Orb) apigen.Orb {
	v, err := orbs.View(ctx, s.st.Q, o)
	if err != nil {
		slog.Warn("threads: 转换 orb 失败", "orb", o.ID, "err", err)
		return apigen.Orb{ID: o.ID, ProviderID: o.ProviderID, Type: o.Type, Image: o.Image, ProjectID: o.ProjectID,
			State: apigen.OrbState(o.State), StateChangedAt: o.StateChangedAt, CreatedAt: o.CreatedAt, ThreadIds: []string{}}
	}
	return v
}

// portalTemplate 返回线程的 portal 地址模板（Config.PortalURLTemplate）；未配置或为空时 nil。
func (s *Service) portalTemplate(threadID string) *string {
	if s.cfg.PortalURLTemplate == nil {
		return nil
	}
	if v := s.cfg.PortalURLTemplate(threadID); v != "" {
		return &v
	}
	return nil
}

// shortCode 返回线程 portal 主机名里的短码（threads.short_code）；id 不是 UUIDv7 形态时为 nil。
func shortCode(threadID string) *string {
	if code, ok := portalhost.Code(threadID); ok {
		return &code
	}
	return nil
}

// threadError 返回线程 state=error 的原因：所在 orb 为 failed 时为 {orb_failed, reason}。
// reasons 缓存 orb id → 原因（nil = orb 未失败），供列表批量转换复用。
func (s *Service) threadError(ctx context.Context, t db.Thread, reasons map[string]*apigen.ThreadError) *apigen.ThreadError {
	if t.State != StateError || t.OrbID == nil {
		return nil
	}
	if e, ok := reasons[*t.OrbID]; ok {
		return e
	}
	var out *apigen.ThreadError
	if o, err := s.st.Q.GetOrb(ctx, *t.OrbID); err == nil && o.State == orbStateFailed {
		reason, err := orbs.FailureReason(ctx, s.st.Q, o.ID)
		if err != nil {
			slog.Warn("threads: 读取 orb 失败原因失败", "orb", o.ID, "err", err)
		}
		out = &apigen.ThreadError{Kind: apigen.OrbFailed, Reason: reason}
	}
	reasons[*t.OrbID] = out
	return out
}

// threadAPI 读取线程行、标签与子线程计数。
func (s *Service) threadAPI(ctx context.Context, t db.Thread) (apigen.Thread, error) {
	labels, err := s.st.Q.ListThreadLabels(ctx, t.ID)
	if err != nil {
		return apigen.Thread{}, err
	}
	out := ThreadToAPI(t, labels)
	out.PortalURLTemplate = s.portalTemplate(t.ID)
	out.Error = s.threadError(ctx, t, map[string]*apigen.ThreadError{})
	children, err := s.childCounts(ctx)
	if err != nil {
		return apigen.Thread{}, err
	}
	if c, ok := children[t.ID]; ok {
		out.ChildCount, out.ChildDoneCount = &c[0], &c[1]
	}
	out.Activity = s.activityPtr(t.ID)
	return out, nil
}

// childCounts 返回 父线程 id → [子线程数, 处于 idle 的子线程数]。
func (s *Service) childCounts(ctx context.Context) (map[string][2]int, error) {
	all, err := s.st.Q.ListThreads(ctx, db.ListThreadsParams{})
	if err != nil {
		return nil, err
	}
	return childCountsOf(all), nil
}

func childCountsOf(all []db.Thread) map[string][2]int {
	out := map[string][2]int{}
	for _, t := range all {
		if t.ParentThreadID == nil {
			continue
		}
		c := out[*t.ParentThreadID]
		c[0]++
		if t.State == StateIdle {
			c[1]++
		}
		out[*t.ParentThreadID] = c
	}
	return out
}
