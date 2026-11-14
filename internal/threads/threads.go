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
	"github.com/Lynricsy/Oryn/internal/orbs"
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
	// Attachments 是已上传附件的 id：建线程时绑定，首条提示词在 orb 与 agent 就绪、附件投递之后才下发。
	Attachments []string
	// BrowserProfileID 是新 orb 选用的浏览器 Profile（M8）：nil = 项目默认；空串 = 不用；否则为 bpf_…。
	// 只对新建 orb 有效；挂到已有 orb 时给非空值 → invalid。
	BrowserProfileID *string
	// ProfileMode 是选用方式（空 = exclusive）。
	ProfileMode apigen.BrowserProfileMode
	// SpaceID 非空时新线程加入该 Space（M9；须存在）。
	SpaceID string
	// ParentThreadID 是发起它的线程（协调对话或 orb 内 agent，M9）。
	ParentThreadID string
	// ScheduleID / ScheduleRun 记录创建它的「新建线程」定时与运行序号（M9）。
	ScheduleID  string
	ScheduleRun int64
	// Tag 非空时跟踪首条提示词的终态：恰好得到一次 Observer.TurnEnded（契约 C1）；空 = 不跟踪。
	Tag string
}

// Create 按 H4.3 新建线程：建线程（provisioning）→ 需要时建 orb 并入队 spawn → orb ready 后 agent.start → 首条 prompt。
func (s *Service) Create(ctx context.Context, p CreateParams) (apigen.Thread, error) {
	if strings.TrimSpace(p.Prompt) == "" && len(p.Images) == 0 && len(p.Attachments) == 0 {
		return apigen.Thread{}, errf("invalid", "prompt, images or attachments required")
	}
	if len(p.Attachments) > 0 {
		if s.attach == nil {
			return apigen.Thread{}, errf("invalid", "attachments are not supported")
		}
		if len(p.Attachments) > maxAttachmentsPerMessage {
			return apigen.Thread{}, errf("invalid", fmt.Sprintf("at most %d attachments per message", maxAttachmentsPerMessage))
		}
		// 预检在建 orb 之前：附件不存在或属于别的线程时不白建 orb
		if err := s.attach.Check(ctx, p.Attachments); err != nil {
			return apigen.Thread{}, asThreadErr(err)
		}
	}
	if p.ProjectID != "" {
		if _, err := s.st.Q.GetProject(ctx, p.ProjectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return apigen.Thread{}, errf("not_found", "project not found")
			}
			return apigen.Thread{}, err
		}
	}
	if p.Provider == orbs.HostProviderName {
		return apigen.Thread{}, errf(CodeCoordinatorRestricted, "provider "+orbs.HostProviderName+" is reserved for coordinator conversations")
	}
	if p.SpaceID != "" {
		if _, err := s.st.Q.GetSpace(ctx, p.SpaceID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return apigen.Thread{}, errf("space_not_found", "space not found")
			}
			return apigen.Thread{}, err
		}
	}
	explicitProfile := p.BrowserProfileID != nil && *p.BrowserProfileID != ""
	if p.OrbID != "" && explicitProfile {
		return apigen.Thread{}, errf("invalid", "browserProfileId cannot be set when attaching to an existing orb")
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
		if o.Coordinator {
			return apigen.Thread{}, errf(CodeCoordinatorRestricted, "threads cannot be attached to the coordinator orb")
		}
	} else {
		var profileID string
		switch {
		case s.profiles != nil:
			id, err := s.profiles.ResolveForNewOrb(ctx, p.BrowserProfileID, p.ProjectID, p.ProfileMode)
			if err != nil {
				return apigen.Thread{}, asThreadErr(err)
			}
			profileID = id
		case explicitProfile:
			return apigen.Thread{}, errf("invalid", "browser profiles are not supported")
		}
		o, err := s.orbs.Create(ctx, OrbCreate{
			Provider: p.Provider, Type: p.Type, ProjectID: p.ProjectID, BrowserProfileID: profileID, ProfileMode: string(p.ProfileMode),
		})
		if err != nil {
			return apigen.Thread{}, errf("orb_create_failed", err.Error())
		}
		orb = o
	}

	id := ids.Thread()
	row := db.CreateThreadParams{
		Kind: KindWork, ID: id, OrbID: &orb.ID, TitleSource: "auto", State: StateProvisioning, SessionDir: sessionDirOf(id),
		Model: modelString(p.Model), ShortCode: shortCode(id),
	}
	if p.SpaceID != "" {
		row.SpaceID = &p.SpaceID
	}
	if p.ParentThreadID != "" {
		row.ParentThreadID = &p.ParentThreadID
	}
	if p.ScheduleID != "" {
		row.ScheduleID, row.ScheduleRun = &p.ScheduleID, &p.ScheduleRun
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
	prompt := map[string]any{"type": "prompt", "message": p.Prompt}
	if len(p.Images) > 0 {
		prompt["images"] = p.Images
	}
	if len(p.Attachments) > 0 {
		// 随首批命令持久化：重启后仍按「投递后再下发」续作（见 queueStartupFrameLocked）
		prompt[orynAttachmentsKey] = p.Attachments
	}
	frame, _ := json.Marshal(prompt)
	frames := [][]byte{frame}
	if p.Title != "" {
		f, _ := json.Marshal(map[string]string{"type": "set_session_name", "name": p.Title})
		frames = append(frames, f)
	}
	// 首批命令与线程行同一事务落库，应答 201 之前即已持久：重启不会丢掉已受理的提示词（CP5）
	startup := startupFrames(frames...)
	if err := s.insertThread(ctx, row, p.Labels, func(q *db.Queries) error {
		if err := q.SetThreadStartupFrames(ctx, db.SetThreadStartupFramesParams{StartupFrames: startup, ID: id}); err != nil {
			return err
		}
		if len(p.Attachments) > 0 {
			return s.attach.BindTx(ctx, q, id, p.Attachments)
		}
		return nil
	}); err != nil {
		return apigen.Thread{}, asThreadErr(err)
	}
	// 加载线程时按 startup_frames 排队（见 get / queueStartupLocked）
	t, err := s.get(ctx, id)
	if err != nil {
		return apigen.Thread{}, err
	}
	t.mu.Lock()
	// orb 先于线程行创建：在此之前到达的 orb 事件（例如 clone 立刻失败 → failed）找不到本线程，
	// 绑定后补算一次状态。事件处理先写 orb 状态缓存再列线程，两种先后顺序都不会漏。
	s.recompute(ctx, t)
	t.mu.Unlock()
	out, err := s.Get(ctx, id)
	if err != nil {
		return apigen.Thread{}, err
	}
	s.out().IndexUpsert(out)
	if p.OrbID != "" {
		s.wake(id, orb.ID)
	} else if s.orbState(orb.ID) == orbStateReady {
		s.scheduleStart(id)
	}
	return out, nil
}

