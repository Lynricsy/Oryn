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

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
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
	// AgentStatus 表示线程 agent 的进程状态变了（快照 agent.status 的增量）。
	AgentStatus(threadID string, status apigen.AgentStatus)
	Orb(o apigen.Orb)
	// TermOutput 是 terminal_output 帧（热路径，未持线程锁）：只发给开启了 term.watch 的连接，可丢弃。
	TermOutput(threadID string, frame json.RawMessage)
	// TermLagged 表示 orbd 丢弃过该线程的终端输出（terminal.gap）：观看终端的连接须重同步。
	TermLagged(threadID string)
	// TermResized 表示某个连接的 terminal_resize 已入队：广播给该线程全部订阅者。
	TermResized(threadID, terminalID string, cols, rows int)
}

// Config 是线程服务参数。
type Config struct {
	// DataDir 是控制面数据目录；blob 存于 <DataDir>/blobs/<前 2 位>/<sha256>。
	DataDir string
	// WakeOnFocus 对应 idle.wake_on_focus。
	WakeOnFocus bool
	// AgentStartTimeout 是 agent.start 的应答超时（默认 60s）。
	AgentStartTimeout time.Duration
	// CallTimeout 是内部 omp 命令（fork_from 等）的超时（默认 2 分钟，含唤醒时间）；也是带附件的命令等待 agent 就绪的上限。
	CallTimeout time.Duration
	// ProvisionTimeout 是新建线程的首条提示词（带附件）等待 orb 与 agent 就绪的上限（默认 30 分钟）。
	ProvisionTimeout time.Duration
	// PortalURLTemplate 返回线程的 portal 地址模板（短码已代入、保留 `{port}`）；返回空串或为 nil 时
	// Thread.portalUrlTemplate 省略（未配置 portal_domain）。
	PortalURLTemplate func(threadID string) string
	// Memory 决定 agent.start 是否启用内置记忆（M16）；nil = 不启用。
	Memory MemoryStarter
	// Projects 是项目级 omp 配置（M21：runner 线程的项目层随 agent.start 交付）；nil = 不交付。
	Projects orbs.ProjectConfigs
	// BoxThreadsChanged 在 box 的 orb 上建、共用 fork、删线程之后调用（M23：box 页的活动流与用量图都数线程，而在已经
	// ready 的 orb 上增删线程不产生任何 orb 事件）；nil = 不通知。
	BoxThreadsChanged func()
}

// DeliveredAttachment 是已投递到 orb 的附件（Path 为 orb 内绝对路径）。
type DeliveredAttachment struct {
	ID, Name, Mime, Path string
	Size                 int64
}

// Attachments 是消息附件服务（internal/attachments 实现，M3 计划 5.3）。
type Attachments interface {
	// Check 在新建线程前预检附件（存在、未绑定、数量与去重），避免白建 orb。
	Check(ctx context.Context, ids []string) error
	// BindTx 在建线程的事务里把附件绑定到新线程。
	BindTx(ctx context.Context, q *db.Queries, threadID string, ids []string) error
	// Deliver 绑定附件并确保它们都在 orb 内（orb 须已可用，不唤醒）。
	Deliver(ctx context.Context, orbID, threadID string, ids []string) ([]DeliveredAttachment, error)
	// DeleteThread 删除线程的全部附件（线程行删除之前调用）。
	DeleteThread(ctx context.Context, threadID string) error
}

