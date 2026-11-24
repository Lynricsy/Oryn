// Package github 是 Oryn 与 GitHub 的深度集成（M14 / M15）：OAuth App 授权的两个 GitHub 身份（我的账号 / Agent 账号）、
// 仓库绑定与仓库 hook、orb 内凭据、事件回流与触发规则。REST / GraphQL 用 net/http 手写，不引入第三方 SDK。
package github

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/schedule"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// 时间与上限常量（规格见 docs/plan-m14.md「全局约束」）。
const (
	hookPollInterval    = 30 * time.Second // 投递日志轮询周期（docs/plan-m15.md「全局约束」）
	hookPollRelaxed     = 5 * time.Minute  // 最近 hookRecentWebhook 内收到过 webhook 的 hook 放宽到此间隔
	hookRecentWebhook   = 10 * time.Minute
	hookSyncInterval    = 10 * time.Minute // 全量重新校正仓库 hook 的周期
	deliveriesQuotaMin  = 50               // 投递日志额度剩余低于它时暂停轮询到额度重置
	flushInterval       = 5 * time.Second
	debounce            = 15 * time.Second
	rollupDebounce      = 5 * time.Second
	permTTL             = 5 * time.Minute
	repoListTTL         = 60 * time.Second
	stateTTL            = 10 * time.Minute
	maxPerLinkPerHour   = 20
	maxCIRounds         = 5
	triggerMaxPerHour   = 10
	maxDeliveryAttempts = 3
	deliveryRetention   = 7 * 24 * time.Hour
	outboxMaxAge        = 24 * time.Hour
	webhookMaxBody      = 25 << 20
	// 防回流与审计（M15 步骤 2.3，docs/plan-m15.md「全局约束」）
	selfWriteWindow    = 120 * time.Second // 无正文的动作与预登记的匹配时间窗
	selfWriteRetention = 7 * 24 * time.Hour
	auditRetention     = 30 * 24 * time.Hour
	selfRecheckDelay   = 5 * time.Second // 槽位登录名的事件还没命中、且该槽位有写操作在途时，延后复查一次
	// REST 轮询（M15 步骤 3.1）
	restPollInterval = 60 * time.Second // 仓库级与对象级 watch 的轮询周期
	rateLimitFloor   = 500              // 槽位的核心额度剩余低于它时，该槽位的轮询间隔加倍
	watchRetire      = 24 * time.Hour   // 对象进入终态满这么久后 watch 退场
	// 触发规则「任何人」档的按人限流（M15 R13）：每小时每个发送者的运行次数上限；OWNER / MEMBER / COLLABORATOR 不限
	anyoneMaxPerHour      = 3
	contributorMaxPerHour = 10
	// issue 任务（M15 步骤 4.3 / 5.5）
	scanInterval           = 60 * time.Second // 任务时限与问题自动关闭共用的扫描周期
	maxCompletionReminders = 2                // 每个任务至多补发的完成提醒次数（R25）
	// freezeAfterTurnGrace 是「回合结束后冻结」的兜底宽限：登记后这么久线程仍没离开 working（agent 没有结束回合），
	// scan 中止当前回合并直接冻结。工具应答经 ctl、orbd 送达 omp 只需秒级，宽限远大于它。
	freezeAfterTurnGrace = 5 * time.Minute
	// OAuth 短命令牌刷新（GitHub 2026-08-14 起新建 OAuth App 默认 8 小时过期，附带 6 个月 refresh_token）。
	tokenRefreshSkew    = 5 * time.Minute  // 距离到期不足此值时主动轮换，避免请求在途过期
	tokenRefreshTimeout = 20 * time.Second // 脱离调用方取消的刷新超时，确保已轮换的 refresh_token 完整落盘
)

// oauthScopes 是授权请求的 scope（固定字面量，见 docs/plan-m15.md「全局约束」）。
const oauthScopes = "repo workflow read:org admin:repo_hook gist write:packages user:email"

// 身份槽位。
const (
	slotUser  = "user"
	slotAgent = "agent"
)

