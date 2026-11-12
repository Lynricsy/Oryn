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
	orbStates map[string]string
	touched   map[string]time.Time // orb → 上次 TouchAgent（节流）
	blobs     map[string]chan struct{}

	nextID atomic.Uint64

	events eventQueue
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
		orbStates: map[string]string{},
		touched:   map[string]time.Time{},
		blobs:     map[string]chan struct{}{},
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
	orbs.SetQuiescence(s.Quiescent)
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.threads[id]; t != nil {
		return t, nil
	}
	t = newThread(row)
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
	st, ok := s.orbStates[orbID]
	s.mu.Unlock()
	if ok {
		return st
	}
	o, err := s.st.Q.GetOrb(context.Background(), orbID)
	if err != nil {
		return ""
	}
	s.mu.Lock()
	if _, ok := s.orbStates[orbID]; !ok {
		s.orbStates[orbID] = o.State
	}
	st = s.orbStates[orbID]
	s.mu.Unlock()
	return st
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

// Quiescent 供 orbs 判定可否冻结 / 停机：该 orb 上全部线程已 settled、无待答 UI、无计划提案、无排队命令。
func (s *Service) Quiescent(orbID string) bool {
	rows, err := s.st.Q.ListThreadsByOrb(context.Background(), &orbID)
	if err != nil {
		return false
	}
	for _, r := range rows {
		t, err := s.get(context.Background(), r.ID)
		if err != nil {
			continue
		}
		t.mu.Lock()
		ok := t.settled && t.live.Quiescent() && len(t.queue) == 0 && len(t.pending) == 0
		t.mu.Unlock()
		if !ok {
			return false
		}
	}
	return true
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
	state := ev.Orb.State
	if state == "" {
		state = ev.To
	}
	s.mu.Lock()
	s.orbStates[ev.OrbID] = state
	s.mu.Unlock()

	rows, err := s.st.Q.ListThreadsByOrb(ctx, &ev.OrbID)
	if err != nil {
		slog.Warn("threads: 读取 orb 线程失败", "orb", ev.OrbID, "err", err)
		return
	}
	o := ev.Orb
	if o.ID == "" {
		if o, err = s.st.Q.GetOrb(ctx, ev.OrbID); err != nil {
			return
		}
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
		if !s.recompute(ctx, t) && failedEdge {
			s.publish(ctx, t.id)
		}
		t.mu.Unlock()
	}
	if state == orbStateReady && (ev.Kind == OrbEventLinkUp || ev.To == orbStateReady) {
		for _, r := range rows {
			if r.ArchivedAt != nil {
				continue
			}
			id := r.ID
			s.go_(func(ctx context.Context) { s.startAgent(ctx, id) })
		}
	}
}
