package egress

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/aiconsent"
)

// consentFake 是可在测试中途改动的同意记录。
type consentFake struct {
	mu  sync.Mutex
	ids map[string]bool
}

func newConsent(ids ...string) *consentFake {
	c := &consentFake{ids: map[string]bool{}}
	c.set(ids...)
	return c
}

// set 把同意记录整体换成 ids（空 = 撤回全部）。
func (c *consentFake) set(ids ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ids = map[string]bool{}
	for _, id := range ids {
		c.ids[id] = true
	}
}

func (f fakeR) AIConsent(recipient string) error {
	if f.consent == nil {
		return &aiconsent.DeniedError{Recipient: recipient}
	}
	f.consent.mu.Lock()
	defer f.consent.mu.Unlock()
	if !f.consent.ids[recipient] {
		return &aiconsent.DeniedError{Recipient: recipient}
	}
	return nil
}

// countingServer 是记下请求次数的假上游。
func countingServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// redirectTransport 把公网主机名（host:port → 测试服务器地址）拨到本机的测试服务器，模拟第三方上游；其余地址照常拨号。
func redirectTransport(m map[string]string) *http.Transport {
	return &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if to, ok := m[addr]; ok {
			addr = to
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
}

func consentCode(t *testing.T, resp *http.Response) string {
	t.Helper()
	var body struct {
		Error struct {
			Code, Message, Recipient string
		}
	}
	b, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(b, &body), string(b))
	assert.Contains(t, body.Error.Message, "设置 → 隐私与数据")
	return body.Error.Code + " " + body.Error.Recipient
}

// 模型路由：没有同意 → 403 ai_consent_required、上游 0 个请求；同意 → 转发；撤回 → 再次拒绝；只同意了上游 A 时，
// 新出现的模型条目上游 B 被拒（B 收不到请求），A 照常。
func TestLLMRouteRequiresConsent(t *testing.T) {
	a, aCount := countingServer(t)
	b, bCount := countingServer(t)
	consent := newConsent()
	h := NewHandler("orb-x", fakeR{
		pol: Policy{Routes: map[string]Route{
			"gw":    {Origin: "http://a.example.com", Provider: "gw", SecretIDs: []string{"s1"}},
			"gw.m1": {Origin: "http://b.example.net", Provider: "gw", SecretIDs: []string{"s1"}},
		}},
		secrets: map[string][2]string{"s1": {"REAL-KEY", ""}},
		consent: consent,
	})
	h.transport = redirectTransport(map[string]string{
		"a.example.com:80": a.Listener.Addr().String(),
		"b.example.net:80": b.Listener.Addr().String(),
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	call := func(route string) *http.Response {
		t.Helper()
		resp := do(t, "POST", srv.URL+"/llm/"+route+"/v1/chat/completions", map[string]string{"Authorization": "Bearer oryn-ph-s1"})
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	resp := call("gw")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, "ai_consent_required model:gw@a.example.com", consentCode(t, resp))
	assert.Zero(t, aCount.Load(), "没有同意时上游一个请求也收不到")

	consent.set("model:gw@a.example.com")
	resp = call("gw")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.EqualValues(t, 1, aCount.Load())

	consent.set() // 撤回
	resp = call("gw")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.EqualValues(t, 1, aCount.Load(), "撤回之后不再转发")

	// 只同意 A：模型条目自带的上游 B 是新的接收方，被拒
	consent.set("model:gw@a.example.com")
	resp = call("gw.m1")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, "ai_consent_required model:gw@b.example.net", consentCode(t, resp))
	assert.Zero(t, bCount.Load(), "B 一个请求也收不到")
	resp = call("gw")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.EqualValues(t, 2, aCount.Load(), "A 照常")

	// 同意 B 之后 B 也放行
	consent.set("model:gw@a.example.com", "model:gw@b.example.net")
	resp = call("gw.m1")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.EqualValues(t, 1, bCount.Load())
}

// Resolver 没有接上同意来源（nil）时一律拒绝；Provider 为空时按路由键算接收方；本机上游不问同意。
func TestLLMRouteConsentDefaults(t *testing.T) {
	a, aCount := countingServer(t)
	local, localCount := countingServer(t)
	h := NewHandler("orb-x", fakeR{pol: Policy{Routes: map[string]Route{
		"deepseek": {Origin: "https://api.deepseek.com"},
		"home":     {Origin: local.URL},
	}}})
	h.transport = redirectTransport(map[string]string{"api.deepseek.com:443": a.Listener.Addr().String()})
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp := do(t, "POST", srv.URL+"/llm/deepseek/chat/completions", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, "ai_consent_required model:deepseek@api.deepseek.com", consentCode(t, resp))
	assert.Zero(t, aCount.Load())

	resp2 := do(t, "POST", srv.URL+"/llm/home/v1/chat", nil)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusOK, resp2.StatusCode, "本机 / 内网上游不是第三方")
	assert.EqualValues(t, 1, localCount.Load())
}

// 正向代理（全局助手联网）：公网目标须同意 web:public；没有同意时 0 次拨号、上游 0 个请求，撤回后再次拒绝。
func TestWebProxyRequiresConsent(t *testing.T) {
	up, upCount := countingServer(t)
	var dials atomic.Int32
	prev := webTestDial
	webTestDial = func(ctx context.Context, addr string) (net.Conn, error) {
		dials.Add(1)
		if addr != "web.example.com:80" && addr != "web.example.com:443" {
			return nil, fmt.Errorf("unexpected dial %s", addr)
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", up.Listener.Addr().String())
	}
	t.Cleanup(func() { webTestDial = prev })
	consent := newConsent()
	srv := httptest.NewServer(NewHandler("coord", fakeR{pol: Policy{NoGit: true, Web: &WebPolicy{}}, consent: consent}))
	defer srv.Close()
	get := func() (int, string) {
		code, br, _ := rawProxy(t, srv.URL, "GET http://web.example.com/search?q=x HTTP/1.1\r\nHost: web.example.com\r\nConnection: close\r\n\r\n")
		body, _ := io.ReadAll(br)
		return code, string(body)
	}
	connect := func() int {
		code, _, conn := rawProxy(t, srv.URL, "CONNECT web.example.com:443 HTTP/1.1\r\nHost: web.example.com:443\r\n\r\n")
		_ = conn.Close()
		return code
	}

	code, body := get()
	assert.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, body, aiconsent.Code)
	assert.Contains(t, body, aiconsent.WebRecipientID)
	assert.Equal(t, http.StatusForbidden, connect())
	assert.Zero(t, dials.Load(), "没有同意时不拨号")
	assert.Zero(t, upCount.Load())

	consent.set(aiconsent.WebRecipientID)
	code, body = get()
	assert.Equal(t, http.StatusOK, code, body)
	assert.EqualValues(t, 1, upCount.Load())
	assert.Equal(t, http.StatusOK, connect())
	assert.EqualValues(t, 2, dials.Load())

	consent.set()
	code, _ = get()
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, http.StatusForbidden, connect())
	assert.EqualValues(t, 2, dials.Load(), "撤回之后不再拨号")
	assert.EqualValues(t, 1, upCount.Load())
}
