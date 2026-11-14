package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/notify"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/webpush/webpushtest"
)

type nopBroadcaster struct{}

func (nopBroadcaster) Notification(apigen.Notification) {}
func (nopBroadcaster) NotificationsRead(string)         {}

type notifyEnv struct {
	t     *testing.T
	srv   *httptest.Server
	st    *store.Store
	token string
	devID string
}

func newNotifyEnv(t *testing.T) *notifyEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.LoadOrCreate(dir)
	require.NoError(t, err)
	svc, err := notify.New(notify.Config{PublicURL: h2PublicURL, DataDir: dir, Box: box}, st, nil, nopBroadcaster{})
	require.NoError(t, err)
	cfg := config.DefaultOrynd()
	cfg.PublicURL, cfg.DataDir = h2PublicURL, dir
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st, AuthMiddleware: auth.Middleware(st)}}
	s.Notify = &api.NotifyDeps{Service: svc}
	srv := httptest.NewServer(api.NewRouter(s))
	t.Cleanup(srv.Close)
	dev, tok, err := auth.CreateDevice(ctx, st, "t", "cli", "")
	require.NoError(t, err)
	return &notifyEnv{t: t, srv: srv, st: st, token: tok, devID: dev.ID}
}

func (e *notifyEnv) do(method, path string, body any) (int, []byte) {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, r)
	require.NoError(e.t, err)
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("User-Agent", "notify-test/1")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func errCode(b []byte) string {
	var e struct{ Code string }
	_ = json.Unmarshal(b, &e)
	return e.Code
}

func TestNotificationsAPI(t *testing.T) {
	e := newNotifyEnv(t)

	st, body := e.do("GET", "/api/v1/notifications", nil)
	require.Equal(t, 200, st, string(body))
	assert.JSONEq(t, `{"notifications":[],"unread":0}`, string(body))
	st, _ = e.do("GET", "/api/v1/notifications?limit=101", nil)
	assert.Equal(t, 400, st)
	st, _ = e.do("POST", "/api/v1/notifications/read", nil)
	assert.Equal(t, 204, st, "省略请求体 = 全部已读")
	st, _ = e.do("POST", "/api/v1/notifications/read", map[string]string{"threadId": "T-x"})
	assert.Equal(t, 204, st)

	// 渠道：201、机密只写不读、错误码映射
	st, body = e.do("POST", "/api/v1/notification-channels", map[string]any{"kind": "webhook", "name": "w",
		"webhook": map[string]any{"url": "https://hooks.example.com/path/T0K3N-abcdef", "secret": "hmac-secret-value"}})
	require.Equal(t, 201, st, string(body))
	assert.NotContains(t, string(body), "hmac-secret-value")
	assert.NotContains(t, string(body), "T0K3N-abcdef")
	var ch apigen.NotificationChannel
	require.NoError(t, json.Unmarshal(body, &ch))
	assert.Equal(t, []apigen.NotificationKind{"done", "input", "error"}, ch.Events)
	assert.Equal(t, apigen.NotificationLocaleZhCN, ch.Locale)

	st, body = e.do("POST", "/api/v1/notification-channels", map[string]any{"kind": "webhook", "name": "w", "webhook": map[string]any{"url": "nope"}})
	assert.Equal(t, 400, st)
	assert.Equal(t, "invalid_channel", errCode(body))

	st, body = e.do("PATCH", "/api/v1/notification-channels/"+ch.ID, map[string]any{"name": "renamed", "events": []string{"error"}})
	require.Equal(t, 200, st, string(body))
	require.NoError(t, json.Unmarshal(body, &ch))
	assert.Equal(t, "renamed", ch.Name)
	assert.Equal(t, []apigen.NotificationKind{"error"}, ch.Events)

	st, body = e.do("GET", "/api/v1/notification-channels", nil)
	require.Equal(t, 200, st)
	assert.NotContains(t, string(body), "hmac-secret-value")

	st, body = e.do("POST", "/api/v1/notification-channels/"+ch.ID+"/telegram/link", nil)
	assert.Equal(t, 400, st)
	assert.Equal(t, "invalid_channel", errCode(body))

	st, _ = e.do("DELETE", "/api/v1/notification-channels/"+ch.ID, nil)
	assert.Equal(t, 204, st)
	st, body = e.do("DELETE", "/api/v1/notification-channels/"+ch.ID, nil)
	assert.Equal(t, 404, st)
	assert.Equal(t, "not_found", errCode(body))

	// 推送订阅：记下设备主体与 User-Agent
	st, body = e.do("GET", "/api/v1/push/vapid-key", nil)
	require.Equal(t, 200, st)
	var vk apigen.VapidKey
	require.NoError(t, json.Unmarshal(body, &vk))
	assert.Len(t, vk.PublicKey, 87)

	c, err := webpushtest.NewClient("https://push.example/sub/1")
	require.NoError(t, err)
	in := map[string]any{"endpoint": c.Sub.Endpoint, "keys": map[string]string{"p256dh": c.Sub.P256dh, "auth": c.Sub.Auth}}
	st, body = e.do("POST", "/api/v1/push/subscriptions", in)
	require.Equal(t, 201, st, string(body))
	var sub apigen.PushSubscription
	require.NoError(t, json.Unmarshal(body, &sub))
	assert.Equal(t, "notify-test/1", sub.UserAgent)
	row, err := e.st.Q.GetPushSubscriptionByEndpoint(context.Background(), c.Sub.Endpoint)
	require.NoError(t, err)
	require.NotNil(t, row.DeviceID)
	assert.Equal(t, e.devID, *row.DeviceID)
	assert.Nil(t, row.WebSessionHash)

	st, body = e.do("POST", "/api/v1/push/subscriptions", map[string]any{"endpoint": "http://push.example/x", "keys": in["keys"]})
	assert.Equal(t, 400, st)
	assert.Equal(t, "push_invalid", errCode(body))
	// kind fcm 不带 keys（旧版 App 的形态）：400 push_invalid，先于 FCM 是否配置的检查
	st, body = e.do("POST", "/api/v1/push/subscriptions", map[string]any{"kind": "fcm", "token": "reg-token"})
	assert.Equal(t, 400, st)
	assert.Equal(t, "push_invalid", errCode(body))
	assert.Contains(t, string(body), "kind fcm requires keys")
	// 服务器未配置 FCM：409 fcm_unavailable
	st, body = e.do("POST", "/api/v1/push/subscriptions", map[string]any{"kind": "fcm", "token": "reg-token", "keys": in["keys"]})
	assert.Equal(t, 409, st)
	assert.Equal(t, "fcm_unavailable", errCode(body))

	st, body = e.do("GET", "/api/v1/push/subscriptions", nil)
	require.Equal(t, 200, st)
	assert.Contains(t, string(body), sub.ID)
	st, _ = e.do("DELETE", "/api/v1/push/subscriptions/"+sub.ID, nil)
	assert.Equal(t, 204, st)
}

