package portal

import (
	"database/sql"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/portalhost"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 鉴权握手（计划 4.3）：
//
//	A  portal 主机，未授权的导航请求 → 302 <主域名>/portal/auth?t=<短码>&p=<端口>&next=<path?query>
//	B  主域名 GET /portal/auth       → 已登录：签发一次性码，302 <portal>/__oryn/auth?code=&next=；未登录：302 /login?next=
//	C  portal GET /__oryn/auth       → 兑换码，写 portal 会话与 Cookie，302 /__oryn/auth/check?next=
//	D  portal GET /__oryn/auth/check → Cookie 带回来了：302 next；被浏览器拦截：说明页（握手不会循环）
//	E  portal GET /__oryn/login      → 已是所有者：302 next；否则等同 A
//
// 回跳主机一律由配置按 (短码, 端口) 重算，没有任何参数能把人带到 portal 域之外。

// handshakeHeaders 是握手各步响应共有的头。
func handshakeHeaders(w http.ResponseWriter) {
	noStore(w)
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func textError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg + "\n"))
}

// AuthHandler 是主域名上的 `GET /portal/auth`（步骤 B）。
func (s *Service) AuthHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handshakeHeaders(w)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			textError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		q := r.URL.Query()
		label := "t-" + q.Get("t") + "-p" + q.Get("p")
		code, port, ok := portalhost.ParseLabel(label)
		if !ok {
			textError(w, http.StatusBadRequest, "invalid portal")
			return
		}
		next := sanitizeNext(q.Get("next"))
		pr, err := auth.Authenticate(w, r, s.st)
		var aerr *auth.Error
		if err != nil && !errors.As(err, &aerr) {
			s.internal(w, false, "鉴权失败", err)
			return
		}
		if err == nil && pr.Kind == auth.KindDevice {
			textError(w, http.StatusBadRequest, "device tokens authenticate directly on the portal host with an Authorization header")
			return
		}
		if err != nil || pr.Kind != auth.KindWeb || pr.Recovery {
			redirect(w, "/login?next="+url.QueryEscape(r.URL.RequestURI()))
			return
		}
		if _, err := s.st.Q.GetThreadByShortCode(r.Context(), code); errors.Is(err, sql.ErrNoRows) {
			textError(w, http.StatusNotFound, "portal not found")
			return
		} else if err != nil {
			s.internal(w, false, "读取线程失败", err)
			return
		}
		tok := s.codes.issue(label, pr.ID, s.now())
		redirect(w, s.base.URL(code, port)+"__oryn/auth?code="+url.QueryEscape(tok)+"&next="+url.QueryEscape(next))
	})
}

// reserved 处理 portal 主机上 `/__oryn/` 下的保留端点（C、D、E）；其余保留路径 404。
func (s *Service) reserved(w http.ResponseWriter, r *http.Request, p *portalReq) {
	handshakeHeaders(w)
	var h func(http.ResponseWriter, *http.Request, *portalReq)
	switch r.URL.Path {
	case "/__oryn/auth":
		h = s.redeem
	case "/__oryn/auth/check":
		h = s.check
	case "/__oryn/login":
		h = s.login
	default:
		s.fail(w, p.nav, http.StatusNotFound, "portal_not_found", "保留路径")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		s.fail(w, p.nav, http.StatusMethodNotAllowed, "method_not_allowed", "只支持 GET")
		return
	}
	h(w, r, p)
}

// redeem 是步骤 C：兑换一次性码，建立 portal 会话并写 Cookie。码无效回到 A。
func (s *Service) redeem(w http.ResponseWriter, r *http.Request, p *portalReq) {
	ctx := r.Context()
	q := r.URL.Query()
	next := sanitizeNext(q.Get("next"))
	now := s.now()
	c, ok := s.codes.redeem(q.Get("code"), now)
	if !ok || c.label != p.label {
		redirect(w, s.authURL(p.code, p.port, next))
		return
	}
	ws, err := s.st.Q.GetWebSession(ctx, c.webHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.internal(w, p.nav, "读取网页会话失败", err)
		return
	}
	if err != nil || ws.ExpiresAt <= now.UnixMilli() || ws.Kind != "full" {
		redirect(w, s.authURL(p.code, p.port, next))
		return
	}
	exp := min(now.Add(SessionTTL).UnixMilli(), ws.ExpiresAt)
	tok := ids.Token("ops")
	if err := s.st.Q.InsertPortalSession(ctx, db.InsertPortalSessionParams{
		IDHash: ids.Hash(tok), WebSessionHash: ws.IDHash, Label: p.label,
		CreatedAt: now.UnixMilli(), ExpiresAt: exp,
	}); err != nil {
		s.internal(w, p.nav, "写入 portal 会话失败", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: tok, Path: "/", MaxAge: max(int((exp-now.UnixMilli())/1000), 1),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteNoneMode, Partitioned: true,
	})
	redirect(w, "/__oryn/auth/check?next="+url.QueryEscape(next))
}

