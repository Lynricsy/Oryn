package threads

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
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

// OrbToAPI 把 orb 行转换成 API 形态；threadIDs 为挂在该 orb 上的线程。
func OrbToAPI(o db.Orb, providerName *string, threadIDs []string) apigen.Orb {
	if threadIDs == nil {
		threadIDs = []string{}
	}
	return apigen.Orb{
		ID:                    o.ID,
		ProviderID:            o.ProviderID,
		ProviderName:          providerName,
		Type:                  o.Type,
		Cpus:                  float32(o.Cpus),
		MemoryMB:              int(o.MemoryMb),
		Image:                 o.Image,
		ProjectID:             o.ProjectID,
		State:                 apigen.OrbState(o.State),
		StateChangedAt:        o.StateChangedAt,
		SnapshotOf:            o.SnapshotOf,
		LastAgentActivityAt:   o.LastAgentActivityAt,
		LastUserInteractionAt: o.LastUserInteractionAt,
		OrbdVersion:           o.OrbdVersion,
		AgentVersion:          o.AgentVersion,
		CreatedAt:             o.CreatedAt,
		DeletedAt:             o.DeletedAt,
		ThreadIds:             threadIDs,
	}
}

// orbAPI 读取 orb 及其 provider 名与线程列表。
func (s *Service) orbAPI(ctx context.Context, o db.Orb) apigen.Orb {
	var name *string
	if p, err := s.st.Q.GetProvider(ctx, o.ProviderID); err == nil {
		name = &p.Name
	}
	var ids []string
	if ts, err := s.st.Q.ListThreadsByOrb(ctx, &o.ID); err == nil {
		for _, t := range ts {
			ids = append(ids, t.ID)
		}
	}
	return OrbToAPI(o, name, ids)
}

// threadAPI 读取线程行、标签与子线程计数。
func (s *Service) threadAPI(ctx context.Context, t db.Thread) (apigen.Thread, error) {
	labels, err := s.st.Q.ListThreadLabels(ctx, t.ID)
	if err != nil {
		return apigen.Thread{}, err
	}
	out := ThreadToAPI(t, labels)
	children, err := s.childCounts(ctx)
	if err != nil {
		return apigen.Thread{}, err
	}
	if c, ok := children[t.ID]; ok {
		out.ChildCount, out.ChildDoneCount = &c[0], &c[1]
	}
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
