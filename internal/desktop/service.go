// Package desktop 是控制面的浏览器 Profile 与桌面服务（M8 §3.2–3.3）：命名 Profile 的增删改查与加密归档
// （<data_dir>/browser-profiles/<id>/<revision>.oss），orb 进入 ready 之前的 Profile 同步、冻结 / 停机前的保存、
// 独占租约与围栏代数、接管与只读副本，以及桌面 WebSocket（noVNC ↔ orb link 上的 desk 流）。
package desktop

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 限额（protocol.md「M8 → 限额」）；变量仅为测试可调小，生产代码不修改。
var (
	// maxArchiveBytes 是 gzip 后归档的上限。
	maxArchiveBytes int64 = 256 << 20
	// maxProfiles 是 Profile 数量上限。
	maxProfiles int64 = 50
	// transferTimeout 是单次导出 / 装载的上限。
	transferTimeout = 120 * time.Second
	// hookTimeout 是一个生命周期钩子（含等锁）的整体上限。
	hookTimeout = 150 * time.Second
	// takeoverSaveTimeout 是接管前对原持有者活体保存的上限（含等它的 orb 锁）。
	takeoverSaveTimeout = 15 * time.Second
	// wakeWait 是桌面类接口唤醒 orb 后等待可用的上限。
	wakeWait = 25 * time.Second
	// browserTimeout 是浏览器启停请求的上限。
	browserTimeout = 45 * time.Second
	// maxViewers 是每台 orb 的桌面观看者上限。
	maxViewers = 8
	// wsWriteTimeout 是桌面 WS 每次写的截止时间。
	wsWriteTimeout = 30 * time.Second
	// wsReadLimit 是桌面 WS 客户端单条消息上限。
	wsReadLimit int64 = 1 << 20
)

// maxNameRunes 是 Profile 名字的字符数上限。
const maxNameRunes = 40

// Orbs 是本包依赖的 orb 服务（*orbs.Service 实现）。
type Orbs interface {
	Get(ctx context.Context, orbID string) (db.Orb, error)
	EnsureUsable(ctx context.Context, orbID, feature string, wake bool, wait time.Duration) error
	Do(ctx context.Context, orbID string, req *http.Request) (*http.Response, error)
	DialDesk(ctx context.Context, orbID string) (net.Conn, error)
	TouchUserCoalesced(ctx context.Context, orbID string)
	ApplyDesktopState(ctx context.Context, orbID string, st apigen.LinkCtlDesktopStateParams)
	RefreshDesktop(ctx context.Context, orbID string)
	SendProfileMode(ctx context.Context, orbID string, p apigen.LinkCtlProfileModeParams) error
	Subscribe(fn func(orbs.Event))
	SetDesktopHooks(h orbs.DesktopHooks)
}

// TicketRedeemer 兑换一次性 WS 票据（*auth.Tickets 实现）。
type TicketRedeemer interface {
	Redeem(ticket string) (auth.Principal, bool)
}

// PrincipalChecker 复核主体当前是否仍有效（*auth.Service 实现）。
type PrincipalChecker interface {
	PrincipalValid(ctx context.Context, p auth.Principal) (bool, error)
}

// Config 是服务配置。
type Config struct {
	DataDir string
	Box     *secrets.Box
	// Now 默认 time.Now。
	Now func() time.Time
	// Valid 非 nil 时，桌面 WS 登记连接之后再向库复核一次主体（与 /api/v1/ws 相同）。
	Valid PrincipalChecker
}

// Error 是带 HTTP 状态的业务错误；Conflict 非 nil 时（profile_in_use）API 以它作为响应体。
type Error struct {
	Code     string
	Status   int
	Message  string
	Conflict *apigen.ProfileInUseConflict
}

func (e *Error) Error() string { return "desktop: " + e.Code + ": " + e.Message }

// Coded 返回错误码与消息（threads 据此转成线程错误）。
func (e *Error) Coded() (code, message string) { return e.Code, e.Message }

// ErrorBody 返回要替代 {code, message} 的响应体；没有时为 nil。
func (e *Error) ErrorBody() any {
	if e.Conflict == nil {
		return nil
	}
	return e.Conflict
}

func errf(status int, code, msg string) *Error {
	return &Error{Code: code, Status: status, Message: msg}
}

var (
	errNotFound  = errf(http.StatusNotFound, "profile_not_found", "Profile 不存在")
	errNoProfile = errf(http.StatusConflict, "no_profile", "该 orb 没有独占持有 Profile")
	errReadonly  = errf(http.StatusConflict, "profile_readonly", "该 orb 以只读副本使用 Profile，不回写")
)

// Service 是 Profile 与桌面服务。
type Service struct {
	cfg     Config
	st      *store.Store
	o       Orbs
	tickets TicketRedeemer
	dir     string

	orbLocks  keyLocks
	profLocks keyLocks
	releases  queue

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	wmu     sync.Mutex
	viewers map[string]int
	conns   map[*deskConn]struct{}

	// 测试钩子（生产为 nil）：onReleased 在释放队列处理完一个 orb 之后调用；beforeSave 在导出体写完、
	// 提交新修订之前调用。
	onReleased func(orbID string)
	beforeSave func(orbID string)
}