// check 是步骤 D：Cookie 带回来了就去 next，否则给说明页而不是再次重定向（避免握手循环）。
func (s *Service) check(w http.ResponseWriter, r *http.Request, p *portalReq) {
	next := sanitizeNext(r.URL.Query().Get("next"))
	_, ok, err := s.cookieOwner(r, p)
	if err != nil {
		s.internal(w, p.nav, "读取 portal 会话失败", err)
		return
	}
	if ok {
		redirect(w, next)
		return
	}
	link := strings.TrimSuffix(s.base.URL(p.code, p.port), "/") + next
	page(w, s.base.Origin, http.StatusOK, "浏览器拦截了 portal 的 Cookie", 0,
		`<p>浏览器拦截了 portal 的 Cookie，无法在当前页面里完成登录（常见于内嵌页面与拦截第三方 Cookie 的浏览器设置）。</p>`+
			`<p><a href="`+html.EscapeString(link)+`" target="_blank" rel="noopener noreferrer">在新窗口打开该 portal</a></p>`)
}

// login 是步骤 E：已是所有者直接去 next，否则走 A。供主站 iframe 与「新窗口」使用：公开中的 portal 也先认出所有者。
func (s *Service) login(w http.ResponseWriter, r *http.Request, p *portalReq) {
	next := sanitizeNext(r.URL.Query().Get("next"))
	if _, isDevice, valid := s.deviceOwner(r); isDevice && valid {
		redirect(w, next)
		return
	}
	_, ok, err := s.cookieOwner(r, p)
	if err != nil {
		s.internal(w, p.nav, "读取 portal 会话失败", err)
		return
	}
	if ok && crossSiteAllowed(r, p.origin) {
		redirect(w, next)
		return
	}
	redirect(w, s.authURL(p.code, p.port, next))
}

// codes 是一次性码表（只在内存中，60 秒、单次），照 auth.Tickets 实现。
type codes struct {
	mu sync.Mutex
	m  map[string]codeEntry
}

type codeEntry struct {
	label   string
	webHash string
	exp     time.Time
}

const maxCodes = 10000

func newCodes() *codes { return &codes{m: map[string]codeEntry{}} }

func (c *codes) issue(label, webHash string, now time.Time) string {
	tok := ids.Token("opc")
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)
	if len(c.m) >= maxCodes {
		c.m = map[string]codeEntry{}
	}
	c.m[tok] = codeEntry{label: label, webHash: webHash, exp: now.Add(CodeTTL)}
	return tok
}

func (c *codes) redeem(tok string, now time.Time) (codeEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[tok]
	delete(c.m, tok)
	if !ok || !now.Before(e.exp) {
		return codeEntry{}, false
	}
	return e, true
}

func (c *codes) prune(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)
}

func (c *codes) pruneLocked(now time.Time) {
	for k, v := range c.m {
		if !now.Before(v.exp) {
			delete(c.m, k)
		}
	}
}

// revokeWeb 作废某网页会话名下全部未兑换的码。
func (c *codes) revokeWeb(webHash string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.m {
		if v.webHash == webHash {
			delete(c.m, k)
		}
	}
}

// Revoke 订阅 auth 吊销事件（网页会话登出 / 过期 / 恢复、设备吊销）：取消该主体的全部 portal 连接，
// 并作废其未兑换的一次性码。portal_sessions 行随 web_sessions 级联删除，新请求随即回到握手。
func (s *Service) Revoke(kind, id string) {
	if kind == auth.KindWeb {
		s.codes.revokeWeb(id)
	}
	s.reg.cancelKey(kind + "/" + id)
}
