// Package portal 是 portal 数据面（M4）：portal 主机 `t-<线程短码>-p<端口>.<portal_domain>` 上的请求分流、
// 所有者鉴权握手（主域名 `/portal/auth` → portal 主机 `/__oryn/auth` → `/__oryn/auth/check`）、
// Fetch Metadata 跨站策略、经 orb link portal 流的反向代理、唤醒过渡页与按端口的限时公开分享。
//
// portal 实体是 (orb, 端口)；线程短码只是找 orb 的别名。所有者凭设备令牌或 host-only Cookie
// `__Host-oryn_portal` 访问；公开中的端口另允许匿名访客。portal 主机的请求永远到不了 /api/v1、link 与 SPA。
package portal

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/portalhost"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// Orbs 是 portal 对 orb 服务的依赖（由 *orbs.Service 实现）。
type Orbs interface {
	Get(ctx context.Context, orbID string) (db.Orb, error)
	EnsureUsable(ctx context.Context, orbID, feature string, wake bool, wait time.Duration) error
	DialPortal(ctx context.Context, orbID string, port int) (net.Conn, error)
	PortalService(ctx context.Context, orbID string, port int) (apigen.OrbService, bool, error)
	TouchUserCoalesced(ctx context.Context, orbID string)
	Refresh(ctx context.Context, orbID string)
}

// Config 是 portal 服务的配置。
type Config struct {
	Base portalhost.Base
	// Now 默认 time.Now。
	Now func() time.Time
	// AfterFunc 默认 time.AfterFunc；测试注入后手动触发。f 不得在 AfterFunc 内同步调用。
	AfterFunc func(d time.Duration, f func()) (stop func())
}

// 常量与限额（计划「限额与错误码」）。
const (
	// CookieName 是 portal 主机上的所有者会话 Cookie（host-only）。
	CookieName = "__Host-oryn_portal"
	// CodeTTL 是一次性码有效期。
	CodeTTL = 60 * time.Second
	// SessionTTL 是 portal 会话上限（不滑动，且不长于其网页会话）。
	SessionTTL = 30 * 24 * time.Hour
	// WakeWait 是非导航请求遇到不可用 orb 时等待唤醒的上限。
	WakeWait = 25 * time.Second

	featurePortal  = "portal"
	maxOrbConns    = 256
	maxAnonConns   = 64
	maxNext        = 2048
	reservedPrefix = "/__oryn/"
	sweepEvery     = time.Hour
)

// ErrNotFound 表示该 (orb, 端口) 不是已声明的 portal。
var ErrNotFound = errors.New("portal: not a declared portal")

// Service 是 portal 数据面。
type Service struct {
	base      portalhost.Base
	now       func() time.Time
	afterFunc func(d time.Duration, f func()) (stop func())
	st        *store.Store
	orbs      Orbs
	proxy     *httputil.ReverseProxy
	codes     *codes
	reg       *registry
	timers    *shareTimers
	wakeWait  time.Duration
}

// New 构造 portal 服务。
func New(cfg Config, st *store.Store, o Orbs) *Service {
	s := &Service{
		base:      cfg.Base,
		now:       cfg.Now,
		afterFunc: cfg.AfterFunc,
		st:        st,
		orbs:      o,
		codes:     newCodes(),
		reg:       newRegistry(maxOrbConns, maxAnonConns),
		timers:    &shareTimers{m: map[shareKey]*shareTimer{}},
		wakeWait:  WakeWait,
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.afterFunc == nil {
		s.afterFunc = func(d time.Duration, f func()) func() {
			t := time.AfterFunc(d, f)
			return func() { t.Stop() }
		}
	}
	s.proxy = s.newProxy()
	return s
}

// portalReq 是命中 portal 主机后解析出的目标。
type portalReq struct {
	code     string
	port     int
	label    string
	orbID    string
	orbState string
	nav      bool
	origin   string // 本 portal 的 origin（跨站检查用）
}

// Middleware 把 portal 主机的请求交给数据面，其余请求交给 next。
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		label, ok := s.base.Match(r.Host)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		s.serve(w, r, label)
	})
}

