package threads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// CreateParams 是新建线程的参数（对应 `POST threads` 请求体）。
type CreateParams struct {
	ProjectID string
	Prompt    string
	Images    []apigen.ImageContent
	// OrbID 非空时挂到已有 orb；否则新建 orb（Provider / Type 为空时由 orbs 取默认）。
	OrbID          string
	Provider, Type string
	Model          *apigen.ModelRef
	ThinkingLevel  string
	Title          string
	Labels         []string
}

// Create 按 H4.3 新建线程：建线程（provisioning）→ 需要时建 orb 并入队 spawn → orb ready 后 agent.start → 首条 prompt。
func (s *Service) Create(ctx context.Context, p CreateParams) (apigen.Thread, error) {
	if strings.TrimSpace(p.Prompt) == "" && len(p.Images) == 0 {
		return apigen.Thread{}, errf("invalid", "prompt or images required")
	}
	if p.ProjectID != "" {
		if _, err := s.st.Q.GetProject(ctx, p.ProjectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return apigen.Thread{}, errf("not_found", "project not found")
			}
			return apigen.Thread{}, err
		}
	}
	var orb db.Orb
	if p.OrbID != "" {
		o, err := s.st.Q.GetOrb(ctx, p.OrbID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (o.DeletedAt != nil || o.State == orbStateDeleted || o.State == "deleting")) {
			return apigen.Thread{}, errf("not_found", "orb not found")
		}
		if err != nil {
			return apigen.Thread{}, err
		}
		orb = o
	} else {
		o, err := s.orbs.Create(ctx, OrbCreate{Provider: p.Provider, Type: p.Type, ProjectID: p.ProjectID})
		if err != nil {
			return apigen.Thread{}, errf("orb_create_failed", err.Error())
		}
		orb = o
	}

	id := ids.Thread()
	row := db.CreateThreadParams{
		ID: id, OrbID: &orb.ID, TitleSource: "auto", State: StateProvisioning, SessionDir: sessionDirOf(id),
		Model: modelString(p.Model),
	}
	if p.ProjectID != "" {
		row.ProjectID = &p.ProjectID
	}
	if p.Title != "" {
		row.Title, row.TitleSource = p.Title, "user"
	}
	if p.ThinkingLevel != "" {
		row.ThinkingLevel = &p.ThinkingLevel
	}
	if err := s.insertThread(ctx, row, p.Labels); err != nil {
		return apigen.Thread{}, err
	}
	t, err := s.get(ctx, id)
	if err != nil {
		return apigen.Thread{}, err
	}
	prompt := map[string]any{"type": "prompt", "message": p.Prompt}
	if len(p.Images) > 0 {
		prompt["images"] = p.Images
	}
	frame, _ := json.Marshal(prompt)
	t.mu.Lock()
	_ = s.enqueueLocked(t, frame, &pendingCmd{cmdType: "prompt"})
	if p.Title != "" {
		frame, _ := json.Marshal(map[string]string{"type": "set_session_name", "name": p.Title})
		_ = s.enqueueLocked(t, frame, &pendingCmd{cmdType: "set_session_name"})
	}
	t.mu.Unlock()
	out, err := s.Get(ctx, id)
	if err != nil {
		return apigen.Thread{}, err
	}
	s.out().IndexUpsert(out)
	if p.OrbID != "" {
		s.wake(id, orb.ID)
	} else if s.orbState(orb.ID) == orbStateReady {
		s.go_(func(ctx context.Context) { s.startAgent(ctx, id) })
	}
	return out, nil
}

