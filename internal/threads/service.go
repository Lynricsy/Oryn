// Package threads 是控制面的线程服务（计划 C4、D、H4.3、H4.5）：线程创建与 fork、
// orb 上 agent 的启动、omp 命令路由、实时缓冲与状态推导、未读、条目入库与派生写入、blob 拉取。
//
// 并发约定：
//   - 每个线程一把锁（thread.mu），保护该线程的内存状态；对 Sink 的回调都在持有该锁时发出，
//     因此同一线程的快照、条目、帧在订阅者看来严格有序。
//   - 锁顺序：thread.mu → Service.mu；持有 thread.mu 时不得调用 Orbs 的方法或 ctl.Call，
//     也不得在 store.Tx 回调里获取 thread.mu。
package threads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// Sink 接收线程服务产生的实时事件（由 internal/realtime 的 hub 实现）。
// 回调在持有线程锁时同步调用，实现方必须只做入队，不得回调 Service。
type Sink interface {
	// IndexUpsert / IndexRemove 对应 index.patch。
	IndexUpsert(t apigen.Thread)
	IndexRemove(threadID string)
	// Entries 是新入库（或内容变化）的条目，已按 ≤200 条分批。
	Entries(threadID string, entries []Entry, leafEntryID *string, lastSeq int64)
	// Frame 是实时 omp 帧（response 以外）。
	Frame(threadID string, frame json.RawMessage)
	UIResolved(threadID, requestID string)
	LiveReset(threadID string)
	// TranscriptReset 表示线程转录被重写（旧条目被删除 / 改写）：客户端须丢弃缓存并以新版本重新订阅。
	TranscriptReset(threadID string, transcriptRev int64)
	// PlanResolved 表示计划提案已被成功处理：所有客户端清除 planProposal。
	PlanResolved(threadID string)
	Orb(o apigen.Orb)
}

// Config 是线程服务参数。
type Config struct {
	// DataDir 是控制面数据目录；blob 存于 <DataDir>/blobs/<前 2 位>/<sha256>。
	DataDir string
	// WakeOnFocus 对应 idle.wake_on_focus。
	WakeOnFocus bool
	// AgentStartTimeout 是 agent.start 的应答超时（默认 60s）。
	AgentStartTimeout time.Duration
	// CallTimeout 是内部 omp 命令（fork_from 等）的超时（默认 2 分钟，含唤醒时间）。
	CallTimeout time.Duration
}

// Service 是线程服务。
type Service struct {
	cfg  Config
	st   *store.Store
	orbs Orbs
	sink atomic.Pointer[sinkBox]

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu        sync.Mutex
	threads   map[string]*thread
	focus     map[string]string // 连接 id → 正在看的线程
	orbStates map[string]orbCache
	touched   map[string]time.Time  // orb → 上次 TouchAgent（节流）
	blobs     map[string]*blobFetch // sha256 → 在途的 blob.fetch

	nextID atomic.Uint64

	events eventQueue

	// afterIngestCommit（仅测试）在条目入库事务提交后、内存状态更新前调用。
	afterIngestCommit func()
}

// orbCache 是 orb 状态缓存；seq 为得出该状态的 orb_events.id，用于丢弃乱序到达的过时事件（CP9）。
type orbCache struct {
	state string
	seq   int64
}

// blobFetch 是一次在途的 blob.fetch：只接受该 orb 为它打开的 blob 流（M5）。
type blobFetch struct {
	orbID   string
	done    chan struct{}
	claimed bool // 已有 blob 流在接收（由 Service.mu 保护）
}

type sinkBox struct{ Sink }

// nopSink 在未设置 Sink 时丢弃事件。
type nopSink struct{}

func (nopSink) IndexUpsert(apigen.Thread)               {}
func (nopSink) IndexRemove(string)                      {}
func (nopSink) Entries(string, []Entry, *string, int64) {}
func (nopSink) Frame(string, json.RawMessage)           {}
func (nopSink) UIResolved(string, string)               {}
func (nopSink) LiveReset(string)                        {}
func (nopSink) TranscriptReset(string, int64)           {}
func (nopSink) PlanResolved(string)                     {}
func (nopSink) Orb(apigen.Orb)                          {}