// Service 是线程服务。
type Service struct {
	cfg  Config
	st   *store.Store
	orbs Orbs
	sink atomic.Pointer[sinkBox]
	obs  atomic.Pointer[observerList]
	// obsMu 串行化 AddObserver。
	obsMu sync.Mutex
	// coord 是协调服务（M9，SetCoordinator 在服务对外可用之前设置；nil = 不支持协调线程）。
	coord Coordinator
	// attach 由 SetAttachments 在服务对外可用之前设置；nil = 不支持附件。
	attach Attachments
	// profiles 是浏览器 Profile 服务（M8；nil = 不支持）。
	profiles Profiles
	// lib 是全局助手文件库（M13，SetLibrary 在服务对外可用之前设置；nil = 助手对话不接受 orynFiles）。
	lib LibraryFiles
	// runnerAgent 是 runner 线程的 SSH 身份与项目层附加交付（M21，SetRunnerAgent 在服务对外可用之前设置；nil = 不支持）。
	runnerAgent RunnerAgent
	// cfgSync 是 /omfg 回写（M12.2，SetConfigSync 在服务对外可用之前设置；nil = 不回写）；cfgSyncTimeout 是一次回写的上限。
	cfgSync        ConfigSync
	cfgSyncTimeout time.Duration
	// cfgSchema 是 omp 设置 schema（M13 6.3，SetConfigSchema 在服务对外可用之前设置；nil = 未知，键变化按需重启处理）。
	cfgSchema ConfigSchema
	// gh 解析自动化线程的 GitHub 身份（M14，SetGitHub 在服务对外可用之前设置；nil = 一律 user）。
	gh GitHub

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// goMu 保护 closing：Close 开始后 go_ 不再登记新任务。否则链路协程收到帧时
	// 调 wg.Add，会与 Close 里的 wg.Wait 并发（WaitGroup 不允许计数归零后 Add 与 Wait 并发）。
	goMu    sync.Mutex
	closing bool

	mu        sync.Mutex
	threads   map[string]*thread
	focus     map[string]string // 连接 id → 正在看的线程
	orbStates map[string]orbCache
	touched   map[string]time.Time  // orb → 上次 TouchAgent（节流）
	userTouch map[string]time.Time  // orb → 上次 TouchUser（节流）
	blobs     map[string]*blobFetch // sha256 → 在途的 blob.fetch
	// activity 是 working 线程的当前活动（最近一次工具调用的意图，见 activity.go）；只在内存中，不落库。
	activity map[string]string
	// agentFail 是 failed 线程（agent 没能启动）的失败原因（Thread.error 的 agent_failed）；只在内存中，与 thread.failed 同生同灭。
	agentFail map[string]string

	nextID atomic.Uint64

	events eventQueue

	// afterIngestCommit（仅测试）在条目入库事务提交后、内存状态更新前调用。
	afterIngestCommit func()
	// afterSpacePrecheck（仅测试）在 Create / Patch 的 Space 预检之后、写引用的事务之前调用（契约 C4）。
	afterSpacePrecheck func()
}

// orbCache 是 orb 状态缓存；seq 为得出该状态的 orb_events.id，用于丢弃乱序到达的过时事件（CP9）。
type orbCache struct {
	state string
	seq   int64
}

// blobFetch 是一次在途的 blob.fetch：只接受该 orb 为它打开的 blob 流（M5）。
type blobFetch struct {
	orbID     string
	done      chan struct{}
	closeOnce sync.Once
	claimed   bool // 已有 blob 流在接收（由 Service.mu 保护）
}

func (f *blobFetch) closeDone() { f.closeOnce.Do(func() { close(f.done) }) }

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
func (nopSink) AgentStatus(string, apigen.AgentStatus)  {}
func (nopSink) Orb(apigen.Orb)                          {}
func (nopSink) TermOutput(string, json.RawMessage)      {}
func (nopSink) TermLagged(string)                       {}
func (nopSink) TermResized(string, string, int, int)    {}