// Threads 是 GitHub 集成用到的线程能力（*threads.Service 实现）。
type Threads interface {
	Create(ctx context.Context, p threads.CreateParams) (apigen.Thread, error)
	Deliver(ctx context.Context, threadID string, m threads.AutoMessage, ack func(err error)) (cancel func() bool, err error)
	Publish(ctx context.Context, threadID string)
	MarkShipped(ctx context.Context, threadID string, at int64) error
	// SetModel 切换线程的模型与思考级别（M15 R14 的指令覆盖）；m 为零值时不改模型，thinking 为空时不改级别。
	SetModel(ctx context.Context, threadID string, m apigen.ModelRef, thinking string) error
	// Abort 中止线程正在进行的回合（M15 R25 的任务时限）；线程不在工作中时什么也不做。
	Abort(ctx context.Context, threadID string) error
}

// Orbs 是 GitHub 集成用到的 orb 能力（M15 R15：任务进入终态时冻结线程的 orb；*orbs.Service 经适配器实现）。
type Orbs interface {
	Freeze(ctx context.Context, orbID string) error
}

// ModelCatalog 是模型目录的只读查询（控制面的中央模型目录实现；github 不导入 modelcat）：`/model <别名>` 在其中查找。
type ModelCatalog interface {
	Models(ctx context.Context) ([]apigen.ModelRef, error)
}

// Notifier 发通知（*notify.Service 实现）。PostThread：标题是线程标题，kind 为 done / error，正文写明 GitHub 事件；
// PostSystem：不属于任何线程的通知（如规则因 Agent 账号不可用而没有建线程，Q6），kind 为 error，link 是站内路径。
type Notifier interface {
	PostThread(ctx context.Context, threadID, kind, body string) error
	PostSystem(ctx context.Context, kind, title, body, link string) error
}

// Attachments 上传一个附件（*attachments.Service 实现）。
type Attachments interface {
	Upload(ctx context.Context, name, threadID string, r io.Reader) (db.Attachment, error)
}

// Sampler 立即采样一个线程相对基线的提交（*stats.Service 实现）。
type Sampler interface {
	SampleCommits(ctx context.Context, threadID string) error
}

// Config 是 GitHub 服务的配置；零值字段取默认。
type Config struct {
	PublicURL      string // cfg.PublicURL
	APIURL, WebURL string // cfg.GitHub.*
	Box            *secrets.Box
	HTTP           *http.Client     // 默认 &http.Client{Timeout: 20 * time.Second}
	Now            func() time.Time // 默认 time.Now
	PollInterval   time.Duration    // 默认 hookPollInterval
	FlushInterval  time.Duration    // 默认 flushInterval
	Debounce       time.Duration    // 默认 debounce
	// AfterFunc 默认包一层 time.AfterFunc；CI 汇总的防抖计时器用，测试注入。
	AfterFunc func(d time.Duration, f func()) (stop func())
	// Changed 在状态变化后广播；nil = 不广播。
	Changed func()
	// HandOff 在开着“交给助手”的触发规则线程的一个 GitHub 回合结束（任何终态）时调用（M19 步骤 3.3，在任务队列里）；
	// nil = 不交接。
	HandOff func(ctx context.Context, h schedule.HandOff)
}

// Error 是 GitHub 服务的领域错误；Status 为 0 时由调用方按 Code 映射。
type Error struct {
	Status        int
	Code, Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return "github: " + e.Code
	}
	return "github: " + e.Code + ": " + e.Message
}

func errf(code, msg string) *Error { return &Error{Code: code, Message: msg} }

