// Package orbs 管理 orb 生命周期（计划 E 节）：状态机与 orb_events、spawn / pause / snapshot / delete 任务、
// 30s 空闲评估（冻结 / 停机）、唤醒、connecting 超时；以及 /orb/v1/link 处理器（hello → setup / resume、
// metrics.sample、setup.log / setup.result 与 orb_setup_runs、project.scripts.*、egress 流）。Threads 经本包导出的 Service API 与 View 与 orb 交互。
package orbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// orb 状态（与 apigen.OrbState 取值一致）。
const (
	StateRequested  = "requested"
	StateSpawning   = "spawning"
	StateConnecting = "connecting"
	StateSettingUp  = "setting_up"
	StateResuming   = "resuming"
	StateReady      = "ready"
	StateFreezing   = "freezing"
	StateFrozen     = "frozen"
	StateStopping   = "stopping"
	StateStopped    = "stopped"
	StateWaking     = "waking"
	StateDeleting   = "deleting"
	StateDeleted    = "deleted"
	StateFailed     = "failed"
)

// 暂停方式。
const (
	PauseFreeze = "freeze"
	PauseStop   = "stop"
)

// Event 种类。
const (
	// EventState 表示状态变化（From → To）。
	EventState = "state"
	// EventLinkUp 表示 orbd link 已连上并收到 hello（From == To == 当前状态）。
	EventLinkUp = "link_up"
	// EventLinkDown 表示 link 断开。
	EventLinkDown = "link_down"
	// EventSetup 表示 orb 新增了一条 setup / resume 运行记录（From == To == 当前状态）。
	EventSetup = "setup"
)

var (
	// ErrNotFound 表示 orb 不存在。
	ErrNotFound = errors.New("orbs: not found")
	// ErrInvalidState 表示当前状态不允许该操作。
	ErrInvalidState = errors.New("orbs: operation not allowed in current state")
	// ErrNoProvider 表示无法确定 provider。
	ErrNoProvider = errors.New("orbs: no provider")
	// ErrBadType 表示 orb 类型无效。
	ErrBadType = errors.New("orbs: invalid orb type")
)

// Event 是 orb 状态或 link 变化通知。
//
// Seq 是事件的提交序号：状态事件 = 该次状态变化写入的 orb_events.id（单调递增）；非状态事件（link_up / link_down /
// setup）= 发出时该 orb 已提交的最大 orb_events.id。事件在事务提交之后发出，不同 goroutine 发出的事件可能乱序到达，
// 消费方须丢弃 Seq 小于已应用值的状态事件。
type Event struct {
	OrbID string
	Kind  string
	From  string
	To    string
	Orb   db.Orb
	Seq   int64
}

// Config 是生命周期参数。
type Config struct {
	Idle     config.Idle
	Defaults config.OrbDefaults
	// PublicURL 作为 spawn 任务的 ORYN_URL（provider 会用自己的 orb_url 覆盖）。
	PublicURL string
	// TokenKey 是 orb 令牌派生密钥（LoadOrCreateTokenKey）；为空时用进程内随机密钥（仅测试）。
	TokenKey []byte
	// EvalInterval 是空闲评估周期（默认 30s）。
	EvalInterval time.Duration
	// ConnectTimeout 是 connecting 状态等待 hello 的时长（默认 120s）。
	ConnectTimeout time.Duration
	// ShutdownGrace 是 shutdown 时给 orbd 的宽限秒数（默认 20）。
	ShutdownGrace int
	// SetupTimeout 是 setup.run 的应答超时（默认 25 分钟；orbd 侧合计 20 分钟）。
	SetupTimeout time.Duration
}

// CreateParams 是新建 orb 的参数。空 Provider / Type 按 C3 规则取默认。
type CreateParams struct {
	Provider  string
	Type      string
	ProjectID string
	// SnapshotOf 是源 orb id（仅记录，并在未给 Provider 时沿用其 provider）。
	SnapshotOf string
	// SnapshotID 是用作镜像的快照。
	SnapshotID string
}

type (
	// RequestFunc 处理 orbd→orynd 的 ctl 请求。
	RequestFunc func(ctx context.Context, orbID string, params json.RawMessage) (any, error)
	// NotifyFunc 处理 orbd→orynd 的 ctl 通知（在 ctl 读循环中同步调用，须尽快返回）。
	NotifyFunc func(ctx context.Context, orbID string, params json.RawMessage)
	// StreamFunc 处理 orbd 发起的流（独立 goroutine，返回时流被关闭）。
	StreamFunc func(ctx context.Context, orbID string, h link.StreamHeader, st *link.Stream)
)

// Service 是 orb 生命周期服务。
type Service struct {
	cfg     Config
	st      *store.Store
	jobs    *jobs.Queue
	setup   SetupSource
	secrets EgressSecrets
	now     func() time.Time

	mu      sync.Mutex
	links   map[string]*orbLink
	linkGen uint64
	timers  map[string]*time.Timer
	// timerSeq 是各 orb 已应用到 connect 计时器的最大事件 Seq（乱序到达的旧状态不再改动计时器）。
	timerSeq  map[string]int64
	wakeAfter map[string]bool
	egress    map[string]*egressServer
	policies  map[string]egress.Policy
	// setupLogs 是各 orb 尚未写入运行记录的 setup.log 输出（见 setuprun.go）。
	setupLogs map[string]*setupLogs

	hmu       sync.RWMutex
	reqs      map[string]RequestFunc
	notifies  map[string]NotifyFunc
	streams   map[string]StreamFunc
	subs      []func(Event)
	quiescent QuiesceFunc

	bg sync.WaitGroup

	// 测试钩子（生产为 nil）：hookWakeDeferred 在 Wake 读到 freezing / stopping 之后、登记延迟唤醒之前调用；
	// hookLinkPublished 在新 link 发布到 links 之后、开始处理其 ctl 消息之前调用。
	hookWakeDeferred  func()
	hookLinkPublished func(orbID string)
}

