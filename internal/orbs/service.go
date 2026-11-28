// Package orbs 管理 orb 生命周期（计划 E 节）：状态机与 orb_events、spawn / pause / snapshot / delete 任务、
// 30s 空闲评估（冻结 / 停机）、唤醒、connecting 超时；以及 /orb/v1/link 处理器（hello → setup / resume、
// metrics.sample、setup.log / setup.result 与 orb_setup_runs、project.scripts.*、egress 流）、
// M4 的服务镜像（services.state → orb_services）、portal.configure 与 portal 流拨号。Threads 经本包导出的 Service API 与 View 与 orb 交互。
package orbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Lynricsy/Oryn/internal/aiconsent"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/memory"
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
	// ErrBoxStopped 表示这台 box 被主人或时段停下（M23 闸门），Wake 不唤醒它。
	ErrBoxStopped = errors.New("orbs: box is stopped")
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
	// PortalURLTemplate 是下发给 orbd 的 portal 地址模板（portalhost.Base.Template()，如
	// `https://t-{code}-p{port}.orb.example.com/`）；空串 = 未配置 portal_domain。
	PortalURLTemplate string
	// Snapshots 是项目快照配置（M11a）；零值 = 关闭。
	Snapshots config.Snapshots
	// Pool 是预热池配置（M11a）；MaxTotal 为 0 = 关闭。
	Pool config.Pool
	// OrbCAPEM 是 orb link TLS 监听的自签 CA 证书（PEM）；非空时随 spawn 参数 orbCaPem 下发，orbd 只信任这张 CA。
	// 空 = 未开启 orb TLS 监听（旧的明文 link）。
	OrbCAPEM string
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
	// BrowserProfileID 非空时新 orb 选用该浏览器 Profile（M8；与 orb 同一事务写入 orb_desktops，进入 ready 之前装载）。
	BrowserProfileID string
	// ProfileMode 是选用方式（apigen.BrowserProfileMode：exclusive / readonly / takeover；空 = exclusive）。
	ProfileMode string
	// Coordinator 为真时建协调 orb（M9）：provider 固定为内置的 @host、类型 host，忽略其余字段。
	Coordinator bool
	// Pool 为真时建预热池 orb（M11a）：pool_state = warming；须有项目，且与 SnapshotID、BrowserProfileID 互斥。
	Pool bool
	// Computer 为真时建助手的电脑（M19）：无项目、不进预热池、不参与项目快照；全库至多一台未删除的。
	Computer bool
	// GithubIdentity 是 orb 的 GitHub 身份槽位（M14：user / agent）；空串等同 user。
	GithubIdentity string
	// Automation 为真时新 orb 标为全自动（M18 D3）：SSH 网关只放行允许全自动线程使用的主机。
	Automation bool
	// Box 为真时建 box 的常驻 orb（M23）：无项目、不进预热池、不来自快照、不冻结、不随线程回收。
	Box bool
	// SandboxNetwork 非空时建 agent 沙箱的 orb（M30）：取值 SandboxNetNone / SandboxNetEgress（创建时的网络模式）；
	// 须显式指定 Provider，与项目、快照、预热池、Profile、box、电脑、协调 orb 互斥；不冻结、不随线程回收。
	SandboxNetwork string
}

// agent 沙箱 orb 的网络模式（M30，与 orbs.sandbox_network 取值一致）。
const (
	SandboxNetNone   = "none"
	SandboxNetEgress = "egress"
)

// CodeSandboxUnsupported 是沙箱的 link 发来 ctl 请求时回的错误码（M30：沙箱 link 默认拒绝一切控制面能力）。
const CodeSandboxUnsupported = "sandbox_unsupported"

// IsSandbox 报告 orb 是否是 agent 沙箱（M30）。
func IsSandbox(o db.Orb) bool { return o.SandboxNetwork != nil }

