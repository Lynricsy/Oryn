package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// 鉴权中间件拒绝一切时，免鉴权路径仍可访问，受保护路径返回 401。
func TestRouterPublicPaths(t *testing.T) {
	deny := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			WriteError(w, http.StatusUnauthorized, "unauthorized", "x")
		})
	}
	h := NewRouter(&Server{Deps: Deps{AuthMiddleware: deny}})
	get := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}
	rec := get("GET", "/healthz")
	assert.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), `"ok":true`)
	assert.Equal(t, 401, get("GET", "/api/v1/threads").Code)
	assert.NotEqual(t, 401, get("POST", "/provider/v1/bye").Code)
	assert.Equal(t, 200, get("GET", "/t/some-thread").Code, "SPA 回退")
	assert.Equal(t, 401, get("DELETE", "/api/v1/devices/pair%2Fstart").Code, "编码路径不得命中免鉴权表")
}

func TestTrustedProxyIP(t *testing.T) {
	var got string
	h := trustedProxyIP(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.RemoteAddr }))
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "172.18.0.5:1234"
	req.Header.Set("X-Real-IP", "1.2.3.4")
	h.ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, "172.18.0.5:1234", got, "非回环来源的转发头不可信")
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Del("X-Real-IP")
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 9.9.9.9")
	h.ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, "9.9.9.9:0", got, "取反代追加的最后一跳")
}
