package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
)

// M6 端到端：真实 orynd 组装下，desktop 设备令牌换一次性码 → /auth/app 兑换成 App 会话 Cookie →
// 以 Cookie 取 WS 票据并连上实时通道 → 吊销设备 → 连接被立即关闭，Cookie 失效。
func TestE2EAppSessionRevokeClosesWS(t *testing.T) {
	cfg := config.DefaultOrynd()
	cfg.DataDir = t.TempDir()
	cfg.PublicURL = "http://localhost"
	ctx := context.Background()
	srv, err := New(ctx, cfg)
	require.NoError(t, err)
	hs := serve(t, srv.Handler, "")
	t.Cleanup(func() { hs.Close(); srv.Close() })

	dev, tok, err := auth.CreateDevice(ctx, srv.Store, "laptop", "desktop", "linux")
	require.NoError(t, err)

	do := func(method, path string, hdr map[string]string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, hs.URL+path, strings.NewReader(""))
		require.NoError(t, err)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := c.Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}
	bearer := map[string]string{"Authorization": "Bearer " + tok}

	// 1. 换码
	resp := do("POST", "/api/v1/auth/app-session", bearer)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var code struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&code))

	// 2. 顶层跳转兑换（挂载在 /api/v1 之外）
	resp = do("GET", api.AppSessionPath+"?"+url.Values{"code": {code.Code}, "next": {"/t/x"}}.Encode(), nil)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "/t/x", resp.Header.Get("Location"))
	var app string
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName {
			app = c.Value
		}
	}
	require.NotEmpty(t, app)
	cookie := map[string]string{"Cookie": auth.CookieName + "=" + app, auth.CSRFHeader: "1"}

	// 3. 以 App 会话取 WS 票据并连上
	resp = do("POST", "/api/v1/ws-ticket", cookie)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var tk struct {
		Ticket string `json:"ticket"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tk))
	dialCtx, cancelDial := context.WithTimeout(ctx, 5*time.Second)
	defer cancelDial()
	ws, _, err := websocket.Dial(dialCtx, strings.Replace(hs.URL, "http", "ws", 1)+api.WSPath+"?ticket="+tk.Ticket, nil)
	require.NoError(t, err)
	defer ws.CloseNow()
	ws.SetReadLimit(64 << 20)
	// 连接已登记：服务端先推线程索引
	readCtx, cancelRead := context.WithTimeout(ctx, 5*time.Second)
	defer cancelRead()
	_, first, err := ws.Read(readCtx)
	require.NoError(t, err)
	assert.Contains(t, string(first), `"index"`)

	// 4. 吊销设备 → App 会话被删，连接被立即关闭
	resp = do("DELETE", "/api/v1/devices/"+dev.ID, bearer)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	closeCtx, cancelClose := context.WithTimeout(ctx, 5*time.Second)
	defer cancelClose()
	for {
		if _, _, err = ws.Read(closeCtx); err != nil {
			break
		}
	}
	require.NoError(t, closeCtx.Err(), "连接应被立即关闭，而不是等到超时")

	resp = do("GET", "/api/v1/me", map[string]string{"Cookie": auth.CookieName + "=" + app})
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	_, _ = io.Copy(io.Discard, resp.Body)
}
