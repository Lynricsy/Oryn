package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/webpush/webpushtest"
)

// hook 是可编程的假 HTTP 接收端：记录请求，按 respond 回应。
type hook struct {
	srv *httptest.Server

	mu      sync.Mutex
	reqs    []hookReq
	status  int
	header  http.Header
	body    string
	respond func(w http.ResponseWriter, r *http.Request, body []byte) bool
}

type hookReq struct {
	Method, Path string
	Header       http.Header
	Body         []byte
}

func newHook(t *testing.T) *hook {
	h := &hook{status: http.StatusOK}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.reqs = append(h.reqs, hookReq{r.Method, r.URL.Path, r.Header.Clone(), b})
		status, hdr, body, fn := h.status, h.header, h.body, h.respond
		h.mu.Unlock()
		if fn != nil && fn(w, r, b) {
			return
		}
		for k, v := range hdr {
			w.Header()[k] = v
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hook) set(status int, body string, hdr http.Header) {
	h.mu.Lock()
	h.status, h.body, h.header = status, body, hdr
	h.mu.Unlock()
}

func (h *hook) requests() []hookReq {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]hookReq(nil), h.reqs...)
}

func (f *fixture) webhookChannel(url, secret string, events ...apigen.NotificationKind) apigen.NotificationChannel {
	f.t.Helper()
	cfg := &apigen.WebhookChannelConfig{URL: url, Headers: map[string]string{"X-Custom": "yes"}}
	if secret != "" {
		cfg.Secret = &secret
	}
	ch, err := f.s.CreateChannel(f.ctx, apigen.NotificationChannelInput{Kind: apigen.NotificationChannelKindWebhook, Name: "hook", Webhook: cfg, Events: events})
	require.NoError(f.t, err)
	return ch
}

// onlyDelivery 生成一条 done 通知并返回其唯一的投递。
func (f *fixture) onlyDelivery(threadID string) db.NotificationDelivery {
	f.t.Helper()
	f.done(threadID)
	ns := f.notifications()
	require.NotEmpty(f.t, ns)
	ds := f.deliveries(ns[0].ID)
	require.Len(f.t, ds, 1)
	return ds[0]
}

func TestDeliverySuccess(t *testing.T) {
	f := newFixture(t)
	h := newHook(t)
	ch := f.webhookChannel(h.srv.URL+"/in", "s3cret")
	id := f.thread("修复登录")
	d := f.onlyDelivery(id)
	assert.Equal(t, statusPending, d.Status)
	require.Len(t, f.out.Notes()[0].Deliveries, 1, "广播的通知带待投递")

	assert.Equal(t, 1, f.s.DeliverDue(f.ctx))
	d = f.delivery(d.ID)
	assert.Equal(t, statusSent, d.Status)
	assert.EqualValues(t, 1, d.Attempts)
	require.Len(t, h.requests(), 1)

	list, err := f.s.List(f.ctx, 10, 0, false)
	require.NoError(t, err)
	require.Len(t, list.Notifications, 1)
	got := list.Notifications[0].Deliveries
	require.Len(t, got, 1)
	assert.Equal(t, apigen.NotificationDeliveryStatusSent, got[0].Status)
	assert.Equal(t, "hook", *got[0].ChannelName)
	assert.Equal(t, ch.ID, *got[0].ChannelID)
	assert.Equal(t, 0, f.s.DeliverDue(f.ctx), "已送达的不再投递")
}

