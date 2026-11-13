package portal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/orbs"
)

// target 是代理一次请求所需的上下文（经 request context 交给 Rewrite / ModifyResponse / ErrorHandler）。
type target struct {
	orbID string
	port  int
	owner bool
	nav   bool
	// clientCtx 是原始请求的 context：它已结束 = 客户端断开，ErrorHandler 不再写响应。
	clientCtx context.Context
}

type targetKey struct{}

func targetFrom(r *http.Request) *target {
	t, _ := r.Context().Value(targetKey{}).(*target)
	return t
}

// newProxy 构造共享的反向代理：每个请求经 orb link 开一条 portal 流（不复用连接），
// 升级（101）由 ReverseProxy 劫持并双向拷贝，请求 context 取消即断开两端。
func (s *Service) newProxy() *httputil.ReverseProxy {
	tr := &http.Transport{
		DialContext:            s.dialPortal,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		ResponseHeaderTimeout:  10 * time.Minute,
		MaxResponseHeaderBytes: 1 << 20,
	}
	return &httputil.ReverseProxy{
		Transport:      tr,
		FlushInterval:  -1,
		Rewrite:        s.rewrite,
		ModifyResponse: s.modifyResponse,
		ErrorHandler:   s.proxyError,
		ErrorLog:       slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	}
}

// dialPortal 从 `<orbID>:<端口>` 解析目标并经 orb link 开 portal 流。
func (s *Service) dialPortal(ctx context.Context, _, addr string) (net.Conn, error) {
	orbID, ps, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(ps)
	if err != nil {
		return nil, err
	}
	c, err := s.orbs.DialPortal(ctx, orbID, port)
	if err != nil {
		return nil, err
	}
	return &portalConn{Conn: c}, nil
}

// portalConn 在 Close 前把截止时间设为当前时间，让阻塞在流上的读写立即返回
// （yamux 流的 Close 只是半关闭，读端要等对端；context 取消时必须立刻断开）。
type portalConn struct{ net.Conn }

func (c *portalConn) Close() error {
	_ = c.Conn.SetDeadline(time.Now())
	return c.Conn.Close()
}

// CloseWrite 转发半关闭；底层不支持时返回错误，让升级拷贝直接结束。
func (c *portalConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return errors.New("portal: half-close unsupported")
}

// AuthenticatedHeader 是给应用的身份头。
const AuthenticatedHeader = "X-Oryn-Authenticated"

// rewrite 构造发给应用的请求：目标 `http://<orbID>:<端口>` + 原路径与查询，Host 保持公开主机名；
// 剥掉全部 X-Oryn-* 入站头、portal Cookie 与设备令牌，再写入身份头。
func (s *Service) rewrite(pr *httputil.ProxyRequest) {
	t := targetFrom(pr.In)
	pr.Out.URL.Scheme = "http"
	pr.Out.URL.Host = net.JoinHostPort(t.orbID, strconv.Itoa(t.port))
	pr.Out.Host = pr.In.Host
	pr.SetXForwarded()
	pr.Out.Header.Set("X-Forwarded-Proto", s.base.Scheme)
	pr.Out.Header.Set("X-Forwarded-Host", pr.In.Host)

	for k := range pr.Out.Header {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), "X-Oryn-") {
			delete(pr.Out.Header, k)
		}
	}
	if t.owner {
		pr.Out.Header.Set(AuthenticatedHeader, "owner=yes")
	} else {
		pr.Out.Header.Set(AuthenticatedHeader, "owner=no")
	}

	if vals := pr.Out.Header.Values("Cookie"); len(vals) > 0 {
		var kept []string
		for _, v := range vals {
			if c := stripCookie(v, CookieName); c != "" {
				kept = append(kept, c)
			}
		}
		pr.Out.Header.Del("Cookie")
		for _, v := range kept {
			pr.Out.Header.Add("Cookie", v)
		}
	}
	if tok, ok := strings.CutPrefix(pr.Out.Header.Get("Authorization"), "Bearer "); ok && strings.HasPrefix(tok, "odt_") {
		pr.Out.Header.Del("Authorization")
	}
}

