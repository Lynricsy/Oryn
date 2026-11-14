package notify

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/webpush/webpushtest"
)

const fcmSecretName = "fcm/service-account"

// fakeGoogle 是假的 OAuth 令牌端点 + FCM v1 端点：校验 JWT 断言签名与声明、记录发送请求，
// 按注册令牌返回预设的错误。
type fakeGoogle struct {
	t   *testing.T
	srv *httptest.Server
	pub *rsa.PublicKey

	mu        sync.Mutex
	tokenReqs []map[string]any // 已验证的 JWT 声明
	sends     []fcmSendReq
	issued    int
	// errFor 是注册令牌 → (HTTP 状态, 响应体)。
	errFor map[string]fcmErr
	// rejectAccess 中的 access token 一律 401。
	rejectAccess map[string]bool
}

type fcmErr struct {
	status int
	body   string
}

type fcmSendReq struct {
	path, auth string
	// raw 是请求体原文：用来断言其中没有任何明文标题 / 正文。
	raw  string
	body map[string]any
}

func newFakeGoogle(t *testing.T, pub *rsa.PublicKey) *fakeGoogle {
	g := &fakeGoogle{t: t, pub: pub, errFor: map[string]fcmErr{}, rejectAccess: map[string]bool{}}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGoogle) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case r.URL.Path == "/token":
		require.NoError(g.t, r.ParseForm())
		assert.Equal(g.t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
		assert.Equal(g.t, "urn:ietf:params:oauth:grant-type:jwt-bearer", r.PostForm.Get("grant_type"))
		claims, err := g.verify(r.PostForm.Get("assertion"))
		if err != nil {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		g.tokenReqs = append(g.tokenReqs, claims)
		g.issued++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "ya29.test-" + string(rune('0'+g.issued)), "expires_in": 3599, "token_type": "Bearer"})
	case strings.HasPrefix(r.URL.Path, "/v1/projects/"):
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		require.NoError(g.t, json.Unmarshal(b, &body))
		auth := r.Header.Get("Authorization")
		g.sends = append(g.sends, fcmSendReq{r.URL.Path, auth, string(b), body})
		if g.rejectAccess[strings.TrimPrefix(auth, "Bearer ")] {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"code":401,"status":"UNAUTHENTICATED"}}`)
			return
		}
		tok, _ := body["message"].(map[string]any)["token"].(string)
		if e, ok := g.errFor[tok]; ok {
			w.WriteHeader(e.status)
			_, _ = io.WriteString(w, e.body)
			return
		}
		_, _ = io.WriteString(w, `{"name":"projects/oryn-test/messages/0:1"}`)
	default:
		http.NotFound(w, r)
	}
}

// verify 校验 RS256 签名并返回声明。
func (g *fakeGoogle) verify(jwt string) (map[string]any, error) {
	parts := strings.Split(jwt, ".")
	require.Len(g.t, parts, 3)
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(g.t, err)
	var h map[string]string
	require.NoError(g.t, json.Unmarshal(hb, &h))
	assert.Equal(g.t, map[string]string{"alg": "RS256", "typ": "JWT", "kid": "kid-1"}, h)
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(g.t, err)
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(g.pub, crypto.SHA256, sum[:], sig); err != nil {
		return nil, err
	}
	cb, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(g.t, err)
	var c map[string]any
	require.NoError(g.t, json.Unmarshal(cb, &c))
	return c, nil
}

func (g *fakeGoogle) snapshot() (tokens []map[string]any, sends []fcmSendReq) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]map[string]any(nil), g.tokenReqs...), append([]fcmSendReq(nil), g.sends...)
}

// serviceAccountJSON 生成一份服务账号 JSON（PKCS#8 私钥，与 Google 下发的格式一致）。
func serviceAccountJSON(t *testing.T) (string, *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	b, err := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "oryn-test", "private_key_id": "kid-1",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": "fcm-sender@oryn-test.iam.gserviceaccount.com", "token_uri": "https://oauth2.googleapis.com/token",
	})
	require.NoError(t, err)
	return string(b), &key.PublicKey
}

// fcmFixture 在夹具上换一个启用 FCM 的服务；secretValue 为空时不写密钥。
func fcmFixture(t *testing.T, secretValue, mode string) (*fixture, *secrets.Vault) {
	t.Helper()
	f := newFixture(t)
	v := secrets.NewVault(f.st, f.box)
	if secretValue != "" {
		_, err := v.Put(f.ctx, fcmSecretName, mode, secretValue, nil)
		require.NoError(t, err)
	}
	return f, v
}

func (f *fixture) useFCM(v *secrets.Vault, g *fakeGoogle) {
	f.t.Helper()
	cfg := FCMConfig{Secret: fcmSecretName, Secrets: v}
	if g != nil {
		cfg.TokenURL, cfg.APIBase = g.srv.URL+"/token", g.srv.URL
	}
	s, err := New(Config{PublicURL: "https://oryn.test", DataDir: f.dir, Box: f.box, Now: f.clock.Now, AfterFunc: f.timers.AfterFunc, FCM: cfg}, f.st, f.th, f.out)
	require.NoError(f.t, err)
	f.s = s
}

// pushClient 生成接收端（App）的 P-256 密钥对与 auth secret。
func pushClient(t *testing.T) *webpushtest.Client {
	t.Helper()
	c, err := webpushtest.NewClient("fcm:")
	require.NoError(t, err)
	return c
}

// fcmInput 构造 fcm 订阅登记；c 为 nil 时不带 keys。
func fcmInput(token string, c *webpushtest.Client) apigen.PushSubscriptionInput {
	in := apigen.PushSubscriptionInput{Kind: new(apigen.PushSubscriptionInputKindFcm), Token: new(token)}
	if c != nil {
		in.Keys = &apigen.PushSubscriptionKeys{P256Dh: c.Sub.P256dh, Auth: c.Sub.Auth}
	}
	return in
}

// fcmPlain 校验一条 FCM 请求的 data 只有 {v, id, e}，用接收端私钥解开 e 并返回明文 JSON。
func fcmPlain(t *testing.T, c *webpushtest.Client, s fcmSendReq) map[string]any {
	t.Helper()
	data, ok := s.body["message"].(map[string]any)["data"].(map[string]any)
	require.True(t, ok)
	require.ElementsMatch(t, []string{"v", "id", "e"}, keysOf(data), "data 只有版本、通知 id 与密文")
	assert.Equal(t, "2", data["v"])
	ct, err := base64.RawURLEncoding.DecodeString(data["e"].(string))
	require.NoError(t, err, "e 是无填充 base64url")
	plain, err := c.Decrypt(ct)
	require.NoError(t, err, "接收端私钥能解开")
	var p map[string]any
	require.NoError(t, json.Unmarshal(plain, &p))
	assert.Equal(t, p["id"], data["id"])
	return p
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func requireNotifyErr(t *testing.T, err error, status int, code string) {
	t.Helper()
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, status, e.Status)
	assert.Equal(t, code, e.Code)
}

// 未配置 FCM：登记 fcm 订阅 409 fcm_unavailable，Web Push 不受影响，fcm 不在投递注册表里。
func TestFCMDisabled(t *testing.T) {
	f := newFixture(t)
	_, err := f.s.CreatePushSubscription(f.ctx, fcmInput("tok-1", pushClient(t)), Owner{}, "")
	requireNotifyErr(t, err, http.StatusConflict, "fcm_unavailable")
	assert.Nil(t, f.s.pushSenders["fcm"])
	assert.NotNil(t, f.s.pushSenders["webpush"])
}

func TestFCMSubscriptionValidation(t *testing.T) {
	sa, _ := serviceAccountJSON(t)

	// 已配置但密钥不存在 / 是 env 模式 / 内容无效 → fcm_unavailable
	f, v := fcmFixture(t, "", "")
	f.useFCM(v, nil)
	_, err := f.s.CreatePushSubscription(f.ctx, fcmInput("tok-1", pushClient(t)), Owner{}, "")
	requireNotifyErr(t, err, http.StatusConflict, "fcm_unavailable")

	f, v = fcmFixture(t, "", "")
	_, err = v.Put(f.ctx, "FCM_SA", secrets.ModeEnv, sa, nil)
	require.NoError(t, err)
	s, err := New(Config{PublicURL: "https://oryn.test", DataDir: f.dir, Box: f.box, Now: f.clock.Now, AfterFunc: f.timers.AfterFunc,
		FCM: FCMConfig{Secret: "FCM_SA", Secrets: v}}, f.st, f.th, f.out)
	require.NoError(t, err)
	_, err = s.CreatePushSubscription(f.ctx, fcmInput("tok-1", pushClient(t)), Owner{}, "")
	requireNotifyErr(t, err, http.StatusConflict, "fcm_unavailable")

	f, v = fcmFixture(t, `{"project_id":"p","client_email":"e","private_key":"not pem"}`, secrets.ModeProxy)
	f.useFCM(v, nil)
	_, err = f.s.CreatePushSubscription(f.ctx, fcmInput("tok-1", pushClient(t)), Owner{}, "")
	requireNotifyErr(t, err, http.StatusConflict, "fcm_unavailable")

	// 形态错误 → push_invalid；kind fcm 不带 keys（旧版 App 的登记形态）同样拒绝
	f, v = fcmFixture(t, sa, secrets.ModeProxy)
	f.useFCM(v, nil)
	c := pushClient(t)
	keys := &apigen.PushSubscriptionKeys{P256Dh: c.Sub.P256dh, Auth: c.Sub.Auth}
	bad := []apigen.PushSubscriptionInput{
		{Kind: new(apigen.PushSubscriptionInputKindFcm), Keys: keys},
		fcmInput("", c),
		fcmInput("has space", c),
		fcmInput("tok\n", c),
		fcmInput(strings.Repeat("a", fcmMaxToken+1), c),
		{Kind: new(apigen.PushSubscriptionInputKindFcm), Token: new("tok"), Endpoint: new("https://push.example/x"), Keys: keys},
		fcmInput("tok", nil),
		{Kind: new(apigen.PushSubscriptionInputKindFcm), Token: new("tok"), Keys: &apigen.PushSubscriptionKeys{}},
		{Kind: new(apigen.PushSubscriptionInputKindFcm), Token: new("tok"), Keys: &apigen.PushSubscriptionKeys{P256Dh: "AAAA", Auth: c.Sub.Auth}},
		{Kind: new(apigen.PushSubscriptionInputKindFcm), Token: new("tok"), Keys: &apigen.PushSubscriptionKeys{P256Dh: c.Sub.P256dh, Auth: "AAAA"}},
	}
	for _, in := range bad {
		_, err := f.s.CreatePushSubscription(f.ctx, in, Owner{}, "")
		requireNotifyErr(t, err, http.StatusBadRequest, "push_invalid")
	}
	_, err = f.s.CreatePushSubscription(f.ctx, fcmInput("tok", nil), Owner{}, "")
	assert.ErrorContains(t, err, "kind fcm requires keys")

	// 合法：按令牌 upsert，endpoint = 令牌；再次登记覆盖 keys
	sub, err := f.s.CreatePushSubscription(f.ctx, fcmInput("tok:APA91b-x_y", c), Owner{}, "OrynMobile/0.1.0 (android)")
	require.NoError(t, err)
	assert.Equal(t, apigen.PushSubscriptionKindFcm, sub.Kind)
	assert.Equal(t, "tok:APA91b-x_y", sub.Endpoint)
	c2 := pushClient(t)
	again, err := f.s.CreatePushSubscription(f.ctx, fcmInput("tok:APA91b-x_y", c2), Owner{}, "UA2")
	require.NoError(t, err)
	assert.Equal(t, sub.ID, again.ID)
	row, err := f.st.Q.GetPushSubscriptionByEndpoint(f.ctx, "tok:APA91b-x_y")
	require.NoError(t, err)
	assert.Equal(t, c2.Sub.P256dh, row.P256dh)
	assert.Equal(t, c2.Sub.Auth, row.Auth)
}

func TestFCMSender(t *testing.T) {
	sa, pub := serviceAccountJSON(t)
	g := newFakeGoogle(t, pub)
	f, v := fcmFixture(t, sa, secrets.ModeProxy)
	f.useFCM(v, g)
	c := pushClient(t)
	sub, err := f.s.CreatePushSubscription(f.ctx, fcmInput("reg-token-1", c), Owner{}, "")
	require.NoError(t, err)

	id := f.thread("推送测试")
	f.s.InputRequested(threads.InputEvent{ThreadID: id, RequestID: "r1", Frame: uiFrame("r1")})
	f.flush()
	n := f.notifications()[0]
	ds := f.deliveries(n.ID)
	require.Len(t, ds, 1)
	assert.Equal(t, "fcm", ds[0].Target)
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))

	tokens, sends := g.snapshot()
	require.Len(t, tokens, 1)
	claims := tokens[0]
	assert.Equal(t, "fcm-sender@oryn-test.iam.gserviceaccount.com", claims["iss"])
	assert.Equal(t, "https://www.googleapis.com/auth/firebase.messaging", claims["scope"])
	assert.Equal(t, g.srv.URL+"/token", claims["aud"])
	assert.Equal(t, float64(f.clock.Now().Unix()), claims["iat"])
	assert.Equal(t, float64(f.clock.Now().Add(time.Hour).Unix()), claims["exp"])

	require.Len(t, sends, 1)
	s0 := sends[0]
	assert.Equal(t, "/v1/projects/oryn-test/messages:send", s0.path)
	assert.Equal(t, "Bearer ya29.test-1", s0.auth)
	msg := s0.body["message"].(map[string]any)
	assert.Equal(t, "reg-token-1", msg["token"])
	assert.NotContains(t, msg, "notification", "data-only")
	// 请求体里没有任何明文：标题、正文、线程 id（url / tag 都含它）、明文字段名都不出现
	for _, leak := range []string{"推送测试", "选 A 还是 B", `\u63a8`, id, id[len(id)-12:], `"title"`, `"body"`, `"url"`, `"tag"`, `"kind"`} {
		assert.NotContains(t, s0.raw, leak, "FCM 请求体泄露明文 %q", leak)
	}
	tag := "oryn-" + id[len(id)-12:]
	assert.Equal(t, map[string]any{"v": float64(1), "id": n.ID, "kind": "input", "title": "推送测试", "body": "选 A 还是 B？",
		"url": "/t/" + id, "tag": tag}, fcmPlain(t, c, s0))
	// 不用 collapse key：每条都是非折叠消息，离线时不会被 FCM 的「最多 4 个 collapse key」规则静默淘汰
	assert.Equal(t, map[string]any{"priority": "HIGH", "ttl": "86400s"}, msg["android"])
	assert.Equal(t, statusSent, f.delivery(ds[0].ID).Status)

	// 55 分钟内复用 access token
	f.clock.Advance(54 * time.Minute)
	f.done(id)
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
	tokens, sends = g.snapshot()
	assert.Len(t, tokens, 1, "缓存命中，不再换 token")
	require.Len(t, sends, 2)
	assert.Equal(t, "Bearer ya29.test-1", sends[1].auth)
	assert.Equal(t, "done", fcmPlain(t, c, sends[1])["kind"])
	assert.NotContains(t, sends[1].body["message"].(map[string]any)["android"], "collapse_key")

	// 满 55 分钟后重新换取
	f.clock.Advance(2 * time.Minute)
	f.s.InputRequested(threads.InputEvent{ThreadID: id, RequestID: "r2", Frame: uiFrame("r2")})
	f.flush()
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
	tokens, sends = g.snapshot()
	require.Len(t, tokens, 2)
	require.Len(t, sends, 3)
	assert.Equal(t, "Bearer ya29.test-2", sends[2].auth)

	subs, err := f.s.ListPushSubscriptions(f.ctx)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Equal(t, sub.ID, subs[0].ID)
	assert.NotNil(t, subs[0].LastSuccessAt)
}

// access token 被拒（401）：作废缓存并按退避重试，重试时换新 token 后成功。
func TestFCMAccessTokenRejected(t *testing.T) {
	sa, pub := serviceAccountJSON(t)
	g := newFakeGoogle(t, pub)
	g.rejectAccess["ya29.test-1"] = true
	f, v := fcmFixture(t, sa, secrets.ModeProxy)
	f.useFCM(v, g)
	_, err := f.s.CreatePushSubscription(f.ctx, fcmInput("reg-token-1", pushClient(t)), Owner{}, "")
	require.NoError(t, err)
	d := f.onlyDelivery(f.thread("t"))
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
	row := f.delivery(d.ID)
	assert.Equal(t, statusPending, row.Status, "401 可重试")
	require.NotNil(t, row.LastError)
	assert.NotContains(t, *row.LastError, "ya29", "错误文本不含 access token")

	f.clock.Advance(10 * time.Second)
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
	assert.Equal(t, statusSent, f.delivery(d.ID).Status)
	tokens, sends := g.snapshot()
	assert.Len(t, tokens, 2)
	require.Len(t, sends, 2)
	assert.Equal(t, "Bearer ya29.test-2", sends[1].auth)
}

// UNREGISTERED → 删除订阅（投递随外键级联删除）；其它 FCM 错误按 HTTP 状态归类，订阅保留。
func TestFCMUnregisteredDeletesSubscription(t *testing.T) {
	sa, pub := serviceAccountJSON(t)
	g := newFakeGoogle(t, pub)
	g.errFor["dead"] = fcmErr{http.StatusNotFound, `{"error":{"code":404,"message":"Requested entity was not found.","status":"NOT_FOUND",
		"details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`}
	g.errFor["bad"] = fcmErr{http.StatusBadRequest, `{"error":{"code":400,"status":"INVALID_ARGUMENT",
		"details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"INVALID_ARGUMENT"}]}}`}
	f, v := fcmFixture(t, sa, secrets.ModeProxy)
	f.useFCM(v, g)
	dead, err := f.s.CreatePushSubscription(f.ctx, fcmInput("dead", pushClient(t)), Owner{}, "")
	require.NoError(t, err)
	bad, err := f.s.CreatePushSubscription(f.ctx, fcmInput("bad", pushClient(t)), Owner{}, "")
	require.NoError(t, err)

	f.done(f.thread("t"))
	n := f.notifications()[0]
	require.Len(t, f.deliveries(n.ID), 2)
	require.Equal(t, 2, f.s.DeliverDue(f.ctx))

	subs, err := f.s.ListPushSubscriptions(f.ctx)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Equal(t, bad.ID, subs[0].ID)
	assert.NotEqual(t, dead.ID, subs[0].ID)
	require.NotNil(t, subs[0].LastError)
	assert.Equal(t, "fcm INVALID_ARGUMENT (HTTP 400)", *subs[0].LastError)
	ds := f.deliveries(n.ID)
	require.Len(t, ds, 1, "失效订阅的投递随订阅删除")
	assert.Equal(t, statusFailed, ds[0].Status, "400 不重试")
}

// 令牌端点拒绝断言（服务账号被禁用等）：投递失败且不重试，不发 FCM 请求。
func TestFCMTokenEndpointRejects(t *testing.T) {
	sa, _ := serviceAccountJSON(t)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	g := newFakeGoogle(t, &other.PublicKey) // 公钥不匹配 → invalid_grant
	f, v := fcmFixture(t, sa, secrets.ModeProxy)
	f.useFCM(v, g)
	_, err := f.s.CreatePushSubscription(f.ctx, fcmInput("reg", pushClient(t)), Owner{}, "")
	require.NoError(t, err)
	d := f.onlyDelivery(f.thread("t"))
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
	row := f.delivery(d.ID)
	assert.Equal(t, statusFailed, row.Status)
	require.NotNil(t, row.LastError)
	assert.Contains(t, *row.LastError, "fcm oauth token: HTTP 400")
	_, sends := g.snapshot()
	assert.Empty(t, sends)
}

// 运行中密钥被删除：已有 fcm 订阅的投递以 fcm_unavailable 失败，不重试。
func TestFCMSecretRemovedAtRuntime(t *testing.T) {
	sa, pub := serviceAccountJSON(t)
	g := newFakeGoogle(t, pub)
	f, v := fcmFixture(t, sa, secrets.ModeProxy)
	f.useFCM(v, g)
	_, err := f.s.CreatePushSubscription(f.ctx, fcmInput("reg", pushClient(t)), Owner{}, "")
	require.NoError(t, err)
	ok, err := v.Delete(context.Background(), fcmSecretName)
	require.NoError(t, err)
	require.True(t, ok)
	d := f.onlyDelivery(f.thread("t"))
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
	row := f.delivery(d.ID)
	assert.Equal(t, statusFailed, row.Status)
	require.NotNil(t, row.LastError)
	assert.Contains(t, *row.LastError, "fcm_unavailable")
	tokens, sends := g.snapshot()
	assert.Empty(t, tokens)
	assert.Empty(t, sends)
}

// 旧版 App 登记的 fcm 订阅没有 keys：投递时不发任何请求（绝不回退明文），直接删除该订阅（投递随外键级联删除）。
func TestFCMLegacySubscriptionWithoutKeys(t *testing.T) {
	sa, pub := serviceAccountJSON(t)
	g := newFakeGoogle(t, pub)
	f, v := fcmFixture(t, sa, secrets.ModeProxy)
	f.useFCM(v, g)
	legacy, err := f.st.Q.UpsertPushSubscription(f.ctx, db.UpsertPushSubscriptionParams{
		ID: ids.New("psub"), Kind: "fcm", Endpoint: "legacy-token", CreatedAt: 1,
	})
	require.NoError(t, err)
	c := pushClient(t)
	_, err = f.s.CreatePushSubscription(f.ctx, fcmInput("new-token", c), Owner{}, "")
	require.NoError(t, err)

	id := f.thread("机密标题")
	f.done(id)
	n := f.notifications()[0]
	require.Len(t, f.deliveries(n.ID), 2)
	require.Equal(t, 2, f.s.DeliverDue(f.ctx))

	_, sends := g.snapshot()
	require.Len(t, sends, 1, "只给带 keys 的订阅发送")
	assert.Equal(t, "new-token", sends[0].body["message"].(map[string]any)["token"])
	assert.NotContains(t, sends[0].raw, "机密标题")
	assert.Equal(t, "机密标题", fcmPlain(t, c, sends[0])["title"])
	subs, err := f.s.ListPushSubscriptions(f.ctx)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.NotEqual(t, legacy.ID, subs[0].ID, "无 keys 的旧订阅已删除")
	ds := f.deliveries(n.ID)
	require.Len(t, ds, 1)
	assert.Equal(t, statusSent, ds[0].Status)
}

// 最长的标题 / 正文（且全部需要 JSON 转义）加密后，FCM data 仍不超过 4096 字节（FCM 对 data 的上限）；
// 正文长度逐步加大，覆盖明文落在 2800–3000 字节（Web Push 上限以内、FCM 已超限）的区间。
func TestFCMPayloadFitsDataLimit(t *testing.T) {
	sa, pub := serviceAccountJSON(t)
	g := newFakeGoogle(t, pub)
	f, v := fcmFixture(t, sa, secrets.ModeProxy)
	f.useFCM(v, g)
	c := pushClient(t)
	_, err := f.s.CreatePushSubscription(f.ctx, fcmInput("reg", c), Owner{}, "")
	require.NoError(t, err)
	cases := []struct{ title, reason string }{
		{strings.Repeat("\u2028", 400), strings.Repeat("<", 400)},
		{strings.Repeat("标", 400), strings.Repeat("题", 400)},
		{"短标题", strings.Repeat("\u2028", 400)},
	}
	for n := 200; n <= 300; n += 4 {
		cases = append(cases, struct{ title, reason string }{strings.Repeat("<", 200), strings.Repeat("<", n)})
	}
	for i, tc := range cases {
		id := f.thread(tc.title)
		f.s.StateChanged(threads.StateEvent{ThreadID: id, Prev: threads.StateWorking, State: threads.StateError, Reason: tc.reason})
		f.flush()
		require.Equal(t, 1, f.s.DeliverDue(f.ctx))
		_, sends := g.snapshot()
		require.Len(t, sends, i+1)
		data := sends[i].body["message"].(map[string]any)["data"].(map[string]any)
		size := 0
		for k, x := range data {
			size += len(k) + len(x.(string))
		}
		assert.LessOrEqual(t, size, 4096, "case %d", i)
		p := fcmPlain(t, c, sends[i])
		title := p["title"].(string)
		assert.Equal(t, min(utf8Len(tc.title), 200), utf8Len(title), "标题保留（截到 200 字符）")
		assert.True(t, strings.HasPrefix(tc.title, strings.TrimSuffix(title, "…")), "case %d", i)
	}
}