// QuiesceFunc 是冻结 / 停机闸门（Threads 提供）：持有该 orb 全部线程的锁，确认全部 settled、无待发命令、
// 无待答 UI、无计划提案后调用 commit 并返回其结果；不满足时不调用 commit、返回 false。
// commit 只在一个数据库事务里做状态的比较并交换（及其任务入队），不回调 Threads、不长时间阻塞；
// 这次状态变化的事件在 QuiesceFunc 返回之后才发出。
type QuiesceFunc func(orbID string, commit func() bool) bool

// New 创建服务，并把任务回调注册到 q。setup / secrets 可为 nil。
func New(cfg Config, st *store.Store, q *jobs.Queue, setup SetupSource, secrets EgressSecrets) *Service {
	if cfg.EvalInterval <= 0 {
		cfg.EvalInterval = 30 * time.Second
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 120 * time.Second
	}
	if cfg.ShutdownGrace <= 0 {
		cfg.ShutdownGrace = 20
	}
	if cfg.SetupTimeout <= 0 {
		cfg.SetupTimeout = 25 * time.Minute
	}
	if len(cfg.TokenKey) == 0 {
		slog.Warn("orbs: 未配置 TokenKey，使用进程内随机密钥（重启后无法为已有 orb 重算令牌）")
		cfg.TokenKey = []byte(ids.Token("key"))
	}
	s := &Service{
		cfg: cfg, st: st, jobs: q, setup: setup, secrets: secrets, now: time.Now,
		links: map[string]*orbLink{}, timers: map[string]*time.Timer{}, timerSeq: map[string]int64{},
		wakeAfter: map[string]bool{}, egress: map[string]*egressServer{}, policies: map[string]egress.Policy{},
		setupLogs: map[string]*setupLogs{},
		reqs:      map[string]RequestFunc{}, notifies: map[string]NotifyFunc{}, streams: map[string]StreamFunc{},
	}
	s.reqs[string(apigen.MethodProjectScriptsGet)] = s.onProjectScriptsGet
	s.reqs[string(apigen.MethodProjectScriptsSet)] = s.onProjectScriptsSet
	q.SetHooks(jobs.Hooks{OnClaimed: s.onJobClaimed, OnFinished: s.onJobFinished})
	return s
}

// Jobs 返回任务队列（REST 层转发 /provider/v1/* 用）。
func (s *Service) Jobs() *jobs.Queue { return s.jobs }

// Config 返回生效的配置。
func (s *Service) Config() Config { return s.cfg }

func (s *Service) nowMs() int64 { return s.now().UnixMilli() }

// Subscribe 注册事件回调（同步调用，不得阻塞）。
func (s *Service) Subscribe(fn func(Event)) {
	s.hmu.Lock()
	s.subs = append(s.subs, fn)
	s.hmu.Unlock()
}

// SetQuiescence 设置冻结 / 停机闸门（见 QuiesceFunc）。未设置时视为始终静止。
func (s *Service) SetQuiescence(fn QuiesceFunc) {
	s.hmu.Lock()
	s.quiescent = fn
	s.hmu.Unlock()
}

// tryQuiesce 经闸门执行 commit；未设置闸门时直接执行。
func (s *Service) tryQuiesce(orbID string, commit func() bool) bool {
	s.hmu.RLock()
	fn := s.quiescent
	s.hmu.RUnlock()
	if fn == nil {
		return commit()
	}
	return fn(orbID, commit)
}

func (s *Service) emit(ev Event) {
	s.hmu.RLock()
	subs := append([]func(Event){}, s.subs...)
	s.hmu.RUnlock()
	for _, fn := range subs {
		fn(ev)
	}
}

// emitInfo 发出非状态事件（link_up / link_down / setup），Seq 取该 orb 当前已提交的最大事件序号。
func (s *Service) emitInfo(ctx context.Context, kind string, orb db.Orb) {
	seq, err := s.st.Q.MaxOrbEventID(ctx, orb.ID)
	if err != nil {
		slog.Warn("读取 orb 事件序号失败", "orb", orb.ID, "err", err)
	}
	s.emit(Event{OrbID: orb.ID, Kind: kind, From: orb.State, To: orb.State, Orb: orb, Seq: seq})
}

// Get 返回 orb。
func (s *Service) Get(ctx context.Context, orbID string) (db.Orb, error) {
	orb, err := s.st.Q.GetOrb(ctx, orbID)
	if errors.Is(err, sql.ErrNoRows) {
		return orb, ErrNotFound
	}
	return orb, err
}

// txn 是一次状态变化事务内的上下文：then 回调用它在同一事务里入队任务 / 轮换令牌。
type txn struct {
	ctx context.Context
	q   *db.Queries
	now int64
	// orb 是状态变化之后（事务内）的行。
	orb db.Orb
	// signal 表示入队了任务（提交后唤醒 provider 长轮询）；kick 表示令牌已轮换（提交后关闭旧 link）。
	signal, kick bool
}

// change 是已提交、待发布的状态变化。
type change struct {
	orb      db.Orb
	from, to string
	seq      int64
	detail   string
	signal   bool
	kick     bool
}

// transitionOpts 是 transitionTx 的可选项。
type transitionOpts struct {
	// guard 在事务内对变化前的行求值；返回 false 时不做变化。
	guard func(db.Orb) bool
	// then 在状态变化成功后于同一事务内执行（入队任务等）；返回错误则整个事务回滚。
	then func(t *txn) error
}