// New 创建线程服务并向 orbs 注册 entries.* / agent.* 处理器、agent 与 blob 流处理器、事件订阅与空闲判定。
func New(cfg Config, st *store.Store, orbs Orbs) *Service {
	if cfg.AgentStartTimeout <= 0 {
		cfg.AgentStartTimeout = 60 * time.Second
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = 2 * time.Minute
	}
	if cfg.ProvisionTimeout <= 0 {
		cfg.ProvisionTimeout = 30 * time.Minute
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		cfg: cfg, st: st, orbs: orbs, ctx: ctx, cancel: cancel,
		threads:   map[string]*thread{},
		focus:     map[string]string{},
		orbStates: map[string]orbCache{},
		touched:   map[string]time.Time{},
		userTouch: map[string]time.Time{},
		blobs:     map[string]*blobFetch{},
		activity:  map[string]string{},
		agentFail: map[string]string{},
	}
	s.sink.Store(&sinkBox{nopSink{}})
	s.obs.Store(&observerList{})
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
	orbs.HandleNotify(string(apigen.MethodTerminalGap), s.onTerminalGap)
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

// SetAttachments 设置附件服务；须在 orb link 接入与 API 对外服务之前调用。
func (s *Service) SetAttachments(a Attachments) { s.attach = a }

// RunnerAgent 是 runner 线程 agent.start 的附加交付与 SSH 身份（M21，由 server 组装 sshgw / orbs / 分享前密钥扫描的来源）。
type RunnerAgent interface {
	// ProjectSSH 返回线程项目层里合成的 ssh.json（该线程可用的本项目主机）；nil = 没有。
	ProjectSSH(ctx context.Context, threadID string) ([]byte, error)
	// KnownSecrets 返回无密钥投递校验用的已知密钥（名 → 明文；密钥库全部值 + SSH 密码登录主机的密码）。
	KnownSecrets(ctx context.Context) (map[string]string, error)
	// NoteWithheld 把线程项目层里被扣下的文件写进 runner orb 的事件备注（清单不变时不重复写）。
	NoteWithheld(ctx context.Context, orbID, threadID string, withheld []agentcfg.Withheld)
	// RegisterThreadKey 登记 runner 线程的 SSH 公钥（agent.start 结果的 sshPublicKey）；同一线程的旧公钥随之失效。
	RegisterThreadKey(threadID, orbID, authorizedKey string) error
	// DropThreadKey 注销线程的 SSH 公钥。
	DropThreadKey(threadID string)
	// DropOrbKeys 注销一台 runner 上全部线程的 SSH 公钥。
	DropOrbKeys(orbID string)
}

// SetRunnerAgent 设置 runner 线程的 SSH 身份与项目层附加交付（M21）；须在 orb link 接入之前调用。
func (s *Service) SetRunnerAgent(r RunnerAgent) { s.runnerAgent = r }

// SetProfiles 设置浏览器 Profile 服务（M8）；须在 API 对外服务之前调用。未设置时建线程不能指定 Profile。
func (s *Service) SetProfiles(p Profiles) { s.profiles = p }

// SetAfterSpacePrecheck 设置 Create / Patch 在 Space 预检之后、写引用的事务之前调用的钩子（仅测试：其它包的回归测试
// 在这里同步执行完整的 DeleteSpace，契约 C4）。须在服务对外可用之前调用。
func (s *Service) SetAfterSpacePrecheck(fn func()) { s.afterSpacePrecheck = fn }

// GitHub 给线程服务解析全自动线程的 GitHub 身份（M14 / M15）。
type GitHub interface {
	// ResolveAutomationSlot 返回身份槽位；note 非空时是 Agent 账号不可用、回退到我的账号的提示（Q6）。
	ResolveAutomationSlot(ctx context.Context, untrusted bool) (slot, note string, err error)
}

// SetGitHub 设置 GitHub 集成（M14）；须在服务对外可用之前调用。
func (s *Service) SetGitHub(g GitHub) { s.gh = g }

func (s *Service) out() Sink { return s.sink.Load().Sink }

// Close 停止后台任务。之后再调 go_ 的任务直接丢弃（ctx 已取消，启动了也做不了事）。
func (s *Service) Close() {
	s.goMu.Lock()
	s.closing = true
	s.goMu.Unlock()
	s.cancel()
	s.wg.Wait()
}

func (s *Service) now() int64 { return time.Now().UnixMilli() }

// go_ 在服务生命周期内启动后台任务；Close 开始后不再启动，返回 false。
func (s *Service) go_(fn func(ctx context.Context)) bool {
	s.goMu.Lock()
	defer s.goMu.Unlock()
	if s.closing {
		return false
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn(s.ctx)
	}()
	return true
}

// thread 是单个线程的内存状态。
type thread struct {
	id         string
	sessionDir string
	// kind 是线程种类（work / space / assistant，创建后不变）。
	kind string
	// main 为真表示全局助手的主线对话（threads.main，M19；创建后不变）。
	main bool
	// research 是研究员对话的角色（threads.research，M26："web" / "private"，其余为空；创建后不变）。
	research string
	// cwd 是 runner 线程的工作目录（threads.cwd，创建后不变；空 = orb 线程，由 orbd 决定）。
	cwd string

	mu sync.Mutex
	// hidden 为真表示尚未被认领的备用助手对话或隐藏研究员对话（threads.hidden；ClaimSpare 认领后置假）。
	hidden bool
	// sessionStartPending 为真表示常驻空跑（主线 / 备用 / 研究员）在就绪时延迟了 oryn_mark_session_start，
	// 避免把尚未有任何消息的空会话经 ensureOnDisk() 提前落盘；首条非只读命令下发前按 sessionStartExplicit 补发。
	sessionStartPending  bool
	sessionStartExplicit bool
	orbID                string
	live                 *Live
	agentStatus          apigen.AgentStatus
	agent                *agentConn
	queue                [][]byte
	pending              map[string]*pendingCmd
	settled              bool
	state                string
	unread               bool
	leaf                 *string
	lastSeq              int64
	resetting            map[string]bool
	ignored              map[string]bool // fork_from 产物：移出前被 tail 到的文件，丢弃
	// forking > 0 时有 fork_from 在途：其间新出现的主会话文件暂存在 held，不入库（见 onAppend / endForkLocked）。
	forking  int
	held     map[string][]apigen.LinkCtlEntriesAppendParams
	starting bool
	// wakeAfterStart 表示刷新停止已完成，但须等在途的旧 start 结束后再检查队列、唤醒新一代。
	wakeAfterStart bool
	// statusSinceStart 为真表示最近一次发起 agent.start 之后收到过 starting / ready（M21）：之后到达的「监督器结束」才属于
	// 当前监督器，才注销 runner 线程的 SSH 公钥。旧监督器的 exited 由 orbd 在处理新 agent.start 之前发出，即使晚于其应答
	// 到达，也排在新监督器的 starting / ready 之前——此时它为假，不误删新登记的公钥。agent.start 的应答带回的公钥等于
	// sshKey（同一代监督器，orbd 不重报状态）时恢复发起前的值。exitSkipped / exitDropped 记下发起 agent.start 之后、
	// 处理其应答之前到达的终止结束（因它为假而跳过 / 当场注销），应答处理据此不把已结束的一代登记回去。
	statusSinceStart bool
	exitSkipped      bool
	exitDropped      bool
	sshKey           string // 最近一次 agent.start 应答的 runner 线程 SSH 公钥：每代监督器一把，用来认出同一代
	failed           bool
	model            *string
	thinking         *string
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
	// gapUI 是 agent.gap 时实时缓冲里仍待答的请求 id（含 PlanRequestID）：gap 只是帧不连续，同一进程会在新流上重放
	// 仍待答的交互，因此不立即对它们发 InputResolved；gap 之后第一份成功的 get_state 应答到达时（重放帧必然先于它）
	// 对没有重现的发 InputResolved（reconcileGapUILocked）；实时缓冲真正丢弃（resetLiveLocked）时一并解决。
	gapUI map[string]bool
	// transcriptRev 是转录版本（threads.transcript_rev）。
	transcriptRev int64
	// ingestedLeafJumps 记录已被 entries.append 离分支条目（oryn-leaf 等）推进的叶子切换：
	// 对应的 switch_leaf / navigate_tree 应答若晚于文件入库到达（哪怕期间已有新消息越过该叶子），
	// 按此记录去重，避免把已前进的 t.leaf 回退覆盖或重复递增 transcript_rev。
	ingestedLeafJumps []ingestedLeafJump
	// deliveries 是在途的附件投递数（命令在投递完成后才入队）：>0 时线程算 working、不静止。
	deliveries int
	// msgOrder 是已受理、尚未入队的消息命令（prompt / steer / follow_up / abort_and_prompt），按受理顺序排列：
	// 带附件的消息受理时即占位，投递完成后才可下发；其后的消息排在它后面，不得越过。非消息命令不经过这里。
	msgOrder []*msgSlot
	// spaceID 是线程所属 Space（空 = 无）；Space 聊天线程为其 Space。
	spaceID string
	// autoActive 为真表示当前这段活动里下发过自动化消息（宿主工具调用的 OwnerTurn = !autoActive，只用于审计）；
	// session_settled 或 agent 退出时清零。
	autoActive bool
	// segMsgs 为真表示自上次 session_settled（或 agent 退出）以来已下发过消息命令；autoOnly 据此判断“静止后的第一条”。
	segMsgs bool
	// autoOnly 为真表示这段活动里下发的消息全是自动化投递（静止后的第一条取其来源，其后逐条取与）；quiet 为真表示
	// 助手在这段自动化活动里调用了 stay_silent（MarkQuiet）：离开 working 时不置未读、不排完成通知。两者只在下一条
	// 消息下发时改写，因此 working→idle 的 StateEvent 带的仍是刚结束那一段的值（M19 步骤 3.3）。
	autoOnly bool
	quiet    bool
	// hostCalls 是在途的宿主工具调用：omp 的调用 id → 取消函数（协调线程）。
	hostCalls map[string]context.CancelFunc
	// hostUI 是控制面发起的待答请求（RequestUI，M19 确认卡）：请求 id → extension_ui_request 帧。它们不来自 agent，
	// 主人的应答不写 agent 流而交给 Coordinator.UIAnswered；实时缓冲重置后重新放回；不挡冻结闸门。
	hostUI map[string]json.RawMessage
	// refresh 为真时，线程在静止后停掉 agent，下一条命令经唤醒路径重新启动（refreshLocked：协调线程取新的
	// AgentProfile；工作线程让需重启的 omp 配置生效）。
	refresh bool
	// stopping 从发起刷新停止到收到 agent.stop 应答为真；期间命令排队，且不得启动下一代。
	stopping bool
	// turns 是已获成功应答、等 prompt_result 的 prompt / abort_and_prompt（含无标签的，按应答顺序，至多 64 项）；
	// steers 是已获成功应答的带标签 steer（等下一个终态 agent_end）；lastYield 是最近一次 yield 的结局（契约 C1）。
	turns     []turnRef
	steers    []string
	lastYield *yieldInfo
	// cfgSyncs 是 omfg 应答已移交后台、仍在回写中央配置的条数（M12.2，omfgSaveLocked 登记、finishOmfg 撤销）：
	// 应答已离开 pending，靠它挡住冻结闸门，免得回写读 orb 时 orb 被冻结。
	cfgSyncs int
	// reloadPending 为真表示 omp 配置变化后还欠一次 reload_plugins（会话静止时下发）；restartReasons 是需重启才能
	// 生效的配置变化（M13 6.3，见 apply.go）。agent 退出时都清空。
	reloadPending  bool
	restartReasons []string
	// variant 是 orbd 在最近一次 agent.start 应答里报回的启动变体（M26；幂等应答时是已有监督器的那个）；variantKnown
	// 为真表示这份回报属于当前的 agent.start（发起新的 agent.start 时置假，应答到达后置真）。新的 agent.start 不清 variant：
	// 宿主工具的放行按最近一次回报判断，宁可多拦。
	variant      string
	variantKnown bool
	// planMode 为真表示最新 get_state 处于计划模式：syncModelLocked 不将临时计划模型/思考级别写回常驻线程行，
	// 重启时 initFrames 也不传显式模型与思考级别，由 Core 从持久化会话恢复计划态与原基线。
	planMode bool
	// pendingModelSync 是全局助手主线离线或即将重启时待显式应用的模型/思考档位切换帧（如恢复默认时的
	// oryn_select_model{role:"default"} + set_thinking_level），持久化在 threads.startup_frames 中，
	// 在下次 onReadyLocked 优先于普通队列下发并在确认后清除（不进入 queue、不阻塞静止停机、不唤醒离线主线）。
	pendingModelSync []json.RawMessage
}

// msgSlot 是消息顺序队列里的一个位置。ready 之前 frame 为 nil；ready 且 frame 为 nil 表示该消息已失败
// （已以合成应答结束），出队时跳过。
type msgSlot struct {
	frame []byte
	p     *pendingCmd
	ready bool
}

const queueLimit = 100

// ErrNotFound 表示线程不存在。
var ErrNotFound = &Error{Code: "not_found", Message: "thread not found"}

// Error 是线程服务返回给 API / WS 的业务错误。
type Error struct {
	Code    string
	Message string
	// Body 非 nil 时 API 以它作为响应体（替代 {code, message}），如 profile_in_use 的 ProfileInUseConflict。
	Body any
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
		// 线程尚未对外可见：在登记前排好已受理的首批命令，保证它们先于任何其它命令下发；
		// 主线纯模型/档位复位帧挂到 pendingModelSync，在下次 onReadyLocked 优先下发而不唤醒离线主线。
		t.mu.Lock()
		if t.main && isModelSyncFrames(startup) {
			t.pendingModelSync = startup
		} else {
			s.queueStartupLocked(t, startup)
		}
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
		id: row.ID, sessionDir: row.SessionDir, kind: row.Kind,
		live: NewLive(), agentStatus: apigen.AgentStatusNone,
		pending: map[string]*pendingCmd{}, resetting: map[string]bool{}, ignored: map[string]bool{}, held: map[string][]apigen.LinkCtlEntriesAppendParams{},
		settled: true, state: row.State, unread: row.Unread, leaf: row.LeafEntryID, lastSeq: row.LastSeq,
		model: row.Model, thinking: row.ThinkingLevel, hostCalls: map[string]context.CancelFunc{},
	}
	t.main = row.Main == 1
	t.hidden = row.Hidden == 1
	if row.Research != nil {
		t.research = *row.Research
	}
	if t.kind == "" {
		t.kind = KindWork
	}
	if row.SpaceID != nil {
		t.spaceID = *row.SpaceID
	}
	if row.Cwd != nil {
		t.cwd = *row.Cwd
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
	delete(s.activity, id)
	delete(s.agentFail, id)
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

// Focused 报告是否有连接正在看该线程。
func (s *Service) Focused(threadID string) bool {
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
	st := t.live.Status(orbReady)
	if st == StateIdle && t.deliveries > 0 {
		return StateWorking
	}
	return st
}

// recompute 重新推导状态；变化时写库、处理未读并推送 index.patch，返回是否变化。调用方持有 t.mu。
func (s *Service) recompute(ctx context.Context, t *thread) bool {
	st := s.status(t)
	if st == t.state {
		return false
	}
	prev := t.state
	t.state = st
	if st != StateWorking {
		s.clearActivity(t.id) // 下面的 publish 一并推送清空
	}
	now := s.now()
	if err := s.st.Q.SetThreadState(ctx, db.SetThreadStateParams{State: st, StateChangedAt: now, ID: t.id}); err != nil {
		slog.Warn("threads: 写入线程状态失败", "thread", t.id, "err", err)
	}
	focused := s.Focused(t.id)
	// 助手在自动化回合里选择沉默（MarkQuiet）：结束时不置未读；出错仍照常
	silent := t.quiet && st != StateError
	if prev == StateWorking && st != StateWorking && st != StateProvisioning && !t.unread && !focused && !silent {
		t.unread = true
		_ = s.st.Q.SetThreadUnread(ctx, db.SetThreadUnreadParams{Unread: true, ID: t.id})
	}
	ev := StateEvent{ThreadID: t.id, Prev: prev, State: st, Focused: focused, Quiet: silent, Auto: t.autoOnly}
	if st == StateError {
		ev.Reason = s.errorReason(ctx, t)
		// 原因只是 prompt_result 报错：线程未失败、orb 不是 failed、agent 重启未耗尽（契约 C1）
		ev.PromptError = !t.failed && s.orbState(t.orbID) != orbStateFailed && !t.live.restartExhausted
	}
	s.observer().StateChanged(ev)
	s.publish(ctx, t.id)
	return true
}

// MarkQuiet 记下助手在这段活动里选择沉默（主线的 stay_silent，M19 步骤 3.3）：只在这段活动的消息全是自动化投递时
// 生效并返回真；主人亲自发过消息的回合返回假，照常结束。生效后离开 working 不置未读、StateEvent 带 Quiet。
func (s *Service) MarkQuiet(ctx context.Context, threadID string) bool {
	t, err := s.get(ctx, threadID)
	if err != nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.autoOnly {
		return false
	}
	t.quiet = true
	return true
}

// publish 读出线程最新行并推送 index.patch；备用对话（hidden）在认领之前不推送。调用方持有 t.mu（或线程尚未对外可见）。
func (s *Service) publish(ctx context.Context, id string) {
	row, err := s.st.Q.GetThread(ctx, id)
	if err != nil || row.Hidden != 0 {
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
			slog.Debug("threads: 冻结闸门未通过", "orb", orbID, "thread", t.id,
				"settled", t.settled, "unsynced", t.unsynced, "starting", t.starting,
				"deliveries", t.deliveries, "msgOrder", len(t.msgOrder), "liveQuiescent", t.liveQuiescentLocked(),
				"queue", len(t.queue), "pending", len(t.pending), "cfgSyncs", t.cfgSyncs)
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
	return t.settled && !t.unsynced && !t.starting && t.deliveries == 0 && len(t.msgOrder) == 0 && t.liveQuiescentLocked() && len(t.queue) == 0 && len(t.pending) == 0 && t.cfgSyncs == 0
}

// liveQuiescentLocked 同 Live.Quiescent，但不把控制面发起的待答请求（hostUI）算作阻塞：它们的应答不经 agent，
// 冻结后照样能作答，实时缓冲重置后也会重新放回。调用方持有 t.mu。
func (t *thread) liveQuiescentLocked() bool {
	if t.live.PlanProposal != nil || t.live.working {
		return false
	}
	for id := range t.live.pendingUI {
		if _, ok := t.hostUI[id]; !ok {
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

// touchUser 节流地记录一次用户交互（每 orb 至多 1s 一次：终端按键每次都是一条 cmd）。
func (s *Service) touchUser(orbID string) {
	if orbID == "" {
		return
	}
	s.mu.Lock()
	now := time.Now()
	if now.Sub(s.userTouch[orbID]) < time.Second {
		s.mu.Unlock()
		return
	}
	s.userTouch[orbID] = now
	s.mu.Unlock()
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
	if ev.Kind == OrbEventSetup || ev.Kind == OrbEventServices || ev.Kind == OrbEventDesktop || ev.Kind == OrbEventPool {
		// 只有运行记录 / 服务镜像变化：线程状态不受影响。事件里的 orb 行可能早于随后的状态变化，重读一次再推送，
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
		if orbGoingDown(state) && s.runnerAgent != nil {
			// M21：runner 离线定时器到期（→ stopped）或 orb 停机 / 删除：清掉这台上全部线程的 SSH 公钥。link 断开的宽限期内
			// 状态不变、登记保留；重连后的 agent.start（幂等）回同一把公钥。
			s.runnerAgent.DropOrbKeys(ev.OrbID)
		}
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
			t.stopping = false
			t.wakeAfterStart = false
			s.setAgentStatusLocked(t, apigen.AgentStatusNone)
			t.unsynced = false
			s.resetLiveLocked(t)
			_ = s.st.Q.SetThreadOrb(ctx, db.SetThreadOrbParams{OrbID: nil, UpdatedAt: s.now(), ID: t.id})
			s.broadcastLiveResetLocked(t)
			s.publish(ctx, t.id)
		case ev.Kind == OrbEventLinkDown:
			// agent 仍在 orbd 中运行；重连后 agent.start 会重新上报 ready
			if t.agentStatus == apigen.AgentStatusReady {
				s.setAgentStatusLocked(t, apigen.AgentStatusStarting)
			}
		}
		if orbGoingDown(state) && len(t.queue) > 0 {
			s.dropPassiveLocked(t, "orb "+state)
		}
		if !orbSuspended(state) && t.agentReady() {
			if len(t.queue) > 0 {
				// orb 暂停期间（或缓存滞后时）就绪的 agent 留下的排队命令：现在可以下发（先补发挂起的 reload_plugins）
				s.onReadyLocked(ctx, t)
			} else {
				// M13 6.3：冻结期间改了配置、解冻时会话本就静止，不会再有 session_settled：在这里补发
				s.reloadPluginsLocked(t)
			}
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
			// 协调对话按需启动（每个都是一个上百 MB 的 omp）：只启动重启前正忙、尚未启动过、或有排队命令的；
			// 其余在收到命令（Command / Deliver）或被唤醒（Wake）时才起 agent。主线例外：它常驻，随时能立即回复。
			if r.Kind != KindWork && r.Main == 0 && r.State != StateWorking && r.State != StateAwaitingInput && r.State != StateProvisioning && !s.hasQueued(ctx, r.ID) {
				continue
			}
			s.scheduleStart(r.ID)
		}
	}
}
