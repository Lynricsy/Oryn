package notify

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/pushproto"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/webpush/webpushtest"
)

// testGrant 是合法形态的网关授权 id（22 个字符）。
const testGrant = "AbCdEfGhIjKlMnOpQrSt_-"

// gwReq 是假网关收到的一次请求（已用 pushproto.Verify 验过主机签名）。
type gwReq struct {
	Path   string
	Raw    string
	Body   map[string]any
	Signer pushproto.Signer
}

// gwReply 是假网关对某个路径的应答。
type gwReply struct {
	Status     int
	Code       string
	RetryAfter string
}

// fakeGateway 是官方推送网关的替身：按 K1 验签（时钟与 orynd 同一个假时钟），记录请求体，按路径回配置的应答。
type fakeGateway struct {
	t     *testing.T
	srv   *httptest.Server
	clock *fakeClock

	mu       sync.Mutex
	reqs     []gwReq
	replies  map[string]gwReply
	redirect string // 非空时一律 307 重定向到这里（不验签、不记录）
}

func newFakeGateway(t *testing.T, clock *fakeClock) *fakeGateway {
	t.Helper()
	g := &fakeGateway{t: t, clock: clock, replies: map[string]gwReply{}}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

// origin 是网关的 origin（回环 http，与 public_origin 一致）。
func (g *fakeGateway) origin() string { return g.srv.URL }

func (g *fakeGateway) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	redirect := g.redirect
	g.mu.Unlock()
	if redirect != "" {
		http.Redirect(w, r, redirect, http.StatusTemporaryRedirect)
		return
	}
	body, err := pushproto.ReadBody(r)
	if err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	signer, err := pushproto.Verify(r, g.origin(), body, g.clock.Now())
	if err != nil || signer.Kind != pushproto.SignerHost {
		g.t.Errorf("网关收到的请求未通过主机验签: %v", err)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil {
		g.t.Errorf("请求体不是 JSON: %v", err)
	}
	g.mu.Lock()
	g.reqs = append(g.reqs, gwReq{Path: r.URL.Path, Raw: string(body), Body: v, Signer: signer})
	rep, ok := g.replies[r.URL.Path]
	g.mu.Unlock()
	if !ok {
		rep.Status = http.StatusAccepted
		if r.URL.Path == pushproto.PathVerify {
			rep.Status = http.StatusNoContent
		}
	}
	if rep.RetryAfter != "" {
		w.Header().Set("Retry-After", rep.RetryAfter)
	}
	if rep.Code != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.Status)
		_ = json.NewEncoder(w).Encode(pushproto.ErrorBody{Code: rep.Code, Message: "fake"})
		return
	}
	w.WriteHeader(rep.Status)
}

func (g *fakeGateway) reply(path string, rep gwReply) {
	g.mu.Lock()
	g.replies[path] = rep
	g.mu.Unlock()
}

