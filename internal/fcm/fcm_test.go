package fcm_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/fcm"
	"github.com/Lynricsy/Oryn/internal/fcm/fcmtest"
)

// clock 是可手动推进的时钟。
type clock struct{ t time.Time }

func (c *clock) Now() time.Time { return c.t }

func setup(t *testing.T) (*fcm.Client, *fcm.Account, *fcmtest.Google, *clock) {
	t.Helper()
	sa, pub := fcmtest.ServiceAccount(t)
	acct, err := fcm.ParseAccount([]byte(sa))
	require.NoError(t, err)
	g := fcmtest.NewGoogle(t, pub)
	clk := &clock{time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)}
	c := &fcm.Client{HTTP: g.Server.Client(), TokenURL: g.TokenURL(), APIBase: g.Server.URL, Now: clk.Now}
	return c, acct, g, clk
}

func msg(token string) fcm.Message {
	return fcm.Message{Token: token, Data: map[string]string{"v": "2", "id": "ntf_1", "e": "AAAA"}, High: true, TTL: 24 * time.Hour}
}

// 请求形态、JWT 声明与 access token 缓存：55 分钟内复用，满 55 分钟重新换取。
func TestSendAndTokenCache(t *testing.T) {
	c, acct, g, clk := setup(t)
	ctx := context.Background()
	require.NoError(t, c.Send(ctx, acct, msg("reg-1")).Err)

	tokens, sends := g.Snapshot()
	require.Len(t, tokens, 1)
	claims := tokens[0]
	assert.Equal(t, fcmtest.ClientEmail, claims["iss"])
	assert.Equal(t, "https://www.googleapis.com/auth/firebase.messaging", claims["scope"])
	assert.Equal(t, g.TokenURL(), claims["aud"])
	assert.Equal(t, float64(clk.t.Unix()), claims["iat"])
	assert.Equal(t, float64(clk.t.Add(time.Hour).Unix()), claims["exp"])

	require.Len(t, sends, 1)
	assert.Equal(t, "/v1/projects/"+fcmtest.ProjectID+"/messages:send", sends[0].Path)
	assert.Equal(t, "Bearer ya29.test-1", sends[0].Auth)
	m := sends[0].Body["message"].(map[string]any)
	assert.Equal(t, "reg-1", m["token"])
	assert.NotContains(t, m, "notification", "data-only")
	assert.Equal(t, map[string]any{"v": "2", "id": "ntf_1", "e": "AAAA"}, m["data"])
	assert.Equal(t, map[string]any{"priority": "HIGH", "ttl": "86400s"}, m["android"], "不设 collapse_key")

	clk.t = clk.t.Add(54 * time.Minute)
	low := msg("reg-1")
	low.High = false
	require.NoError(t, c.Send(ctx, acct, low).Err)
	tokens, sends = g.Snapshot()
	assert.Len(t, tokens, 1, "缓存命中，不再换 token")
	assert.Equal(t, "Bearer ya29.test-1", sends[1].Auth)
	assert.Equal(t, "NORMAL", sends[1].Body["message"].(map[string]any)["android"].(map[string]any)["priority"])

	clk.t = clk.t.Add(2 * time.Minute)
	require.NoError(t, c.Send(ctx, acct, msg("reg-1")).Err)
	tokens, sends = g.Snapshot()
	require.Len(t, tokens, 2)
	assert.Equal(t, "Bearer ya29.test-2", sends[2].Auth)
}

// 换了服务账号：作废缓存，用新账号重新换取。
func TestTokenCacheFollowsAccount(t *testing.T) {
	c, acct, g, _ := setup(t)
	ctx := context.Background()
	require.NoError(t, c.Send(ctx, acct, msg("reg-1")).Err)
	sa2, pub2 := fcmtest.ServiceAccount(t)
	acct2, err := fcm.ParseAccount([]byte(sa2))
	require.NoError(t, err)
	g2 := fcmtest.NewGoogle(t, pub2)
	c.TokenURL, c.APIBase = g2.TokenURL(), g2.Server.URL
	require.NoError(t, c.Send(ctx, acct2, msg("reg-1")).Err)
	tokens, _ := g2.Snapshot()
	assert.Len(t, tokens, 1, "新账号必须重新换 token")
	tokens, _ = g.Snapshot()
	assert.Len(t, tokens, 1)
}