// transitionTx 在一个事务里以比较并交换的方式把 orb 从 from 移到 to、写 orb_events，并执行 opts.then；
// 只提交不发布：调用方在合适的时机（例如闸门释放线程锁之后）调用 publish。
func (s *Service) transitionTx(ctx context.Context, orbID, from, to string, detail map[string]any, opts transitionOpts) (*change, error) {
	if detail == nil {
		detail = map[string]any{}
	}
	raw, _ := json.Marshal(detail)
	now := s.nowMs()
	var ch *change
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		if opts.guard != nil {
			cur, err := q.GetOrb(ctx, orbID)
			if err != nil {
				return err
			}
			if cur.State != from || !opts.guard(cur) {
				return nil
			}
		}
		n, err := q.TransitionOrbState(ctx, db.TransitionOrbStateParams{ToState: to, StateChangedAt: now, ID: orbID, FromState: from})
		if err != nil || n == 0 {
			return err
		}
		if to == StateDeleted {
			if err := q.MarkOrbDeleted(ctx, db.MarkOrbDeletedParams{DeletedAt: now, ID: orbID}); err != nil {
				return err
			}
		}
		seq, err := q.InsertOrbEventSeq(ctx, db.InsertOrbEventSeqParams{OrbID: orbID, Ts: now, FromState: from, ToState: to, Detail: string(raw)})
		if err != nil {
			return err
		}
		t := &txn{ctx: ctx, q: q, now: now}
		if opts.then != nil {
			if t.orb, err = q.GetOrb(ctx, orbID); err != nil {
				return err
			}
			if err := opts.then(t); err != nil {
				return err
			}
		}
		orb, err := q.GetOrb(ctx, orbID)
		if err != nil {
			return err
		}
		ch = &change{orb: orb, from: from, to: to, seq: seq, detail: string(raw), signal: t.signal, kick: t.kick}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ch, nil
}

// publish 发布已提交的状态变化：唤醒 provider、关闭持旧令牌的 link、更新 connect 计时器、发出事件。nil 时无操作。
func (s *Service) publish(ch *change) {
	if ch == nil {
		return
	}
	slog.Info("orb 状态变化", "orb", ch.orb.ID, "from", ch.from, "to", ch.to, "seq", ch.seq, "detail", ch.detail)
	// 先关闭持旧令牌的 link，再唤醒 provider：spawn 送达新令牌时旧连接已不在。
	if ch.kick {
		s.Kick(ch.orb.ID, "orb token rotated")
	}
	if ch.signal {
		s.jobs.Signal(ch.orb.ProviderID)
	}
	s.armConnectTimer(ch.orb.ID, ch.to, ch.seq)
	s.emit(Event{OrbID: ch.orb.ID, Kind: EventState, From: ch.from, To: ch.to, Orb: ch.orb, Seq: ch.seq})
}

// transition 以比较并交换的方式把 orb 从 from 移到 to，写 orb_events 并发布。返回是否发生了变化。
func (s *Service) transition(ctx context.Context, orbID, from, to string, detail map[string]any) (db.Orb, bool, error) {
	return s.transitionThen(ctx, orbID, from, to, detail, nil)
}

// transitionThen 同 transition，then 在同一事务内执行（入队状态变化所需的任务）。
func (s *Service) transitionThen(ctx context.Context, orbID, from, to string, detail map[string]any, then func(t *txn) error) (db.Orb, bool, error) {
	ch, err := s.transitionTx(ctx, orbID, from, to, detail, transitionOpts{then: then})
	if err != nil || ch == nil {
		return db.Orb{}, false, err
	}
	s.publish(ch)
	return ch.orb, true, nil
}

// enqueueIn 在事务内为 t.orb 入队任务。
func (s *Service) enqueueIn(t *txn, op string, params any) error {
	if _, err := s.jobs.EnqueueTx(t.ctx, t.q, t.now, t.orb.ProviderID, t.orb.ID, op, params); err != nil {
		return err
	}
	t.signal = true
	return nil
}

// spawnIn 在事务内轮换 orb 令牌（token_gen+1）并入队携带新令牌的 spawn 任务。
func (s *Service) spawnIn(t *txn) error {
	gen := t.orb.TokenGen + 1
	token := orbToken(s.cfg.TokenKey, t.orb.ID, gen)
	n, err := t.q.RotateOrbToken(t.ctx, db.RotateOrbTokenParams{TokenHash: ids.Hash(token), ID: t.orb.ID, TokenGen: t.orb.TokenGen})
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("orbs: orb %s 的令牌代数已变化", t.orb.ID)
	}
	t.orb.TokenGen = gen
	t.kick = true
	p := apigen.JobSpawnParams{
		Image: "", Type: t.orb.Type,
		Env: apigen.JobSpawnEnv{OrynURL: s.cfg.PublicURL, OrynOrbID: t.orb.ID, OrynOrbToken: token},
	}
	if t.orb.SnapshotOf != nil && *t.orb.SnapshotOf != "" {
		p.SnapshotID = t.orb.SnapshotOf
	}
	return s.enqueueIn(t, jobs.OpSpawn, p)
}

// enqueueIfState 在 orb 仍处于 state 时（同一事务内判断）入队任务；返回是否入队。
func (s *Service) enqueueIfState(ctx context.Context, orbID, state, op string, params any) (bool, error) {
	var orb db.Orb
	queued := false
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		var err error
		if orb, err = q.GetOrb(ctx, orbID); err != nil || orb.State != state {
			return err
		}
		if _, err := s.jobs.EnqueueTx(ctx, q, s.nowMs(), orb.ProviderID, orbID, op, params); err != nil {
			return err
		}
		queued = true
		return nil
	})
	if queued {
		s.jobs.Signal(orb.ProviderID)
	}
	return queued, err
}

