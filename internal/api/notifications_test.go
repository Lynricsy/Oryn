package api_test

import (
	"bytes"
	"context"
	"encoding/json"
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
	// 服务器未配置 FCM：409 fcm_unavailable
	st, body = e.do("POST", "/api/v1/push/subscriptions", map[string]any{"kind": "fcm", "token": "reg-token"})
	assert.Equal(t, 409, st)
	assert.Equal(t, "fcm_unavailable", errCode(body))

	st, body = e.do("GET", "/api/v1/push/subscriptions", nil)
	require.Equal(t, 200, st)
	assert.Contains(t, string(body), sub.ID)
	st, _ = e.do("DELETE", "/api/v1/push/subscriptions/"+sub.ID, nil)
	assert.Equal(t, 204, st)
}
