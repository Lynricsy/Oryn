// Package api 组装控制面的 HTTP 路由与 REST 处理器。
package api

import (
	"encoding/json"
	"net/http"

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
	// Mounts 是额外挂载的非 REST 处理器（provider 协议、orb link、WebSocket），键为 chi 路由模式。
	Mounts map[string]http.Handler
}

// Server 实现 apigen.ServerInterface。各领域在自己的文件里为 *Server 实现方法，
// 未实现的方法由内嵌的 apigen.Unimplemented 返回 501。
type Server struct {
	apigen.Unimplemented
	Deps
	area
}

// publicPaths 是 /api/v1 下免鉴权的路径。
var publicPaths = map[string]bool{
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
	r.Use(middleware.RealIP, middleware.Recoverer)
	r.Get("/healthz", Healthz)
	for pattern, h := range s.Mounts {
		r.Handle(pattern, h)
	}
	r.Group(func(r chi.Router) {
		if s.AuthMiddleware != nil {
			authMW := s.AuthMiddleware
			r.Use(func(next http.Handler) http.Handler {
				protected := authMW(next)
				return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					if publicPaths[req.URL.Path] {
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
	})
	r.Handle("/*", webui.Handler())
	return r
}

// Healthz 返回 {"ok":true,"version":"…"}，免鉴权。
func Healthz(w http.ResponseWriter, _ *http.Request) {
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