// armConnectTimer 在进入 connecting 时启动 hello 超时；离开时取消。seq 小于已应用值的（乱序到达的旧）状态被忽略。
func (s *Service) armConnectTimer(orbID, state string, seq int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq < s.timerSeq[orbID] {
		return
	}
	s.timerSeq[orbID] = seq
	if t := s.timers[orbID]; t != nil {
		t.Stop()
		delete(s.timers, orbID)
	}
	if state == StateDeleted {
		delete(s.timerSeq, orbID)
	}
	if state != StateConnecting {
		return
	}
	s.timers[orbID] = time.AfterFunc(s.cfg.ConnectTimeout, func() {
		ctx := context.Background()
		_, moved, err := s.transition(ctx, orbID, StateConnecting, StateFailed, map[string]any{"reason": "no hello within connect timeout"})
		if err != nil {
			slog.Warn("connecting 超时处理失败", "orb", orbID, "err", err)
		} else if moved {
			slog.Warn("orb 在超时时间内没有连上", "orb", orbID, "timeout", s.cfg.ConnectTimeout)
		}
	})
}

// resolveProvider 按「显式 → 项目默认 → orb_defaults → 源 orb → 唯一 provider」确定 provider。
func (s *Service) resolveProvider(ctx context.Context, name string, prj *db.Project, src *db.Orb) (db.Provider, error) {
	if name == "" && prj != nil && prj.DefaultProvider != nil {
		name = *prj.DefaultProvider
	}
	if name == "" {
		name = s.cfg.Defaults.Provider
	}
	if name != "" {
		p, err := s.st.Q.GetProviderByName(ctx, name)
		if errors.Is(err, sql.ErrNoRows) {
			return p, fmt.Errorf("%w: provider %q 不存在", ErrNoProvider, name)
		}
		if err == nil && p.DisabledAt != nil {
			return p, fmt.Errorf("%w: provider %q 已停用", ErrNoProvider, name)
		}
		return p, err
	}
	if src != nil {
		return s.st.Q.GetProvider(ctx, src.ProviderID)
	}
	all, err := s.st.Q.ListProviders(ctx)
	if err != nil {
		return db.Provider{}, err
	}
	var live []db.Provider
	for _, p := range all {
		if p.DisabledAt == nil {
			live = append(live, p)
		}
	}
	if len(live) == 1 {
		return live[0], nil
	}
	return db.Provider{}, fmt.Errorf("%w: 未指定 provider 且无法确定默认值（共 %d 个可用）", ErrNoProvider, len(live))
}

// Create 新建 orb（requested）并入队 spawn 任务。
func (s *Service) Create(ctx context.Context, p CreateParams) (_ db.Orb, err error) {
	if p.SnapshotID != "" && p.SnapshotOf != "" {
		// 快照是专为这次创建拍的：创建失败时没有 orb 会用它，交给源 orb 的 provider 回收。
		defer func() {
			if err != nil {
				bg := context.WithoutCancel(ctx)
				if src, gerr := s.st.Q.GetOrb(bg, p.SnapshotOf); gerr == nil {
					s.releaseSnapshot(bg, src.ProviderID, src.ID, p.SnapshotID)
				}
			}
		}()
	}
	var prj *db.Project
	if p.ProjectID != "" {
		v, err := s.st.Q.GetProject(ctx, p.ProjectID)
		if err != nil {
			return db.Orb{}, fmt.Errorf("读取项目 %s: %w", p.ProjectID, err)
		}
		prj = &v
	}
	var src *db.Orb
	if p.SnapshotOf != "" {
		if v, err := s.st.Q.GetOrb(ctx, p.SnapshotOf); err == nil {
			src = &v
		}
	}
	prov, err := s.resolveProvider(ctx, p.Provider, prj, src)
	if err != nil {
		return db.Orb{}, err
	}
	typ := p.Type
	if typ == "" && src != nil && p.Provider == "" {
		typ = src.Type
	}
	if typ == "" && prj != nil && prj.DefaultOrbType != nil {
		typ = *prj.DefaultOrbType
	}
	if typ == "" {
		typ = s.cfg.Defaults.Type
	}
	if typ == "" {
		typ = prov.DefaultType
	}
	if typ == "" {
		return db.Orb{}, fmt.Errorf("%w: 无法确定 orb 类型（provider %q 尚未上线？）", ErrBadType, prov.Name)
	}
	var types []apigen.OrbType
	_ = json.Unmarshal([]byte(prov.OrbTypes), &types)
	var spec *apigen.OrbType
	for i := range types {
		if types[i].Name == typ {
			spec = &types[i]
		}
	}
	if spec == nil && len(types) > 0 {
		return db.Orb{}, fmt.Errorf("%w: provider %q 没有类型 %q", ErrBadType, prov.Name, typ)
	}
	orbID := ids.Orb()
	now := s.nowMs()
	// 第 0 代令牌从不下发：入队 spawn 时立即轮换到第 1 代。
	params := db.CreateOrbParams{
		ID: orbID, ProviderID: prov.ID, Type: typ, Image: p.SnapshotID,
		State: StateRequested, StateChangedAt: now, TokenHash: ids.Hash(orbToken(s.cfg.TokenKey, orbID, 0)), CreatedAt: now,
	}
	if spec != nil {
		params.Cpus = int64(math.Round(float64(spec.Cpus)))
		params.MemoryMb = int64(spec.MemoryMB)
	}
	if prj != nil {
		params.ProjectID = &prj.ID
	}
	if p.SnapshotID != "" {
		params.SnapshotOf = &p.SnapshotID
	}
	detail := map[string]any{"provider": prov.Name, "type": typ}
	if p.SnapshotOf != "" {
		detail["sourceOrb"] = p.SnapshotOf
	}
	raw, _ := json.Marshal(detail)
	// orb、requested 事件与首个 spawn 任务同一事务提交：不存在「有 orb 没任务」的中间状态。
	var ch *change
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateOrb(ctx, params); err != nil {
			return err
		}
		seq, err := q.InsertOrbEventSeq(ctx, db.InsertOrbEventSeqParams{OrbID: orbID, Ts: now, FromState: "", ToState: StateRequested, Detail: string(raw)})
		if err != nil {
			return err
		}
		t := &txn{ctx: ctx, q: q, now: now}
		if t.orb, err = q.GetOrb(ctx, orbID); err != nil {
			return err
		}
		if err := s.spawnIn(t); err != nil {
			return err
		}
		orb, err := q.GetOrb(ctx, orbID)
		ch = &change{orb: orb, from: "", to: StateRequested, seq: seq, detail: string(raw), signal: true}
		return err
	})
	if err != nil {
		return db.Orb{}, fmt.Errorf("创建 orb: %w", err)
	}
	slog.Info("orb 已创建", "orb", orbID, "provider", prov.Name, "type", typ, "snapshot", p.SnapshotID)
	s.publish(ch)
	return ch.orb, nil
}