func TestDeliveryRetrySchedule(t *testing.T) {
	f := newFixture(t)
	h := newHook(t)
	h.set(http.StatusInternalServerError, "boom", nil)
	ch := f.webhookChannel(h.srv.URL, "")
	d := f.onlyDelivery(f.thread("t"))

	for i, wait := range []time.Duration{10 * time.Second, 60 * time.Second, 5 * time.Minute} {
		before := f.clock.Now().UnixMilli()
		require.Equal(t, 1, f.s.DeliverDue(f.ctx))
		d = f.delivery(d.ID)
		assert.Equal(t, statusPending, d.Status, "第 %d 次失败后重排", i+1)
		assert.EqualValues(t, i+1, d.Attempts)
		require.NotNil(t, d.NextAttemptAt)
		assert.Equal(t, before+wait.Milliseconds(), *d.NextAttemptAt)
		assert.Contains(t, *d.LastError, "500")
		assert.Equal(t, 0, f.s.DeliverDue(f.ctx), "未到点不投递")
		f.clock.Advance(wait)
	}
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
	d = f.delivery(d.ID)
	assert.Equal(t, statusFailed, d.Status, "第 4 次尝试后放弃")
	assert.EqualValues(t, 4, d.Attempts)
	assert.Len(t, h.requests(), 4)
	row, err := f.st.Q.GetNotificationChannel(f.ctx, ch.ID)
	require.NoError(t, err)
	require.NotNil(t, row.LastError)
	assert.Contains(t, *row.LastError, "500")

	// 之后成功一次即清空渠道的 last_error
	h.set(http.StatusOK, "", nil)
	f.onlyDeliveryAgain(t)
	row, err = f.st.Q.GetNotificationChannel(f.ctx, ch.ID)
	require.NoError(t, err)
	assert.Nil(t, row.LastError)
}

func (f *fixture) onlyDeliveryAgain(t *testing.T) {
	f.done(f.thread("again"))
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
}

func TestDeliveryNonRetryableAndRetryAfter(t *testing.T) {
	f := newFixture(t)
	h := newHook(t)
	h.set(http.StatusBadRequest, "bad", nil)
	f.webhookChannel(h.srv.URL, "")
	d := f.onlyDelivery(f.thread("t"))
	f.s.DeliverDue(f.ctx)
	d = f.delivery(d.ID)
	assert.Equal(t, statusFailed, d.Status, "4xx 不重试")
	assert.EqualValues(t, 1, d.Attempts)

	g := newFixture(t)
	h2 := newHook(t)
	h2.set(http.StatusTooManyRequests, "slow down", http.Header{"Retry-After": {"120"}})
	g.webhookChannel(h2.srv.URL, "")
	d = g.onlyDelivery(g.thread("t"))
	now := g.clock.Now().UnixMilli()
	g.s.DeliverDue(g.ctx)
	d = g.delivery(d.ID)
	assert.Equal(t, statusPending, d.Status)
	assert.Equal(t, now+120_000, *d.NextAttemptAt, "Retry-After 大于退避时采信它")
}

func TestDeliveryExpired(t *testing.T) {
	f := newFixture(t)
	h := newHook(t)
	f.webhookChannel(h.srv.URL, "")
	d := f.onlyDelivery(f.thread("t"))
	f.clock.Advance(61 * time.Minute)
	f.s.DeliverDue(f.ctx)
	d = f.delivery(d.ID)
	assert.Equal(t, statusFailed, d.Status)
	assert.Equal(t, "expired", *d.LastError)
	assert.Empty(t, h.requests())
}

func TestDeliveryChannelFilters(t *testing.T) {
	f := newFixture(t)
	h := newHook(t)
	f.webhookChannel(h.srv.URL, "", apigen.NotificationKindError)
	off, err := f.s.CreateChannel(f.ctx, apigen.NotificationChannelInput{Kind: apigen.NotificationChannelKindNtfy, Name: "off", Enabled: new(false), Ntfy: &apigen.NtfyChannelConfig{Topic: "oryn-topic-123456"}})
	require.NoError(t, err)
	assert.False(t, off.Enabled)

	id := f.thread("t")
	f.done(id)
	ns := f.notifications()
	require.Len(t, ns, 1)
	assert.Empty(t, f.deliveries(ns[0].ID), "events 不含 done、停用的渠道 → 不生成投递")

	f.s.StateChanged(threads.StateEvent{ThreadID: id, Prev: threads.StateWorking, State: threads.StateError})
	f.flush()
	ns = f.notifications()
	require.Len(t, ns, 2)
	ds := f.deliveries(ns[0].ID)
	require.Len(t, ds, 1)
	assert.Equal(t, "webhook", ds[0].Target)

	// 生成之后才停用：投递时放弃
	_, err = f.s.UpdateChannel(f.ctx, *ds[0].ChannelID, apigen.NotificationChannelPatch{Enabled: new(false)})
	require.NoError(t, err)
	f.s.DeliverDue(f.ctx)
	assert.Equal(t, statusFailed, f.delivery(ds[0].ID).Status)
	assert.Empty(t, h.requests())
}