// Service 是 GitHub 集成的控制面服务。
type Service struct {
	cfg     Config
	st      *store.Store
	th      Threads
	n       Notifier
	att     Attachments
	sampler Sampler
	models  ModelCatalog // nil = 没有模型目录：`/model` 只在规则的模型池里查找
	orbs    Orbs         // nil = 不冻结（测试）
	// repoSync 是项目仓库镜像的拉取入口（M24）；nil = push 事件不触发拉取（测试）。
	repoSync RepoSync
	// intn 返回 [0, n) 的随机数（模型池随机取一个，测试注入）。
	intn func(n int) int

	mu        sync.Mutex
	repoLists map[[32]byte]repoListEntry // 用户令牌的 sha256 → 仓库列表（步骤 2.2，惰性初始化）
	perms     map[permKey]permEntry      // (仓库, 登录名) → 驱动判定的权限结果（惰性初始化）
	// 令牌刷新（受 refreshMu 保护，惰性初始化）：每槽位同时只发一次 refresh_token 轮换请求。
	refreshMu    sync.Mutex
	refreshCalls map[string]*refreshCall

	// PR 跟踪（步骤 5.1 / 5.2，受 mu 保护，惰性初始化）：线程 → 上次按 HEAD 反查过 PR 的 sha；排队中的 CI 汇总。
	sampledHeads map[string]string
	rollups      map[pullKey]*pendingRollup
	// freezeAfterTurn（M15 4.3，受 mu 保护，惰性初始化）：线程 → 登记时刻。线程自己的工具（github_abort_task）让任务进入
	// 终态时，冻结推迟到线程离开 working（agent 已收到工具结果、回合结束）再做；超过 freezeAfterTurnGrace 仍未离开时由
	// scan 中止回合并冻结（任务已是 abandoned，任务时限扫描不再管它）。只在内存里：orynd 在此之间重启时这次冻结丢失，
	// 与任务队列相同。
	freezeAfterTurn map[string]time.Time

	// 事件分发（步骤 4.1）：处理器注册表、任务队列、队列中 / 处理中的投递 guid。
	handlers map[string]handler
	jobs     chan func(ctx context.Context)
	qmu      sync.Mutex
	inflight map[string]bool

	// 仓库 hook（M15 步骤 1.2 / 1.3）：hookMu 串行化 hook 的增删改，避免绑定与定时同步并发时重复建 hook；
	// pollMu 串行化投递日志轮询并保护 hookPolled（各仓库上次轮询的时间）与 hookPause（额度见底后的恢复时刻）。
	hookMu     sync.Mutex
	pollMu     sync.Mutex
	hookPolled map[int64]time.Time
	hookPause  time.Time

	// selfInflight 是各槽位经出站代理在途（已 Begin 未 End）的 GitHub 写操作数（步骤 2.3，受 mu 保护，惰性初始化）。
	selfInflight map[string]int

	// REST 轮询（M15 步骤 3.1）：watchMu 串行化轮询轮次，并保护 watchNext（各槽位下次可轮询的时刻）、
	// watchLow（核心额度低于 rateLimitFloor 的槽位）、watchPause（额度耗尽后各槽位恢复的时刻）、repoNodes（仓库 id → 仓库的
	// id / node_id / full_name，合成载荷用）与 watchLooked（仓库级 watch 已查过的 (仓库, 编号, updated_at)，游标停在同一秒时
	// 不重复查询；只是省额度，重启后丢失无妨）。
	watchMu     sync.Mutex
	watchNext   map[string]time.Time
	watchLow    map[string]bool
	watchPause  map[string]time.Time
	repoNodes   map[int64]repoPayload
	watchLooked map[watchLookKey]struct{}
}

// New 构造服务。
func New(cfg Config, st *store.Store, th Threads, n Notifier, att Attachments) *Service {
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = hookPollInterval
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = flushInterval
	}
	if cfg.Debounce <= 0 {
		cfg.Debounce = debounce
	}
	if cfg.AfterFunc == nil {
		cfg.AfterFunc = func(d time.Duration, f func()) func() {
			t := time.AfterFunc(d, f)
			return func() { t.Stop() }
		}
	}
	s := &Service{
		cfg: cfg, st: st, th: th, n: n, att: att, intn: rand.IntN,
		jobs:     make(chan func(ctx context.Context), jobQueueSize),
		inflight: map[string]bool{},
	}
	s.handlers = s.bindHandlers()
	return s
}

// SetSampler 注入提交采样（stats 在 GitHub 服务之后构造）。
func (s *Service) SetSampler(sm Sampler) { s.sampler = sm }

// SetModelCatalog 注入模型目录（目录在 GitHub 服务之后构造；未配置 omp 时不注入）。
func (s *Service) SetModelCatalog(mc ModelCatalog) { s.models = mc }

// SetOrbs 注入 orb 能力（任务进入终态时冻结 orb）。
func (s *Service) SetOrbs(o Orbs) { s.orbs = o }

func (s *Service) now() time.Time { return s.cfg.Now() }
func (s *Service) nowMs() int64   { return s.cfg.Now().UnixMilli() }

func (s *Service) changed() {
	if s.cfg.Changed != nil {
		s.cfg.Changed()
	}
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// isUnique 判断 SQLite 唯一约束冲突。
func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