// App 补拉被 FCM 丢弃的推送：最近 50 条都已读时，较早仍待回答的 input 不能被挤出窗口。
// 不带 unread 的一页只有已读通知；unread=true 必须返回它，按 before 翻页也不能跳过它。
func TestListNotificationsUnreadOnly(t *testing.T) {
	e := newNotifyEnv(t)
	ctx := context.Background()
	for i := range 50 {
		_, err := e.st.Q.InsertNotification(ctx, db.InsertNotificationParams{
			ID: fmt.Sprintf("N-done-%02d", i), Kind: "done", Title: "t", CreatedAt: int64(1000 + i),
		})
		require.NoError(t, err)
	}
	_, err := e.st.Q.MarkAllNotificationsRead(ctx, 2000)
	require.NoError(t, err)
	_, err = e.st.Q.InsertNotification(ctx, db.InsertNotificationParams{
		ID: "N-input-old", Kind: "input", Title: "t", CreatedAt: 500,
	})
	require.NoError(t, err)

	list := func(q string) apigen.NotificationList {
		st, body := e.do("GET", "/api/v1/notifications?"+q, nil)
		require.Equal(t, 200, st, string(body))
		var l apigen.NotificationList
		require.NoError(t, json.Unmarshal(body, &l))
		return l
	}
	all := list("limit=50")
	require.Len(t, all.Notifications, 50)
	for _, n := range all.Notifications {
		assert.NotNil(t, n.ReadAt, "不带 unread 的第一页全是已读：%s", n.ID)
	}

	un := list("limit=50&unread=true")
	require.Len(t, un.Notifications, 1)
	assert.Equal(t, "N-input-old", un.Notifications[0].ID)
	assert.Equal(t, apigen.NotificationKindInput, un.Notifications[0].Kind)
	assert.Equal(t, 1, un.Unread)

	page := list("limit=1&unread=true&before=1000")
	require.Len(t, page.Notifications, 1, "按 before 翻页仍能拿到较早的未读")
	assert.Equal(t, "N-input-old", page.Notifications[0].ID)
	assert.Empty(t, list("limit=50&unread=true&before=500").Notifications)
}
