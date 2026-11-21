package boxes

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
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
	var (
		mu   sync.Mutex
		seen []*http.Request
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Clone(context.Background()))
		mu.Unlock()
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

// 旧 release 的慢探测晚于新 release 的探测返回时，结果按探测时读到的 live release 丢弃，不盖掉新结果。
func TestPreviewProbeStaleReleaseDropped(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var n atomic.Int32
	e, _ := previewEnv(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			// 旧探测：卡住，放行后回一个没有预览标签的页面
			close(entered)
			<-release
			_, _ = w.Write([]byte("<html><head></head><body>old</body></html>"))
			return
		}
		_, _ = w.Write([]byte(previewPage))
	}))
	old := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	go func() {
		defer close(old)
		e.s.probePreview(e.ctx, "hello")
	}()
	// 任何提前退出（require 失败）都先放行卡住的 handler 并等旧探测结束，httptest.Server.Close 才不会挂住
	defer func() {
		unblock()
		<-old
	}()
	select {
	case <-entered:
	case <-old:
		t.Fatal("旧探测没有发出请求就结束了")
	case <-time.After(10 * time.Second):
		t.Fatal("等旧探测的请求超时")
	}

	// 新 release 上线，它的探测先完成
	a, err := e.st.Q.GetBoxAppByName(e.ctx, "hello")
	require.NoError(t, err)
	rel := ids.New("bxr")
	_, err = e.st.Q.SetBoxAppLive(e.ctx, db.SetBoxAppLiveParams{
		LiveReleaseID: &rel, Repo: a.Repo, Root: a.Root, Source: a.Source, UpdatedAt: 3, ID: a.ID,
	})
	require.NoError(t, err)
	e.s.probePreview(e.ctx, "hello")
	require.True(t, e.preview("hello"))
	checked, err := e.st.Q.GetBoxAppByName(e.ctx, "hello")
	require.NoError(t, err)
	e.clk.Add(time.Minute) // 旧探测若落库，preview_checked_at 会变

	unblock()
	<-old
	after, err := e.st.Q.GetBoxAppByName(e.ctx, "hello")
	require.NoError(t, err)
	assert.True(t, after.Preview, "旧 release 的结果不落库")
	assert.Equal(t, checked.PreviewCheckedAt, after.PreviewCheckedAt)
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