func (s *Service) insertThread(ctx context.Context, row db.CreateThreadParams, labels []string) error {
	now := s.now()
	row.StateChangedAt, row.CreatedAt, row.UpdatedAt = now, now, now
	return s.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateThread(ctx, row); err != nil {
			return err
		}
		for _, l := range labels {
			if l = strings.TrimSpace(l); l != "" {
				if err := q.AddThreadLabel(ctx, db.AddThreadLabelParams{ThreadID: row.ID, Label: l}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// Get 返回线程的 API 形态。
func (s *Service) Get(ctx context.Context, id string) (apigen.Thread, error) {
	row, err := s.st.Q.GetThread(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return apigen.Thread{}, ErrNotFound
	}
	if err != nil {
		return apigen.Thread{}, err
	}
	return s.threadAPI(ctx, row)
}

// ListFilter 对应 `GET threads` 的查询参数。
type ListFilter struct {
	State   string
	Project string // "none" = 只要无项目线程
	// Archived 为 nil 时只返回未归档线程。
	Archived *bool
	Q        string
}

// List 返回线程列表（置顶优先、最近活动优先）。
func (s *Service) List(ctx context.Context, f ListFilter) ([]apigen.Thread, error) {
	all, err := s.st.Q.ListThreads(ctx, db.ListThreadsParams{})
	if err != nil {
		return nil, err
	}
	labelRows, err := s.st.Q.ListAllThreadLabels(ctx)
	if err != nil {
		return nil, err
	}
	labels := map[string][]string{}
	for _, l := range labelRows {
		labels[l.ThreadID] = append(labels[l.ThreadID], l.Label)
	}
	children := childCountsOf(all)
	archived := false
	if f.Archived != nil {
		archived = *f.Archived
	}
	q := strings.ToLower(f.Q)
	out := make([]apigen.Thread, 0, len(all))
	for _, t := range all {
		if (t.ArchivedAt != nil) != archived || (f.State != "" && t.State != f.State) {
			continue
		}
		switch {
		case f.Project == "none" && t.ProjectID != nil:
			continue
		case f.Project != "" && f.Project != "none" && (t.ProjectID == nil || *t.ProjectID != f.Project):
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(t.Title), q) && !anyContains(labels[t.ID], q) {
			continue
		}
		api := ThreadToAPI(t, labels[t.ID])
		if c, ok := children[t.ID]; ok {
			api.ChildCount, api.ChildDoneCount = &c[0], &c[1]
		}
		out = append(out, api)
	}
	return out, nil
}

func anyContains(ls []string, q string) bool {
	for _, l := range ls {
		if strings.Contains(strings.ToLower(l), q) {
			return true
		}
	}
	return false
}

// Index 返回 WS `index` 的线程摘要（全部未归档与已归档线程）。
func (s *Service) Index(ctx context.Context) ([]apigen.Thread, error) {
	live, err := s.List(ctx, ListFilter{})
	if err != nil {
		return nil, err
	}
	yes := true
	arch, err := s.List(ctx, ListFilter{Archived: &yes})
	if err != nil {
		return nil, err
	}
	return append(live, arch...), nil
}

// Patch 对应 `PATCH threads/{id}`：只改出现的字段；color 传空串清除，snoozedUntil 传 0 清除。
func (s *Service) Patch(ctx context.Context, id string, p apigen.ThreadPatch) (apigen.Thread, error) {
	t, err := s.get(ctx, id)
	if err != nil {
		return apigen.Thread{}, err
	}
	t.mu.Lock()
	row, err := s.st.Q.GetThread(ctx, id)
	if err != nil {
		t.mu.Unlock()
		return apigen.Thread{}, err
	}
	now := s.now()
	m := db.UpdateThreadMetaParams{
		ID: id, Title: row.Title, TitleSource: row.TitleSource, Color: row.Color, Pinned: row.Pinned,
		SnoozedUntil: row.SnoozedUntil, ArchivedAt: row.ArchivedAt, Unread: row.Unread, UpdatedAt: now,
	}
	renamed := ""
	if p.Title != nil && *p.Title != row.Title {
		m.Title, m.TitleSource = *p.Title, "user"
		renamed = *p.Title
	}
	if p.Color != nil {
		m.Color = nil
		if *p.Color != "" {
			m.Color = p.Color
		}
	}
	if p.Pinned != nil {
		m.Pinned = *p.Pinned
	}
	if p.SnoozedUntil != nil {
		m.SnoozedUntil = nil
		if *p.SnoozedUntil != 0 {
			m.SnoozedUntil = p.SnoozedUntil
		}
	}
	if p.Archived != nil {
		switch {
		case *p.Archived && row.ArchivedAt == nil:
			m.ArchivedAt = &now
		case !*p.Archived:
			m.ArchivedAt = nil
		}
	}
	if p.Unread != nil {
		m.Unread = *p.Unread
	}
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.UpdateThreadMeta(ctx, m); err != nil {
			return err
		}
		if p.Labels != nil {
			if err := q.ClearThreadLabels(ctx, id); err != nil {
				return err
			}
			for _, l := range p.Labels {
				if l = strings.TrimSpace(l); l != "" {
					if err := q.AddThreadLabel(ctx, db.AddThreadLabelParams{ThreadID: id, Label: l}); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if err == nil {
		t.unread = m.Unread
		s.publish(ctx, id)
	}
	t.mu.Unlock()
	if err != nil {
		return apigen.Thread{}, err
	}
	if renamed != "" {
		s.renameSession(t, renamed)
	}
	return s.Get(ctx, id)
}

// Delete 删除线程（条目随之删除；orb 保留）。agent 若在运行则先停止。
func (s *Service) Delete(ctx context.Context, id string) error {
	t, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	t.mu.Lock()
	orbID := t.orbID
	if t.agent != nil {
		t.agent.close()
		t.agent = nil
	}
	s.failPendingLocked(t, "thread deleted", true)
	t.queue = nil
	t.mu.Unlock()
	if ctl, ok := s.orbs.Ctl(orbID); ok && orbID != "" {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := ctl.Call(cctx, "agent.stop", apigen.LinkCtlAgentStopParams{ThreadID: id, GraceSeconds: 5}, nil); err != nil {
			slog.Debug("threads: agent.stop 失败", "thread", id, "err", err)
		}
		cancel()
	}
	n, err := s.st.Q.DeleteThread(ctx, id)
	if err != nil {
		return err
	}
	s.forget(id)
	if n == 0 {
		return ErrNotFound
	}
	t.mu.Lock()
	s.out().IndexRemove(id)
	t.mu.Unlock()
	return nil
}

// EntryPage 是条目分页结果（REST `GET threads/{id}/entries`）。
type EntryPage struct {
	Entries     []Entry `json:"entries"`
	HasMore     bool    `json:"hasMore"`
	LastSeq     int64   `json:"lastSeq"`
	LeafEntryID *string `json:"leafEntryId,omitempty"`
}

// Entries 返回 seq > after 的条目，至多 limit 条（1–500，默认 200）。
func (s *Service) Entries(ctx context.Context, id string, after int64, limit int) (EntryPage, error) {
	if limit <= 0 {
		limit = entriesBatch
	}
	limit = min(limit, 500)
	row, err := s.st.Q.GetThread(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return EntryPage{}, ErrNotFound
	}
	if err != nil {
		return EntryPage{}, err
	}
	rows, err := s.st.Q.ListEntriesAfter(ctx, db.ListEntriesAfterParams{ThreadID: id, AfterSeq: after, Limit: int64(limit + 1)})
	if err != nil {
		return EntryPage{}, err
	}
	page := EntryPage{LastSeq: row.LastSeq, LeafEntryID: row.LeafEntryID, Entries: make([]Entry, 0, min(len(rows), limit))}
	if len(rows) > limit {
		page.HasMore = true
		rows = rows[:limit]
	}
	for _, e := range rows {
		page.Entries = append(page.Entries, entryFromDB(e))
	}
	return page, nil
}

// EntriesRange 返回 after < seq ≤ upTo 的条目（WS 订阅补发用）。
func (s *Service) EntriesRange(ctx context.Context, id string, after, upTo int64, limit int) ([]Entry, error) {
	rows, err := s.st.Q.ListEntriesAfter(ctx, db.ListEntriesAfterParams{ThreadID: id, AfterSeq: after, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(rows))
	for _, e := range rows {
		if e.Seq > upTo {
			break
		}
		out = append(out, entryFromDB(e))
	}
	return out, nil
}

// Snapshot 是 WS `thread.snapshot` 消息。
type Snapshot struct {
	Type        string        `json:"type"`
	ThreadID    string        `json:"threadId"`
	Thread      apigen.Thread `json:"thread"`
	Orb         *apigen.Orb   `json:"orb,omitempty"`
	Agent       AgentSnapshot `json:"agent"`
	LeafEntryID *string       `json:"leafEntryId,omitempty"`
	LastSeq     int64         `json:"lastSeq"`
}

// AgentSnapshot 对应规格 AgentLiveState（帧保持原始字节）。
type AgentSnapshot struct {
	Status       apigen.AgentStatus `json:"status"`
	State        json.RawMessage    `json:"state,omitempty"`
	Queue        json.RawMessage    `json:"queue,omitempty"`
	PendingUI    []json.RawMessage  `json:"pendingUi"`
	PlanProposal json.RawMessage    `json:"planProposal,omitempty"`
	Subagents    []json.RawMessage  `json:"subagents"`
	Live         []json.RawMessage  `json:"live"`
}

// Subscribe 在持有线程锁时构造快照并交给 fn（fn 必须在返回前完成订阅登记，
// 此后该线程的全部事件都晚于快照到达 Sink）。
func (s *Service) Subscribe(ctx context.Context, id string, fn func(Snapshot)) error {
	t, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	row, err := s.st.Q.GetThread(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	api, err := s.threadAPI(ctx, row)
	if err != nil {
		return err
	}
	snap := Snapshot{Type: "thread.snapshot", ThreadID: id, Thread: api, LeafEntryID: t.leaf, LastSeq: t.lastSeq}
	if t.orbID != "" {
		if o, err := s.st.Q.GetOrb(ctx, t.orbID); err == nil {
			a := s.orbAPI(ctx, o)
			snap.Orb = &a
		}
	}
	l := t.live
	snap.Agent = AgentSnapshot{
		Status: t.agentStatus, State: l.State, Queue: l.Queue, PlanProposal: l.PlanProposal,
		PendingUI: nonNilFrames(l.PendingUI()), Subagents: nonNilFrames(l.Subagents()), Live: nonNilFrames(l.LiveFrames()),
	}
	fn(snap)
	return nil
}

func nonNilFrames(f []json.RawMessage) []json.RawMessage {
	if f == nil {
		return []json.RawMessage{}
	}
	return f
}

// ---- fork ----

// Fork 对应 `POST threads/{id}/fork`（H4.5）。env = "share" 共用 orb；"copy" 快照出副本 orb。
func (s *Service) Fork(ctx context.Context, srcID string, entryID *string, env string) (apigen.Thread, error) {
	src, err := s.get(ctx, srcID)
	if err != nil {
		return apigen.Thread{}, err
	}
	row, err := s.st.Q.GetThread(ctx, srcID)
	if err != nil {
		return apigen.Thread{}, err
	}
	src.mu.Lock()
	orbID := src.orbID
	entry := ""
	if entryID != nil && *entryID != "" {
		entry = *entryID
	} else if src.leaf != nil {
		entry = *src.leaf
	}
	src.mu.Unlock()
	if orbID == "" {
		return apigen.Thread{}, errf("no_orb", "source thread has no orb")
	}
	if entry == "" {
		return apigen.Thread{}, errf("invalid", "source thread has no entries to fork from")
	}
	newID := ids.Thread()
	child := db.CreateThreadParams{
		ID: newID, ProjectID: row.ProjectID, Title: row.Title, TitleSource: "auto", State: StateProvisioning,
		ParentThreadID: &row.ID, ForkedFromEntryID: &entry, SessionDir: sessionDirOf(newID),
		Model: row.Model, ThinkingLevel: row.ThinkingLevel, Color: row.Color,
	}
	switch env {
	case "share":
		return s.forkShare(ctx, src, child, orbID, entry)
	case "copy":
		// 只有显式给了 entryId 才在副本上再 fork；否则副本即完整会话
		forkAt := ""
		if entryID != nil {
			forkAt = *entryID
		}
		return s.forkCopy(ctx, src, child, orbID, forkAt)
	}
	return apigen.Thread{}, errf("invalid", "env must be copy or share")
}

// forkShare：源 agent fork_from 写出新会话文件 → fs.move 到新线程会话目录 → agent.start。
// 回退（计划 Assumptions）：fs.move 失败，或新 agent 未能打开该文件就退出时，
// 改为以源会话目录的副本启动（fs.copyTree）再发 fork{entryId}。
func (s *Service) forkShare(ctx context.Context, src *thread, child db.CreateThreadParams, orbID, entry string) (apigen.Thread, error) {
	frame, _ := json.Marshal(map[string]string{"type": "fork_from", "entryId": entry})
	_, data, err := s.call(ctx, src, frame)
	if err != nil {
		return apigen.Thread{}, err
	}
	var res struct {
		SessionPath string `json:"sessionPath"`
	}
	if json.Unmarshal(data, &res) != nil || res.SessionPath == "" {
		return apigen.Thread{}, errf("agent_error", "fork_from returned no sessionPath")
	}
	ctl, ok := s.orbs.Ctl(orbID)
	if !ok {
		return apigen.Thread{}, errf("orb_unavailable", "orb link is not connected")
	}
	base := path.Base(res.SessionPath)
	child.OrbID = &orbID
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	moveErr := ctl.Call(cctx, "fs.move", apigen.LinkCtlFsMoveParams{From: res.SessionPath, To: child.SessionDir + "/" + base}, nil)
	cancel()
	if moveErr == nil {
		// fork_from 的产物写在源会话目录里，源 tailer 可能已在移走前读到它
		s.dropFile(ctx, src, base)
	} else {
		slog.Warn("threads: fork share 移动会话文件失败，改用目录副本", "thread", src.id, "err", moveErr)
	}
	if err := s.insertThread(ctx, child, nil); err != nil {
		return apigen.Thread{}, err
	}
	t, err := s.get(ctx, child.ID)
	if err != nil {
		return apigen.Thread{}, err
	}
	t.mu.Lock()
	if moveErr != nil {
		t.copyFrom, t.forkEntry = src.id, entry
	}
	waiter := make(chan apigen.AgentStatus, 1)
	t.readyWaiters = append(t.readyWaiters, waiter)
	t.mu.Unlock()
	out, err := s.Get(ctx, child.ID)
	if err != nil {
		return apigen.Thread{}, err
	}
	s.out().IndexUpsert(out)
	s.go_(func(ctx context.Context) {
		s.startAgent(ctx, child.ID)
		if moveErr != nil {
			return
		}
		select {
		case st := <-waiter:
			if st == apigen.AgentStatusReady {
				return
			}
		case <-time.After(s.cfg.AgentStartTimeout):
			return
		case <-ctx.Done():
			return
		}
		s.forkShareFallback(ctx, t, src.id, orbID, base, entry)
	})
	return out, nil
}

// forkShareFallback：新 agent 打不开移入的会话文件 → 停掉它、移走该文件、清掉已入库条目，
// 再以源会话目录副本启动并发 fork{entryId}。
func (s *Service) forkShareFallback(ctx context.Context, t *thread, srcID, orbID, base, entry string) {
	slog.Warn("threads: fork share 的新 agent 未能就绪，改用目录副本", "thread", t.id)
	ctl, ok := s.orbs.Ctl(orbID)
	if !ok {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_ = ctl.Call(cctx, "agent.stop", apigen.LinkCtlAgentStopParams{ThreadID: t.id, GraceSeconds: 5}, nil)
	trash := fmt.Sprintf("~/.oryn/trash/%s-%s", t.id, base)
	if err := ctl.Call(cctx, "fs.move", apigen.LinkCtlFsMoveParams{From: t.sessionDir + "/" + base, To: trash}, nil); err != nil {
		var ce *link.CtlError
		if !errors.As(err, &ce) {
			return
		}
	}
	s.dropFile(ctx, t, base)
	t.mu.Lock()
	t.copyFrom, t.forkEntry, t.failed = srcID, entry, false
	t.live.Reset()
	t.mu.Unlock()
	s.startAgent(ctx, t.id)
}

// forkCopy：源 orb 快照 → 以快照新建 orb → 新 orb 就绪后 fs.copyTree 源会话目录 → agent.start →
// 给了 entryId 时向新 agent 发 fork{entryId}。快照与建 orb 在后台进行，接口立即返回新线程。
func (s *Service) forkCopy(ctx context.Context, src *thread, child db.CreateThreadParams, orbID, entry string) (apigen.Thread, error) {
	srcOrb, err := s.st.Q.GetOrb(ctx, orbID)
	if err != nil {
		return apigen.Thread{}, err
	}
	if err := s.insertThread(ctx, child, nil); err != nil {
		return apigen.Thread{}, err
	}
	t, err := s.get(ctx, child.ID)
	if err != nil {
		return apigen.Thread{}, err
	}
	t.mu.Lock()
	t.copyFrom, t.forkEntry = src.id, entry
	t.mu.Unlock()
	out, err := s.Get(ctx, child.ID)
	if err != nil {
		return apigen.Thread{}, err
	}
	s.out().IndexUpsert(out)
	project := ""
	if srcOrb.ProjectID != nil {
		project = *srcOrb.ProjectID
	}
	s.go_(func(ctx context.Context) {
		snap, err := s.orbs.Snapshot(ctx, orbID)
		if err != nil {
			slog.Warn("threads: fork copy 快照失败", "orb", orbID, "err", err)
			s.markFailed(ctx, t)
			return
		}
		o, err := s.orbs.Create(ctx, OrbCreate{Type: srcOrb.Type, ProjectID: project, SnapshotOf: orbID, SnapshotID: snap})
		if err != nil {
			slog.Warn("threads: fork copy 新建 orb 失败", "err", err)
			s.markFailed(ctx, t)
			return
		}
		t.mu.Lock()
		t.orbID = o.ID
		_ = s.st.Q.SetThreadOrb(ctx, db.SetThreadOrbParams{OrbID: &o.ID, UpdatedAt: s.now(), ID: t.id})
		s.publish(ctx, t.id)
		t.mu.Unlock()
		if s.orbState(o.ID) == orbStateReady {
			s.startAgent(ctx, t.id)
		}
	})
	return out, nil
}