func TestWebhookRequest(t *testing.T) {
	f := newFixture(t)
	h := newHook(t)
	f.webhookChannel(h.srv.URL+"/hooks/abc", "s3cret")
	id := f.thread("修复登录")
	f.done(id)
	f.s.DeliverDue(f.ctx)
	reqs := h.requests()
	require.Len(t, reqs, 1)
	r := reqs[0]
	assert.Equal(t, http.MethodPost, r.Method)
	assert.Equal(t, "/hooks/abc", r.Path)
	assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
	assert.True(t, strings.HasPrefix(r.Header.Get("User-Agent"), "Oryn/"))
	assert.Equal(t, "thread.done", r.Header.Get("X-Oryn-Event"))
	assert.Equal(t, "yes", r.Header.Get("X-Custom"))
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(r.Body)
	assert.Equal(t, "sha256="+hex.EncodeToString(mac.Sum(nil)), r.Header.Get("X-Oryn-Signature"))
	var body struct {
		Event, ID, Title, Body string
		TS                     int64
		Thread                 struct{ ID, Title, State, URL string }
	}
	require.NoError(t, json.Unmarshal(r.Body, &body))
	assert.Equal(t, "thread.done", body.Event)
	assert.Equal(t, f.notifications()[0].ID, body.ID)
	assert.Equal(t, "修复登录", body.Title)
	assert.Equal(t, id, body.Thread.ID)
	assert.Equal(t, "idle", body.Thread.State)
	assert.Equal(t, "https://oryn.test/t/"+id, body.Thread.URL)
	assert.Equal(t, f.clock.Now().UnixMilli(), body.TS)
}

func TestNtfyAndBarkRequests(t *testing.T) {
	f := newFixture(t)
	ntfy, bark := newHook(t), newHook(t)
	bark.set(http.StatusOK, `{"code":200,"message":"success"}`, nil)
	_, err := f.s.CreateChannel(f.ctx, apigen.NotificationChannelInput{Kind: apigen.NotificationChannelKindNtfy, Name: "n",
		Ntfy: &apigen.NtfyChannelConfig{Server: new(ntfy.srv.URL), Topic: "oryn-topic-123456", Token: new("tk_secret")}})
	require.NoError(t, err)
	bch, err := f.s.CreateChannel(f.ctx, apigen.NotificationChannelInput{Kind: apigen.NotificationChannelKindBark, Name: "b", Locale: new(apigen.NotificationLocaleEn),
		Bark: &apigen.BarkChannelConfig{Server: new(bark.srv.URL + "/"), DeviceKey: "devkey-secret"}})
	require.NoError(t, err)

	id := f.thread("")
	f.s.InputRequested(threads.InputEvent{ThreadID: id, RequestID: "r1", Frame: uiFrame("r1")})
	f.flush()
	require.Equal(t, 2, f.s.DeliverDue(f.ctx))

	nr := ntfy.requests()
	require.Len(t, nr, 1)
	assert.Equal(t, "/", nr[0].Path)
	assert.Equal(t, "Bearer tk_secret", nr[0].Header.Get("Authorization"))
	var nb map[string]any
	require.NoError(t, json.Unmarshal(nr[0].Body, &nb))
	assert.Equal(t, map[string]any{"topic": "oryn-topic-123456", "title": "未命名线程", "message": "选 A 还是 B？", "click": "https://oryn.test/t/" + id,
		"tags": []any{"question"}, "priority": float64(4)}, nb)

	br := bark.requests()
	require.Len(t, br, 1)
	assert.Equal(t, "/push", br[0].Path)
	var bb map[string]any
	require.NoError(t, json.Unmarshal(br[0].Body, &bb))
	assert.Equal(t, map[string]any{"device_key": "devkey-secret", "title": "Untitled thread", "body": "选 A 还是 B？", "url": "https://oryn.test/t/" + id,
		"group": "Oryn", "level": "timeSensitive"}, bb)

	// Bark：HTTP 200 但 code != 200 → 失败
	bark.set(http.StatusOK, `{"code":400,"message":"failed to get device token"}`, nil)
	res, err := f.s.TestChannel(f.ctx, bch.ID)
	require.NoError(t, err)
	assert.False(t, res.Ok)
	assert.Contains(t, *res.Error, "failed to get device token")
	got, err := f.s.GetChannel(f.ctx, bch.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LastError)
	bark.set(http.StatusOK, `{"code":200}`, nil)
	res, err = f.s.TestChannel(f.ctx, bch.ID)
	require.NoError(t, err)
	assert.True(t, res.Ok)
	got, _ = f.s.GetChannel(f.ctx, bch.ID)
	assert.Nil(t, got.LastError)
	assert.Len(t, f.notifications(), 1, "测试不写 notifications 表")
}