// 内置协调宿主 provider（M9 协调宿主分册 H6）。名称 `@host` 不满足 API 的 provider 名称正则，用户建不出同名行。
const (
	HostProviderName = "@host"
	HostType         = "host"
	// HostDriver 是 @host 行的 driver 列。
	HostDriver = "host"
	// FeatureCoordinator 是协调宿主（orynd coordinator-host）在 hello 里上报的特性。
	FeatureCoordinator = "coordinator"
)

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
	// github 是 GitHub 集成（M14，SetGitHub 在服务对外可用之前设置；nil = 未启用）。
	github GitHub
	// memory 是记忆网关（M16，SetMemory 在服务对外可用之前设置；nil = 没有记忆通道）。
	memory *memory.Gateway
	// pushes 接收出站代理观察到的成功推送（M24，SetPushSink 在服务对外可用之前设置；nil = 不记录）。
	pushes PushSink
	// hostedGit 在本地服务 orb 所属项目的托管仓库（M24 阶段 ③，SetHostedGit 在服务对外可用之前设置；nil = 没有通道，
	// 托管路由一律 403）。
	hostedGit egress.HostedGitServer
	// aiConsent 是 AI 数据同意（SetAIConsent 在服务对外可用之前设置；nil = 出站代理一律拒绝第三方 AI 上游）。
	aiConsent aiconsent.Checker
	now       func() time.Time

	// idle / snaps 是运行时覆盖的空闲策略与快照配置（SetOps；nil = 用 cfg 里构造时的初值）。
	idle  atomic.Pointer[config.Idle]
	snaps atomic.Pointer[config.Snapshots]

	mu      sync.Mutex
	links   map[string]*orbLink
	linkGen uint64
	timers  map[string]*time.Timer
	// timerSeq 是各 orb 已应用到 connect 计时器的最大事件 Seq（乱序到达的旧状态不再改动计时器）。
	timerSeq  map[string]int64
	wakeAfter map[string]bool
	egress    map[string]*egressServer
	policies  map[string]egress.Policy
	// policyGen 是出站策略的代际：InvalidateProjectPolicies 每次加一，读配置之前取的代际已变的策略不写入 policies
	// （见 egress.go setPolicy）。
	policyGen uint64
	// setupLogs 是各 orb 尚未写入运行记录的 setup.log 输出（见 setuprun.go）。
	setupLogs map[string]*setupLogs
	// httpTr 是经 fs 流访问 orbd 本机 API 的共享 Transport（见 orbhttp.go）。
	httpOnce sync.Once
	httpTr   *http.Transport
	// userTouched 是各 orb 经 TouchUserCoalesced 的写库状态：已成功写库的时间与在途写入（见 orbhttp.go）。
	userTouched map[string]*userTouch

	hmu       sync.RWMutex
	reqs      map[string]RequestFunc
	notifies  map[string]NotifyFunc
	streams   map[string]StreamFunc
	subs      []func(Event)
	quiescent QuiesceFunc
	// boxGate 是 box 闸门（M23，SetBoxGate）：返回 false 时 Wake 不唤醒这台 box 的 orb；nil = 不设闸门。
	boxGate func(orbID string) bool
	// deskHooks 是 Profile 生命周期钩子（M8，见 desktop.go）。
	deskHooks DesktopHooks

	bg sync.WaitGroup

	// 测试钩子（生产为 nil）：hookWakeDeferred 在 Wake 读到 freezing / stopping 之后、登记延迟唤醒之前调用；
	// hookLinkPublished 在新 link 发布到 links 之后、开始处理其 ctl 消息之前调用；hookHelloStart 在 onHello
	// 开头（portal.configure 之前）调用；hookAwaitPrepared 在 runPhase 发现 link 尚未准备完成、开始等待时调用；
	// hookTouchWrite 在 TouchUserCoalesced 登记在途写入之后、写库之前调用；hookTouchWait 在调用方开始等
	// 在途写入时调用。
	hookWakeDeferred  func()
	hookLinkPublished func(orbID string)
	hookHelloStart    func(orbID string)
	hookAwaitPrepared func(orbID string)
	hookTouchWrite    func(ctx context.Context, orbID string)
	hookTouchWait     func(orbID string)
	// hookSnapshotBuilding（仅测试）在项目快照登记为 building 之后、入队 snapshot 任务之前调用。
	hookSnapshotBuilding func(orbID, snapID string)
	// hookRotateToken（仅测试）在快照后轮换令牌的事务里最先调用，返回错误即让该事务失败。
	hookRotateToken func(orbID string) error
	// hookRotateBegin（仅测试）在 rotateToken 开头、轮换事务之前调用。
	hookRotateBegin func(orbID string)
	// hookAfterFunc（仅测试）替换 runner 离线定时器的 time.AfterFunc（见 runner.go afterFunc）。
	hookAfterFunc func(d time.Duration, fn func()) func() bool
	// hookPolicyRebuilt（仅测试）在 policyFor 重建出策略之后、写缓存之前调用。
	hookPolicyRebuilt func(orbID string)
	// runnerOffline 是各 runner orb 的离线定时器（受 mu 保护，见 runner.go）。
	runnerOffline map[string]*runnerTimer
	// runnerWithheld 是最近一次写进事件备注的「被扣下的文件」摘要，键为「runner orb/线程」（全局层线程为空；M21，受 mu
	// 保护，见 NoteRunnerWithheld）。
	runnerWithheld map[string]string
	// closed 在 Close 之后为真（受 mu 保护）。
	closed bool

	// poolKick 是容量 1 的对账触发通道（PoolKick）；poolBackoff 是各项目池 orb setup 失败后的退避截止时间（受 mu 保护）。
	poolKick    chan struct{}
	poolBackoff map[string]time.Time

	// omp 是 omp 配置立即下发（M13，见 apply.go；受 mu 保护，nil = 未开启）。
	omp *ompApplier
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
		userTouched: map[string]*userTouch{},
		poolKick:    make(chan struct{}, 1), poolBackoff: map[string]time.Time{},
		runnerOffline: map[string]*runnerTimer{}, runnerWithheld: map[string]string{},
	}
	s.reqs[string(apigen.MethodProjectScriptsGet)] = s.onProjectScriptsGet
	s.reqs[string(apigen.MethodProjectScriptsSet)] = s.onProjectScriptsSet
	// M15：orb 内 GitHub 工具与 github_prepare 的通过上报（runner 的 link 不放行，见 runnerRequests）
	s.reqs[string(apigen.MethodGithubCall)] = s.onGithubCall
	s.reqs[string(apigen.MethodGithubPrepareReport)] = s.onGithubPrepareReport
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

// SetGitHub 设置 GitHub 集成（M14）；须在服务对外可用之前调用。
func (s *Service) SetGitHub(g GitHub) { s.github = g }

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
	// job 是最近一次 enqueueIn 入队的任务 id。
	job string
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
	// check 在事务内、状态变化之前求值，可读其它表（如引用 orb 的线程）；返回 false 时不做变化。
	check func(q *db.Queries) (bool, error)
	// then 在状态变化成功后于同一事务内执行（入队任务等）；返回错误则整个事务回滚。
	then func(t *txn) error
	// spawnJob 为真时把 then 入队的任务 id 记进这次 orb_events 的 detail.spawnJob：之后结束的任务据此认出
	// 自己是不是这次变化入队的（stopSpawn），不靠时间先后。
	spawnJob bool
}

