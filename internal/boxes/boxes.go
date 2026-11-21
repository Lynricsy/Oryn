// Package boxes 是 M23 的 box：由 provider 拉起、带 box 标记的常驻 orb，持有名字、期望状态、运行时段与 app。
// orb 本身的状态机仍归 internal/orbs；本包只把 orb 推向 boxes.desired（对账），并在 orb 之上管理 app 与部署。
package boxes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/portalhost"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 限额（计划「名字与限额」）。
const (
	// MaxBoxes 是全库 box 的上限。
	MaxBoxes = 16
	// MaxAppsPerBox 是每台 box 的 app 上限。
	MaxAppsPerBox = 16
)

// 错误码（HTTP 状态见 internal/api 的 writeBoxErr）。
const (
	CodeInvalid            = "invalid_request"
	CodeForbidden          = "forbidden"
	CodeBoxNotFound        = "box_not_found"
	CodeAppNotFound        = "app_not_found"
	CodeReleaseNotFound    = "release_not_found"
	CodeDeployNotFound     = "deploy_not_found"
	CodeBoxExists          = "box_exists"
	CodeBoxLimit           = "box_limit"
	CodeBoxStopped         = "box_stopped"
	CodeBoxNotReady        = "box_not_ready"
	CodeBoxUnsupported     = "box_unsupported"
	CodeOrbIsBox           = "orb_is_box"
	CodeAppExistsElsewhere = "app_exists_elsewhere"
	CodeAppRepoMismatch    = "app_repo_mismatch"
	CodeAppNotDeployed     = "app_not_deployed"
	CodeAppLimit           = "app_limit"
	CodeDeployInProgress   = "deploy_in_progress"
	CodeBoxChanges         = "box_changes"
	CodeReleaseTooLarge    = "release_too_large"
)

// 期望状态与其来源（boxes.desired / desired_by）。
const (
	desiredRunning = "running"
	desiredStopped = "stopped"
	byOwner        = "owner"
	bySchedule     = "schedule"
)

// 活动的发起者（Actor.Kind）。
const (
	ActorOwner     = "owner"
	ActorAgent     = "agent"
	ActorAssistant = "assistant"
	ActorSchedule  = "schedule"
	ActorSystem    = "system"
)

// Actor 是一次操作的发起者：Kind ∈ owner|agent|assistant|schedule|system；ThreadID 是 agent / 助手所在的线程。
type Actor struct{ Kind, ThreadID string }

// Error 是带错误码的业务错误（Message 面向用户）。
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Message }

