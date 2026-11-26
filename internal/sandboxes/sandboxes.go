// Package sandboxes 管理 agent 沙箱（M30）：线程在自己的 orb 里临时开出的零凭据平级环境。
//
// 沙箱是一台 orbs.sandbox_network 非空的特殊 orb（精简模式 orbd，link 经 provider 中继），sandboxes 行记归属、
// 回收期限与当前网络模式。权限主体是 link 认证的 owner orb：别的 orb 看不到也碰不到它的沙箱；owner_thread_id 只做
// 归因与级联（线程归档 / 删除即销毁）。owner orb 内的调用经 `sandbox` 流到达 OnStream，一条流一次调用。
package sandboxes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 错误码（C5）。
const (
	CodeDisabled           = "sandbox_disabled"
	CodeNoProvider         = "no_sandbox_provider"
	CodeInvalid            = "invalid_request"
	CodeInvalidType        = "invalid_type"
	CodeForbidden          = "forbidden"
	CodeQuotaPerOrb        = "quota_per_orb"
	CodeQuotaTotal         = "quota_total"
	CodeNotFound           = "sandbox_not_found"
	CodeNotReady           = "sandbox_not_ready"
	CodeNetworkUnavailable = "network_unavailable"
	CodeCreateFailed       = "create_failed"
	CodeCreateTimeout      = "create_timeout"
	CodeCapacity           = "capacity"
	CodeOrbdOutdated       = "orbd_outdated"
	CodeTooLarge           = "transfer_too_large"
	CodeLinkLost           = "link_lost"
	CodeInternal           = "internal"
)

// 销毁原因（sandboxes.destroy_reason）。
const (
	ReasonAgent          = "agent"
	ReasonOwner          = "owner"
	ReasonIdle           = "idle"
	ReasonTTL            = "ttl"
	ReasonMaxLifetime    = "max_lifetime"
	ReasonThreadArchived = "thread_archived"
	ReasonThreadDeleted  = "thread_deleted"
	ReasonOwnerOrbGone   = "owner_orb_gone"
	ReasonLost           = "lost"
	ReasonCreateFailed   = "create_failed"
	ReasonCreateTimeout  = "create_timeout"
)

// 行状态。
const (
	stateCreating   = "creating"
	stateReady      = "ready"
	stateDestroying = "destroying"
	stateDestroyed  = "destroyed"
	stateFailed     = "failed"
)

// 后端。
const (
	backendDocker = "docker"
	backendKVM    = "kvm"
)

const (
	// recentWindow 是 list 里保留已结束沙箱的时长。
	recentWindow = 60 * time.Minute
	// trimAfter 是已结束的行保留的时长。
	trimAfter = 7 * 24 * time.Hour
	// trimEvery 是裁剪周期。
	trimEvery = time.Hour
	// orphanGrace 是孤儿沙箱 orb 的宽限：创建流程在 orbs.Create 与写行之间的窗口。
	orphanGrace = 60 * time.Second
	// createPoll 是等待沙箱就绪的轮询间隔。
	createPoll = 500 * time.Millisecond
	// eventBuffer 是 orb 事件队列的容量；满了就丢（对账兜底）。
	eventBuffer = 256
)