// New 创建线程服务并向 orbs 注册 entries.* / agent.* 处理器、agent 与 blob 流处理器、事件订阅与空闲判定。
func New(cfg Config, st *store.Store, orbs Orbs) *Service {
	if cfg.AgentStartTimeout <= 0 {
		cfg.AgentStartTimeout = 60 * time.Second
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = 2 * time.Minute
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		cfg: cfg, st: st, orbs: orbs, ctx: ctx, cancel: cancel,
		threads:   map[string]*thread{},
		focus:     map[string]string{},
		orbStates: map[string]orbCache{},
		touched:   map[string]time.Time{},
		blobs:     map[string]*blobFetch{},
	}
	s.sink.Store(&sinkBox{nopSink{}})
	s.events.init()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.events.run(ctx, s.handleOrbEvent)
	}()

	orbs.HandleRequest("entries.cursors", s.onCursors)
	orbs.HandleRequest("entries.append", s.onAppend)
	orbs.HandleNotify("entries.reset", s.onReset)
	orbs.HandleNotify("entries.reset_done", s.onResetDone)
	orbs.HandleNotify("agent.status", s.onAgentStatus)
	orbs.HandleNotify("agent.gap", s.onAgentGap)
	orbs.HandleStream("agent", s.onAgentStream)
	orbs.HandleStream("blob", s.onBlobStream)
	orbs.Subscribe(func(ev OrbEvent) { s.events.push(ev) })
	orbs.SetQuiescence(s.TryQuiesce)
	// 控制面重启：续作未完成的 fork（快照 / 建 orb 阶段没有 orb 事件会驱动它们）
	s.go_(s.recoverForks)
	return s
}

// SetSink 设置实时事件接收方（realtime hub）。
func (s *Service) SetSink(k Sink) { s.sink.Store(&sinkBox{k}) }

func (s *Service) out() Sink { return s.sink.Load().Sink }

// Close 停止后台任务。
func (s *Service) Close() {
	s.cancel()
	s.wg.Wait()
}

func (s *Service) now() int64 { return time.Now().UnixMilli() }

// go_ 在服务生命周期内启动后台任务。
func (s *Service) go_(fn func(ctx context.Context)) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn(s.ctx)
	}()
}

// thread 是单个线程的内存状态。
type thread struct {
	id         string
	sessionDir string

	mu          sync.Mutex
	orbID       string
	live        *Live
	agentStatus apigen.AgentStatus
	agent       *agentConn
	queue       [][]byte
	pending     map[string]*pendingCmd
	settled     bool
	state       string
	unread      bool
	leaf        *string
	lastSeq     int64
	resetting   map[string]bool
	ignored     map[string]bool // fork_from 产物：移出前被 tail 到的文件，丢弃
	// forking > 0 时有 fork_from 在途：其间新出现的主会话文件暂存在 held，不入库（见 onAppend / endForkLocked）。
	forking  int
	held     map[string][]apigen.LinkCtlEntriesAppendParams
	starting bool
	failed   bool
	model    *string
	thinking *string
	// copyFrom 非空时，agent 首次启动前先 fs.copyTree ~/.oryn/threads/<copyFrom> → 本线程会话目录（fork copy）。
	copyFrom string
	// forkEntry 非空时，agent 就绪后向其发送 fork{entryId}（fork copy / 回退路径）。
	forkEntry string
	// readyWaiters 在 agent 就绪或退出时收到通知（fork share 回退检测用）。
	readyWaiters []chan apigen.AgentStatus
	// forkOp 是持久化的 fork 续作阶段（threads.fork_op），nil = 无。
	forkOp *forkOp
	// forkRunning 为真时 runForkCopy 正在推进本线程（启动时的恢复与新 fork 不会重复执行）。
	forkRunning bool
	// unsynced 为真表示运行 / 静止标志可能与 agent 实际状态不符（控制面重启时线程正忙、agent.gap、agent 退出后待重启）：
	// 直到下一次 get_state 应答恢复 working / settled 之前，线程不算静止（不得冻结）。
	unsynced bool
	// transcriptRev 是转录版本（threads.transcript_rev）。
	transcriptRev int64
}

const queueLimit = 100

// ErrNotFound 表示线程不存在。
var ErrNotFound = &Error{Code: "not_found", Message: "thread not found"}

// Error 是线程服务返回给 API / WS 的业务错误。
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errf(code, msg string) *Error { return &Error{Code: code, Message: msg} }