func (g *fakeGateway) requests(path string) []gwReq {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []gwReq
	for _, r := range g.reqs {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// useGateway 在同一个库与数据目录上换一个启用网关的服务（模拟改配置后重启）。
func (f *fixture) useGateway(origin string) {
	f.t.Helper()
	s, err := New(Config{PublicURL: "https://oryn.test", DataDir: f.dir, Box: f.box, Now: f.clock.Now, AfterFunc: f.timers.AfterFunc,
		Gateway: GatewayConfig{Origin: origin}}, f.st, f.th, f.out)
	require.NoError(f.t, err)
	f.s = s
}

// pushClient 生成接收端（App）的 P-256 密钥对与 auth secret。
func pushClient(t *testing.T) *webpushtest.Client {
	t.Helper()
	c, err := webpushtest.NewClient("gateway:")
	require.NoError(t, err)
	return c
}

// gatewayInput 构造 gateway 订阅登记；c 为 nil 时不带 keys。
func gatewayInput(origin, grant string, c *webpushtest.Client) apigen.PushSubscriptionInput {
	in := apigen.PushSubscriptionInput{Kind: new(apigen.PushSubscriptionInputKindGateway), Gateway: new(origin), Grant: new(grant)}
	if c != nil {
		in.Keys = &apigen.PushSubscriptionKeys{P256Dh: c.Sub.P256dh, Auth: c.Sub.Auth}
	}
	return in
}

func requireNotifyErr(t *testing.T, err error, status int, code string) {
	t.Helper()
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, status, e.Status)
	assert.Equal(t, code, e.Code)
}

// gatewayPlain 校验一次 /v1/push 只带 {grant, id, collapse?, withdraw?, e}，用接收端私钥解开 e 并返回明文。
func gatewayPlain(t *testing.T, c *webpushtest.Client, r gwReq) []byte {
	t.Helper()
	for k := range r.Body {
		assert.Contains(t, []string{"grant", "id", "collapse", "withdraw", "e"}, k, "推送请求体只有授权、通知 id、折叠键、撤回标记与密文")
	}
	e, ok := r.Body["e"].(string)
	require.True(t, ok)
	assert.LessOrEqual(t, len(e), pushproto.MaxCipher)
	ct, err := base64.RawURLEncoding.DecodeString(e)
	require.NoError(t, err, "e 是无填充 base64url")
	plain, err := c.Decrypt(ct)
	require.NoError(t, err, "接收端私钥能解开")
	return plain
}

// 网关关闭（push.gateway.url 为空）：网关信息 404、登记 409 gateway_disabled，不生成主机密钥，gateway 不在投递注册表里。
func TestGatewayDisabled(t *testing.T) {
	f := newFixture(t)
	_, err := f.s.GatewayInfo()
	requireNotifyErr(t, err, http.StatusNotFound, "gateway_disabled")
	_, err = f.s.CreatePushSubscription(f.ctx, gatewayInput("https://push.oryn.best", testGrant, pushClient(t)), Owner{}, "")
	requireNotifyErr(t, err, http.StatusConflict, "gateway_disabled")
	assert.Nil(t, f.s.pushSenders[kindGateway])
	assert.NotNil(t, f.s.pushSenders["webpush"])
	_, err = os.Stat(filepath.Join(f.dir, pushproto.HostKeyFile))
	assert.ErrorIs(t, err, os.ErrNotExist, "未启用网关时不生成主机密钥")

	// 启用后：启动期生成主机密钥，网关信息给出 origin 与其公钥；重启后公钥不变
	g := newFakeGateway(t, f.clock)
	f.useGateway(g.origin())
	info, err := f.s.GatewayInfo()
	require.NoError(t, err)
	assert.Equal(t, apigen.PushGateway{V: 1, Origin: g.origin(), HostKey: info.HostKey}, info)
	_, err = pushproto.ParseHostKey(info.HostKey)
	require.NoError(t, err)
	st, err := os.Stat(filepath.Join(f.dir, pushproto.HostKeyFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	assert.NotNil(t, f.s.pushSenders[kindGateway])
	f.useGateway(g.origin())
	again, err := f.s.GatewayInfo()
	require.NoError(t, err)
	assert.Equal(t, info.HostKey, again.HostKey)
}

// K3 的登记错误各一例：push_invalid、gateway_disabled（见 TestGatewayDisabled）、gateway_changed、gateway_grant_invalid、
// gateway_unreachable；通过后 endpoint = <origin>/v1/grants/<grant>。
func TestGatewaySubscriptionValidation(t *testing.T) {
	f := newFixture(t)
	g := newFakeGateway(t, f.clock)
	f.useGateway(g.origin())
	c := pushClient(t)
	keys := &apigen.PushSubscriptionKeys{P256Dh: c.Sub.P256dh, Auth: c.Sub.Auth}
	kind := new(apigen.PushSubscriptionInputKindGateway)

	// 形态错误 → push_invalid（不联系网关）；已删除的 kind fcm 同样拒绝
	bad := []apigen.PushSubscriptionInput{
		{Kind: new(apigen.PushSubscriptionInputKind("fcm")), Keys: keys, Endpoint: new("tok")},
		{Kind: kind, Grant: new(testGrant), Keys: keys},
		{Kind: kind, Gateway: new(""), Grant: new(testGrant), Keys: keys},
		{Kind: kind, Gateway: new(strings.Repeat("a", 256)), Grant: new(testGrant), Keys: keys},
		{Kind: kind, Gateway: new(g.origin()), Keys: keys},
		gatewayInput(g.origin(), "short", c),
		gatewayInput(g.origin(), testGrant+"x", c),
		gatewayInput(g.origin(), "AbCdEfGhIjKlMnOpQrSt/=", c),
		gatewayInput(g.origin(), testGrant, nil),
		{Kind: kind, Gateway: new(g.origin()), Grant: new(testGrant), Keys: keys, Endpoint: new("https://push.example/x")},
		{Kind: kind, Gateway: new(g.origin()), Grant: new(testGrant), Keys: &apigen.PushSubscriptionKeys{P256Dh: "AAAA", Auth: c.Sub.Auth}},
		{Kind: kind, Gateway: new(g.origin()), Grant: new(testGrant), Keys: &apigen.PushSubscriptionKeys{P256Dh: c.Sub.P256dh, Auth: "AAAA"}},
		// webpush 形态不得带 gateway / grant
		{Endpoint: new("https://push.example/x"), Keys: keys, Grant: new(testGrant)},
	}
	for i, in := range bad {
		_, err := f.s.CreatePushSubscription(f.ctx, in, Owner{}, "")
		requireNotifyErr(t, err, http.StatusBadRequest, "push_invalid")
		assert.Empty(t, g.requests(pushproto.PathVerify), "case %d", i)
	}

	// gateway 不等于当前 origin → 409 gateway_changed（不联系任何网关）
	_, err := f.s.CreatePushSubscription(f.ctx, gatewayInput("https://push.oryn.best", testGrant, c), Owner{}, "")
	requireNotifyErr(t, err, http.StatusConflict, "gateway_changed")
	assert.Empty(t, g.requests(pushproto.PathVerify))

	// 网关 verify 回 410 / 403 → 409 gateway_grant_invalid
	for _, rep := range []gwReply{{Status: http.StatusGone, Code: pushproto.CodeGrantRevoked}, {Status: http.StatusForbidden, Code: pushproto.CodeHostMismatch}} {
		g.reply(pushproto.PathVerify, rep)
		_, err = f.s.CreatePushSubscription(f.ctx, gatewayInput(g.origin(), testGrant, c), Owner{}, "")
		requireNotifyErr(t, err, http.StatusConflict, "gateway_grant_invalid")
		assert.ErrorContains(t, err, rep.Code)
	}
	// 5xx → 502 gateway_unreachable
	g.reply(pushproto.PathVerify, gwReply{Status: http.StatusServiceUnavailable, Code: pushproto.CodeUnavailable})
	_, err = f.s.CreatePushSubscription(f.ctx, gatewayInput(g.origin(), testGrant, c), Owner{}, "")
	requireNotifyErr(t, err, http.StatusBadGateway, "gateway_unreachable")
	// 网络错误 → 502 gateway_unreachable
	dead := httptest.NewServer(http.NotFoundHandler())
	deadOrigin := dead.URL
	dead.Close()
	f.useGateway(deadOrigin)
	_, err = f.s.CreatePushSubscription(f.ctx, gatewayInput(deadOrigin, testGrant, c), Owner{}, "")
	requireNotifyErr(t, err, http.StatusBadGateway, "gateway_unreachable")
	subs, err := f.s.ListPushSubscriptions(f.ctx)
	require.NoError(t, err)
	assert.Empty(t, subs, "失败的登记不写入订阅")

	// 合法：verify 带主机签名与授权 id；按 endpoint upsert，再次登记覆盖 keys
	f.useGateway(g.origin())
	g.reply(pushproto.PathVerify, gwReply{Status: http.StatusNoContent})
	info, err := f.s.GatewayInfo()
	require.NoError(t, err)
	sub, err := f.s.CreatePushSubscription(f.ctx, gatewayInput(g.origin(), testGrant, c), Owner{}, "OrynMobile/0.2.0 (ios)")
	require.NoError(t, err)
	assert.Equal(t, apigen.PushSubscriptionKindGateway, sub.Kind)
	assert.Equal(t, g.origin()+"/v1/grants/"+testGrant, sub.Endpoint)
	vs := g.requests(pushproto.PathVerify)
	require.NotEmpty(t, vs)
	last := vs[len(vs)-1]
	assert.Equal(t, map[string]any{"grant": testGrant}, last.Body)
	assert.Equal(t, info.HostKey, last.Signer.Key)

	c2 := pushClient(t)
	again, err := f.s.CreatePushSubscription(f.ctx, gatewayInput(g.origin(), testGrant, c2), Owner{}, "UA2")
	require.NoError(t, err)
	assert.Equal(t, sub.ID, again.ID)
	row, err := f.st.Q.GetPushSubscriptionByEndpoint(f.ctx, sub.Endpoint)
	require.NoError(t, err)
	assert.Equal(t, c2.Sub.P256dh, row.P256dh)
	assert.Equal(t, c2.Sub.Auth, row.Auth)

	// 同一 endpoint 不能以 webpush 登记来换掉网关订阅的密钥
	_, err = f.s.CreatePushSubscription(f.ctx, apigen.PushSubscriptionInput{Endpoint: new(sub.Endpoint), Keys: keys}, Owner{}, "")
	requireNotifyErr(t, err, http.StatusBadRequest, "push_invalid")
}

func TestGatewaySender(t *testing.T) {
	f := newFixture(t)
	g := newFakeGateway(t, f.clock)
	f.useGateway(g.origin())
	c := pushClient(t)
	sub, err := f.s.CreatePushSubscription(f.ctx, gatewayInput(g.origin(), testGrant, c), Owner{}, "")
	require.NoError(t, err)
	info, err := f.s.GatewayInfo()
	require.NoError(t, err)

	id := f.thread("推送测试")
	tag := "oryn-" + id[len(id)-12:]
	f.s.InputRequested(threads.InputEvent{ThreadID: id, RequestID: "r1", Frame: uiFrame("r1")})
	f.flush()
	n := f.notifications()[0]
	ds := f.deliveries(n.ID)
	require.Len(t, ds, 1)
	assert.Equal(t, kindGateway, ds[0].Target)
	want, err := pushPayloadLimit(f.s.message(f.ctx, n, ds[0]), pushproto.MaxPlaintext)
	require.NoError(t, err)
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))

	pushes := g.requests(pushproto.PathPush)
	require.Len(t, pushes, 1)
	p0 := pushes[0]
	assert.Equal(t, info.HostKey, p0.Signer.Key, "以本机主机密钥签名")
	assert.Equal(t, testGrant, p0.Body["grant"])
	assert.Equal(t, n.ID, p0.Body["id"])
	assert.Equal(t, pushproto.Collapse(testGrant, tag), p0.Body["collapse"])
	assert.NotContains(t, p0.Body, "withdraw")
	// 请求体里没有任何明文：标题、正文、线程 id（url / tag 都含它）、明文字段名都不出现
	for _, leak := range []string{"推送测试", "选 A 还是 B", `\u63a8`, id, id[len(id)-12:], `"title"`, `"body"`, `"url"`, `"tag"`, `"kind"`, `"actions"`} {
		assert.NotContains(t, p0.Raw, leak, "网关请求体泄露明文 %q", leak)
	}
	assert.Equal(t, string(want), string(gatewayPlain(t, c, p0)), "明文等于 pushPayloadLimit(m, 2800)")
	assert.Equal(t, statusSent, f.delivery(ds[0].ID).Status)
	subs, err := f.s.ListPushSubscriptions(f.ctx)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Equal(t, sub.ID, subs[0].ID)
	assert.NotNil(t, subs[0].LastSuccessAt)

	// 请求在别处作答：撤回带 withdraw、不带 collapse（不顶掉同线程尚未送达的通知）
	f.s.InputResolved(id, "r1")
	f.flush()
	pushes = g.requests(pushproto.PathPush)
	require.Len(t, pushes, 2)
	w := pushes[1]
	assert.Equal(t, true, w.Body["withdraw"])
	assert.NotContains(t, w.Body, "collapse")
	var wp map[string]any
	require.NoError(t, json.Unmarshal(gatewayPlain(t, c, w), &wp))
	assert.Equal(t, map[string]any{"v": 1.0, "kind": "withdraw", "id": n.ID, "tag": tag}, wp)

	// 最长的标题 / 正文（全部需要 JSON 转义）：明文降到 2800 字节以内，e 不超过网关上限
	long := f.thread(strings.Repeat("\u2028", 400))
	f.s.StateChanged(threads.StateEvent{ThreadID: long, Prev: threads.StateWorking, State: threads.StateError, Reason: strings.Repeat("<", 300)})
	f.flush()
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
	pushes = g.requests(pushproto.PathPush)
	require.Len(t, pushes, 3)
	plain := gatewayPlain(t, c, pushes[2])
	assert.LessOrEqual(t, len(plain), pushproto.MaxPlaintext)
	assert.Equal(t, pushproto.Collapse(testGrant, "oryn-"+long[len(long)-12:]), pushes[2].Body["collapse"])
}

// 投递结果：410 / 403 删订阅；429 按 Retry-After 重排；502 按退避重排；401 不重试。
func TestGatewaySenderResults(t *testing.T) {
	cases := []struct {
		name  string
		rep   gwReply
		gone  bool
		retry time.Duration // 0 = 不重试
	}{
		{"revoked", gwReply{Status: http.StatusGone, Code: pushproto.CodeGrantRevoked}, true, 0},
		{"unregistered", gwReply{Status: http.StatusGone, Code: pushproto.CodeDeviceUnregistered}, true, 0},
		{"host mismatch", gwReply{Status: http.StatusForbidden, Code: pushproto.CodeHostMismatch}, true, 0},
		{"rate limited", gwReply{Status: http.StatusTooManyRequests, Code: pushproto.CodeRateLimited, RetryAfter: "30"}, false, 30 * time.Second},
		{"upstream", gwReply{Status: http.StatusBadGateway, Code: pushproto.CodeUpstream}, false, 10 * time.Second},
		{"unavailable", gwReply{Status: http.StatusServiceUnavailable, Code: pushproto.CodeUnavailable, RetryAfter: "120"}, false, 120 * time.Second},
		{"clock skew", gwReply{Status: http.StatusUnauthorized, Code: pushproto.CodeClockSkew}, false, 0},
		{"too big", gwReply{Status: http.StatusRequestEntityTooLarge, Code: pushproto.CodeTooBig}, false, 0},
		{"malformed", gwReply{Status: http.StatusBadRequest, Code: pushproto.CodeMalformed}, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			g := newFakeGateway(t, f.clock)
			f.useGateway(g.origin())
			sub, err := f.s.CreatePushSubscription(f.ctx, gatewayInput(g.origin(), testGrant, pushClient(t)), Owner{}, "")
			require.NoError(t, err)
			g.reply(pushproto.PathPush, tc.rep)
			d := f.onlyDelivery(f.thread("t"))
			require.Equal(t, 1, f.s.DeliverDue(f.ctx))
			require.Len(t, g.requests(pushproto.PathPush), 1)

			subs, err := f.s.ListPushSubscriptions(f.ctx)
			require.NoError(t, err)
			if tc.gone {
				assert.Empty(t, subs, "订阅已删除")
				_, err := f.st.Q.GetNotificationDelivery(f.ctx, d.ID)
				assert.Error(t, err, "投递随订阅级联删除")
				return
			}
			require.Len(t, subs, 1)
			assert.Equal(t, sub.ID, subs[0].ID)
			require.NotNil(t, subs[0].LastError)
			assert.Contains(t, *subs[0].LastError, tc.rep.Code)
			row := f.delivery(d.ID)
			if tc.retry == 0 {
				assert.Equal(t, statusFailed, row.Status, "不重试")
				assert.EqualValues(t, 1, row.Attempts)
				return
			}
			assert.Equal(t, statusPending, row.Status)
			require.NotNil(t, row.NextAttemptAt)
			assert.Equal(t, f.s.now()+tc.retry.Milliseconds(), *row.NextAttemptAt)
		})
	}
}