// isNavigation：GET 且（Sec-Fetch-Mode: navigate，或无 Sec-Fetch-Mode 而 Accept 含 text/html）。
func isNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if m := r.Header.Get("Sec-Fetch-Mode"); m != "" {
		return m == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func (s *Service) serve(w http.ResponseWriter, r *http.Request, label string) {
	ctx := r.Context()
	nav := isNavigation(r)
	code, port, ok := portalhost.ParseLabel(label)
	if !ok {
		s.fail(w, nav, http.StatusNotFound, "portal_not_found", "portal 不存在")
		return
	}
	th, err := s.st.Q.GetThreadByShortCode(ctx, code)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && th.OrbID == nil) {
		s.fail(w, nav, http.StatusNotFound, "portal_not_found", "portal 不存在")
		return
	}
	if err != nil {
		s.internal(w, nav, "读取线程失败", err)
		return
	}
	orb, err := s.orbs.Get(ctx, *th.OrbID)
	if errors.Is(err, orbs.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
		s.fail(w, nav, http.StatusNotFound, "portal_not_found", "portal 不存在")
		return
	}
	if err != nil {
		s.internal(w, nav, "读取 orb 失败", err)
		return
	}
	if orb.State == orbs.StateDeleting || orb.State == orbs.StateDeleted {
		s.fail(w, nav, http.StatusGone, "orb_deleted", "orb 已删除")
		return
	}
	p := &portalReq{
		code: code, port: port, label: label, orbID: orb.ID, orbState: orb.State, nav: nav,
		origin: strings.TrimSuffix(s.base.URL(code, port), "/"),
	}
	if isReserved(r.URL.Path) {
		s.reserved(w, r, p)
		return
	}

	epoch := s.reg.current()
	who, ok := s.authorize(w, r, p)
	if !ok {
		return
	}
	// 通过鉴权的请求（所有者与公开访客）都计一次用户交互；交互先于可用性判定
	s.orbs.TouchUserCoalesced(ctx, p.orbID)
	if !s.ensureUsable(w, r, p) {
		return
	}

	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var leave func()
	for i := range 3 {
		leave, err = s.reg.enter(epoch, p.orbID, who.key, who.anon, cancel)
		if !errors.Is(err, errStale) || i == 2 {
			break
		}
		// 鉴权与登记之间发生了吊销 / 分享终止：重新判定身份
		epoch = s.reg.current()
		if who, ok = s.authorize(w, r, p); !ok {
			return
		}
	}
	if err != nil {
		w.Header().Set("Retry-After", "3")
		s.fail(w, nav, http.StatusServiceUnavailable, "portal_busy", "portal 并发连接过多，请稍后重试")
		return
	}
	defer leave()
	t := &target{orbID: p.orbID, port: p.port, owner: !who.anon, nav: nav, clientCtx: ctx}
	s.proxy.ServeHTTP(w, r.WithContext(context.WithValue(pctx, targetKey{}, t)))
}

// isReserved 报告路径是否落在保留前缀 `/__oryn/` 下（按原样与按 path.Clean 后各判一次，
// `/a/../__oryn/x` 这类路径同样不会交给应用）。
func isReserved(p string) bool {
	cp := path.Clean("/" + p)
	return strings.HasPrefix(p, reservedPrefix) || cp+"/" == reservedPrefix || strings.HasPrefix(cp, reservedPrefix)
}

// identity 是通过鉴权的访问者：所有者（key = web/<会话哈希> 或 device/<id>）或匿名访客（key = share/<orb>/<端口>）。
type identity struct {
	key  string
	anon bool
}

func anonKey(orbID string, port int) string { return "share/" + orbID + "/" + strconv.Itoa(port) }