func TestChannelValidation(t *testing.T) {
	f := newFixture(t)
	bad := []apigen.NotificationChannelInput{
		{Kind: "email", Name: "x"},
		{Kind: apigen.NotificationChannelKindWebhook, Name: "x"},
		{Kind: apigen.NotificationChannelKindWebhook, Name: "", Webhook: &apigen.WebhookChannelConfig{URL: "https://h.example"}},
		{Kind: apigen.NotificationChannelKindWebhook, Name: "x", Webhook: &apigen.WebhookChannelConfig{URL: "ftp://h.example"}},
		{Kind: apigen.NotificationChannelKindWebhook, Name: "x", Webhook: &apigen.WebhookChannelConfig{URL: "https://h.example", Headers: map[string]string{"host": "a"}}},
		{Kind: apigen.NotificationChannelKindWebhook, Name: "x", Webhook: &apigen.WebhookChannelConfig{URL: "https://h.example", Headers: map[string]string{"Content-Type": "a"}}},
		{Kind: apigen.NotificationChannelKindWebhook, Name: "x", Webhook: &apigen.WebhookChannelConfig{URL: "https://h.example"}, Ntfy: &apigen.NtfyChannelConfig{Topic: "t"}},
		{Kind: apigen.NotificationChannelKindNtfy, Name: "x", Ntfy: &apigen.NtfyChannelConfig{Topic: ""}},
		{Kind: apigen.NotificationChannelKindNtfy, Name: "x", Ntfy: &apigen.NtfyChannelConfig{Topic: "t", Server: new("javascript:alert(1)")}},
		{Kind: apigen.NotificationChannelKindBark, Name: "x", Bark: &apigen.BarkChannelConfig{}},
		{Kind: apigen.NotificationChannelKindWebhook, Name: "x", Webhook: &apigen.WebhookChannelConfig{URL: "https://h.example"}, Events: []apigen.NotificationKind{"later"}},
	}
	many := map[string]string{}
	for i := range 17 {
		many["X-H"+string(rune('a'+i))] = "v"
	}
	bad = append(bad, apigen.NotificationChannelInput{Kind: apigen.NotificationChannelKindWebhook, Name: "x", Webhook: &apigen.WebhookChannelConfig{URL: "https://h.example", Headers: many}})
	for i, in := range bad {
		_, err := f.s.CreateChannel(f.ctx, in)
		var e *Error
		require.ErrorAs(t, err, &e, "case %d", i)
		assert.Equal(t, "invalid_channel", e.Code, "case %d", i)
		assert.Equal(t, http.StatusBadRequest, e.Status)
	}
	chs, err := f.s.ListChannels(f.ctx)
	require.NoError(t, err)
	assert.Empty(t, chs)
}