// Error 是带错误码的业务错误。
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errf(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// Config 是沙箱服务参数。
type Config struct {
	Sandbox config.Sandbox
	// Changed 在某线程名下的沙箱变化之后调用（→ Hub.SandboxesChanged）；nil = 不通知。
	Changed func(threadID string)
	// Now 是时钟；默认 time.Now。
	Now func() time.Time
	// Tick 是对账周期；默认 30s。
	Tick time.Duration
	// CreateTimeout 是创建时等待沙箱就绪的上限；默认 120s。
	CreateTimeout time.Duration
	// LostAfter 是 provider 在线而沙箱 link 连续不在多久之后按 lost 销毁；默认 2m。
	LostAfter time.Duration
}

// Orbs 是沙箱依赖的 orb 生命周期（*orbs.Service 满足）。
type Orbs interface {
	Create(ctx context.Context, p orbs.CreateParams) (db.Orb, error)
	Get(ctx context.Context, orbID string) (db.Orb, error)
	Delete(ctx context.Context, orbID string) error
	Connected(orbID string) bool
	HasFeature(orbID, feature string) bool
	Do(ctx context.Context, orbID string, req *http.Request) (*http.Response, error)
	Subscribe(fn func(orbs.Event))
	SetSandboxNetwork(ctx context.Context, orbID, network string) error
}

// Service 是沙箱服务。
type Service struct {
	cfg  Config
	st   *store.Store
	orbs Orbs

	// createMu 串行化配额核对与建 orb / 写行，免得并发创建越过配额。
	createMu sync.Mutex
	// events 是 orb 事件队列（Subscribe 回调只投递，由 Run 处理）。
	events chan orbs.Event

	mu sync.Mutex
	// busy 是各沙箱正在进行的 exec / cp / 改网络数；非零时不按空闲回收。
	busy map[string]int
	// lostSince 是各沙箱最早被观察到「provider 在线而 link 不在」的时刻（只在内存里，进程启动从零开始）。
	lostSince map[string]time.Time
	lastTrim  time.Time
}

// New 建服务并订阅 orb 事件；调用方随后在后台跑 Run。
func New(cfg Config, st *store.Store, o Orbs) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Tick <= 0 {
		cfg.Tick = 30 * time.Second
	}
	if cfg.CreateTimeout <= 0 {
		cfg.CreateTimeout = 120 * time.Second
	}
	if cfg.LostAfter <= 0 {
		cfg.LostAfter = 2 * time.Minute
	}
	s := &Service{
		cfg: cfg, st: st, orbs: o,
		events:    make(chan orbs.Event, eventBuffer),
		busy:      map[string]int{},
		lostSince: map[string]time.Time{},
	}
	o.Subscribe(func(ev orbs.Event) {
		if ev.Kind != orbs.EventState {
			return
		}
		select {
		case s.events <- ev:
		default:
			slog.Warn("sandboxes: orb 事件队列已满，丢弃（对账兜底）", "orb", ev.OrbID, "to", ev.To)
		}
	})
	return s
}

func (s *Service) now() time.Time { return s.cfg.Now() }

func (s *Service) changed(threadID string) {
	if s.cfg.Changed != nil {
		s.cfg.Changed(threadID)
	}
}

// view 把行转成客户端视图。
func view(sb db.Sandbox) apigen.Sandbox {
	return apigen.Sandbox{
		ID: sb.ID, Backend: apigen.SandboxBackend(sb.Backend), Type: sb.Type, Provider: sb.ProviderName,
		Network: apigen.SandboxNetwork(sb.Network), CreatedNetwork: apigen.SandboxNetwork(sb.CreatedNetwork),
		State: apigen.SandboxState(sb.State), OwnerThreadID: sb.OwnerThreadID, IdleSec: int(sb.IdleSec),
		LastActiveAt: sb.LastActiveAt, IdleDeadline: sb.LastActiveAt + sb.IdleSec*1000, TTLDeadline: sb.TtlDeadline,
		HardDeadline: sb.HardDeadline, CreatedAt: sb.CreatedAt, ReadyAt: sb.ReadyAt, DestroyedAt: sb.DestroyedAt,
		DestroyReason: sb.DestroyReason, Error: sb.Error,
	}
}

func views(rows []db.Sandbox) []apigen.Sandbox {
	out := make([]apigen.Sandbox, 0, len(rows))
	for _, r := range rows {
		out = append(out, view(r))
	}
	return out
}

// List 返回线程名下的沙箱（在用的加最近 60 分钟内结束的）；threadID 为空时返回全部在用的。
func (s *Service) List(ctx context.Context, threadID string) ([]apigen.Sandbox, error) {
	if threadID == "" {
		rows, err := s.st.Q.ListLiveSandboxes(ctx)
		return views(rows), err
	}
	since := s.now().Add(-recentWindow).UnixMilli()
	rows, err := s.st.Q.ListSandboxesByThread(ctx, db.ListSandboxesByThreadParams{OwnerThreadID: threadID, Since: &since})
	return views(rows), err
}

// Destroy 由主人发起销毁（reason owner）；已结束的沙箱直接成功。
func (s *Service) Destroy(ctx context.Context, id string) error {
	sb, err := s.st.Q.GetSandbox(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return errf(CodeNotFound, "sandbox %s not found", id)
	}
	if err != nil {
		return err
	}
	return s.destroy(ctx, sb, ReasonOwner)
}

