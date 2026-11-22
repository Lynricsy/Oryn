package api

import (
	"bytes"
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
)

// Internal 在进程内调用 REST 处理器：不经鉴权中间件，主体是全局助手对话（auth.KindAssistant）。只给 coord 的 oryn 工具用。
type Internal struct{ h http.Handler }

// NewInternal 返回只挂 REST 路由的进程内入口（不挂鉴权、Portal、Mounts 与 SPA）。
func NewInternal(s *Server) *Internal {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	apigen.HandlerWithOptions(s, apigen.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: paramError})
	r.NotFound(notFound)
	return &Internal{h: r}
}

// Do 执行一次请求并缓冲响应。target 是已转义的路径加可选 ?query（含 /api/v1 前缀）；out 是响应体的前 max 字节，total 是全长。
func (i *Internal) Do(ctx context.Context, threadID, method, target, contentType string, body []byte, max int) (status int, header http.Header, out []byte, total int) {
	ctx = auth.WithPrincipal(ctx, auth.Principal{Kind: auth.KindAssistant, ID: threadID})
	req, err := http.NewRequestWithContext(ctx, method, "http://oryn.internal"+target, bytes.NewReader(body))
	if err != nil {
		msg := []byte(`{"code":"invalid_request","message":"bad request target"}`)
		return http.StatusBadRequest, http.Header{"Content-Type": {"application/json"}}, msg, len(msg)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.RemoteAddr = "127.0.0.1:0"
	rec := &bufferedResponse{header: http.Header{}, max: max}
	i.h.ServeHTTP(rec, req)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec.status, rec.header, rec.body.Bytes(), rec.total
}

// bufferedResponse 是 Do 用的响应记录器：只保留前 max 字节，total 累计全长。
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
	max    int
	total  int
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	b.total += len(p)
	if room := b.max - b.body.Len(); room > 0 {
		b.body.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (b *bufferedResponse) Flush() {}