// insertThread 在一个事务里写入线程行、标签与 extra（持久化的续作）。
func (s *Service) insertThread(ctx context.Context, row db.CreateThreadParams, labels []string, extra func(q *db.Queries) error) error {
	now := s.now()
	row.StateChangedAt, row.CreatedAt, row.UpdatedAt = now, now, now
	return s.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateThread(ctx, row); err != nil {
			return err
		}
		if extra != nil {
			if err := extra(q); err != nil {
				return err
			}
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
	// Kind 为空时只列工作线程；"all" = 全部种类；否则只列该种类（M9）。
	Kind string
	// Space 非空时只列属于该 Space 的线程（M9）。
	Space string
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
	shareRows, err := s.st.Q.ListActiveThreadShareCounts(ctx)
	if err != nil {
		return nil, err
	}
	shares := make(map[string]int, len(shareRows))
	for _, r := range shareRows {
		shares[r.ThreadID] = int(r.N)
	}
	schedRows, err := s.st.Q.ListScheduleSummaries(ctx)
	if err != nil {
		return nil, err
	}
	schedules := make(map[string]*apigen.ThreadScheduleSummary, len(schedRows))
	for _, r := range schedRows {
		schedules[r.ThreadID] = &apigen.ThreadScheduleSummary{Enabled: r.Enabled, NextRunAt: r.NextRunAt}
	}
	archived := false
	if f.Archived != nil {
		archived = *f.Archived
	}
	kind := f.Kind
	if kind == "" {
		kind = KindWork
	}
	q := strings.ToLower(f.Q)
	reasons := map[string]*apigen.ThreadError{}
	out := make([]apigen.Thread, 0, len(all))
	for _, t := range all {
		if (t.ArchivedAt != nil) != archived || (f.State != "" && t.State != f.State) {
			continue
		}
		if (kind != "all" && t.Kind != kind) || (f.Space != "" && (t.SpaceID == nil || *t.SpaceID != f.Space)) {
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
		api.PortalURLTemplate = s.portalTemplate(t.ID)
		api.Error = s.threadError(ctx, t, reasons)
		if c, ok := children[t.ID]; ok {
			api.ChildCount, api.ChildDoneCount = &c[0], &c[1]
		}
		api.Activity = s.activityPtr(t.ID)
		if n := shares[t.ID]; n > 0 {
			api.PublicShares = &n
		}
		api.Schedule = schedules[t.ID]
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
	live, err := s.List(ctx, ListFilter{Kind: "all"})
	if err != nil {
		return nil, err
	}
	yes := true
	arch, err := s.List(ctx, ListFilter{Archived: &yes, Kind: "all"})
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
	var newSpace *string // p.SpaceID 给出时的新值（nil = 移出）
	if p.SpaceID != nil {
		if t.kind != KindWork {
			return apigen.Thread{}, errf(CodeCoordinatorRestricted, "only work threads can join a Space")
		}
		if *p.SpaceID != "" {
			if _, err := s.st.Q.GetSpace(ctx, *p.SpaceID); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return apigen.Thread{}, errf("space_not_found", "space not found")
				}
				return apigen.Thread{}, err
			}
			newSpace = p.SpaceID
		}
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
	shipped := row.ShippedAt
	if p.Shipped != nil {
		if *p.Shipped {
			shipped = &now
			if m.ArchivedAt == nil {
				// 标记为已交付即同时归档
				m.ArchivedAt = &now
			}
		} else {
			shipped = nil
		}
	}
	if p.Unread != nil {
		m.Unread = *p.Unread
	}
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.UpdateThreadMeta(ctx, m); err != nil {
			return err
		}
		if p.SpaceID != nil {
			if _, err := q.SetThreadSpace(ctx, db.SetThreadSpaceParams{SpaceID: newSpace, UpdatedAt: now, ID: id}); err != nil {
				return err
			}
		}
		if p.Shipped != nil {
			if err := q.SetThreadShipped(ctx, db.SetThreadShippedParams{ShippedAt: shipped, UpdatedAt: now, ID: id}); err != nil {
				return err
			}
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
		if p.SpaceID != nil {
			t.spaceID = ""
			if newSpace != nil {
				t.spaceID = *newSpace
			}
		}
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

// Delete 删除线程（条目随之删除；orb 保留）。agent 若在运行则先停止。Space 的聊天线程不能直接删除（删 Space）。
func (s *Service) Delete(ctx context.Context, id string) error {
	t, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	if t.kind == KindSpace {
		return errf("space_thread", "delete the Space instead of its chat thread")
	}
	return s.deleteThread(ctx, t)
}

// DeleteSpaceThread 删除 Space 的聊天线程（只供 internal/coord 删除 Space 时调用）。
func (s *Service) DeleteSpaceThread(ctx context.Context, id string) error {
	t, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	return s.deleteThread(ctx, t)
}

func (s *Service) deleteThread(ctx context.Context, t *thread) error {
	id := t.id
	t.mu.Lock()
	orbID := t.orbID
	if t.agent != nil {
		t.agent.close()
		t.agent = nil
	}
	s.failPendingLocked(t, "thread deleted", true)
	cancelHostCallsLocked(t)
	t.queue = nil
	t.mu.Unlock()
	if ctl, ok := s.orbs.Ctl(orbID); ok && orbID != "" {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := ctl.Call(cctx, "agent.stop", apigen.LinkCtlAgentStopParams{ThreadID: id, GraceSeconds: 5}, nil); err != nil {
			slog.Debug("threads: agent.stop 失败", "thread", id, "err", err)
		}
		cancel()
	}
	if s.attach != nil {
		// 先删附件文件与 orb 内副本（行随线程级联删除）；失败时孤儿文件由附件服务的清理回收
		if err := s.attach.DeleteThread(ctx, id); err != nil {
			slog.Warn("threads: 删除线程附件失败", "thread", id, "err", err)
		}
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
	// TranscriptRev 是当前转录版本；FullReplay 由 hub 按客户端的版本决定（见 realtime.subscribe）。
	TranscriptRev int64 `json:"transcriptRev"`
	FullReplay    bool  `json:"fullReplay"`
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
	snap := Snapshot{Type: "thread.snapshot", ThreadID: id, Thread: api, LeafEntryID: t.leaf, LastSeq: t.lastSeq, TranscriptRev: t.transcriptRev}
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
	if src.kind != KindWork {
		return apigen.Thread{}, errf(CodeCoordinatorRestricted, "coordinator conversations cannot be forked")
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
		Kind: "work", ID: newID, ProjectID: row.ProjectID, Title: row.Title, TitleSource: "auto", State: StateProvisioning,
		ParentThreadID: &row.ID, ForkedFromEntryID: &entry, SessionDir: sessionDirOf(newID),
		Model: row.Model, ThinkingLevel: row.ThinkingLevel, Color: row.Color, ShortCode: shortCode(newID),
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
	// fork_from 把新会话文件写进源会话目录：在途期间暂存新出现的主文件，免得产物被当成源线程的新会话入库
	src.mu.Lock()
	src.forking++
	src.mu.Unlock()
	_, data, err := s.call(ctx, src, frame)
	var res struct {
		SessionPath string `json:"sessionPath"`
	}
	if err == nil && (json.Unmarshal(data, &res) != nil || res.SessionPath == "") {
		err = errf("agent_error", "fork_from returned no sessionPath")
	}
	product := ""
	if err == nil {
		product = path.Base(res.SessionPath)
	}
	src.mu.Lock()
	s.endForkLocked(ctx, src, product)
	src.mu.Unlock()
	if err != nil {
		return apigen.Thread{}, err
	}
	ctl, ok := s.orbs.Ctl(orbID)
	if !ok {
		return apigen.Thread{}, errf("orb_unavailable", "orb link is not connected")
	}
	base := product
	child.OrbID = &orbID
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	moveErr := ctl.Call(cctx, "fs.move", apigen.LinkCtlFsMoveParams{From: res.SessionPath, To: child.SessionDir + "/" + base}, nil)
	cancel()
	if moveErr != nil {
		slog.Warn("threads: fork share 移动会话文件失败，改用目录副本", "thread", src.id, "err", moveErr)
	}
	var extra func(q *db.Queries) error
	if moveErr != nil {
		// 回退：以源会话目录副本启动再 fork；阶段随线程行落库，重启后照样续作（加载时恢复 copyFrom / forkEntry）
		op := forkOp{Stage: forkStageCopy, Src: src.id, SrcOrb: orbID, ForkAt: entry}
		extra = func(q *db.Queries) error {
			return q.SetThreadForkOp(ctx, db.SetThreadForkOpParams{ForkOp: op.bytes(), ID: child.ID})
		}
	}
	if err := s.insertThread(ctx, child, nil, extra); err != nil {
		return apigen.Thread{}, err
	}
	t, err := s.get(ctx, child.ID)
	if err != nil {
		return apigen.Thread{}, err
	}
	t.mu.Lock()
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
	_ = s.setForkOpLocked(ctx, t, &forkOp{Stage: forkStageCopy, Src: srcID, SrcOrb: orbID, ForkAt: entry})
	s.resetLiveLocked(t)
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
	op := forkOp{Stage: forkStageSnapshot, Src: src.id, SrcOrb: orbID, ForkAt: entry, Type: srcOrb.Type}
	if srcOrb.ProjectID != nil {
		op.ProjectID = *srcOrb.ProjectID
	}
	// 后台阶段先落库再应答：重启后由 recoverForks 从快照阶段续作（CP5）
	if err := s.insertThread(ctx, child, nil, func(q *db.Queries) error {
		return q.SetThreadForkOp(ctx, db.SetThreadForkOpParams{ForkOp: op.bytes(), ID: child.ID})
	}); err != nil {
		return apigen.Thread{}, err
	}
	t, err := s.get(ctx, child.ID)
	if err != nil {
		return apigen.Thread{}, err
	}
	out, err := s.Get(ctx, child.ID)
	if err != nil {
		return apigen.Thread{}, err
	}
	s.out().IndexUpsert(out)
	s.go_(func(ctx context.Context) { s.runForkCopy(ctx, t, op) })
	return out, nil
}