// get 返回线程的内存状态，必要时从库中加载。
func (s *Service) get(ctx context.Context, id string) (*thread, error) {
	s.mu.Lock()
	t := s.threads[id]
	s.mu.Unlock()
	if t != nil {
		return t, nil
	}
	row, err := s.st.Q.GetThread(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t = newThread(row)
	if startup := s.pendingStartup(ctx, row); len(startup) > 0 {
		// 线程尚未对外可见：在登记前排好已受理的首批命令，保证它们先于任何其它命令下发
		t.mu.Lock()
		s.queueStartupLocked(t, startup)
		t.mu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur := s.threads[id]; cur != nil {
		return cur, nil
	}
	s.threads[id] = t
	return t, nil
}

func newThread(row db.Thread) *thread {
	t := &thread{
		id: row.ID, sessionDir: row.SessionDir,
		live: NewLive(), agentStatus: apigen.AgentStatusNone,
		pending: map[string]*pendingCmd{}, resetting: map[string]bool{}, ignored: map[string]bool{}, held: map[string][]apigen.LinkCtlEntriesAppendParams{},
		settled: true, state: row.State, unread: row.Unread, leaf: row.LeafEntryID, lastSeq: row.LastSeq,
		model: row.Model, thinking: row.ThinkingLevel,
	}
	if row.OrbID != nil {
		t.orbID = *row.OrbID
	}
	if row.State != StateProvisioning {
		// 控制面重启前 agent 已就绪过
		t.live.AgentReady()
	}
	t.transcriptRev = row.TranscriptRev
	switch row.State {
	case StateWorking:
		// 重启前回合正在进行：agent_start 已在重启前送达、不会重放。先按库里的状态保持 working，
		// 重连后的 get_state（isStreaming / isSettled）给出权威值（CP10）。
		t.live.SetWorking(true)
		t.settled, t.unsynced = false, true
	case StateAwaitingInput:
		// 待答 UI 由 orbd 在新 agent 流上重放；在 get_state 之前同样不算静止
		t.settled, t.unsynced = false, true
	}
	if op := parseForkOp(row.ForkOp); op != nil {
		t.forkOp = op
		switch op.Stage {
		case forkStageCopy:
			t.copyFrom, t.forkEntry = op.Src, op.ForkAt
		case forkStageFork:
			t.forkEntry = op.ForkAt
		}
	}
	return t
}

func (s *Service) forget(id string) {
	s.mu.Lock()
	delete(s.threads, id)
	s.mu.Unlock()
}

// orbState 返回 orb 的当前状态（缓存于事件，缺失时读库）。
func (s *Service) orbState(orbID string) string {
	if orbID == "" {
		return ""
	}
	s.mu.Lock()
	c, ok := s.orbStates[orbID]
	s.mu.Unlock()
	if ok {
		return c.state
	}
	r, err := s.st.Q.GetOrbStateSeq(context.Background(), orbID)
	if err != nil {
		return ""
	}
	s.applyOrbState(orbID, r.State, r.Seq)
	s.mu.Lock()
	c = s.orbStates[orbID]
	s.mu.Unlock()
	return c.state
}

// applyOrbState 以 seq 为序更新 orb 状态缓存：seq 小于已应用值的（过时）不生效，返回是否生效。
func (s *Service) applyOrbState(orbID, state string, seq int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.orbStates[orbID]; ok && seq < c.seq {
		return false
	}
	s.orbStates[orbID] = orbCache{state: state, seq: seq}
	return true
}

func (s *Service) focused(threadID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.focus {
		if id == threadID {
			return true
		}
	}
	return false
}

// agentReady 报告 agent 是否可接收命令。调用方持有 t.mu。
func (t *thread) agentReady() bool {
	return t.agentStatus == apigen.AgentStatusReady && t.agent != nil
}

// status 推导线程状态：所在 orb 为 failed 时一律 error（Thread.error 带原因），其余按 C4 规则。调用方持有 t.mu。
func (s *Service) status(t *thread) string {
	orbState := s.orbState(t.orbID)
	if t.failed || orbState == orbStateFailed {
		return StateError
	}
	orbReady := orbState == orbStateReady && t.agentReady()
	return t.live.Status(orbReady)
}

// recompute 重新推导状态；变化时写库、处理未读并推送 index.patch，返回是否变化。调用方持有 t.mu。
func (s *Service) recompute(ctx context.Context, t *thread) bool {
	st := s.status(t)
	if st == t.state {
		return false
	}
	prev := t.state
	t.state = st
	now := s.now()
	if err := s.st.Q.SetThreadState(ctx, db.SetThreadStateParams{State: st, StateChangedAt: now, ID: t.id}); err != nil {
		slog.Warn("threads: 写入线程状态失败", "thread", t.id, "err", err)
	}
	if prev == StateWorking && st != StateWorking && st != StateProvisioning && !t.unread && !s.focused(t.id) {
		t.unread = true
		_ = s.st.Q.SetThreadUnread(ctx, db.SetThreadUnreadParams{Unread: true, ID: t.id})
	}
	s.publish(ctx, t.id)
	return true
}

// publish 读出线程最新行并推送 index.patch。调用方持有 t.mu（或线程尚未对外可见）。
func (s *Service) publish(ctx context.Context, id string) {
	row, err := s.st.Q.GetThread(ctx, id)
	if err != nil {
		return
	}
	api, err := s.threadAPI(ctx, row)
	if err != nil {
		return
	}
	s.out().IndexUpsert(api)
}

// TryQuiesce 是 orbs 的冻结 / 停机闸门（契约 §1）：按线程 id 顺序锁住该 orb 的全部线程，确认它们都已 settled、
// 无待答 UI / 计划提案、无排队或未决命令、运行状态已同步、没有在启动 agent，然后在仍持锁时调用 commit
// （orbs 的状态 CAS）并返回其结果。命令准入（enqueueLocked）同样持线程锁，因此要么先于本闸门被看到
// （不静止，不冻结），要么晚于 commit——此时缓存已被刷新为 commit 之后的状态，新命令排队并唤醒 orb。
func (s *Service) TryQuiesce(orbID string, commit func() bool) bool {
	ctx := context.Background()
	rows, err := s.st.Q.ListThreadsByOrb(ctx, &orbID)
	if err != nil {
		return false
	}
	ts := make([]*thread, 0, len(rows))
	for _, r := range rows {
		t, err := s.get(ctx, r.ID)
		if err != nil {
			return false
		}
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].id < ts[j].id })
	for _, t := range ts {
		t.mu.Lock()
	}
	defer func() {
		for _, t := range ts {
			t.mu.Unlock()
		}
	}()
	// 列表与加锁之间新绑定到该 orb 的线程不受这些锁保护：再核对一次，有变化则放弃本轮
	again, err := s.st.Q.ListThreadsByOrb(ctx, &orbID)
	if err != nil || len(again) != len(ts) {
		return false
	}
	locked := make(map[string]bool, len(ts))
	for _, t := range ts {
		locked[t.id] = true
	}
	for _, r := range again {
		if !locked[r.ID] {
			return false
		}
	}
	for _, t := range ts {
		if !t.quiescentLocked() {
			return false
		}
	}
	if !commit() {
		return false
	}
	if r, err := s.st.Q.GetOrbStateSeq(ctx, orbID); err == nil {
		s.applyOrbState(orbID, r.State, r.Seq)
	} else {
		slog.Warn("threads: 冻结后读取 orb 状态失败", "orb", orbID, "err", err)
	}
	return true
}