func errf(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

var boxNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,28}[a-z0-9])?$`)

// ValidName 报告 name 是否是合规的 box 名：小写字母、数字与连字符，至多 30 个字符，首尾不是连字符，不含 `--`。
func ValidName(name string) bool {
	return boxNameRe.MatchString(name) && !strings.Contains(name, "--")
}

// Orbs 是 box 依赖的 orb 生命周期（*orbs.Service 满足）。
type Orbs interface {
	Create(ctx context.Context, p orbs.CreateParams) (db.Orb, error)
	Get(ctx context.Context, orbID string) (db.Orb, error)
	Delete(ctx context.Context, orbID string) error
	Wake(ctx context.Context, orbID string) error
	Pause(ctx context.Context, orbID, mode string) error
	Revive(ctx context.Context, orbID string) error
	Connected(orbID string) bool
	HasFeature(orbID, feature string) bool
	Do(ctx context.Context, orbID string, req *http.Request) (*http.Response, error)
	DialPortal(ctx context.Context, orbID string, port int) (net.Conn, error)
	Subscribe(fn func(orbs.Event))
	SetBoxGate(fn func(orbID string) bool)
}

// Threads 是 box 依赖的线程信息（internal/server 的 boxThreads 实现）。
type Threads interface {
	// Busy 报告 orb 上是否有正在工作的线程（时段停机据此推迟）。
	Busy(ctx context.Context, orbID string) (bool, error)
}

// Config 是 box 服务参数。
type Config struct {
	// Base 是 portal 地址基（app 地址 `<app>.<portal_domain>`）。
	Base portalhost.Base
	// Box 加解密 app 密钥。
	Box *secrets.Box
	// Changed 在 box 相关的任何变化之后调用（防抖 250ms；→ Hub.BoxesChanged）；nil = 不通知。
	Changed func()
	// DropApp 断开该 app 的匿名连接；nil = 没有 portal 数据面。
	DropApp func(app string)
	// Now 是时钟；默认 time.Now。
	Now func() time.Time
	// Tick 是对账周期；默认 30s。
	Tick time.Duration
	// UnlinkedAfter 是 ready 却没有 link 多久之后 Revive；默认 90s。
	UnlinkedAfter time.Duration
}

// changedDelay 是 Changed 的合并时长。
const changedDelay = 250 * time.Millisecond

// Service 是 box 服务。
type Service struct {
	cfg  Config
	st   *store.Store
	orbs Orbs
	th   Threads

	// kickCh 是容量 1 的立即对账信号。
	kickCh chan struct{}
	// recMu 串行化对账（Run 的循环与测试直接调用）。
	recMu sync.Mutex
	// hookBeforeScheduleTx（仅测试）在时段判定之后、写入事务之前调用。
	hookBeforeScheduleTx func()

	mu sync.Mutex
	// unlinkedSince 是各 box 最早被观察到「ready 却没有 link」的时刻。
	unlinkedSince map[string]time.Time
	// lastWake 是各 box 上一次因 failed 而重试唤醒的时刻。
	lastWake map[string]time.Time

	chMu    sync.Mutex
	chTimer *time.Timer

	// root 是服务自己的根 ctx（Run 结束时取消）：部署的传输与激活从它派生，不随调用方的 ctx 结束。
	root context.Context
	stop context.CancelFunc
	// flMu 保护 inflight：本进程里正在提交（传输或激活）的部署，按部署 id。
	flMu     sync.Mutex
	inflight map[string]*flight
	// alMu 保护 appLocks：按 app 名的进程内互斥（带引用计数，用完回收）。
	alMu     sync.Mutex
	appLocks map[string]*appLock
}

// appLock 是一个 app 名的互斥与等待它的人数。
type appLock struct {
	mu   sync.Mutex
	refs int
}

// lockApp 取 app 名的互斥：PrepareDeploy 从查未结束的部署到插入部署行、Undeploy / Rollback / Restart / SetPublic 从
// 查未结束的部署到 orbd 调用与写库，都整段持有它，免得两者交错留下没有 app 行的部署。持有期间不得开着 SQLite 事务
// 跨网络调用。返回解锁函数。
func (s *Service) lockApp(name string) (unlock func()) {
	s.alMu.Lock()
	if s.appLocks == nil {
		s.appLocks = map[string]*appLock{}
	}
	l := s.appLocks[name]
	if l == nil {
		l = &appLock{}
		s.appLocks[name] = l
	}
	l.refs++
	s.alMu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		s.alMu.Lock()
		if l.refs--; l.refs == 0 {
			delete(s.appLocks, name)
		}
		s.alMu.Unlock()
	}
}

// New 构造服务，并立即向 orbs 注册 box 闸门与事件订阅（不等 Run）。
func New(cfg Config, st *store.Store, o Orbs, th Threads) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Tick <= 0 {
		cfg.Tick = 30 * time.Second
	}
	if cfg.UnlinkedAfter <= 0 {
		cfg.UnlinkedAfter = 90 * time.Second
	}
	s := &Service{
		cfg: cfg, st: st, orbs: o, th: th,
		kickCh:        make(chan struct{}, 1),
		unlinkedSince: map[string]time.Time{}, lastWake: map[string]time.Time{},
		inflight: map[string]*flight{},
	}
	s.root, s.stop = context.WithCancel(context.Background())
	o.SetBoxGate(s.gate)
	o.Subscribe(func(ev orbs.Event) {
		if ev.Orb.Box == 0 {
			return
		}
		s.kick()
		s.changed()
	})
	return s
}

func (s *Service) now() time.Time { return s.cfg.Now() }

// gate 是 box 闸门：该 orb 的 box 期望运行时放行（查不到 box 也放行）。
func (s *Service) gate(orbID string) bool {
	b, err := s.st.Q.GetBoxByOrb(context.Background(), orbID)
	if err != nil {
		return true
	}
	return b.Desired == desiredRunning
}

// kick 请求一次立即对账（非阻塞）。
func (s *Service) kick() {
	select {
	case s.kickCh <- struct{}{}:
	default:
	}
}

// Touch 请求一次（合并后的）Changed 通知；线程增删时由 threads 调用。
func (s *Service) Touch() { s.changed() }

// changed 在 changedDelay 之内合并多次变化，只调一次 Changed。
func (s *Service) changed() {
	if s.cfg.Changed == nil {
		return
	}
	s.chMu.Lock()
	defer s.chMu.Unlock()
	if s.chTimer != nil {
		return
	}
	s.chTimer = time.AfterFunc(changedDelay, func() {
		s.chMu.Lock()
		s.chTimer = nil
		s.chMu.Unlock()
		s.cfg.Changed()
	})
}

// lookup 按 id（`box_…`）或名字取 box 行（不看 orb 状态）；不存在 → box_not_found。
func (s *Service) lookup(ctx context.Context, q *db.Queries, ref string) (db.Box, error) {
	var b db.Box
	var err error
	if strings.HasPrefix(ref, "box_") {
		b, err = q.GetBox(ctx, ref)
	} else {
		b, err = q.GetBoxByName(ctx, ref)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return db.Box{}, errf(CodeBoxNotFound, "box %s not found", ref)
	}
	return b, err
}

// live 取 box 行与它的 orb；orb 已删除（或正在删除）的 box 视为不存在。
func (s *Service) live(ctx context.Context, ref string) (db.Box, db.Orb, error) {
	b, err := s.lookup(ctx, s.st.Q, ref)
	if err != nil {
		return db.Box{}, db.Orb{}, err
	}
	o, err := s.orbs.Get(ctx, b.OrbID)
	if errors.Is(err, orbs.ErrNotFound) || (err == nil && gone(o)) {
		return db.Box{}, db.Orb{}, errf(CodeBoxNotFound, "box %s not found", ref)
	}
	if err != nil {
		return db.Box{}, db.Orb{}, err
	}
	return b, o, nil
}

func gone(o db.Orb) bool { return o.State == orbs.StateDeleting || o.State == orbs.StateDeleted }

// activity 是一条活动的明细（box_activity.detail）。
type activity struct {
	Commit    string `json:"commit,omitempty"`
	ReleaseID string `json:"releaseId,omitempty"`
	ThreadID  string `json:"threadId,omitempty"`
	Error     string `json:"error,omitempty"`
	Public    *bool  `json:"public,omitempty"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
}