// 配置加密：库里的 config_enc 与接口响应都搜不到机密全文。
func TestChannelSecretsNeverLeak(t *testing.T) {
	f := newFixture(t)
	bot := newTestBot(t)
	secrets := []string{"hook-secret-value", "/hooks/T0K3N-in-path-xyz", "ntfy-topic-secret-9f3c", "tk_ntfy_token", "bark-device-key-123", bot.Token}
	_, err := f.s.CreateChannel(f.ctx, apigen.NotificationChannelInput{Kind: apigen.NotificationChannelKindWebhook, Name: "w",
		Webhook: &apigen.WebhookChannelConfig{URL: "https://hooks.example.com/hooks/T0K3N-in-path-xyz", Secret: new("hook-secret-value")}})
	require.NoError(t, err)
	_, err = f.s.CreateChannel(f.ctx, apigen.NotificationChannelInput{Kind: apigen.NotificationChannelKindNtfy, Name: "n",
		Ntfy: &apigen.NtfyChannelConfig{Topic: "ntfy-topic-secret-9f3c", Token: new("tk_ntfy_token")}})
	require.NoError(t, err)
	_, err = f.s.CreateChannel(f.ctx, apigen.NotificationChannelInput{Kind: apigen.NotificationChannelKindBark, Name: "b",
		Bark: &apigen.BarkChannelConfig{DeviceKey: "bark-device-key-123"}})
	require.NoError(t, err)
	_, err = f.s.CreateChannel(f.ctx, apigen.NotificationChannelInput{Kind: apigen.NotificationChannelKindTelegram, Name: "tg",
		Telegram: &apigen.TelegramChannelConfig{BotToken: bot.Token, APIBase: new(bot.URL())}})
	require.NoError(t, err)

	chs, err := f.s.ListChannels(f.ctx)
	require.NoError(t, err)
	out, _ := json.Marshal(chs)
	for _, sec := range secrets {
		assert.NotContains(t, string(out), sec)
	}
	summaries := map[string]string{}
	for _, c := range chs {
		summaries[c.Name] = c.Summary
	}
	assert.Equal(t, map[string]string{"w": "hooks.example.com/…-xyz", "n": "ntfy.sh/…9f3c", "b": "api.day.app", "tg": "@oryn_test_bot"}, summaries)

	rows, err := f.st.Q.ListNotificationChannels(f.ctx)
	require.NoError(t, err)
	for _, r := range rows {
		for _, sec := range secrets {
			assert.NotContains(t, string(r.ConfigEnc), sec)
			assert.NotContains(t, r.State, sec)
		}
	}
}

func TestWebPushSender(t *testing.T) {
	f := newFixture(t)
	type got struct {
		h    http.Header
		body []byte
	}
	reqs := make(chan got, 4)
	var status = http.StatusCreated
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		reqs <- got{r.Header.Clone(), b}
		mu.Lock()
		defer mu.Unlock()
		w.WriteHeader(status)
	}))
	defer srv.Close()
	c, err := webpushtest.NewClient(srv.URL + "/push/abc")
	require.NoError(t, err)
	sub, err := f.s.CreatePushSubscription(f.ctx, apigen.PushSubscriptionInput{Endpoint: new(c.Sub.Endpoint), Keys: &apigen.PushSubscriptionKeys{P256Dh: c.Sub.P256dh, Auth: c.Sub.Auth}}, Owner{}, "Mozilla/5.0 Test")
	require.NoError(t, err)
	assert.Equal(t, apigen.PushSubscriptionKindWebpush, sub.Kind)
	again, err := f.s.CreatePushSubscription(f.ctx, apigen.PushSubscriptionInput{Endpoint: new(c.Sub.Endpoint), Keys: &apigen.PushSubscriptionKeys{P256Dh: c.Sub.P256dh, Auth: c.Sub.Auth}}, Owner{}, "UA2")
	require.NoError(t, err)
	assert.Equal(t, sub.ID, again.ID, "按 endpoint upsert")

	id := f.thread("推送测试")
	f.s.InputRequested(threads.InputEvent{ThreadID: id, RequestID: "r1", Frame: uiFrame("r1")})
	f.flush()
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
	g := <-reqs
	assert.Equal(t, "aes128gcm", g.h.Get("Content-Encoding"))
	assert.Equal(t, "86400", g.h.Get("TTL"))
	assert.Equal(t, "high", g.h.Get("Urgency"))
	assert.Len(t, g.h.Get("Topic"), 32)
	claims, err := webpushtest.VerifyVAPID(g.h.Get("Authorization"), f.s.VapidKey())
	require.NoError(t, err)
	assert.Equal(t, srv.URL, claims["aud"])
	assert.Equal(t, "https://oryn.test", claims["sub"])
	plain, err := c.Decrypt(g.body)
	require.NoError(t, err)
	var p map[string]any
	require.NoError(t, json.Unmarshal(plain, &p))
	n := f.notifications()[0]
	assert.Equal(t, map[string]any{"v": float64(1), "id": n.ID, "kind": "input", "title": "推送测试", "body": "选 A 还是 B？",
		"url": "/t/" + id, "tag": "oryn-" + id[len(id)-12:]}, p)
	subs, err := f.s.ListPushSubscriptions(f.ctx)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.NotNil(t, subs[0].LastSuccessAt)

	// 端点回 410 → 订阅被删除（投递随外键级联删除）
	mu.Lock()
	status = http.StatusGone
	mu.Unlock()
	f.done(id)
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
	<-reqs
	subs, err = f.s.ListPushSubscriptions(f.ctx)
	require.NoError(t, err)
	assert.Empty(t, subs)
	assert.Empty(t, f.deliveries(f.notifications()[0].ID))
}