// transitionTx 在一个事务里以比较并交换的方式把 orb 从 from 移到 to、写 orb_events，并执行 opts.then；
// 只提交不发布：调用方在合适的时机（例如闸门释放线程锁之后）调用 publish。
func (s *Service) transitionTx(ctx context.Context, orbID, from, to string, detail map[string]any, opts transitionOpts) (*change, error) {
	if detail == nil {
		detail = map[string]any{}
	}
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
		if opts.check != nil {
			ok, err := opts.check(q)
			if err != nil || !ok {
				return err
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
		t := &txn{ctx: ctx, q: q, now: now}
		if opts.then != nil {
			if t.orb, err = q.GetOrb(ctx, orbID); err != nil {
				return err
			}
			if err := opts.then(t); err != nil {
				return err
			}
		}
		if opts.spawnJob && t.job != "" {
			detail["spawnJob"] = t.job
		}
		raw, _ := json.Marshal(detail)
		seq, err := q.InsertOrbEventSeq(ctx, db.InsertOrbEventSeqParams{OrbID: orbID, Ts: now, FromState: from, ToState: to, Detail: string(raw)})
		if err != nil {
			return err
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
	if !IsRunner(ch.orb) {
		// runner 的 connecting 不计 hello 超时：它没有 provider 去重试，断开由离线定时器处理
		s.armConnectTimer(ch.orb.ID, ch.to, ch.seq)
	}
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
	j, err := s.jobs.EnqueueTx(t.ctx, t.q, t.now, t.orb.ProviderID, t.orb.ID, op, params)
	if err != nil {
		return err
	}
	t.signal, t.job = true, j.ID
	return nil
}

// rotateIn 在事务内轮换 orb 令牌（token_gen+1，比较并交换），返回新令牌；旧令牌随即不能再建立 link。
func (s *Service) rotateIn(t *txn) (string, error) {
	gen := t.orb.TokenGen + 1
	token := orbToken(s.cfg.TokenKey, t.orb.ID, gen)
	n, err := t.q.RotateOrbToken(t.ctx, db.RotateOrbTokenParams{TokenHash: ids.Hash(token), ID: t.orb.ID, TokenGen: t.orb.TokenGen})
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", fmt.Errorf("orbs: orb %s 的令牌代数已变化", t.orb.ID)
	}
	t.orb.TokenGen = gen
	return token, nil
}

// spawnIn 在事务内轮换 orb 令牌（token_gen+1）并入队携带新令牌的 spawn 任务。
func (s *Service) spawnIn(t *txn) error {
	token, err := s.rotateIn(t)
	if err != nil {
		return err
	}
	t.kick = true
	p := apigen.JobSpawnParams{
		Image: "", Type: t.orb.Type,
		Env: apigen.JobSpawnEnv{OrynURL: s.cfg.PublicURL, OrynOrbID: t.orb.ID, OrynOrbToken: token},
	}
	if s.cfg.OrbCAPEM != "" {
		p.OrbCaPem = new(s.cfg.OrbCAPEM)
	}
	if t.orb.SnapshotOf != nil && *t.orb.SnapshotOf != "" {
		p.SnapshotID = t.orb.SnapshotOf
		// 项目快照可以缺失（provider 上被清理）：退回基础镜像、照常 setup；fork 快照是用户数据，必须存在。
		if s.isProjectSnapshot(t.ctx, t.q, *t.orb.SnapshotOf) {
			p.SnapshotOptional = new(true)
		}
	}
	if t.orb.SandboxNetwork != nil {
		p.Sandbox = &apigen.JobSpawnSandbox{Network: apigen.SandboxNetwork(*t.orb.SandboxNetwork)}
	}
	return s.enqueueIn(t, jobs.OpSpawn, p)
}

// tokenDeliverable 报告该状态下能否经 spawn 送达轮换后的令牌：机器在跑（或正被拉起）。这些状态下该 orb 排在前面的
// 任务只可能是 spawn / snapshot（pause 只在 freezing / stopping 入队），送达用的 spawn 不会把已冻结、停机的机器
// 又拉起来；provider 对已在运行的机器只写令牌文件并 SIGHUP orbd，orbd 随即用新令牌重拨。
func tokenDeliverable(state string) bool {
	switch state {
	case StateRequested, StateSpawning, StateWaking, StateConnecting, StateSettingUp, StateResuming, StateReady:
		return true
	}
	return false
}

// rotateToken 在 orb 被拍快照之后轮换它的令牌（S1 纵深防御）：快照（项目快照或 fork 快照）里残留的令牌随即作废，
// 从快照派生的机器即使从磁盘里恢复出旧令牌，也连不上源 orb 的 link。
//
// 机器在跑时复用 spawnIn（与认领 ready 池 orb 同一条路径，状态不变）：同一事务入队携带新令牌的 spawn，提交后关闭
// 令牌代数旧于新代数的当前 link（orbLink.tokenGen；包括轮换进行中用旧令牌刚建立、已通过复核的 link；不误关 orbd
// 用新令牌重拨的 link。提交之后才发布的旧令牌 link 复核时令牌已失效，自行关闭），再唤醒 provider。冻结、停机等状态只轮换：
// 下次唤醒的 spawn 会再轮换并送达。freezing 期间轮换、冻结又失败回到 ready 的 orb，现有 link 不受影响，但 link 断开后
// 要等下一次 spawn（如手动停机再唤醒）才能重连。协调 orb（link 不经令牌鉴权）与 runner（令牌归用户保管）不轮换。
//
// 返回错误表示令牌没能轮换（快照里那一代令牌仍有效）：调用方必须放弃并回收该快照，不得发布或交付。
func (s *Service) rotateToken(ctx context.Context, orbID string) error {
	if s.hookRotateBegin != nil {
		s.hookRotateBegin(orbID)
	}
	var orb db.Orb
	rotated, delivered := false, false
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		if s.hookRotateToken != nil {
			if err := s.hookRotateToken(orbID); err != nil {
				return err
			}
		}
		cur, err := q.GetOrb(ctx, orbID)
		if err != nil {
			return err
		}
		if cur.Coordinator || IsRunner(cur) || cur.State == StateDeleting || cur.State == StateDeleted {
			return nil
		}
		t := &txn{ctx: ctx, q: q, now: s.nowMs(), orb: cur}
		if tokenDeliverable(cur.State) {
			err = s.spawnIn(t)
		} else {
			_, err = s.rotateIn(t)
		}
		if err != nil {
			return err
		}
		orb, rotated, delivered = t.orb, true, t.signal
		return nil
	})
	if err != nil {
		return fmt.Errorf("%w: %v", errTokenRotate, err)
	}
	if !rotated {
		return nil
	}
	slog.Info("快照后已轮换 orb 令牌", "orb", orbID, "state", orb.State, "gen", orb.TokenGen, "delivered", delivered)
	if delivered {
		s.mu.Lock()
		cur := s.links[orbID]
		s.mu.Unlock()
		if cur != nil && cur.tokenGen < orb.TokenGen {
			_ = cur.sess.Close()
		}
		s.jobs.Signal(orb.ProviderID)
	}
	return nil
}

// errTokenRotate 表示快照已拍成、但随后轮换源 orb 令牌失败：快照里的令牌仍可用，快照必须作废。
var errTokenRotate = errors.New("orbs: 快照后轮换 orb 令牌失败")

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

// Defaults 返回配置的 orb_defaults（客户端据此预先显示新 orb 的落点，解析顺序见 resolveTarget）。
func (s *Service) Defaults() config.OrbDefaults { return s.cfg.Defaults }

