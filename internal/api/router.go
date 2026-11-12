// Package api 组装控制面的 HTTP 路由与 REST 处理器。
package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Lynricsy/Oryn/internal/version"
	"github.com/Lynricsy/Oryn/internal/webui"
)

// Deps 是路由所需的依赖。
type Deps struct{}

// NewRouter 返回控制面根路由。
func NewRouter(_ Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RealIP, middleware.Recoverer)
	r.Get("/healthz", Healthz)
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