// enqueueSpawn 在 orb 仍处于 state 时轮换令牌并入队 spawn（不改变状态）；返回是否入队。
func (s *Service) enqueueSpawn(ctx context.Context, orbID, state string) (bool, error) {
	var orb db.Orb
	kicked := false
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		cur, err := q.GetOrb(ctx, orbID)
		if err != nil || cur.State != state {
			return err
		}
		t := &txn{ctx: ctx, q: q, now: s.nowMs(), orb: cur}
		if err := s.spawnIn(t); err != nil {
			return err
		}
		orb, kicked = t.orb, true
		return nil
	})
	if err != nil || !kicked {
		return false, err
	}
	s.Kick(orbID, "orb token rotated")
	s.jobs.Signal(orb.ProviderID)
	return true, nil
}

// Wake 唤醒 orb：frozen / stopped / failed → waking 并（同一事务）入队 spawn；正在冻结 / 停机时在其完成后再唤醒；
// 已在运行或正在启动时无操作。
func (s *Service) Wake(ctx context.Context, orbID string) error {
	orb, err := s.Get(ctx, orbID)
	if err != nil {
		return err
	}
	switch orb.State {
	case StateFrozen, StateStopped, StateFailed:
		_, moved, err := s.transitionThen(ctx, orbID, orb.State, StateWaking, nil, s.spawnIn)
		if err != nil {
			return err
		}
		if !moved {
			return s.Wake(ctx, orbID)
		}
		return nil
	case StateFreezing, StateStopping:
		if s.hookWakeDeferred != nil {
			s.hookWakeDeferred()
		}
		s.mu.Lock()
		s.wakeAfter[orbID] = true
		s.mu.Unlock()
		// 暂停任务的完成回调先改状态、再消费标记：登记之后重读状态，若已离开 freezing / stopping，
		// 回调可能在登记之前就消费过（不存在的）标记——收回标记并按新状态重试（重复唤醒由比较并交换去重）。
		cur, err := s.Get(ctx, orbID)
		if err != nil {
			return err
		}
		if cur.State == StateFreezing || cur.State == StateStopping {
			return nil
		}
		s.mu.Lock()
		delete(s.wakeAfter, orbID)
		s.mu.Unlock()
		return s.Wake(ctx, orbID)
	case StateDeleting, StateDeleted:
		return fmt.Errorf("%w: orb %s 已删除", ErrInvalidState, orbID)
	}
	return nil
}

// Pause 手动冻结或停机。
func (s *Service) Pause(ctx context.Context, orbID, mode string) error {
	if mode != PauseFreeze && mode != PauseStop {
		return fmt.Errorf("%w: 暂停方式 %q", ErrInvalidState, mode)
	}
	orb, err := s.Get(ctx, orbID)
	if err != nil {
		return err
	}
	switch {
	case mode == PauseFreeze && orb.State == StateFrozen, mode == PauseStop && orb.State == StateStopped:
		return nil
	case mode == PauseFreeze && orb.State == StateReady:
		ch, err := s.freezeTx(ctx, orbID, "manual", nil)
		s.publish(ch)
		return err
	case mode == PauseStop && (orb.State == StateReady || orb.State == StateFrozen):
		ch, err := s.stopTx(ctx, orb.ID, orb.State, "manual", nil)
		s.afterStop(ch)
		return err
	}
	return fmt.Errorf("%w: orb %s 处于 %s，不能 %s", ErrInvalidState, orbID, orb.State, mode)
}

// freezeTx 提交 ready → freezing 与 pause{freeze} 任务（guard 在事务内对变化前的行复核）；不发布。
func (s *Service) freezeTx(ctx context.Context, orbID, reason string, guard func(db.Orb) bool) (*change, error) {
	return s.transitionTx(ctx, orbID, StateReady, StateFreezing, map[string]any{"reason": reason}, transitionOpts{
		guard: guard,
		then:  func(t *txn) error { return s.enqueueIn(t, jobs.OpPause, apigen.JobPauseParams{Mode: apigen.Freeze}) },
	})
}

// stopTx 提交 from → stopping（不发布）。ready → stopping 之后由 afterStop 先 shutdown 再入队 pause{stop}；
// frozen → stopping 在同一事务里入队 spawn 解冻，link 恢复后再 shutdown 与 pause{stop}（见 onJobFinished）。
func (s *Service) stopTx(ctx context.Context, orbID, from, reason string, guard func(db.Orb) bool) (*change, error) {
	opts := transitionOpts{guard: guard}
	if from == StateFrozen {
		opts.then = s.spawnIn
	}
	return s.transitionTx(ctx, orbID, from, StateStopping, map[string]any{"reason": reason}, opts)
}

// afterStop 发布 stopTx 的结果，并为 ready → stopping 启动 shutdown → pause{stop}。
func (s *Service) afterStop(ch *change) {
	if ch == nil {
		return
	}
	s.publish(ch)
	if ch.from == StateReady {
		orbID := ch.orb.ID
		s.goBG(func() { s.shutdownThenPauseStop(orbID, 0) })
	}
}