// resolveProvider 按「显式 → 项目默认 → orb_defaults → 源 orb → 唯一 provider」确定 provider。
// 内置的 @host 只给协调 orb 用：显式指定它一律拒绝，「唯一 provider」回退也不计入它。
func (s *Service) resolveProvider(ctx context.Context, name string, prj *db.Project, src *db.Orb) (db.Provider, error) {
	if name == "" && prj != nil && prj.DefaultProvider != nil {
		name = *prj.DefaultProvider
	}
	if name == "" {
		name = s.cfg.Defaults.Provider
	}
	if name == HostProviderName {
		return db.Provider{}, fmt.Errorf("%w: provider %q is reserved", ErrNoProvider, HostProviderName)
	}
	if name != "" {
		p, err := s.st.Q.GetProviderByName(ctx, name)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && p.Driver == DriverRunner) {
			// runner 的 provider 行不经 provider 进程、不能新建 orb：对外与不存在相同
			return db.Provider{}, fmt.Errorf("%w: provider %q 不存在", ErrNoProvider, name)
		}
		if err == nil && p.DisabledAt != nil {
			return p, fmt.Errorf("%w: provider %q 已停用", ErrNoProvider, name)
		}
		return p, err
	}
	if src != nil {
		p, err := s.st.Q.GetProvider(ctx, src.ProviderID)
		if err == nil && p.Driver == HostDriver {
			return db.Provider{}, fmt.Errorf("%w: provider %q is reserved", ErrNoProvider, HostProviderName)
		}
		if err == nil && p.Driver == DriverRunner {
			return db.Provider{}, fmt.Errorf("%w: %w", ErrNoProvider, ErrRunnerUnsupported)
		}
		return p, err
	}
	all, err := s.st.Q.ListProviders(ctx)
	if err != nil {
		return db.Provider{}, err
	}
	var live []db.Provider
	for _, p := range all {
		if p.DisabledAt == nil && p.Driver != HostDriver && p.Driver != DriverRunner {
			live = append(live, p)
		}
	}
	if len(live) == 1 {
		return live[0], nil
	}
	return db.Provider{}, fmt.Errorf("%w: 未指定 provider 且无法确定默认值（共 %d 个可用）", ErrNoProvider, len(live))
}

// target 是新 orb 的落点：项目、provider、类型与规格（Create 与 ClaimPool 共用）。
type target struct {
	prj  *db.Project
	prov db.Provider
	typ  string
	spec *apigen.OrbType
}

// resolveTarget 按「项目 → provider → 类型 → 规格」解析新 orb 的落点。
func (s *Service) resolveTarget(ctx context.Context, p CreateParams) (target, error) {
	var t target
	if p.ProjectID != "" {
		v, err := s.st.Q.GetProject(ctx, p.ProjectID)
		if err != nil {
			return t, fmt.Errorf("读取项目 %s: %w", p.ProjectID, err)
		}
		t.prj = &v
	}
	var src *db.Orb
	if p.SnapshotOf != "" {
		if v, err := s.st.Q.GetOrb(ctx, p.SnapshotOf); err == nil {
			src = &v
		}
	}
	prov, err := s.resolveProvider(ctx, p.Provider, t.prj, src)
	if err != nil {
		return t, err
	}
	t.prov = prov
	typ := p.Type
	if typ == "" && src != nil && p.Provider == "" {
		typ = src.Type
	}
	if typ == "" && t.prj != nil && t.prj.DefaultOrbType != nil {
		typ = *t.prj.DefaultOrbType
	}
	if typ == "" {
		typ = s.cfg.Defaults.Type
	}
	if typ == "" {
		typ = prov.DefaultType
	}
	if typ == "" {
		return t, fmt.Errorf("%w: 无法确定 orb 类型（provider %q 尚未上线？）", ErrBadType, prov.Name)
	}
	t.typ = typ
	types := providerTypes(prov)
	for i := range types {
		if types[i].Name == typ {
			t.spec = &types[i]
		}
	}
	if t.spec == nil && len(types) > 0 {
		return t, fmt.Errorf("%w: provider %q 没有类型 %q", ErrBadType, prov.Name, typ)
	}
	return t, nil
}

// providerTypes 解析 provider 上报的类型表。
func providerTypes(prov db.Provider) []apigen.OrbType {
	var types []apigen.OrbType
	_ = json.Unmarshal([]byte(prov.OrbTypes), &types)
	return types
}

// Create 新建 orb（requested）并入队 spawn 任务。有项目时按 M11a 2.2 选用该项目的就绪快照。
func (s *Service) Create(ctx context.Context, p CreateParams) (_ db.Orb, err error) {
	if p.SandboxNetwork != "" && (p.SandboxNetwork != SandboxNetNone && p.SandboxNetwork != SandboxNetEgress ||
		p.Coordinator || p.Pool || p.Box || p.Computer || p.ProjectID != "" || p.SnapshotID != "" || p.SnapshotOf != "" ||
		p.BrowserProfileID != "" || p.Provider == "") {
		return db.Orb{}, fmt.Errorf("%w: 沙箱 orb 须指定 provider 与网络模式，不属于项目，不能来自快照、预热池或 Profile", ErrInvalidState)
	}
	if p.Coordinator {
		prov, err := s.st.Q.GetProviderByName(ctx, HostProviderName)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && prov.DisabledAt != nil) {
			return db.Orb{}, fmt.Errorf("%w: 内置 provider %q 尚未注册", ErrNoProvider, HostProviderName)
		}
		if err != nil {
			return db.Orb{}, err
		}
		return s.insertOrb(ctx, CreateParams{Coordinator: true}, target{prov: prov, typ: HostType}, snapPick{})
	}
	if p.Pool && (p.ProjectID == "" || p.SnapshotID != "" || p.BrowserProfileID != "") {
		return db.Orb{}, fmt.Errorf("%w: 预热池 orb 须属于项目，且不能指定快照或浏览器 Profile", ErrInvalidState)
	}
	if p.Computer && (p.Pool || p.ProjectID != "" || p.SnapshotID != "" || p.SnapshotOf != "") {
		return db.Orb{}, fmt.Errorf("%w: 助手的电脑不属于项目，不能来自快照或预热池", ErrInvalidState)
	}
	if p.Box && (p.Pool || p.ProjectID != "" || p.SnapshotID != "" || p.SnapshotOf != "") {
		return db.Orb{}, fmt.Errorf("%w: box 的 orb 不属于项目，不能来自快照或预热池", ErrInvalidState)
	}
	if p.SnapshotOf != "" {
		if src, err := s.st.Q.GetOrb(ctx, p.SnapshotOf); err == nil && IsRunner(src) {
			// runner 的目录是用户机器上的，没有可拍的环境
			return db.Orb{}, ErrRunnerUnsupported
		}
	}
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
	t, err := s.resolveTarget(ctx, p)
	if err != nil {
		return db.Orb{}, err
	}
	return s.insertOrb(ctx, p, t, s.pickProjectSnapshot(ctx, p, t))
}