func TestPushSubscriptionValidation(t *testing.T) {
	f := newFixture(t)
	c, err := webpushtest.NewClient("https://push.example/x")
	require.NoError(t, err)
	keys := apigen.PushSubscriptionKeys{P256Dh: c.Sub.P256dh, Auth: c.Sub.Auth}
	bad := []apigen.PushSubscriptionInput{
		{Endpoint: new("http://push.example/x"), Keys: &keys},
		{Endpoint: new("ftp://push.example/x"), Keys: &keys},
		{Endpoint: new("/relative"), Keys: &keys},
		{Endpoint: new("https://push.example/x"), Keys: &apigen.PushSubscriptionKeys{P256Dh: "AAAA", Auth: c.Sub.Auth}},
		{Endpoint: new("https://push.example/x"), Keys: &apigen.PushSubscriptionKeys{P256Dh: c.Sub.P256dh, Auth: "AAAA"}},
		{Endpoint: new("https://push.example/x")},
		{Keys: &keys},
		{Endpoint: new("https://push.example/x"), Keys: &keys, Token: new("tok")},
		{Kind: new(apigen.PushSubscriptionInputKind("apns")), Endpoint: new("https://push.example/x"), Keys: &keys},
	}
	for i, in := range bad {
		_, err := f.s.CreatePushSubscription(f.ctx, in, Owner{}, "")
		var e *Error
		require.ErrorAs(t, err, &e, "case %d", i)
		assert.Equal(t, "push_invalid", e.Code)
	}
	for _, ep := range []string{"https://push.example/x", "http://127.0.0.1:9/x", "http://localhost:9/y"} {
		_, err := f.s.CreatePushSubscription(f.ctx, apigen.PushSubscriptionInput{Endpoint: new(ep), Keys: &keys}, Owner{}, "")
		require.NoError(t, err, ep)
	}
	subs, err := f.s.ListPushSubscriptions(f.ctx)
	require.NoError(t, err)
	require.Len(t, subs, 3)
	require.NoError(t, f.s.DeletePushSubscription(f.ctx, subs[0].ID))
	assert.ErrorIs(t, f.s.DeletePushSubscription(f.ctx, subs[0].ID), ErrNotFound)
}