// shutdownThenPauseStop 等待 link（至多 wait）后发 shutdown，再在 orb 仍处于 stopping 时入队 pause{stop}。
func (s *Service) shutdownThenPauseStop(orbID string, wait time.Duration) {
	ctx := context.Background()
	s.shutdownOrbd(ctx, orbID, wait)
	if _, err := s.enqueueIfState(ctx, orbID, StateStopping, jobs.OpPause, apigen.JobPauseParams{Mode: apigen.Stop}); err != nil {
		slog.Warn("入队停机任务失败", "orb", orbID, "err", err)
		_, _, _ = s.transition(ctx, orbID, StateStopping, StateFailed, map[string]any{"reason": err.Error()})
	}
}

// shutdownOrbd 向 orbd 发 shutdown（无 link 时至多等待 wait）。失败只记录。
func (s *Service) shutdownOrbd(ctx context.Context, orbID string, wait time.Duration) {
	l := s.waitLink(ctx, orbID, wait)
	if l == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(s.cfg.ShutdownGrace+10)*time.Second)
	defer cancel()
	var res apigen.LinkCtlOkResult
	if err := l.ctl.Call(cctx, string(apigen.MethodShutdown), apigen.LinkCtlShutdownParams{GraceSeconds: s.cfg.ShutdownGrace}, &res); err != nil {
		slog.Warn("orbd shutdown 失败（继续）", "orb", orbID, "err", err)
	}
}

// Delete 删除 orb：→ deleting，向 orbd 发 shutdown（可达时），再入队 delete；冻结中的 orb 无法应答 shutdown，
// 与状态变化同一事务直接入队 delete。
func (s *Service) Delete(ctx context.Context, orbID string) error {
	for {
		orb, err := s.Get(ctx, orbID)
		if err != nil {
			return err
		}
		if orb.State == StateDeleted || orb.State == StateDeleting {
			return nil
		}
		frozen := orb.State == StateFrozen || orb.State == StateFreezing
		var then func(t *txn) error
		if frozen {
			then = func(t *txn) error { return s.enqueueIn(t, jobs.OpDelete, nil) }
		}
		_, moved, err := s.transitionThen(ctx, orbID, orb.State, StateDeleting, nil, then)
		if err != nil {
			return err
		}
		if !moved {
			continue
		}
		s.mu.Lock()
		delete(s.wakeAfter, orbID)
		s.mu.Unlock()
		if !frozen {
			s.goBG(func() { s.shutdownThenDelete(orbID, 0) })
		}
		return nil
	}
}

// shutdownThenDelete 向 orbd 发 shutdown（无 link 时至多等待 wait），再在 orb 仍处于 deleting 时入队 delete。
func (s *Service) shutdownThenDelete(orbID string, wait time.Duration) {
	ctx := context.Background()
	s.shutdownOrbd(ctx, orbID, wait)
	if _, err := s.enqueueIfState(ctx, orbID, StateDeleting, jobs.OpDelete, nil); err != nil {
		slog.Warn("入队删除任务失败", "orb", orbID, "err", err)
		_, _, _ = s.transition(ctx, orbID, StateDeleting, StateFailed, map[string]any{"reason": err.Error()})
	}
}

// releaseSnapshot 在没有未删除的 orb 使用快照 snap 时入队 delete_snapshot（使用它的容器删除后镜像才可删除）。
func (s *Service) releaseSnapshot(ctx context.Context, providerID, orbID, snap string) {
	n, err := s.st.Q.CountLiveOrbsBySnapshot(ctx, &snap)
	if err != nil {
		slog.Warn("查询快照引用失败", "snapshot", snap, "err", err)
		return
	}
	if n > 0 {
		return
	}
	if _, err := s.jobs.Enqueue(ctx, providerID, orbID, jobs.OpDeleteSnapshot, apigen.JobDeleteSnapshotParams{SnapshotID: snap}); err != nil {
		slog.Warn("入队快照删除失败", "snapshot", snap, "err", err)
		return
	}
	slog.Info("快照已无引用，入队删除", "snapshot", snap)
}

// Snapshot 为 orb 入队 snapshot 任务并等待完成，返回快照 id（Docker 镜像名）。
func (s *Service) Snapshot(ctx context.Context, orbID string) (string, error) {
	orb, err := s.Get(ctx, orbID)
	if err != nil {
		return "", err
	}
	if orb.State == StateDeleting || orb.State == StateDeleted {
		return "", fmt.Errorf("%w: orb %s 已删除", ErrInvalidState, orbID)
	}
	snap := ids.Snapshot()
	job, err := s.jobs.Enqueue(ctx, orb.ProviderID, orbID, jobs.OpSnapshot, apigen.JobSnapshotParams{SnapshotID: snap})
	if err != nil {
		return "", err
	}
	done, err := s.jobs.Wait(ctx, job.ID)
	if err != nil {
		return "", err
	}
	if done.State != jobs.StateSucceeded {
		msg := "snapshot failed"
		if done.Error != nil {
			msg = *done.Error
		}
		return "", fmt.Errorf("快照 orb %s: %s", orbID, msg)
	}
	return snap, nil
}

// TouchUser 记录一次用户交互。
func (s *Service) TouchUser(ctx context.Context, orbID string) {
	ts := s.nowMs()
	if err := s.st.Q.TouchOrbUserInteraction(ctx, db.TouchOrbUserInteractionParams{Ts: &ts, ID: orbID}); err != nil {
		slog.Warn("记录用户交互失败", "orb", orbID, "err", err)
	}
}