// insertOrb 写入 orb 行（requested）、requested 事件与首个 spawn 任务（同一事务），然后发布。
// pick.snapID 非空时在同一事务里登记一次项目快照的使用；快照已被退役（0 行）则照常从基础镜像起。
func (s *Service) insertOrb(ctx context.Context, p CreateParams, t target, pick snapPick) (db.Orb, error) {
	orbID := ids.Orb()
	now := s.nowMs()
	// 第 0 代令牌从不下发：入队 spawn 时立即轮换到第 1 代。
	base := db.CreateOrbParams{
		ID: orbID, ProviderID: t.prov.ID, Type: t.typ, Image: p.SnapshotID,
		State: StateRequested, StateChangedAt: now, TokenHash: ids.Hash(orbToken(s.cfg.TokenKey, orbID, 0)), CreatedAt: now,
		Coordinator: p.Coordinator, BaseImageID: t.prov.BaseImageID,
	}
	if t.spec != nil {
		// 0 = 不限；0.5 核这类小于 1 的配额至少记 1 核，免得取整后被当成不限
		if t.spec.Cpus > 0 {
			base.Cpus = max(1, int64(math.Round(float64(t.spec.Cpus))))
		}
		base.MemoryMb = int64(t.spec.MemoryMB)
	}
	if t.prj != nil {
		base.ProjectID = &t.prj.ID
	}
	if p.SnapshotID != "" {
		base.SnapshotOf = &p.SnapshotID
	}
	if pick.hash != "" {
		base.ConfigHash = &pick.hash
	}
	if p.Pool {
		base.PoolState = new(PoolWarming)
	}
	if p.GithubIdentity != "" {
		base.GithubIdentity = &p.GithubIdentity
	}
	if p.Automation {
		base.Automation = new(true)
	}
	if p.Computer {
		base.Computer = new(int64(1))
	}
	if p.Box {
		base.Box = new(int64(1))
	}
	if p.SandboxNetwork != "" {
		base.SandboxNetwork = &p.SandboxNetwork
	}
	// orb、requested 事件与首个 spawn 任务同一事务提交：不存在「有 orb 没任务」的中间状态。
	var ch *change
	var params db.CreateOrbParams
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		params = base
		detail := map[string]any{"provider": t.prov.Name, "type": t.typ}
		if p.SnapshotOf != "" {
			detail["sourceOrb"] = p.SnapshotOf
		}
		if p.Pool {
			detail["pool"] = true
		}
		if p.SandboxNetwork != "" {
			detail["sandbox"] = true
		}
		if pick.snapID != "" {
			n, err := q.TouchProjectSnapshotUse(ctx, db.TouchProjectSnapshotUseParams{LastUsedAt: now, ID: pick.snapID})
			if err != nil {
				return err
			}
			if n == 1 {
				params.Image, params.SnapshotOf = pick.snapID, &pick.snapID
				detail["projectSnapshot"] = pick.snapID
			}
		}
		raw, _ := json.Marshal(detail)
		if p.Computer {
			// 全库至多一台未删除的助手的电脑（M19 R23）：与插入同一事务复核
			cur, err := q.GetComputerOrb(ctx)
			if err == nil && cur.State != StateDeleting && cur.State != StateDeleted {
				return fmt.Errorf("%w: 助手的电脑已存在（%s）", ErrInvalidState, cur.ID)
			}
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if err := q.CreateOrb(ctx, params); err != nil {
			return err
		}
		seq, err := q.InsertOrbEventSeq(ctx, db.InsertOrbEventSeqParams{OrbID: orbID, Ts: now, FromState: "", ToState: StateRequested, Detail: string(raw)})
		if err != nil {
			return err
		}
		tx := &txn{ctx: ctx, q: q, now: now}
		if tx.orb, err = q.GetOrb(ctx, orbID); err != nil {
			return err
		}
		if err := s.spawnIn(tx); err != nil {
			return err
		}
		if p.BrowserProfileID != "" {
			if err := bindProfileIn(ctx, q, orbID, p.BrowserProfileID, p.ProfileMode, now); err != nil {
				return err
			}
		}
		orb, err := q.GetOrb(ctx, orbID)
		ch = &change{orb: orb, from: "", to: StateRequested, seq: seq, detail: string(raw), signal: true}
		return err
	})
	if err != nil {
		return db.Orb{}, fmt.Errorf("创建 orb: %w", err)
	}
	slog.Info("orb 已创建", "orb", orbID, "provider", t.prov.Name, "type", t.typ, "snapshot", params.Image, "pool", p.Pool)
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
	if IsSandbox(orb) {
		// 沙箱的生命周期归 sandboxes 服务：停机即销毁，不唤醒
		return fmt.Errorf("%w: orb %s 是沙箱", ErrInvalidState, orbID)
	}
	if orb.Box != 0 && !s.boxAllowed(orbID) {
		return ErrBoxStopped
	}
	if IsRunner(orb) {
		// runner 只能自己接入：stopped = 离线，其余状态无事可做
		switch orb.State {
		case StateStopped:
			return ErrRunnerOffline
		case StateDeleting, StateDeleted:
			return fmt.Errorf("%w: orb %s 已删除", ErrInvalidState, orbID)
		}
		return nil
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
	if IsRunner(orb) {
		return ErrRunnerUnsupported
	}
	if IsSandbox(orb) {
		return fmt.Errorf("%w: orb %s 是沙箱，不能暂停", ErrInvalidState, orbID)
	}
	switch {
	case mode == PauseFreeze && orb.State == StateFrozen, mode == PauseStop && orb.State == StateStopped:
		return nil
	case mode == PauseFreeze && orb.State == StateReady:
		// 冻结前的 Profile 活体保存必须先于 freezeTx（它与 pause 任务同一事务提交）。
		s.preFreeze(ctx, orbID)
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
		opts.then, opts.spawnJob = s.spawnIn, true
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

// stopSpawn 报告 spawn 任务是否就是这次停机要等的那一次：orb 最近一次状态变化是 frozen → stopping，且这次变化在同一事务里
// 入队、记进 detail.spawnJob 的就是这个任务。之前入队、在 stopping 期间才结束的 spawn（如快照后送达新令牌的那次），
// 以及 ready → stopping（根本不入队 spawn）期间结束的 spawn 都不算：它成功会再发一遍 shutdown 与 pause{stop}，失败会把
// 正常停机误判为 failed。按任务标识认，不比较时间：两个事务可以落在同一毫秒。
func (s *Service) stopSpawn(ctx context.Context, job db.Job, orb db.Orb) bool {
	ev, err := s.st.Q.GetLatestOrbStateChange(ctx, orb.ID)
	if err != nil {
		slog.Warn("读取 orb 最近一次状态变化失败", "orb", orb.ID, "err", err)
		return false
	}
	if ev.FromState != StateFrozen || ev.ToState != StateStopping {
		return false
	}
	var detail struct {
		SpawnJob string `json:"spawnJob"`
	}
	return json.Unmarshal([]byte(ev.Detail), &detail) == nil && detail.SpawnJob != "" && detail.SpawnJob == job.ID
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
	if h := s.desktopHooksFor(l); h != nil {
		h.BeforeShutdown(ctx, orbID)
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
	_, err := s.deleteIf(ctx, orbID, nil)
	return err
}

// DeleteUnused 在除 except 之外没有任何线程（含已归档的）引用 orb 时删除它，返回是否删除；except 为空 = 不能有任何线程。
// orb 的生命周期跟随线程：最后一个线程删除后由线程服务调用（except 为空）；定时的 keepLast 归档旧运行后调用（except =
// 那个运行线程，只回收它独占的 orb，别的线程——哪怕已归档——还在用就保留）。except 只在它此刻仍是归档状态时才排除：
// 归档与这里分两次提交，其间被取消归档的运行线程照常算作引用，orb 保留。「无引用」与 → deleting 在同一事务里判定，
// 写线程行的事务又复核 orb 未进入删除（threads.checkOrbTx），并发挂上来的线程与回收串行。runner、协调 orb、助手的电脑
// 与 box 各有归属，从不在这里删除。
func (s *Service) DeleteUnused(ctx context.Context, orbID, except string) (bool, error) {
	orb, err := s.Get(ctx, orbID)
	if err != nil {
		return false, err
	}
	if IsRunner(orb) || orb.Coordinator || orb.Computer != 0 || orb.Box != 0 || IsSandbox(orb) {
		return false, nil
	}
	return s.deleteIf(ctx, orbID, func(q *db.Queries) (bool, error) {
		n, err := q.CountOtherThreadsByOrb(ctx, db.CountOtherThreadsByOrbParams{OrbID: orbID, ExceptID: except})
		return n == 0, err
	})
}

// deleteIf 是 Delete 与 DeleteUnused 的共同实现：check 非 nil 时在状态变化的事务里求值，返回 false 则不删除。
func (s *Service) deleteIf(ctx context.Context, orbID string, check func(q *db.Queries) (bool, error)) (bool, error) {
	for {
		orb, err := s.Get(ctx, orbID)
		if err != nil {
			return false, err
		}
		if orb.State == StateDeleted || orb.State == StateDeleting {
			return false, nil
		}
		if IsRunner(orb) {
			// runner 的 orb 随 runner 一起删除（DeleteRunner）
			return false, ErrRunnerUnsupported
		}
		frozen := orb.State == StateFrozen || orb.State == StateFreezing
		opts := transitionOpts{}
		if frozen {
			opts.then = func(t *txn) error { return s.enqueueIn(t, jobs.OpDelete, nil) }
		}
		declined := false
		if check != nil {
			opts.check = func(q *db.Queries) (bool, error) {
				ok, err := check(q)
				declined = err == nil && !ok
				return ok, err
			}
		}
		ch, err := s.transitionTx(ctx, orbID, orb.State, StateDeleting, nil, opts)
		if err != nil {
			return false, err
		}
		if ch == nil {
			if declined {
				return false, nil
			}
			continue
		}
		s.publish(ch)
		s.mu.Lock()
		delete(s.wakeAfter, orbID)
		s.mu.Unlock()
		if !frozen {
			s.goBG(func() { s.shutdownThenDelete(orbID, 0) })
		}
		return true, nil
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
// 项目快照不走这里：它们由 sweepSnapshots 按退役与引用回收。
func (s *Service) releaseSnapshot(ctx context.Context, providerID, orbID, snap string) {
	if s.isProjectSnapshot(ctx, s.st.Q, snap) {
		return
	}
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
	if IsRunner(orb) {
		return "", ErrRunnerUnsupported
	}
	if IsSandbox(orb) {
		return "", fmt.Errorf("%w: orb %s 是沙箱，不能拍快照", ErrInvalidState, orbID)
	}
	snap := ids.Snapshot()
	if _, err := s.snapshotAs(ctx, orb, snap); err != nil {
		if errors.Is(err, errTokenRotate) {
			// 快照已在 provider 上、却带着仍有效的令牌：不交给调用方，入队删除
			if _, derr := s.jobs.Enqueue(context.WithoutCancel(ctx), orb.ProviderID, orb.ID, jobs.OpDeleteSnapshot, apigen.JobDeleteSnapshotParams{SnapshotID: snap}); derr != nil {
				slog.Warn("入队快照删除失败", "snapshot", snap, "err", derr)
			}
		}
		return "", err
	}
	return snap, nil
}

// snapshotAs 为 orb 入队以 snap 为名的 snapshot 任务并等待完成，返回终态任务（其 result 带 snapshotBytes）。
// 成功后轮换 orb 令牌（见 rotateToken）：快照里那一代令牌在快照交给任何人使用之前就已作废；轮换失败时返回
// errTokenRotate，调用方负责回收快照（Snapshot 入队删除；项目快照经 discardBuild 删除 building 行与镜像）。
func (s *Service) snapshotAs(ctx context.Context, orb db.Orb, snap string) (db.Job, error) {
	if orb.State == StateDeleting || orb.State == StateDeleted {
		return db.Job{}, fmt.Errorf("%w: orb %s 已删除", ErrInvalidState, orb.ID)
	}
	job, err := s.jobs.Enqueue(ctx, orb.ProviderID, orb.ID, jobs.OpSnapshot, apigen.JobSnapshotParams{SnapshotID: snap})
	if err != nil {
		return db.Job{}, err
	}
	done, err := s.jobs.Wait(ctx, job.ID)
	if err != nil {
		return db.Job{}, err
	}
	if done.State != jobs.StateSucceeded {
		msg := "snapshot failed"
		if done.Error != nil {
			msg = *done.Error
		}
		return done, fmt.Errorf("快照 orb %s: %s", orb.ID, msg)
	}
	if err := s.rotateToken(context.WithoutCancel(ctx), orb.ID); err != nil {
		return done, err
	}
	return done, nil
}

// SetSandboxNetwork 为沙箱 orb 入队 set_network 任务并等待完成（M30）。任务失败时返回的错误文本就是任务的错误原文
// （provider 以 `network_unavailable:` 开头表示创建时没有网卡、不能开网），调用方据此映射错误码。
func (s *Service) SetSandboxNetwork(ctx context.Context, orbID, network string) error {
	if network != SandboxNetNone && network != SandboxNetEgress {
		return fmt.Errorf("%w: 网络模式 %q", ErrInvalidState, network)
	}
	orb, err := s.Get(ctx, orbID)
	if err != nil {
		return err
	}
	if !IsSandbox(orb) {
		return fmt.Errorf("%w: orb %s 不是沙箱", ErrInvalidState, orbID)
	}
	if orb.State == StateDeleting || orb.State == StateDeleted {
		return fmt.Errorf("%w: orb %s 已删除", ErrInvalidState, orbID)
	}
	job, err := s.jobs.Enqueue(ctx, orb.ProviderID, orb.ID, jobs.OpSetNetwork, apigen.JobNetworkParams{Network: apigen.SandboxNetwork(network)})
	if err != nil {
		return err
	}
	done, err := s.jobs.Wait(ctx, job.ID)
	if err != nil {
		return err
	}
	if done.State != jobs.StateSucceeded {
		msg := "set_network failed"
		if done.Error != nil {
			msg = *done.Error
		}
		return errors.New(msg)
	}
	return nil
}

// TouchUser 记录一次用户交互。
func (s *Service) TouchUser(ctx context.Context, orbID string) {
	_ = s.touchUser(ctx, orbID, s.now())
}

// touchUser 把 at 写为 orb 最近一次用户交互的时间；失败记日志并返回错误。
func (s *Service) touchUser(ctx context.Context, orbID string, at time.Time) error {
	ts := at.UnixMilli()
	err := s.st.Q.TouchOrbUserInteraction(ctx, db.TouchOrbUserInteractionParams{Ts: &ts, ID: orbID})
	if err != nil {
		slog.Warn("记录用户交互失败", "orb", orbID, "err", err)
	}
	return err
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
	if job.Op == jobs.OpDeleteSnapshot {
		if ok {
			s.onSnapshotDeleted(ctx, job)
		}
		return
	}
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
			// S-P1-1：是否真的从项目快照起来以 provider 的 snapshotUsed 为准。只看这台 orb 第一次成功的 spawn（F4：不看
			// 当前是否 waking——首次 spawn 失败、Wake 重试的那次成功才第一次确认来源；此后的 spawn 复用已确认的机器）。
			// 只有明确 true 才保留 snapshot_of（同一事务清除，runPhase 看到的已是新值）：
			//   - false：退回了基础镜像，快照退役 image_missing，这台 orb 可以开新血统；
			//   - 缺省（如 dispatch 脚本没打标记）：来源未知，镜像未被证明缺失，快照不退役；
			//     在 orb 事件里记下来源未知，afterSetupSnapshot 据此禁止它发布任何新快照。
			var fellBack string
			var then func(t *txn) error
			if r := jobs.ResultOf(job); ok && (r.SnapshotUsed == nil || !*r.SnapshotUsed) &&
				orb.SnapshotOf != nil && *orb.SnapshotOf != "" && s.isProjectSnapshot(ctx, s.st.Q, *orb.SnapshotOf) &&
				s.firstSpawn(ctx, orbID) {
				src := *orb.SnapshotOf
				if r.SnapshotUsed == nil {
					detail[detailSnapshotUnknown] = src
				} else {
					fellBack = src
					detail["snapshotUsed"] = false
				}
				then = func(t *txn) error {
					_, err := t.q.ClearOrbSnapshot(t.ctx, db.ClearOrbSnapshotParams{ID: orbID, SnapshotOf: src})
					return err
				}
			}
			if _, moved, _ := s.transitionThen(ctx, orbID, orb.State, to, detail, then); moved && ok {
				if fellBack != "" {
					s.retireSnapshot(ctx, fellBack, RetireImageMissing)
				}
				s.advance(orbID)
			}
		case StateStopping:
			if !s.stopSpawn(ctx, job, orb) {
				slog.Info("忽略不属于这次停机的 spawn", "orb", orbID, "job", job.ID)
				return
			}
			if ok {
				s.goBG(func() { s.shutdownThenPauseStop(orbID, s.cfg.ConnectTimeout/2) })
			} else {
				_, _, _ = s.transition(ctx, orbID, StateStopping, StateFailed, detail)
			}
		}
	case jobs.OpPause:
		if jobPauseMode(job) == PauseFreeze {
			if ok {
				_, _, _ = s.transition(ctx, orbID, StateFreezing, StateFrozen, detail)
			} else if !s.refreshClaimed(ctx, orbID, StateFreezing, detail) {
				// 冻结失败回到 ready；冻结期间被认领的池 orb（CP-3）改走与认领 ready 相同的刷新路径，不能直接 ready
				_, _, _ = s.transition(ctx, orbID, StateFreezing, StateReady, detail)
			}
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
			s.dropPortalState(ctx, orbID)
			if err := s.st.Q.DeleteOrbDesktop(ctx, orbID); err != nil {
				slog.Warn("清理 orb 桌面镜像失败", "orb", orbID, "err", err)
			}
			if orb.SnapshotOf != nil && *orb.SnapshotOf != "" {
				s.releaseSnapshot(ctx, orb.ProviderID, orbID, *orb.SnapshotOf)
			}
		}
	}
}

// Run 恢复中间状态并运行空闲评估，直到 ctx 结束。任务队列的租约巡检（jobs.Queue.Run）由调用方另行运行。
// 对账之后收回升级前（或上次运行期间）下发、主人现在没有同意的环境变量降级密钥（见 envwithdraw.go）。
func (s *Service) Run(ctx context.Context) {
	s.recover(ctx)
	s.WithdrawEnvKeys(ctx, "startup")
	t := time.NewTicker(s.cfg.EvalInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.EvaluateIdle(ctx)
			s.sweepSnapshots(ctx)
			s.poolReconcile(ctx)
		case <-s.poolKick:
			s.poolReconcile(ctx)
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
	s.recoverPool(ctx)
}

func (s *Service) reconcile(ctx context.Context, orb db.Orb) error {
	if IsRunner(orb) {
		// runner 的 link 在重启后必然断了：在线状态回到 stopped，等它重连（不计 connect 超时、没有任务可对账）
		if orb.State == StateReady || orb.State == StateConnecting {
			_, _, err := s.transition(ctx, orb.ID, orb.State, StateStopped, map[string]any{"reason": "control plane restarted"})
			return err
		}
		return nil
	}
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
		// 不属于这次停机的 spawn（stopSpawn）不算：否则会把它的终态当作停机进度重放，而不是补发 shutdown → pause{stop}
		j = latest(true, func(j db.Job) bool {
			return op(jobs.OpPause)(j) || (j.Op == jobs.OpSpawn && s.stopSpawn(ctx, j, orb))
		})
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

// EvaluateIdle 执行一次空闲评估：ready 且空闲超时、或线程已全部归档 → 冻结；frozen 超过 stop_after_frozen → 停机。
// 两者都经冻结闸门（tryQuiesce）提交，空闲条件在提交事务内对最新的行复核（归档触发的冻结只由闸门确认静止）。
func (s *Service) EvaluateIdle(ctx context.Context) {
	for _, state := range []string{StateReady, StateFrozen} {
		orbs, err := s.st.Q.ListOrbsByState(ctx, state)
		if err != nil {
			slog.Warn("空闲评估失败", "err", err)
			return
		}
		for _, orb := range orbs {
			if IsRunner(orb) || orb.Box != 0 || orb.Coordinator || IsSandbox(orb) {
				// runner 与 box 是常驻的：不冻结、不停机。协调 orb 同样常驻：全局助手要随时能立即回复，
				// 冻结它就是结束协调宿主子进程与其中全部 omp，下一条消息要付完整的冷启动（见 coord.keepWarm）。
				// 沙箱的空闲回收归 sandboxes 服务（到期即销毁），这里不冻结
				continue
			}
			idle := s.idleFrozenTooLong
			reason := "idle"
			if state == StateReady {
				idle = s.idleReady
				if !idle(orb) && s.retired(ctx, orb) {
					// 线程全部归档：不等空闲计时，交给闸门确认静止后立即冻结（与 Amp 归档即暂停一致）
					idle, reason = nil, "archived"
				}
			}
			if idle != nil && !idle(orb) {
				continue
			}
			if state == StateReady {
				// 冻结前的 Profile 活体保存（只在静止且有东西可存时）；之后 freezeTx 仍在事务内复核空闲条件。
				s.preFreeze(ctx, orb.ID)
			}
			var ch *change
			var cerr error
			s.tryQuiesce(orb.ID, func() bool {
				if state == StateReady {
					ch, cerr = s.freezeTx(ctx, orb.ID, reason, idle)
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

// SetOps 在运行时换掉空闲策略与快照配置（控制面运维参数，见 internal/opsconf）。空闲三项在下一个评估周期生效；
// 快照上限在下一次建快照时生效，调小不会退役已有快照。
func (s *Service) SetOps(idle config.Idle, snaps config.Snapshots) {
	s.idle.Store(&idle)
	s.snaps.Store(&snaps)
}

// idleCfg 返回当前生效的空闲策略。
func (s *Service) idleCfg() config.Idle {
	if p := s.idle.Load(); p != nil {
		return *p
	}
	return s.cfg.Idle
}

// snapCfg 返回当前生效的快照配置。
func (s *Service) snapCfg() config.Snapshots {
	if p := s.snaps.Load(); p != nil {
		return *p
	}
	return s.cfg.Snapshots
}

// idleReady 判断 ready 的 orb 是否已超过 agent 与用户空闲时长。
func (s *Service) idleReady(orb db.Orb) bool {
	now := s.nowMs()
	agent := max(orb.StateChangedAt, deref64(orb.LastAgentActivityAt))
	user := max(orb.StateChangedAt, deref64(orb.LastUserInteractionAt))
	idle := s.idleCfg()
	return now-agent >= idle.AgentIdle.D().Milliseconds() && now-user >= idle.UserIdle.D().Milliseconds()
}

// retired 判断 orb 是否只剩已归档的线程（至少有一个线程，且都已归档）。协调 orb 与助手的电脑不属于线程，不在此列；
// 预热池里尚未认领的 orb 没有线程，同样不算。
func (s *Service) retired(ctx context.Context, orb db.Orb) bool {
	if orb.Coordinator || orb.Computer != 0 || orb.Box != 0 || IsSandbox(orb) {
		return false
	}
	n, err := s.st.Q.OrbThreadCounts(ctx, orb.ID)
	if err != nil {
		slog.Warn("统计 orb 的线程失败", "orb", orb.ID, "err", err)
		return false
	}
	return n.Total > 0 && n.Unarchived == 0
}

// idleFrozenTooLong 判断 frozen 的 orb 是否已冻结超过 stop_after_frozen。
func (s *Service) idleFrozenTooLong(orb db.Orb) bool {
	return s.nowMs()-orb.StateChangedAt >= s.idleCfg().StopAfterFrozen.D().Milliseconds()
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
	// runner 的离线定时器：关闭之后断开的 link 也不再起新的（见 armRunnerOffline）
	s.closed = true
	for _, t := range s.runnerOffline {
		t.stop()
	}
	s.runnerOffline = map[string]*runnerTimer{}
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