// quiescentLocked 报告线程是否静止（冻结 / 停机的前提）。调用方持有 t.mu。
func (t *thread) quiescentLocked() bool {
	return t.settled && !t.unsynced && !t.starting && t.live.Quiescent() && len(t.queue) == 0 && len(t.pending) == 0
}

// touchAgent 节流地记录 agent 活动（每 orb 至多 5s 一次）。
func (s *Service) touchAgent(orbID string) {
	if orbID == "" {
		return
	}
	s.mu.Lock()
	now := time.Now()
	if now.Sub(s.touched[orbID]) < 5*time.Second {
		s.mu.Unlock()
		return
	}
	s.touched[orbID] = now
	s.mu.Unlock()
	s.go_(func(ctx context.Context) { s.orbs.TouchAgent(ctx, orbID) })
}

// touchUser 记录一次用户交互。
func (s *Service) touchUser(orbID string) {
	if orbID == "" {
		return
	}
	s.go_(func(ctx context.Context) { s.orbs.TouchUser(ctx, orbID) })
}

// ---- orb 事件 ----

// eventQueue 把 orbs 的事件回调转到单个 goroutine 顺序处理（回调可能发生在 orbs 的锁或事务内）。
type eventQueue struct {
	mu   sync.Mutex
	q    []OrbEvent
	wake chan struct{}
}

func (e *eventQueue) init() { e.wake = make(chan struct{}, 1) }