// authorize 判定访问者身份（先所有者，后公开）；未授权时写出响应并返回 ok=false。
func (s *Service) authorize(w http.ResponseWriter, r *http.Request, p *portalReq) (identity, bool) {
	ctx := r.Context()
	if pr, isDevice, valid := s.deviceOwner(r); isDevice {
		if !valid {
			noStore(w)
			s.fail(w, p.nav, http.StatusUnauthorized, "portal_unauthorized", "设备令牌无效或已吊销")
			return identity{}, false
		}
		return identity{key: pr}, true
	}
	blocked := false
	webHash, ok, err := s.cookieOwner(r, p)
	if err != nil {
		s.internal(w, p.nav, "读取 portal 会话失败", err)
		return identity{}, false
	}
	if ok {
		if crossSiteAllowed(r, p.origin) {
			return identity{key: auth.KindWeb + "/" + webHash}, true
		}
		blocked = true
	}
	shared, err := s.shareActive(ctx, p.orbID, p.port)
	if err != nil {
		s.internal(w, p.nav, "读取公开分享失败", err)
		return identity{}, false
	}
	if shared {
		return identity{key: anonKey(p.orbID, p.port), anon: true}, true
	}
	noStore(w)
	switch {
	case blocked:
		s.fail(w, p.nav, http.StatusForbidden, "cross_site_blocked", "跨站请求不能使用 portal 登录态")
	case p.nav:
		redirect(w, s.authURL(p.code, p.port, sanitizeNext(r.URL.RequestURI())))
	default:
		s.fail(w, false, http.StatusUnauthorized, "portal_unauthorized", "需要登录后访问该 portal")
	}
	return identity{}, false
}

// deviceOwner：`Authorization: Bearer odt_…` 时 isDevice=true，valid 表示令牌有效（未吊销）。
// 其它形态的 Authorization 属于应用，不参与判定。
func (s *Service) deviceOwner(r *http.Request) (key string, isDevice, valid bool) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || ids.TokenPrefix(tok) != "odt" {
		return "", false, false
	}
	pr, ok := auth.DeviceByToken(r.Context(), s.st, tok)
	if !ok {
		return "", true, false
	}
	return pr.Kind + "/" + pr.ID, true, true
}

// cookieOwner 校验 portal Cookie：会话有效、所属网页会话有效且为完整会话、标签等于本主机。
// 返回网页会话哈希（吊销订阅的主体 id）。不做跨站检查。
func (s *Service) cookieOwner(r *http.Request, p *portalReq) (string, bool, error) {
	now := s.now().UnixMilli()
	for _, c := range r.CookiesNamed(CookieName) {
		if c.Value == "" {
			continue
		}
		ps, err := s.st.Q.GetPortalSession(r.Context(), db.GetPortalSessionParams{IDHash: ids.Hash(c.Value), Now: now})
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", false, err
		}
		if ps.Label == p.label {
			return ps.WebSessionHash, true, nil
		}
	}
	return "", false, nil
}

// shareActive 报告 (orb, 端口) 当前是否公开中；每个匿名请求都读库，定时器迟到也不影响到期判定。
func (s *Service) shareActive(ctx context.Context, orbID string, port int) (bool, error) {
	sh, err := s.st.Q.GetPortalShare(ctx, db.GetPortalShareParams{OrbID: orbID, Port: int64(port)})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return sh.ExpiresAt > s.now().UnixMilli(), nil
}

// crossSiteAllowed 是「凭 Cookie 成为所有者」这一步的 Fetch Metadata 策略：
// 同源 / 用户直接发起放行；同站 / 跨站只放行 GET/HEAD 的文档或 iframe 导航；
// 无 Sec-Fetch-Site 时有 Origin 且不是本 portal 则拒绝，其余（非浏览器客户端、老浏览器）放行。
func crossSiteAllowed(r *http.Request, origin string) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		switch site {
		case "same-origin", "none":
			return true
		case "same-site", "cross-site":
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				return false
			}
			if r.Header.Get("Sec-Fetch-Mode") != "navigate" {
				return false
			}
			d := r.Header.Get("Sec-Fetch-Dest")
			return d == "document" || d == "iframe"
		default:
			return false
		}
	}
	if o := r.Header.Get("Origin"); o != "" && o != origin {
		return false
	}
	return true
}

