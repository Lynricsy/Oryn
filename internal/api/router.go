// Package api 组装控制面的 HTTP 路由与 REST 处理器。
package api

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/version"
	"github.com/Lynricsy/Oryn/internal/webui"
)

// Deps 是路由所需的依赖。
type Deps struct {
	Config config.Orynd
	Store  *store.Store
	// AuthMiddleware 保护 /api/v1 下除免鉴权路径外的全部端点；为 nil 时不鉴权（仅测试）。
	AuthMiddleware func(http.Handler) http.Handler
	// Mounts 是额外挂载的非 REST 处理器（provider 协议、orb link、WebSocket、portal 握手），键为 chi 路由模式。
	Mounts map[string]http.Handler
	// Portal 是 portal 数据面的主机分流中间件（未配置 portal_domain 时为 nil）：先于全部路由执行，
	// portal 主机的请求永远到不了 /api/v1、link 与 SPA。
	Portal func(http.Handler) http.Handler
	// FrameSrc 是 SPA CSP 的 frame-src 源（portal 泛域名；未启用为空）。
	FrameSrc string
}

// Server 实现 apigen.ServerInterface。各领域在自己的文件里为 *Server 实现方法，
// 未实现的方法由内嵌的 apigen.Unimplemented 返回 501。
type Server struct {
	apigen.Unimplemented
	Deps
	area
}

// publicPaths 是 /api/v1 下免鉴权的路径；另外 /provider/v1/ 前缀由 provider 令牌自行鉴权，
// /api/v1/public/ 前缀是公开分享的匿名只读接口（以 slug 为能力令牌）。
var publicPaths = map[string]bool{
	"/healthz":                   true,
	"/api/v1/auth/setup/begin":   true,
	"/api/v1/auth/setup/finish":  true,
	"/api/v1/auth/login/begin":   true,
	"/api/v1/auth/login/finish":  true,
	"/api/v1/auth/recovery":      true,
	"/api/v1/devices/pair/start": true,
	"/api/v1/devices/pair/poll":  true,
}

// NewRouter 返回控制面根路由。
func NewRouter(s *Server) http.Handler {
	r := chi.NewRouter()
	r.Use(trustedProxyIP, middleware.Recoverer)
	if s.Portal != nil {
		r.Use(s.Portal)
	}
	for pattern, h := range s.Mounts {
		r.Handle(pattern, h)
	}
	r.Group(func(r chi.Router) {
		if s.AuthMiddleware != nil {
			authMW := s.AuthMiddleware
			r.Use(func(next http.Handler) http.Handler {
				protected := authMW(next)
				return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					// 含编码字符（如 %2F）的路径一律按受保护处理：chi 按 RawPath 路由，
					// 解码后的 Path 与实际命中的路由可能不一致。
					public := req.URL.RawPath == "" &&
						(publicPaths[req.URL.Path] || strings.HasPrefix(req.URL.Path, "/provider/v1/") ||
							strings.HasPrefix(req.URL.Path, "/api/v1/public/"))
					if public {
						next.ServeHTTP(w, req)
						return
					}
					protected.ServeHTTP(w, req)
				})
			})
		}
		apigen.HandlerWithOptions(s, apigen.ChiServerOptions{
			BaseRouter: r,
			ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
				WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
			},
		})
		// 服务端路径前缀下没有路由的请求一律 404 JSON，不回落到 SPA（否则客户端拿到 200 text/html）。
		// 放在鉴权组里：匿名请求与真实端点一样先过鉴权（免鉴权前缀直接 404）。
		for _, prefix := range serverPrefixes {
			r.Handle(prefix, http.HandlerFunc(notFound))
			r.Handle(prefix+"/*", http.HandlerFunc(notFound))
		}
	})
	r.Handle("/*", webui.Handler(s.FrameSrc))
	return r
}

// serverPrefixes 是只属于服务端的路径前缀（REST、provider 协议、orb link），不提供给 SPA 路由。
var serverPrefixes = []string{"/api", "/provider", "/orb"}

// notFound 写出 404 {code:"not_found"}。
func notFound(w http.ResponseWriter, _ *http.Request) {
	WriteError(w, http.StatusNotFound, "not_found", "no such endpoint")
}

// trustedProxyIP 只在请求来自本机反代（回环地址）时采信 X-Real-IP / X-Forwarded-For。
// orb 经 docker 网桥直连控制面，其自报的转发头不可信。
func trustedProxyIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err == nil {
			if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
				if real := forwardedClient(r); real != "" {
					r.RemoteAddr = net.JoinHostPort(real, "0")
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// forwardedClient 优先取反代设置的 X-Real-IP，否则取 X-Forwarded-For 的最后一跳（由反代追加，客户端无法伪造）。
func forwardedClient(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(v) != nil {
		return v
	}
	xff := r.Header.Values("X-Forwarded-For")
	if len(xff) == 0 {
		return ""
	}
	parts := strings.Split(xff[len(xff)-1], ",")
	if v := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(v) != nil {
		return v
	}
	return ""
}

// GetHealthz 返回 {"ok":true,"version":"…"}，免鉴权（见 publicPaths）。
func (s *Server) GetHealthz(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version.Version})
}

// WriteJSON 以 JSON 写出响应体。
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError 写出 {code, message} 错误体。
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, map[string]string{"code": code, "message": message})
}