// 回归（B4）：设备归属的推送订阅首次投递失败排队重试，其间设备被吊销（auth.RevokeDevice 只写 revoked_at，
// 订阅与投递行不会级联删除）：重试到期时不得再向端点发送，投递以失败结束。
func TestWebPushRetrySkipsRevokedDevice(t *testing.T) {
	f := newFixture(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c, err := webpushtest.NewClient(srv.URL + "/push/dev")
	require.NoError(t, err)
	dev := ids.New("dev")
	require.NoError(t, f.st.Q.CreateDevice(f.ctx, db.CreateDeviceParams{ID: dev, Name: "phone", Kind: "mobile", Platform: "ios", TokenHash: ids.Hash(dev), CreatedAt: 1}))
	_, err = f.s.CreatePushSubscription(f.ctx, apigen.PushSubscriptionInput{Endpoint: new(c.Sub.Endpoint), Keys: &apigen.PushSubscriptionKeys{P256Dh: c.Sub.P256dh, Auth: c.Sub.Auth}}, Owner{DeviceID: &dev}, "UA")
	require.NoError(t, err)

	d := f.onlyDelivery(f.thread("私密标题"))
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
	require.EqualValues(t, 1, hits.Load(), "首次投递到达端点（503 → 重排）")
	d = f.delivery(d.ID)
	require.Equal(t, statusPending, d.Status)

	now := f.clock.Now().UnixMilli()
	n, err := f.st.Q.RevokeDevice(f.ctx, db.RevokeDeviceParams{RevokedAt: &now, ID: dev})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	f.clock.Advance(10 * time.Second)
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
	assert.EqualValues(t, 1, hits.Load(), "吊销后不再推送")
	d = f.delivery(d.ID)
	assert.Equal(t, statusFailed, d.Status)
	require.NotNil(t, d.LastError)
	assert.Equal(t, "subscription owner revoked", *d.LastError)
	assert.Equal(t, 0, f.s.DeliverDue(f.ctx))
}

// 回归（F6）：Webhook 网络错误（*url.Error）里的完整 URL 路径与查询串是凭据：落库的 last_error、渠道的 lastError /
// lastDelivery.error、通知列表里的投递错误与测试接口的回显都只保留 scheme + host。
func TestNetworkErrorRedactsChannelURL(t *testing.T) {
	f := newFixture(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	base := dead.URL
	dead.Close() // 连接被拒：请求得不到响应
	const secretPath, secretQuery = "T0001/B0002/XsecretTokenX", "sig=QsecretQ"
	ch := f.webhookChannel(base+"/services/"+secretPath+"?"+secretQuery, "")
	d := f.onlyDelivery(f.thread("t"))
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))

	check := func(what string, s *string) {
		t.Helper()
		require.NotNil(t, s, what)
		assert.NotContains(t, *s, "XsecretTokenX", what)
		assert.NotContains(t, *s, "QsecretQ", what)
		assert.Contains(t, *s, base, what+"：保留 scheme + host 便于排查")
	}
	row := f.delivery(d.ID)
	assert.Equal(t, statusPending, row.Status, "网络错误可重试")
	check("delivery.last_error", row.LastError)
	chRow, err := f.st.Q.GetNotificationChannel(f.ctx, ch.ID)
	require.NoError(t, err)
	check("channel.last_error", chRow.LastError)
	list, err := f.s.List(f.ctx, 10, 0, false)
	require.NoError(t, err)
	check("notification.deliveries[].error", list.Notifications[0].Deliveries[0].Error)
	chs, err := f.s.ListChannels(f.ctx)
	require.NoError(t, err)
	require.Len(t, chs, 1)
	check("channel.lastError", chs[0].LastError)
	require.NotNil(t, chs[0].LastDelivery)
	check("channel.lastDelivery.error", chs[0].LastDelivery.Error)
	res, err := f.s.TestChannel(f.ctx, ch.ID)
	require.NoError(t, err)
	assert.False(t, res.Ok)
	check("test.error", res.Error)
}

// redactURLError 对被包装的 *url.Error 同样生效，且保留错误链。
func TestRedactURLError(t *testing.T) {
	ue := &url.Error{Op: "Post", URL: "https://hooks.example.com/services/XsecretX?token=QsecretQ", Err: context.DeadlineExceeded}
	err := redactURLError(fmt.Errorf("send: %w", ue))
	assert.Equal(t, `send: Post "https://hooks.example.com": context deadline exceeded`, err.Error())
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	var got *url.Error
	assert.ErrorAs(t, err, &got)
	plain := errors.New("HTTP 500: boom")
	assert.Same(t, plain, redactURLError(plain), "没有 URL 的错误原样返回")
	assert.Equal(t, `Post "[redacted-url]": x`, redactURLError(&url.Error{Op: "Post", URL: "::bad", Err: errors.New("x")}).Error())
}