// access token 被拒（401）：作废缓存并可重试，下一次换新 token 成功；错误文本不含 access token。
func TestAccessTokenRejected(t *testing.T) {
	c, acct, g, _ := setup(t)
	g.RejectAccess("ya29.test-1")
	ctx := context.Background()
	r := c.Send(ctx, acct, msg("reg-1"))
	require.Error(t, r.Err)
	assert.True(t, r.Retry)
	assert.False(t, r.Unregistered)
	assert.NotContains(t, r.Err.Error(), "ya29")

	require.NoError(t, c.Send(ctx, acct, msg("reg-1")).Err)
	tokens, sends := g.Snapshot()
	assert.Len(t, tokens, 2)
	require.Len(t, sends, 2)
	assert.Equal(t, "Bearer ya29.test-2", sends[1].Auth)
}

// UNREGISTERED → Unregistered；其它 FCM 错误按 HTTP 状态归类并带 errorCode；Retry-After 被尊重。
func TestErrorClassification(t *testing.T) {
	c, acct, g, _ := setup(t)
	g.SetErr("dead", fcmtest.Err{Status: http.StatusNotFound, Body: `{"error":{"code":404,"status":"NOT_FOUND",
		"details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`})
	g.SetErr("bad", fcmtest.Err{Status: http.StatusBadRequest, Body: `{"error":{"code":400,"status":"INVALID_ARGUMENT",
		"details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"INVALID_ARGUMENT"}]}}`})
	g.SetErr("busy", fcmtest.Err{Status: http.StatusServiceUnavailable, Body: `{"error":{"code":503,"status":"UNAVAILABLE"}}`})
	ctx := context.Background()

	r := c.Send(ctx, acct, msg("dead"))
	assert.True(t, r.Unregistered)
	assert.False(t, r.Retry)
	assert.EqualError(t, r.Err, "fcm token unregistered (HTTP 404)")

	r = c.Send(ctx, acct, msg("bad"))
	assert.False(t, r.Unregistered)
	assert.False(t, r.Retry, "400 不重试")
	assert.EqualError(t, r.Err, "fcm INVALID_ARGUMENT (HTTP 400)")

	r = c.Send(ctx, acct, msg("busy"))
	assert.True(t, r.Retry, "5xx 重试")
	assert.EqualError(t, r.Err, "fcm UNAVAILABLE (HTTP 503)")
}

// 令牌端点拒绝断言（服务账号被禁用、公钥不匹配）：不重试、不发 FCM 请求。
func TestTokenEndpointRejects(t *testing.T) {
	sa, _ := fcmtest.ServiceAccount(t)
	acct, err := fcm.ParseAccount([]byte(sa))
	require.NoError(t, err)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	g := fcmtest.NewGoogle(t, &other.PublicKey)
	c := &fcm.Client{HTTP: g.Server.Client(), TokenURL: g.TokenURL(), APIBase: g.Server.URL}
	r := c.Send(context.Background(), acct, msg("reg"))
	require.Error(t, r.Err)
	assert.False(t, r.Retry)
	assert.Contains(t, r.Err.Error(), "fcm oauth token: HTTP 400")
	_, sends := g.Snapshot()
	assert.Empty(t, sends)
}

func TestParseAccountAndToken(t *testing.T) {
	for _, raw := range []string{`not json`, `{"project_id":"p","client_email":"e"}`, `{"project_id":"p","client_email":"e","private_key":"not pem"}`} {
		_, err := fcm.ParseAccount([]byte(raw))
		assert.ErrorIs(t, err, fcm.ErrInvalidAccount, raw)
	}
	for _, tok := range []string{"", "has space", "tok\n", "tök", strings.Repeat("a", fcm.MaxToken+1)} {
		assert.False(t, fcm.ValidToken(tok), "%q", tok)
	}
	assert.True(t, fcm.ValidToken("tok:APA91b-x_y"))
}