// New 创建服务：建 <data_dir>/browser-profiles/（0700）并清掉残留的 .tmp-*，注册生命周期钩子与 orb 事件订阅。
func New(cfg Config, st *store.Store, o Orbs, tickets TicketRedeemer) (*Service, error) {
	if cfg.Box == nil {
		return nil, errors.New("desktop: Box 未配置")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	dir := filepath.Join(cfg.DataDir, "browser-profiles")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("desktop: 创建 %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	if tmps, err := filepath.Glob(filepath.Join(dir, "*", ".tmp-*")); err == nil {
		for _, p := range tmps {
			_ = os.Remove(p)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		cfg: cfg, st: st, o: o, tickets: tickets, dir: dir, ctx: ctx, cancel: cancel,
		orbLocks: keyLocks{m: map[string]*keyLock{}}, profLocks: keyLocks{m: map[string]*keyLock{}},
		viewers: map[string]int{}, conns: map[*deskConn]struct{}{},
	}
	s.releases.init()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.releaseLoop()
	}()
	o.SetDesktopHooks(s)
	o.Subscribe(s.onOrbEvent)
	return s, nil
}

// Close 停止释放队列并断开全部桌面 WS。
func (s *Service) Close() {
	s.cancel()
	s.wmu.Lock()
	conns := make([]*deskConn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.wmu.Unlock()
	for _, c := range conns {
		c.closeWith(websocket.StatusGoingAway, "server shutting down")
	}
	s.wg.Wait()
}

func (s *Service) nowMs() int64 { return s.cfg.Now().UnixMilli() }

// profileDir 是 Profile 的归档目录。
func (s *Service) profileDir(id string) string { return filepath.Join(s.dir, id) }

// archivePath 是 Profile 某个修订的归档文件。
func (s *Service) archivePath(id string, rev int64) string {
	return filepath.Join(s.profileDir(id), fmt.Sprintf("%d.oss", rev))
}

// archiveName 是归档加密时的关联数据（绑定 Profile 与修订，文件不能被挪作他用）。
func archiveName(id string, rev int64) string { return fmt.Sprintf("browser-profile/%s/%d", id, rev) }

// ---- orb 离开运行态时释放租约 ----

// onOrbEvent 是 orb 事件回调（同步、不得阻塞）：状态进入 frozen / stopped / failed / deleted 时排入释放队列。
func (s *Service) onOrbEvent(ev orbs.Event) {
	if ev.Kind != orbs.EventState || !orbDown(ev.To) {
		return
	}
	s.releases.push(ev.OrbID)
}

func orbDown(state string) bool {
	switch state {
	case orbs.StateFrozen, orbs.StateStopped, orbs.StateFailed, orbs.StateDeleted:
		return true
	}
	return false
}

func (s *Service) releaseLoop() {
	for {
		orbID, ok := s.releases.pop(s.ctx)
		if !ok {
			return
		}
		s.releaseIfDown(s.ctx, orbID)
		if s.onReleased != nil {
			s.onReleased(orbID)
		}
	}
}

// releaseIfDown 在 orb 仍处于非运行态时（同一事务内判断）释放它持有的租约：事件可能晚到，
// 期间 orb 已被唤醒并重新取得租约时不能误放。
func (s *Service) releaseIfDown(ctx context.Context, orbID string) {
	var n int64
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		orb, err := q.GetOrb(ctx, orbID)
		if err != nil || !orbDown(orb.State) {
			return err
		}
		n, err = q.ReleaseBrowserProfileByOrb(ctx, orbID)
		return err
	})
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) && ctx.Err() == nil {
			slog.Warn("释放浏览器 Profile 租约失败", "orb", orbID, "err", err)
		}
		return
	}
	if n > 0 {
		slog.Info("orb 离开运行态，释放浏览器 Profile 租约", "orb", orbID)
		s.o.RefreshDesktop(ctx, orbID)
	}
}

// ---- 小工具 ----

// queue 是无界 FIFO（推入不阻塞），由单个消费者取出。
type queue struct {
	mu   sync.Mutex
	q    []string
	wake chan struct{}
}

func (q *queue) init() { q.wake = make(chan struct{}, 1) }

func (q *queue) push(v string) {
	q.mu.Lock()
	q.q = append(q.q, v)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *queue) pop(ctx context.Context) (string, bool) {
	for {
		q.mu.Lock()
		if len(q.q) > 0 {
			v := q.q[0]
			q.q = q.q[1:]
			q.mu.Unlock()
			return v, true
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", false
		case <-q.wake:
		}
	}
}

// keyLocks 是按键的互斥锁，等锁可被 ctx 取消；不再使用的键随即回收。
type keyLocks struct {
	mu sync.Mutex
	m  map[string]*keyLock
}

type keyLock struct {
	ch   chan struct{}
	refs int
}

func (k *keyLocks) lock(ctx context.Context, key string) (func(), error) {
	k.mu.Lock()
	l := k.m[key]
	if l == nil {
		l = &keyLock{ch: make(chan struct{}, 1)}
		k.m[key] = l
	}
	l.refs++
	k.mu.Unlock()
	select {
	case l.ch <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() {
				<-l.ch
				k.drop(key, l)
			})
		}, nil
	case <-ctx.Done():
		k.drop(key, l)
		return nil, ctx.Err()
	}
}

func (k *keyLocks) drop(key string, l *keyLock) {
	k.mu.Lock()
	l.refs--
	if l.refs == 0 {
		delete(k.m, key)
	}
	k.mu.Unlock()
}

func deref64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func strOr(p *string, def string) string {
	if p == nil || strings.TrimSpace(*p) == "" {
		return def
	}
	return *p
}