// record 在事务里记一条活动。by.ThreadID 非空且明细没有线程时补上。
func (s *Service) record(ctx context.Context, q *db.Queries, boxID, kind string, by Actor, app string, d activity) error {
	if d.ThreadID == "" {
		d.ThreadID = by.ThreadID
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	p := db.InsertBoxActivityParams{BoxID: boxID, Ts: s.now().UnixMilli(), Kind: kind, Actor: by.Kind, Detail: string(raw)}
	if app != "" {
		p.App = &app
	}
	return q.InsertBoxActivity(ctx, p)
}

// List 返回全部 box（按创建时间；orb 已删除的不列出）。
func (s *Service) List(ctx context.Context) ([]apigen.Box, error) {
	rows, err := s.st.Q.ListBoxes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.Box, 0, len(rows))
	for _, b := range rows {
		o, err := s.orbs.Get(ctx, b.OrbID)
		if errors.Is(err, orbs.ErrNotFound) || (err == nil && gone(o)) {
			continue
		}
		if err != nil {
			return nil, err
		}
		v, err := s.view(ctx, b, o)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Get 返回一台 box（ref = id 或名字）。
func (s *Service) Get(ctx context.Context, ref string) (apigen.Box, error) {
	b, o, err := s.live(ctx, ref)
	if err != nil {
		return apigen.Box{}, err
	}
	return s.view(ctx, b, o)
}

// Create 新建一台 box：先建它的常驻 orb，再写 box 行。
func (s *Service) Create(ctx context.Context, name, provider, typ string, by Actor) (apigen.Box, error) {
	if !ValidName(name) {
		return apigen.Box{}, errf(CodeInvalid, "invalid box name %q: use lowercase letters, digits and hyphens (up to 30 characters)", name)
	}
	if err := s.checkCreate(ctx, s.st.Q, name); err != nil {
		return apigen.Box{}, err
	}
	o, err := s.orbs.Create(ctx, orbs.CreateParams{Provider: provider, Type: typ, Box: true})
	if err != nil {
		return apigen.Box{}, errf(CodeInvalid, "%s", err.Error())
	}
	now := s.now().UnixMilli()
	row := db.Box{ID: ids.New("box"), Name: name, OrbID: o.ID, Desired: desiredRunning, DesiredBy: byOwner, DesiredAt: now, CreatedAt: now}
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if err := s.checkCreate(ctx, q, name); err != nil {
			return err
		}
		if err := q.CreateBox(ctx, db.CreateBoxParams{
			ID: row.ID, Name: row.Name, OrbID: row.OrbID, Desired: row.Desired, DesiredBy: row.DesiredBy,
			DesiredAt: row.DesiredAt, CreatedAt: row.CreatedAt,
		}); err != nil {
			return err
		}
		return s.record(ctx, q, row.ID, string(apigen.BoxActivityCreated), by, "", activity{})
	})
	if err != nil {
		// 收回刚建的 orb；这一步也失败时由对账的孤儿清理兜底
		if derr := s.orbs.Delete(context.WithoutCancel(ctx), o.ID); derr != nil {
			slog.Warn("boxes: 回收孤儿 orb 失败", "orb", o.ID, "err", derr)
		}
		return apigen.Box{}, err
	}
	s.changed()
	s.kick()
	return s.view(ctx, row, o)
}