// TouchAgent 记录一次 agent 活动。
func (s *Service) TouchAgent(ctx context.Context, orbID string) {
	ts := s.nowMs()
	if err := s.st.Q.TouchOrbAgentActivity(ctx, db.TouchOrbAgentActivityParams{Ts: &ts, ID: orbID}); err != nil {
		slog.Warn("记录 agent 活动失败", "orb", orbID, "err", err)
	}
}

func jobPauseMode(job db.Job) string {
	var p apigen.JobPauseParams
	_ = json.Unmarshal([]byte(job.Params), &p)
	return string(p.Mode)
}

func (s *Service) onJobClaimed(ctx context.Context, job db.Job) {
	if job.OrbID == nil || job.Op != jobs.OpSpawn {
		return
	}
	if _, _, err := s.transition(ctx, *job.OrbID, StateRequested, StateSpawning, map[string]any{"job": job.ID, "attempt": job.Attempt}); err != nil {
		slog.Warn("spawn 认领状态更新失败", "orb", *job.OrbID, "err", err)
	}
}

// onJobFinished 按任务结果推进 orb 状态。所有推进都是基于当前状态的比较并交换，重复调用（重启后的 reconcile
// 重放同一个终态任务）不会重复生效。
func (s *Service) onJobFinished(ctx context.Context, job db.Job, ok bool) {
	if job.OrbID == nil {
		return
	}
	orbID := *job.OrbID
	orb, err := s.Get(ctx, orbID)
	if err != nil {
		return
	}
	detail := map[string]any{"job": job.ID}
	if job.Error != nil {
		detail["error"] = *job.Error
	}
	switch job.Op {
	case jobs.OpSpawn:
		switch orb.State {
		case StateSpawning, StateWaking, StateRequested:
			to := StateConnecting
			if !ok {
				to = StateFailed
			}
			if _, moved, _ := s.transition(ctx, orbID, orb.State, to, detail); moved && ok {
				s.advance(orbID)
			}
		case StateStopping:
			if ok {
				s.goBG(func() { s.shutdownThenPauseStop(orbID, s.cfg.ConnectTimeout/2) })
			} else {
				_, _, _ = s.transition(ctx, orbID, StateStopping, StateFailed, detail)
			}
		}
	case jobs.OpPause:
		if jobPauseMode(job) == PauseFreeze {
			to := StateFrozen
			if !ok {
				to = StateReady
			}
			_, _, _ = s.transition(ctx, orbID, StateFreezing, to, detail)
		} else {
			to := StateStopped
			if !ok {
				to = StateFailed
			}
			_, _, _ = s.transition(ctx, orbID, StateStopping, to, detail)
		}
		s.mu.Lock()
		again := s.wakeAfter[orbID]
		delete(s.wakeAfter, orbID)
		s.mu.Unlock()
		if again {
			if err := s.Wake(ctx, orbID); err != nil {
				slog.Warn("延迟唤醒失败", "orb", orbID, "err", err)
			}
		}
	case jobs.OpDelete:
		if !ok {
			_, _, _ = s.transition(ctx, orbID, StateDeleting, StateFailed, detail)
			return
		}
		if _, moved, _ := s.transition(ctx, orbID, StateDeleting, StateDeleted, detail); moved {
			s.Kick(orbID, "orb deleted")
			s.closeEgress(orbID)
			s.dropSetupLogs(orbID)
			if orb.SnapshotOf != nil && *orb.SnapshotOf != "" {
				s.releaseSnapshot(ctx, orb.ProviderID, orbID, *orb.SnapshotOf)
			}
		}
	}
}

// Run 恢复中间状态并运行空闲评估，直到 ctx 结束。任务队列的租约巡检（jobs.Queue.Run）由调用方另行运行。
func (s *Service) Run(ctx context.Context) {
	s.recover(ctx)
	t := time.NewTicker(s.cfg.EvalInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.EvaluateIdle(ctx)
		}
	}
}

// recover 在 orynd 启动时把每个处于中间状态的 orb 与任务表对账（幂等：可重复执行）。
//
// 状态变化与它所需的任务（spawn / pause{freeze} / 冻结时的 delete）同一事务提交；不能同事务的后续动作
// （ready → stopping / deleting 之后的 shutdown 与入队、任务终态之后的状态推进）可能因重启丢失，这里补做：
//   - setting_up / resuming → connecting（等待重新 hello）；connecting 重新计时。
//   - 任务仍在 queued / claimed：什么也不做，完成回调会推进。
//   - 本状态对应的任务已终态：重放 onJobFinished（基于当前状态比较并交换，已生效的重放无操作）。
//   - 本状态对应的任务不存在：补入队（requested / spawning / waking → spawn；freezing → pause{freeze}；
//     stopping → shutdown 后 pause{stop}；deleting → shutdown 后 delete）。
func (s *Service) recover(ctx context.Context) {
	orbs, err := s.st.Q.ListLiveOrbs(ctx)
	if err != nil {
		slog.Warn("恢复 orb 状态失败", "err", err)
		return
	}
	for _, orb := range orbs {
		if err := s.reconcile(ctx, orb); err != nil {
			slog.Warn("orb 对账失败", "orb", orb.ID, "state", orb.State, "err", err)
		}
	}
}

