package portal

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/auth"
)

// M6：App 会话（desktop 设备令牌换来的网页会话 Cookie）原样走握手 B → C → D——portal 无需改动；
// 吊销该设备时 auth.RevokeDevice 删除其会话并发布 (web, 哈希)，portal 的 WebSocket 断开、Cookie 失效。
func TestAppSessionHandshake(t *testing.T) {
	e := newEnv(t)
	svc, err := auth.NewService(e.st, t.TempDir(), "https://oryn.test")
	require.NoError(t, err)
	svc.Subscribe(e.svc.Revoke)

	dev, _, err := auth.CreateDevice(e.ctx, e.st, "laptop", "desktop", "linux")
	require.NoError(t, err)
	code, _, err := svc.IssueAppCode(e.ctx, auth.Principal{Kind: auth.KindDevice, ID: dev.ID})
	require.NoError(t, err)
	app, _, ok, err := svc.RedeemAppCode(e.ctx, code, "OrynDesktop")
	require.NoError(t, err)
	require.True(t, ok)

	// 以 App 会话代替普通网页会话走完 B → C → D
	e.webCookie = app
	cookie := e.login(portA)
	ec := readEcho(t, e.get(portA, "/echo", portalCookie(cookie)...))
	assert.Equal(t, "owner=yes", ec.Header.Get(AuthenticatedHeader))

	ws, _, err := e.dialWS(portA, headers(portalCookie(cookie)...))
	require.NoError(t, err)
	defer ws.CloseNow()
	echoOnce(t, ws, "x")

	require.NoError(t, svc.RevokeDevice(e.ctx, dev.ID))
	requireClosed(t, ws)
	resp := e.get(portA, "/", append(portalCookie(cookie), "Accept", "text/html")...)
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, e.authURL(portA, "/"), resp.Header.Get("Location"), "portal 会话随 App 会话级联删除")

	// 被删除的 App 会话不能再发起握手 B（回到登录页）
	rec := e.stepB("t="+e.code+"&p=3000&next=%2F", e.webSessionCookie())
	require.Equal(t, http.StatusFound, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/login?next=")
}