// checkCreate 检查数量上限与重名。
func (s *Service) checkCreate(ctx context.Context, q *db.Queries, name string) error {
	n, err := q.CountBoxes(ctx)
	if err != nil {
		return err
	}
	if n >= MaxBoxes {
		return errf(CodeBoxLimit, "at most %d boxes", MaxBoxes)
	}
	if _, err := q.GetBoxByName(ctx, name); err == nil {
		return errf(CodeBoxExists, "a box named %s already exists", name)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

// Rename 改名。app 地址不含 box 名，不受影响。
func (s *Service) Rename(ctx context.Context, ref, name string, by Actor) (apigen.Box, error) {
	if !ValidName(name) {
		return apigen.Box{}, errf(CodeInvalid, "invalid box name %q: use lowercase letters, digits and hyphens (up to 30 characters)", name)
	}
	b, _, err := s.live(ctx, ref)
	if err != nil {
		return apigen.Box{}, err
	}
	if b.Name != name {
		err = s.st.Tx(ctx, func(q *db.Queries) error {
			open, err := q.ListOpenBoxDeploysByBox(ctx, b.ID)
			if err != nil {
				return err
			}
			if len(open) > 0 {
				return errf(CodeDeployInProgress, "box %s has a deploy in progress", b.Name)
			}
			if _, err := q.GetBoxByName(ctx, name); err == nil {
				return errf(CodeBoxExists, "a box named %s already exists", name)
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if _, err := q.RenameBox(ctx, db.RenameBoxParams{Name: name, ID: b.ID}); err != nil {
				return err
			}
			return s.record(ctx, q, b.ID, string(apigen.BoxActivityRenamed), by, "", activity{From: b.Name, To: name})
		})
		if err != nil {
			return apigen.Box{}, err
		}
		s.changed()
	}
	return s.Get(ctx, b.ID)
}

var hhmmRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// SetSchedule 设置（sch 非 nil）或清除（nil）每日运行时段。时段在下一次对账时生效。
func (s *Service) SetSchedule(ctx context.Context, ref string, sch *apigen.BoxSchedule, by Actor) (apigen.Box, error) {
	p := db.SetBoxScheduleParams{}
	if sch != nil {
		if !hhmmRe.MatchString(sch.On) || !hhmmRe.MatchString(sch.Off) {
			return apigen.Box{}, errf(CodeInvalid, "on and off must be HH:MM")
		}
		if sch.On == sch.Off {
			return apigen.Box{}, errf(CodeInvalid, "on and off must differ")
		}
		if sch.Tz == "" || sch.Tz == "Local" {
			return apigen.Box{}, errf(CodeInvalid, "tz must be an IANA time zone name")
		}
		if _, err := time.LoadLocation(sch.Tz); err != nil {
			return apigen.Box{}, errf(CodeInvalid, "unknown time zone %q", sch.Tz)
		}
		p.SchedOn, p.SchedOff, p.SchedTz = &sch.On, &sch.Off, &sch.Tz
	}
	b, _, err := s.live(ctx, ref)
	if err != nil {
		return apigen.Box{}, err
	}
	p.ID = b.ID
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.SetBoxSchedule(ctx, p); err != nil {
			return err
		}
		return s.record(ctx, q, b.ID, string(apigen.BoxActivitySchedule), by, "", activity{})
	})
	if err != nil {
		return apigen.Box{}, err
	}
	s.changed()
	s.kick()
	return s.Get(ctx, b.ID)
}

// Start 让 box 期望运行（手动启停只保持到时段的下一个边界）。
func (s *Service) Start(ctx context.Context, ref string, by Actor) (apigen.Box, error) {
	return s.setDesired(ctx, ref, desiredRunning, by)
}

// Stop 让 box 期望停机。
func (s *Service) Stop(ctx context.Context, ref string, by Actor) (apigen.Box, error) {
	return s.setDesired(ctx, ref, desiredStopped, by)
}

func (s *Service) setDesired(ctx context.Context, ref, desired string, by Actor) (apigen.Box, error) {
	b, _, err := s.live(ctx, ref)
	if err != nil {
		return apigen.Box{}, err
	}
	kind := apigen.BoxActivityStarted
	if desired == desiredStopped {
		kind = apigen.BoxActivityStopped
	}
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.SetBoxDesired(ctx, db.SetBoxDesiredParams{Desired: desired, DesiredBy: byOwner, DesiredAt: s.now().UnixMilli(), ID: b.ID}); err != nil {
			return err
		}
		if b.Desired == desired {
			return nil
		}
		return s.record(ctx, q, b.ID, string(kind), by, "", activity{})
	})
	if err != nil {
		return apigen.Box{}, err
	}
	if desired == desiredRunning {
		// 主人手动启动：不等 failed 的重试间隔
		s.mu.Lock()
		delete(s.lastWake, b.ID)
		s.mu.Unlock()
	}
	s.changed()
	s.kick()
	return s.Get(ctx, b.ID)
}

// Delete 删除 box：先中止在途部署，再删 orb（可重试），最后删 box 行（级联其余表）。box 行是按名字重试删除的唯一把手，
// 而 box 的 orb 不受空闲与无主回收管，所以次序不能反。
func (s *Service) Delete(ctx context.Context, ref string, by Actor) error {
	b, err := s.lookup(ctx, s.st.Q, ref)
	if err != nil {
		return err
	}
	slog.Info("boxes: 删除 box", "box", b.ID, "name", b.Name, "by", by.Kind)
	s.abortDeploys(b.ID)
	if err := s.orbs.Delete(ctx, b.OrbID); err != nil && !errors.Is(err, orbs.ErrNotFound) {
		return err
	}
	if _, err := s.st.Q.DeleteBox(ctx, b.ID); err != nil {
		return err
	}
	s.forget(b.ID)
	s.changed()
	return nil
}

// forget 清掉一台 box 的内存状态。
func (s *Service) forget(boxID string) {
	s.mu.Lock()
	delete(s.unlinkedSince, boxID)
	delete(s.lastWake, boxID)
	s.mu.Unlock()
}