func (s *Service) reconcile(ctx context.Context, orb db.Orb) error {
	switch orb.State {
	case StateSettingUp, StateResuming:
		_, _, err := s.transition(ctx, orb.ID, orb.State, StateConnecting, map[string]any{"reason": "control plane restarted"})
		return err
	case StateConnecting:
		seq, err := s.st.Q.MaxOrbEventID(ctx, orb.ID)
		if err != nil {
			return err
		}
		s.armConnectTimer(orb.ID, StateConnecting, seq)
		return nil
	case StateRequested, StateSpawning, StateWaking, StateFreezing, StateStopping, StateDeleting:
	default:
		return nil
	}
	all, err := s.st.Q.ListJobsByOrb(ctx, &orb.ID)
	if err != nil {
		return err
	}
	// latest 返回本状态期间（created_at ≥ state_changed_at；spawning 例外，它在认领 spawn 时才进入）入队的、
	// 满足 match 的最新任务。
	latest := func(sinceState bool, match func(db.Job) bool) *db.Job {
		for i := len(all) - 1; i >= 0; i-- {
			j := all[i]
			if sinceState && j.CreatedAt < orb.StateChangedAt {
				return nil
			}
			if match(j) {
				return &j
			}
		}
		return nil
	}
	op := func(want ...string) func(db.Job) bool {
		return func(j db.Job) bool {
			for _, w := range want {
				if j.Op != w {
					continue
				}
				if j.Op == jobs.OpPause {
					mode := PauseStop
					if orb.State == StateFreezing {
						mode = PauseFreeze
					}
					return jobPauseMode(j) == mode
				}
				return true
			}
			return false
		}
	}
	var j *db.Job
	switch orb.State {
	case StateRequested, StateWaking:
		j = latest(true, op(jobs.OpSpawn))
	case StateSpawning:
		j = latest(false, op(jobs.OpSpawn))
	case StateFreezing:
		j = latest(true, op(jobs.OpPause))
	case StateStopping:
		j = latest(true, op(jobs.OpSpawn, jobs.OpPause))
	case StateDeleting:
		j = latest(true, op(jobs.OpDelete))
	}
	if j != nil {
		if j.State == jobs.StateQueued || j.State == jobs.StateClaimed {
			return nil
		}
		slog.Info("对账：重放任务终态", "orb", orb.ID, "state", orb.State, "job", j.ID, "op", j.Op, "jobState", j.State)
		s.onJobFinished(ctx, *j, j.State == jobs.StateSucceeded)
		return nil
	}
	slog.Info("对账：补入队缺失的任务", "orb", orb.ID, "state", orb.State)
	switch orb.State {
	case StateRequested, StateSpawning, StateWaking:
		_, err = s.enqueueSpawn(ctx, orb.ID, orb.State)
	case StateFreezing:
		_, err = s.enqueueIfState(ctx, orb.ID, StateFreezing, jobs.OpPause, apigen.JobPauseParams{Mode: apigen.Freeze})
	case StateStopping:
		s.goBG(func() { s.shutdownThenPauseStop(orb.ID, s.cfg.ConnectTimeout/2) })
	case StateDeleting:
		s.goBG(func() { s.shutdownThenDelete(orb.ID, s.cfg.ConnectTimeout/2) })
	}
	return err
}

// EvaluateIdle 执行一次空闲评估：ready 且空闲超时 → 冻结；frozen 超过 stop_after_frozen → 停机。
// 两者都经冻结闸门（tryQuiesce）提交，空闲条件在提交事务内对最新的行复核。
func (s *Service) EvaluateIdle(ctx context.Context) {
	for _, state := range []string{StateReady, StateFrozen} {
		orbs, err := s.st.Q.ListOrbsByState(ctx, state)
		if err != nil {
			slog.Warn("空闲评估失败", "err", err)
			return
		}
		for _, orb := range orbs {
			idle := s.idleFrozenTooLong
			if state == StateReady {
				idle = s.idleReady
			}
			if !idle(orb) {
				continue
			}
			var ch *change
			var cerr error
			s.tryQuiesce(orb.ID, func() bool {
				if state == StateReady {
					ch, cerr = s.freezeTx(ctx, orb.ID, "idle", idle)
				} else {
					ch, cerr = s.stopTx(ctx, orb.ID, StateFrozen, "frozen too long", idle)
				}
				return cerr == nil && ch != nil
			})
			if cerr != nil {
				slog.Warn("空闲冻结 / 停机失败", "orb", orb.ID, "state", state, "err", cerr)
			}
			// 事件在闸门释放线程锁之后才发布。
			if state == StateReady {
				s.publish(ch)
			} else {
				s.afterStop(ch)
			}
		}
	}
}

// idleReady 判断 ready 的 orb 是否已超过 agent 与用户空闲时长。
func (s *Service) idleReady(orb db.Orb) bool {
	now := s.nowMs()
	agent := max(orb.StateChangedAt, deref64(orb.LastAgentActivityAt))
	user := max(orb.StateChangedAt, deref64(orb.LastUserInteractionAt))
	return now-agent >= s.cfg.Idle.AgentIdle.D().Milliseconds() && now-user >= s.cfg.Idle.UserIdle.D().Milliseconds()
}

// idleFrozenTooLong 判断 frozen 的 orb 是否已冻结超过 stop_after_frozen。
func (s *Service) idleFrozenTooLong(orb db.Orb) bool {
	return s.nowMs()-orb.StateChangedAt >= s.cfg.Idle.StopAfterFrozen.D().Milliseconds()
}

// Close 关闭全部 link 与 egress 服务，并等待后台流程结束（最多 ShutdownGrace+15 秒）。
func (s *Service) Close() {
	s.mu.Lock()
	links := make([]*orbLink, 0, len(s.links))
	for _, l := range s.links {
		links = append(links, l)
	}
	orbIDs := make([]string, 0, len(s.egress))
	for id := range s.egress {
		orbIDs = append(orbIDs, id)
	}
	for _, t := range s.timers {
		t.Stop()
	}
	s.timers = map[string]*time.Timer{}
	s.mu.Unlock()
	for _, l := range links {
		_ = l.sess.Close()
	}
	for _, id := range orbIDs {
		s.closeEgress(id)
	}
	done := make(chan struct{})
	go func() { s.bg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Duration(s.cfg.ShutdownGrace+15) * time.Second):
	}
}

func (s *Service) goBG(fn func()) {
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		fn()
	}()
}

func deref64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