// stripCookie 按 `;` 切分 Cookie 头，去掉名字为 name 的段后原样拼回（不经 http.Cookie 重新序列化）。
func stripCookie(header, name string) string {
	parts := strings.Split(header, ";")
	kept := parts[:0]
	for _, part := range parts {
		n, _, _ := strings.Cut(part, "=")
		if strings.TrimSpace(n) == name {
			continue
		}
		kept = append(kept, part)
	}
	return strings.TrimSpace(strings.Join(kept, ";"))
}

// modifyResponse 让 portal 能被主站内嵌：删除 X-Frame-Options、各条 CSP 去掉 frame-ancestors 后
// 追加 `frame-ancestors 'self' <主域名>`；丢弃应用试图设置的 portal Cookie；匿名响应加 noindex。
func (s *Service) modifyResponse(res *http.Response) error {
	h := res.Header
	h.Del("X-Frame-Options")
	if vals := h.Values("Content-Security-Policy"); len(vals) > 0 {
		h.Del("Content-Security-Policy")
		for _, v := range vals {
			if v = dropDirective(v, "frame-ancestors"); v != "" {
				h.Add("Content-Security-Policy", v)
			}
		}
	}
	h.Add("Content-Security-Policy", "frame-ancestors 'self' "+s.base.Origin)
	if vals := h.Values("Set-Cookie"); len(vals) > 0 {
		h.Del("Set-Cookie")
		for _, v := range vals {
			n, _, _ := strings.Cut(v, "=")
			if strings.TrimSpace(n) != CookieName {
				h.Add("Set-Cookie", v)
			}
		}
	}
	if t := targetFrom(res.Request); t != nil && !t.owner {
		h.Set("X-Robots-Tag", "noindex, nofollow")
	}
	return nil
}

// dropDirective 从一条 CSP 中删除名为 name 的指令（大小写不敏感），其余指令原样保留。
func dropDirective(csp, name string) string {
	parts := strings.Split(csp, ";")
	kept := parts[:0]
	for _, d := range parts {
		f := strings.Fields(d)
		if len(f) == 0 || strings.EqualFold(f[0], name) {
			continue
		}
		kept = append(kept, strings.TrimSpace(d))
	}
	return strings.Join(kept, "; ")
}

// proxyError 把拨号与上游错误映射为 portal 错误码。
func (s *Service) proxyError(w http.ResponseWriter, r *http.Request, err error) {
	t := targetFrom(r)
	if t == nil || t.clientCtx.Err() != nil {
		return // 客户端已断开
	}
	if r.Context().Err() != nil {
		// 连接被吊销或分享终止取消
		s.fail(w, t.nav, http.StatusUnauthorized, "portal_unauthorized", "访问已被撤销")
		return
	}
	var de *orbs.PortalDialError
	var ne net.Error
	switch {
	case errors.As(err, &de):
		switch de.Code {
		case "not_exposed":
			s.fail(w, t.nav, http.StatusNotFound, "portal_not_found", "该端口没有声明 portal")
		case "forbidden_port":
			s.fail(w, t.nav, http.StatusForbidden, "forbidden_port", "该端口不允许暴露")
		case "connection_refused":
			s.failRefresh(w, t.nav, http.StatusBadGateway, "service_unreachable", "服务没有在该端口监听（可能仍在启动）")
		case "timeout":
			s.fail(w, t.nav, http.StatusGatewayTimeout, "portal_timeout", "连接服务超时")
		default:
			s.failRefresh(w, t.nav, http.StatusBadGateway, "portal_unreachable", fmt.Sprintf("无法连接服务：%s", de.Message))
		}
	case errors.Is(err, orbs.ErrOrbNotReady):
		w.Header().Set("Retry-After", "3")
		s.fail(w, t.nav, http.StatusServiceUnavailable, "orb_unavailable", "orb 暂不可用，请稍后重试")
	case errors.Is(err, orbs.ErrOrbdOutdated):
		s.fail(w, t.nav, http.StatusConflict, "orbd_outdated", "该 orb 的 orbd 版本过旧，不支持 portal")
	case errors.As(err, &ne) && ne.Timeout():
		s.fail(w, t.nav, http.StatusGatewayTimeout, "portal_timeout", "等待服务响应超时")
	default:
		slog.Debug("portal: 代理失败", "orb", t.orbID, "port", t.port, "err", err)
		s.failRefresh(w, t.nav, http.StatusBadGateway, "portal_unreachable", "无法连接服务")
	}
}