// destroy 把在用的沙箱置为 destroying 并删除它的 orb；行在 orb 到达 deleted 时由事件或对账置为 destroyed。
// 已在销毁或已结束时什么也不做。orb 删除失败只记日志，对账会重试。
func (s *Service) destroy(ctx context.Context, sb db.Sandbox, reason string) error {
	n, err := s.st.Q.MarkSandboxDestroying(ctx, db.MarkSandboxDestroyingParams{Reason: &reason, ID: sb.ID})
	if err != nil || n == 0 {
		return err
	}
	slog.Info("sandboxes: 销毁沙箱", "sandbox", sb.ID, "orb", sb.OrbID, "reason", reason)
	s.mu.Lock()
	delete(s.lostSince, sb.ID)
	s.mu.Unlock()
	if err := s.orbs.Delete(ctx, sb.OrbID); err != nil && !errors.Is(err, orbs.ErrNotFound) {
		slog.Warn("sandboxes: 删除沙箱 orb 失败，对账重试", "sandbox", sb.ID, "orb", sb.OrbID, "err", err)
	}
	s.changed(sb.OwnerThreadID)
	return nil
}

// touch 记一次活动（只对 ready 的行生效），并通知客户端刷新空闲倒计时。
func (s *Service) touch(ctx context.Context, sb db.Sandbox) {
	if err := s.st.Q.TouchSandbox(ctx, db.TouchSandboxParams{Now: s.now().UnixMilli(), ID: sb.ID}); err != nil {
		slog.Warn("sandboxes: 记录活动失败", "sandbox", sb.ID, "err", err)
		return
	}
	s.changed(sb.OwnerThreadID)
}

// hold 把沙箱记为忙（exec / cp / 改网络进行中），返回释放函数；进出各记一次活动。
func (s *Service) hold(ctx context.Context, sb db.Sandbox) (release func()) {
	s.mu.Lock()
	s.busy[sb.ID]++
	s.mu.Unlock()
	s.touch(ctx, sb)
	return func() {
		s.mu.Lock()
		if s.busy[sb.ID]--; s.busy[sb.ID] <= 0 {
			delete(s.busy, sb.ID)
		}
		s.mu.Unlock()
		s.touch(context.WithoutCancel(ctx), sb)
	}
}

func (s *Service) isBusy(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy[id] > 0
}

// caller 校验调用方线程：存在、是工作线程、未归档、属于发起 link 的 orb，且该 orb 不是 runner 或沙箱。
// 自动化线程可以用（与 box 不同：沙箱零凭据，不扩大权限）。
func (s *Service) caller(ctx context.Context, linkOrbID, threadID string) (db.Orb, error) {
	t, err := s.st.Q.GetThread(ctx, threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Orb{}, errf(CodeForbidden, "calling thread not found")
	}
	if err != nil {
		return db.Orb{}, err
	}
	if t.Kind != "work" || t.ArchivedAt != nil || t.OrbID == nil || *t.OrbID != linkOrbID {
		return db.Orb{}, errf(CodeForbidden, "calling thread does not belong to this orb")
	}
	o, err := s.st.Q.GetOrb(ctx, linkOrbID)
	if err != nil {
		return db.Orb{}, err
	}
	if orbs.IsRunner(o) || orbs.IsSandbox(o) {
		return db.Orb{}, errf(CodeForbidden, "sandboxes are not available on this machine")
	}
	return o, nil
}

// own 定位 owner orb 自己的沙箱；别的 orb 的沙箱一律报不存在（不暴露是否存在）。needReady 时还要求 ready 且 link 在线。
func (s *Service) own(ctx context.Context, linkOrbID, id string, needReady bool) (db.Sandbox, error) {
	sb, err := s.st.Q.GetSandbox(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && sb.OwnerOrbID != linkOrbID) {
		return db.Sandbox{}, errf(CodeNotFound, "sandbox %s not found", id)
	}
	if err != nil {
		return db.Sandbox{}, err
	}
	if needReady && (sb.State != stateReady || !s.orbs.Connected(sb.OrbID)) {
		return db.Sandbox{}, errf(CodeNotReady, "sandbox %s is %s", id, sb.State)
	}
	return sb, nil
}