func (e *eventQueue) push(ev OrbEvent) {
	e.mu.Lock()
	e.q = append(e.q, ev)
	e.mu.Unlock()
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *eventQueue) run(ctx context.Context, fn func(context.Context, OrbEvent)) {
	for {
		e.mu.Lock()
		q := e.q
		e.q = nil
		e.mu.Unlock()
		for _, ev := range q {
			fn(ctx, ev)
		}
		if len(q) > 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-e.wake:
		}
	}
}

func (s *Service) handleOrbEvent(ctx context.Context, ev OrbEvent) {
	if ev.Kind == OrbEventSetup {
		// 只有运行记录变化：线程状态不受影响。事件里的 orb 行可能早于随后的状态变化，重读一次再推送，
		// 也不覆盖状态缓存。
		if o, err := s.st.Q.GetOrb(ctx, ev.OrbID); err == nil {
			s.out().Orb(s.orbAPI(ctx, o))
		}
		return
	}
	// 状态缓存只信两个来源：状态变化事件的目标状态，或库里的当前行。link_up / link_down 事件携带的
	// orb 快照可能早于其后的状态变化（例如 link 连上时还在 connecting，事件送达时已 failed），
	// 用它覆盖缓存会把线程推回 provisioning。
	// 状态事件按 Seq 排序：比已应用的旧的事件来自更早提交的变化，丢弃（CP9）。
	var state string
	if ev.Kind == OrbEventState && ev.To != "" {
		if !s.applyOrbState(ev.OrbID, ev.To, ev.Seq) {
			slog.Debug("threads: 丢弃过时的 orb 状态事件", "orb", ev.OrbID, "to", ev.To, "seq", ev.Seq)
			return
		}
		state = ev.To
	} else {
		r, err := s.st.Q.GetOrbStateSeq(ctx, ev.OrbID)
		if err != nil {
			slog.Warn("threads: 读取 orb 失败", "orb", ev.OrbID, "err", err)
			return
		}
		s.applyOrbState(ev.OrbID, r.State, r.Seq)
		state = s.orbState(ev.OrbID)
	}

	rows, err := s.st.Q.ListThreadsByOrb(ctx, &ev.OrbID)
	if err != nil {
		slog.Warn("threads: 读取 orb 线程失败", "orb", ev.OrbID, "err", err)
		return
	}
	// 推送给客户端的 orb 同样取库里的当前行，不用事件携带的（可能过时的）快照
	o, err := s.st.Q.GetOrb(ctx, ev.OrbID)
	if err != nil {
		return
	}
	s.out().Orb(s.orbAPI(ctx, o))
	// 进入或离开 failed 时 Thread.error 随之出现 / 消失，即使线程状态本身没变（例如原本就是 error）也要推送
	failedEdge := ev.Kind == OrbEventState && (ev.To == orbStateFailed || ev.From == orbStateFailed)

	for _, r := range rows {
		t, err := s.get(ctx, r.ID)
		if err != nil {
			continue
		}
		t.mu.Lock()
		switch {
		case state == orbStateDeleted:
			t.orbID = ""
			t.agent = nil
			t.agentStatus = apigen.AgentStatusNone
			t.unsynced = false
			t.live.Reset()
			_ = s.st.Q.SetThreadOrb(ctx, db.SetThreadOrbParams{OrbID: nil, UpdatedAt: s.now(), ID: t.id})
			s.out().LiveReset(t.id)
			s.publish(ctx, t.id)
		case ev.Kind == OrbEventLinkDown:
			// agent 仍在 orbd 中运行；重连后 agent.start 会重新上报 ready
			if t.agentStatus == apigen.AgentStatusReady {
				t.agentStatus = apigen.AgentStatusStarting
			}
		}
		if !orbSuspended(state) && t.agentReady() && len(t.queue) > 0 {
			// orb 暂停期间（或缓存滞后时）就绪的 agent 留下的排队命令：现在可以下发
			s.onReadyLocked(ctx, t)
		}
		if !s.recompute(ctx, t) && failedEdge {
			s.publish(ctx, t.id)
		}
		t.mu.Unlock()
	}
	if state == orbStateReady && (ev.Kind == OrbEventLinkUp || ev.To == orbStateReady) {
		for _, r := range rows {
			// 归档线程不主动启动 agent，除非重启前它正忙（否则其 unsynced 永远不会清除、orb 无法冻结）
			if r.ArchivedAt != nil && r.State != StateWorking && r.State != StateAwaitingInput {
				continue
			}
			id := r.ID
			s.go_(func(ctx context.Context) { s.startAgent(ctx, id) })
		}
	}
}
