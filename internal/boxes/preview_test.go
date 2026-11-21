package boxes

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/orbs"
)

func TestPreviewMeta(t *testing.T) {
	for name, tc := range map[string]struct {
		html string
		want bool
	}{
		"enabled iframe":       {`<html><head><meta name="oryn-preview" content="v1; enabled; iframe"></head><body></body></html>`, true},
		"order and spaces":     {`<head><meta content=" v1 ;iframe ; enabled ; future" name="oryn-preview"/></head>`, true},
		"missing v1":           {`<head><meta name="oryn-preview" content="enabled; iframe"></head>`, false},
		"v2":                   {`<head><meta name="oryn-preview" content="v2; enabled; iframe"></head>`, false},
		"missing iframe":       {`<head><meta name="oryn-preview" content="v1; enabled"></head>`, false},
		"missing enabled":      {`<head><meta name="oryn-preview" content="v1; iframe"></head>`, false},
		"in body":              {`<head></head><body><meta name="oryn-preview" content="v1; enabled; iframe"></body>`, false},
		"after head closed":    {`<html><head><title>x</title></head><meta name="oryn-preview" content="v1; enabled; iframe">`, false},
		"other meta first":     {`<head><meta name="viewport" content="width=device-width"><meta name="oryn-preview" content="v1; enabled; iframe"></head>`, true},
		"amp tag not accepted": {`<head><meta name="amp-preview" content="v1; enabled; iframe"></head>`, false},
		"no head element":      {`<meta name="oryn-preview" content="v1; enabled; iframe"><p>hi`, true},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, hasPreviewMeta(strings.NewReader(tc.html)))
		})
	}
}

const previewPage = `<!doctype html><html><head><meta name="oryn-preview" content="v1; enabled; iframe"></head><body>hi</body></html>`

// previewEnv 建一台 ready 的 box 与一个 live 的 app，DialPortal 接到 h。
func previewEnv(t *testing.T, h http.Handler) (*env, *[]*http.Request) {
	e := newEnv(t, at("2026-11-20T12:00:00Z"))
	b := e.box("b1", orbs.StateReady)
	e.orbs.mu.Lock()
	e.orbs.connected[b.OrbID] = true
	e.orbs.mu.Unlock()
	e.app(b.ID, "hello", "", true, true)
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Clone(context.Background()))
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	e.orbs.dial = func(ctx context.Context, orbID string, port int) (net.Conn, error) {
		assert.Equal(t, b.OrbID, orbID)
		assert.Equal(t, 30000, port)
		var d net.Dialer
		return d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}
	return e, &seen
}

func (e *env) preview(name string) bool {
	e.t.Helper()
	a, err := e.st.Q.GetBoxAppByName(e.ctx, name)
	require.NoError(e.t, err)
	return a.Preview
}

func TestPreviewProbe(t *testing.T) {
	e, seen := previewEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.Redirect(w, r, "/home", http.StatusFound)
		case "/home":
			_, _ = w.Write([]byte(previewPage))
		}
	}))
	e.s.probePreview(e.ctx, "hello")
	assert.True(t, e.preview("hello"), "同主机重定向跟随后找到标签")
	require.Len(t, *seen, 2)
	r := (*seen)[0]
	assert.Equal(t, "hello.apps.oryn.test", r.Host)
	assert.Equal(t, "owner=no", r.Header.Get("X-Oryn-Authenticated"))
	assert.Empty(t, r.Header.Get("Cookie"))
	// 公开地址是 https：流上是明文 HTTP，但带公开的 X-Forwarded-Proto / Host（与 portal 代理一致）
	assert.Equal(t, "https", r.Header.Get("X-Forwarded-Proto"))
	assert.Equal(t, "hello.apps.oryn.test", r.Header.Get("X-Forwarded-Host"))
}

// HTTPS 部署里常见的绝对地址重定向（https://<app 主机>/...）是同源的，要跟随。
func TestPreviewProbeAbsoluteHTTPSRedirect(t *testing.T) {
	e, seen := previewEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "https://hello.apps.oryn.test/login", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(previewPage))
	}))
	e.s.probePreview(e.ctx, "hello")
	assert.True(t, e.preview("hello"))
	require.Len(t, *seen, 2)
	assert.Equal(t, "/login", (*seen)[1].URL.Path)
	assert.Equal(t, "hello.apps.oryn.test", (*seen)[1].Host)
}

// 降级到 http 的同主机重定向不是同源（公开 scheme 是 https），不跟随。
func TestPreviewProbeRejectsSchemeDowngrade(t *testing.T) {
	e, seen := previewEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "http://hello.apps.oryn.test/login", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(previewPage))
	}))
	e.s.probePreview(e.ctx, "hello")
	assert.False(t, e.preview("hello"))
	assert.Len(t, *seen, 1)
}

func TestPreviewProbeIgnoresOtherHosts(t *testing.T) {
	e, seen := previewEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "http://evil.example/", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(previewPage))
	}))
	_, err := e.st.DB.ExecContext(e.ctx, `UPDATE box_apps SET preview = 1`)
	require.NoError(t, err)
	e.s.probePreview(e.ctx, "hello")
	assert.False(t, e.preview("hello"), "跨主机的重定向不跟随，探测记为没有预览")
	assert.Len(t, *seen, 1)
}

func TestPreviewProbeAfterRelease(t *testing.T) {
	e, _ := previewEnv(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(previewPage))
	}))
	b, err := e.st.Q.GetBoxByName(e.ctx, "b1")
	require.NoError(t, err)
	// released 是部署成功与回滚成功之后的钩子：后台探测并写回 preview 列
	e.s.released(b.ID, "hello")
	require.Eventually(t, func() bool { return e.preview("hello") }, 5*time.Second, 20*time.Millisecond)
}
