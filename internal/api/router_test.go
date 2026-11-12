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
}