// ensureUsable 确认 orb 可用；不可用时按请求类型唤醒（导航回过渡页，其余等待至多 wakeWait）。
// 返回 false 时已写出响应。
func (s *Service) ensureUsable(w http.ResponseWriter, r *http.Request, p *portalReq) bool {
	ctx := r.Context()
	err := s.orbs.EnsureUsable(ctx, p.orbID, featurePortal, false, 0)
	if err == nil {
		return true
	}
	if !errors.Is(err, orbs.ErrOrbNotReady) {
		s.usableErr(w, p, err)
		return false
	}
	// 冻结 / 停机 / 启动中：镜像里没有该端口的 portal 就不唤醒
	_, found, perr := s.orbs.PortalService(ctx, p.orbID, p.port)
	if perr != nil {
		s.internal(w, p.nav, "读取服务镜像失败", perr)
		return false
	}
	if !found {
		s.fail(w, p.nav, http.StatusNotFound, "portal_not_found", "该端口没有声明 portal")
		return false
	}
	if p.orbState == orbs.StateFailed {
		s.fail(w, p.nav, http.StatusServiceUnavailable, "orb_unavailable", "orb 启动失败，请回到线程页重试")
		return false
	}
	if p.nav {
		// 立即返回（已触发唤醒），过渡页每 2 秒自动刷新
		if err := s.orbs.EnsureUsable(ctx, p.orbID, featurePortal, true, 0); err != nil && !errors.Is(err, orbs.ErrOrbNotReady) {
			s.usableErr(w, p, err)
			return false
		}
		wakingPage(w, s.base.Origin)
		return false
	}
	err = s.orbs.EnsureUsable(ctx, p.orbID, featurePortal, true, s.wakeWait)
	if err == nil {
		return true
	}
	s.usableErr(w, p, err)
	return false
}

func (s *Service) usableErr(w http.ResponseWriter, p *portalReq, err error) {
	switch {
	case errors.Is(err, orbs.ErrOrbdOutdated):
		s.fail(w, p.nav, http.StatusConflict, "orbd_outdated", "该 orb 的 orbd 版本过旧，不支持 portal")
	case errors.Is(err, orbs.ErrOrbGone):
		s.fail(w, p.nav, http.StatusGone, "orb_deleted", "orb 已删除")
	case errors.Is(err, orbs.ErrNotFound):
		s.fail(w, p.nav, http.StatusNotFound, "portal_not_found", "portal 不存在")
	case errors.Is(err, orbs.ErrOrbNotReady), errors.Is(err, context.DeadlineExceeded):
		w.Header().Set("Retry-After", "3")
		s.fail(w, p.nav, http.StatusServiceUnavailable, "orb_unavailable", "orb 暂不可用，请稍后重试")
	default:
		s.internal(w, p.nav, "判定 orb 可用性失败", err)
	}
}

func (s *Service) internal(w http.ResponseWriter, nav bool, msg string, err error) {
	slog.Error("portal: "+msg, "err", err)
	s.fail(w, nav, http.StatusInternalServerError, "internal", msg)
}

// authURL 是握手步骤 A 的目标：主域名 `/portal/auth?t=&p=&next=`。
func (s *Service) authURL(code string, port int, next string) string {
	return s.base.Origin + "/portal/auth?t=" + code + "&p=" + strconv.Itoa(port) + "&next=" + url.QueryEscape(next)
}

// sanitizeNext 是握手各步共用的回跳路径规则：以单个 `/` 开头、第二个字符不是 `/` 或 `\`、
// 不含控制字符、≤2048 字节，否则取 `/`。
func sanitizeNext(next string) string {
	if next == "" || len(next) > maxNext || next[0] != '/' {
		return "/"
	}
	if len(next) > 1 && (next[1] == '/' || next[1] == '\\') {
		return "/"
	}
	for i := range len(next) {
		if c := next[i]; c < 0x20 || c == 0x7f {
			return "/"
		}
	}
	return next
}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }

// redirect 写 302；Location 原样输出（调用方保证已编码）。
func redirect(w http.ResponseWriter, loc string) {
	noStore(w)
	w.Header().Set("Location", loc)
	w.WriteHeader(http.StatusFound)
}