// backendOf 把 provider 驱动映射到沙箱后端；process 是测试驱动，算 docker。
func backendOf(driver string) string {
	switch driver {
	case "docker", "process":
		return backendDocker
	case "firecracker":
		return backendKVM
	}
	return ""
}

// candidates 返回某后端可用的 provider：未停用、租约有效、hello 声明了 sandbox 能力。
func (s *Service) candidates(ctx context.Context, backend string) ([]db.Provider, error) {
	all, err := s.st.Q.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now().UnixMilli()
	var out []db.Provider
	for _, p := range all {
		if backendOf(p.Driver) != backend || !providerOnline(p, now) {
			continue
		}
		var caps apigen.ProviderCapabilities
		if json.Unmarshal([]byte(p.Capabilities), &caps) != nil || caps.Sandbox == nil || !*caps.Sandbox {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func providerOnline(p db.Provider, now int64) bool {
	return p.DisabledAt == nil && p.InstanceID != nil && p.LeaseUntil != nil && *p.LeaseUntil > now
}

// pick 选 provider 与类型：backend 给了就只看它，没给先看 default_backend 再看另一个；候选里有 owner orb 所在的
// provider 就用它，否则取名字最小的。typ 为空取该 provider 的默认类型。
func (s *Service) pick(ctx context.Context, owner db.Orb, backend, typ string) (db.Provider, string, string, error) {
	order := []string{backend}
	if backend == "" {
		def := s.cfg.Sandbox.DefaultBackend
		other := backendDocker
		if def == backendDocker {
			other = backendKVM
		}
		order = []string{def, other}
	}
	for _, b := range order {
		cands, err := s.candidates(ctx, b)
		if err != nil {
			return db.Provider{}, "", "", err
		}
		if len(cands) == 0 {
			continue
		}
		slices.SortFunc(cands, func(a, c db.Provider) int { return strings.Compare(a.Name, c.Name) })
		p := cands[0]
		if i := slices.IndexFunc(cands, func(c db.Provider) bool { return c.ID == owner.ProviderID }); i >= 0 {
			p = cands[i]
		}
		var types []apigen.OrbType
		_ = json.Unmarshal([]byte(p.OrbTypes), &types)
		if typ == "" {
			typ = p.DefaultType
		}
		names := make([]string, 0, len(types))
		for _, t := range types {
			names = append(names, t.Name)
		}
		if !slices.Contains(names, typ) {
			return db.Provider{}, "", "", errf(CodeInvalidType, "type %q is not offered by provider %s; available: %s", typ, p.Name, strings.Join(names, ", "))
		}
		return p, typ, b, nil
	}
	if backend != "" {
		return db.Provider{}, "", "", errf(CodeNoProvider, "no online provider offers %s sandboxes", backend)
	}
	return db.Provider{}, "", "", errf(CodeNoProvider, "no online provider offers sandboxes")
}

// status 报告沙箱是否可用与可用的后端。
func (s *Service) status(ctx context.Context) (map[string]any, error) {
	backends := []string{}
	for _, b := range []string{backendKVM, backendDocker} {
		c, err := s.candidates(ctx, b)
		if err != nil {
			return nil, err
		}
		if len(c) > 0 {
			backends = append(backends, b)
		}
	}
	out := map[string]any{"available": s.cfg.Sandbox.Enabled && len(backends) > 0, "backends": backends}
	switch {
	case !s.cfg.Sandbox.Enabled:
		out["reason"] = "sandboxes are disabled on this Oryn server"
	case len(backends) == 0:
		out["reason"] = "no online provider offers sandboxes"
	}
	return out, nil
}

// orbError 把 orbd 的非 2xx 应答转成错误（orbd 的 {code, message} 原样透传）。
func orbError(resp *http.Response, fallback string) error {
	var e struct{ Code, Message string }
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(b, &e) == nil && e.Code != "" {
		return &Error{Code: e.Code, Message: e.Message}
	}
	return errf(fallback, "orbd returned %d", resp.StatusCode)
}

// streamErr 把错误转成流上的 error 行；非 *Error 记日志并回 internal。
func streamErr(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	var ce *link.CtlError
	if errors.As(err, &ce) {
		return &Error{Code: ce.Code, Message: ce.Message}
	}
	slog.Warn("sandboxes: 调用失败", "err", err)
	return &Error{Code: CodeInternal, Message: err.Error()}
}