// push.gateway.url 改了：旧网关的订阅在下一次投递时删除，新旧网关都不收到推送。
func TestGatewayOriginChangedDropsSubscription(t *testing.T) {
	f := newFixture(t)
	oldGW := newFakeGateway(t, f.clock)
	newGW := newFakeGateway(t, f.clock)
	f.useGateway(oldGW.origin())
	_, err := f.s.CreatePushSubscription(f.ctx, gatewayInput(oldGW.origin(), testGrant, pushClient(t)), Owner{}, "")
	require.NoError(t, err)

	f.useGateway(newGW.origin())
	d := f.onlyDelivery(f.thread("t"))
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
	assert.Empty(t, oldGW.requests(pushproto.PathPush))
	assert.Empty(t, newGW.requests(pushproto.PathPush))
	subs, err := f.s.ListPushSubscriptions(f.ctx)
	require.NoError(t, err)
	assert.Empty(t, subs)
	_, err = f.st.Q.GetNotificationDelivery(f.ctx, d.ID)
	assert.Error(t, err, "投递随订阅级联删除")
}

// 网关回重定向：不跟随（签名只覆盖原 origin 与路径），按失败处理且不重试。
func TestGatewayNoRedirect(t *testing.T) {
	f := newFixture(t)
	g := newFakeGateway(t, f.clock)
	f.useGateway(g.origin())
	_, err := f.s.CreatePushSubscription(f.ctx, gatewayInput(g.origin(), testGrant, pushClient(t)), Owner{}, "")
	require.NoError(t, err)
	var hits int
	var mu sync.Mutex
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(target.Close)
	g.mu.Lock()
	g.redirect = target.URL + "/v1/push"
	g.mu.Unlock()
	d := f.onlyDelivery(f.thread("t"))
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
	assert.Equal(t, statusFailed, f.delivery(d.ID).Status)
	mu.Lock()
	assert.Zero(t, hits)
	mu.Unlock()
}
