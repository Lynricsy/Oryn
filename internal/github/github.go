// Package github 是 Oryn 与 GitHub 的深度集成（M14）：GitHub App 的创建与安装、两个 GitHub 身份（我的账号 /
// Agent 账号）、仓库绑定、orb 内凭据、事件回流与触发规则。REST / GraphQL 用 net/http 手写，不引入第三方 SDK。
package github

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// 时间与上限常量（规格见 docs/plan-m14.md「全局约束」）。
const (
	pollInterval        = 30 * time.Second
	flushInterval       = 5 * time.Second
	debounce            = 15 * time.Second
	rollupDebounce      = 5 * time.Second
	permTTL             = 5 * time.Minute
	repoListTTL         = 60 * time.Second
	stateTTL            = 10 * time.Minute
	installTokenTTL     = 50 * time.Minute
	scopedTokenTTL      = 50 * time.Minute
	refreshSkew         = 30 * time.Minute
	maxPerLinkPerHour   = 20
	maxCIRounds         = 5
	triggerMaxPerHour   = 10
	maxDeliveryAttempts = 3
	deliveryRetention   = 7 * 24 * time.Hour
	outboxMaxAge        = 24 * time.Hour
	webhookMaxBody      = 25 << 20
)

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
}

// Notifier 发一条线程通知（*notify.Service 实现）。
type Notifier interface {
	Post(ctx context.Context, threadID, title, body string) error
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
	PollInterval   time.Duration    // 默认 pollInterval
	FlushInterval  time.Duration    // 默认 flushInterval
	Debounce       time.Duration    // 默认 debounce
	// AfterFunc 默认包一层 time.AfterFunc；CI 汇总的防抖计时器用，测试注入。
	AfterFunc func(d time.Duration, f func()) (stop func())
	// Changed 在状态变化后广播；nil = 不广播。
	Changed func()
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

type cachedToken struct {
	token string
	exp   time.Time
}

// Service 是 GitHub 集成的控制面服务。
type Service struct {
	cfg     Config
	st      *store.Store
	th      Threads
	n       Notifier
	att     Attachments
	sampler Sampler

	slotMu map[string]*sync.Mutex // 按槽位串行化刷新：GitHub 的刷新令牌用一次即废

	mu         sync.Mutex
	instTokens map[int64]cachedToken      // 安装 id → 安装令牌
	scoped     map[scopedKey]scopedToken  // (槽位, 仓库 id) → 按仓库收窄的令牌（M14 步骤 3.2，惰性初始化）
	repoLists  map[[32]byte]repoListEntry // 用户令牌的 sha256 → 仓库列表（步骤 2.2，惰性初始化）
	perms      map[permKey]permEntry      // (仓库 id, 登录名) → 是否有写权限（步骤 4.3，惰性初始化）

	// PR 跟踪（步骤 5.1，受 mu 保护，惰性初始化）：线程 → 上次按 HEAD 反查过 PR 的 sha。
	sampledHeads map[string]string

	// 事件分发（步骤 4.1）：处理器注册表、任务队列、队列中 / 处理中的投递 guid。
	handlers map[string]handler
	jobs     chan func(ctx context.Context)
	qmu      sync.Mutex
	inflight map[string]bool
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
		cfg.PollInterval = pollInterval
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
		cfg: cfg, st: st, th: th, n: n, att: att,
		slotMu:     map[string]*sync.Mutex{slotUser: {}, slotAgent: {}},
		instTokens: map[int64]cachedToken{},
		jobs:       make(chan func(ctx context.Context), jobQueueSize),
		inflight:   map[string]bool{},
	}
	s.handlers = s.bindHandlers()
	return s
}

// SetSampler 注入提交采样（stats 在 GitHub 服务之后构造）。
func (s *Service) SetSampler(sm Sampler) { s.sampler = sm }

func (s *Service) now() time.Time { return s.cfg.Now() }
func (s *Service) nowMs() int64   { return s.cfg.Now().UnixMilli() }

func (s *Service) changed() {
	if s.cfg.Changed != nil {
		s.cfg.Changed()
	}
}

// clearTokenCaches 丢弃内存里的全部令牌缓存（App 删除、安装变化后）。
func (s *Service) clearTokenCaches() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.instTokens)
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// isUnique 判断 SQLite 唯一约束冲突。
func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
