package pushgw

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Lynricsy/Oryn/internal/apns/apnstest"
	"github.com/Lynricsy/Oryn/internal/fcm/fcmtest"
	"github.com/Lynricsy/Oryn/internal/pushproto"
)

const plaintext = `{"v":1,"id":"n1","kind":"turn","title":"secret title","body":"secret body","url":"/t/1","tag":"t1"}`

func TestInfo(t *testing.T) {
	e := newEnv(t, nil)
	w := e.call(http.MethodGet, pushproto.PathInfo, nil, nil)
	expect(t, w, http.StatusOK, "")
	want := `{"v":1,"apns":{"topic":"com.lynricsy.oryn","environments":["production","sandbox"]},"fcm":{"projectId":"oryn-test"}}`
	if got := strings.TrimSpace(w.Body.String()); got != want {
		t.Errorf("info = %s", got)
	}
	bare := newEnv(t, func(c *Config) { c.APNs, c.FCM = APNs{}, FCM{} })
	w = bare.call(http.MethodGet, pushproto.PathInfo, nil, nil)
	if got := strings.TrimSpace(w.Body.String()); got != `{"v":1}` {
		t.Errorf("bare info = %s", got)
	}
	w = bare.call(http.MethodGet, "/healthz", nil, nil)
	var hz map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &hz); err != nil || hz["ok"] != true || hz["version"] == "" {
		t.Errorf("healthz = %s", w.Body.String())
	}
	expect(t, bare.call(http.MethodGet, "/nope", nil, nil), http.StatusNotFound, pushproto.CodeNotFound)
}

func TestChallengeGrantPushFCM(t *testing.T) {
	e := newEnv(t, nil)
	d, h := newDevice(t), newHost(t)
	tok := fcmToken(t)
	id, nonce := e.challenge(d, pushproto.ChannelFCM, "", tok)

	_, sends := e.google.Snapshot()
	if len(sends) != 1 || sends[0].Path != "/v1/projects/"+fcmtest.ProjectID+"/messages:send" {
		t.Fatalf("sends = %+v", sends)
	}
	wantChallenge := map[string]any{"message": map[string]any{
		"token":   tok,
		"data":    map[string]any{"kind": pushproto.ChallengeKind, "challengeId": id, "nonce": nonce},
		"android": map[string]any{"priority": "HIGH", "ttl": "300s"},
	}}
	if !reflect.DeepEqual(sends[0].Body, wantChallenge) {
		t.Errorf("challenge body = %s", sends[0].Raw)
	}
	if !pushproto.IDPattern.MatchString(nonce) || nonce == id {
		t.Errorf("nonce %q", nonce)
	}

	w := e.call(http.MethodPost, pushproto.PathGrants, pushproto.GrantRequest{ChallengeID: id, Nonce: nonce, HostKey: h.pub}, asDevice(d))
	expect(t, w, http.StatusCreated, "")
	var gr pushproto.GrantResponse
	_ = json.Unmarshal(w.Body.Bytes(), &gr)
	expect(t, e.verify(h, gr.Grant), http.StatusNoContent, "")

	ciph, rcpt := cipher(t, plaintext)
	expect(t, e.push(h, pushproto.PushRequest{Grant: gr.Grant, ID: "n1", Collapse: pushproto.Collapse(gr.Grant, "t1"), E: ciph}), http.StatusAccepted, "")
	_, sends = e.google.Snapshot()
	if len(sends) != 2 {
		t.Fatalf("sends = %d", len(sends))
	}
	wantPush := map[string]any{"message": map[string]any{
		"token":   tok,
		"data":    map[string]any{"v": "2", "id": "n1", "e": ciph},
		"android": map[string]any{"priority": "HIGH", "ttl": "86400s"},
	}}
	if !reflect.DeepEqual(sends[1].Body, wantPush) {
		t.Errorf("push body = %s", sends[1].Raw)
	}
	for _, s := range []string{"secret", "notification", "collapse"} {
		if strings.Contains(sends[1].Raw, s) {
			t.Errorf("push body contains %q: %s", s, sends[1].Raw)
		}
	}
	ct, _ := b64Decode(ciph)
	if got, err := rcpt.Decrypt(ct); err != nil || string(got) != plaintext {
		t.Errorf("e does not decrypt to the plaintext: %v", err)
	}

	// 撤回：NORMAL
	expect(t, e.push(h, pushproto.PushRequest{Grant: gr.Grant, ID: "n1", Withdraw: true, E: ciph}), http.StatusAccepted, "")
	_, sends = e.google.Snapshot()
	if p := sends[2].Body["message"].(map[string]any)["android"].(map[string]any)["priority"]; p != "NORMAL" {
		t.Errorf("withdraw priority = %v", p)
	}

	// access token 被拒：重新换取后重试一次
	e.google.RejectAccess("ya29.test-1")
	expect(t, e.push(h, pushproto.PushRequest{Grant: gr.Grant, ID: "n2", E: ciph}), http.StatusAccepted, "")
	_, sends = e.google.Snapshot()
	if len(sends) != 5 || sends[3].Auth != "Bearer ya29.test-1" || sends[4].Auth != "Bearer ya29.test-2" {
		t.Errorf("401 retry: %d sends", len(sends))
	}
}

func TestChallengeGrantPushAPNs(t *testing.T) {
	e := newEnv(t, nil)
	d, h := newDevice(t), newHost(t)
	tok := apnsToken(t)
	id, nonce := e.challenge(d, pushproto.ChannelAPNs, "sandbox", tok)

	reqs := e.apns.Requests()
	if len(reqs) != 1 {
		t.Fatalf("apns requests = %d", len(reqs))
	}
	c := reqs[0]
	if c.Env != "sandbox" || c.Token != tok || c.Proto != 2 {
		t.Errorf("challenge env=%s proto=%d", c.Env, c.Proto)
	}
	for k, v := range map[string]string{"apns-topic": testApp, "apns-push-type": "alert", "apns-priority": "10", "apns-expiration": "0"} {
		if got := c.Header.Get(k); got != v {
			t.Errorf("challenge %s = %q, want %q", k, got, v)
		}
	}
	if c.Header.Values("apns-collapse-id") != nil {
		t.Error("challenge has apns-collapse-id")
	}
	wantChallenge := map[string]any{
		"aps":  map[string]any{"alert": map[string]any{"title": "Oryn", "body": "Verifying push notifications"}},
		"kind": pushproto.ChallengeKind, "challengeId": id, "nonce": nonce,
	}
	if !reflect.DeepEqual(c.Body, wantChallenge) {
		t.Errorf("challenge payload = %s", c.Raw)
	}
	if c.Claims["iss"] != apnstest.TeamID || c.Claims["iat"] != float64(e.clk.Now().Unix()) {
		t.Errorf("JWT claims = %v", c.Claims)
	}

	w := e.call(http.MethodPost, pushproto.PathGrants, pushproto.GrantRequest{ChallengeID: id, Nonce: nonce, HostKey: h.pub}, asDevice(d))
	expect(t, w, http.StatusCreated, "")
	var gr pushproto.GrantResponse
	_ = json.Unmarshal(w.Body.Bytes(), &gr)

	e.clk.Add(10 * time.Minute)
	ciph, _ := cipher(t, plaintext)
	collapse := pushproto.Collapse(gr.Grant, "t1")
	expect(t, e.push(h, pushproto.PushRequest{Grant: gr.Grant, ID: "n1", Collapse: collapse, E: ciph}), http.StatusAccepted, "")
	reqs = e.apns.Requests()
	if len(reqs) != 2 {
		t.Fatalf("apns requests = %d", len(reqs))
	}
	p := reqs[1]
	want := map[string]string{
		"apns-topic": testApp, "apns-push-type": "alert", "apns-priority": "10",
		"apns-expiration": strconv.FormatInt(e.clk.Now().Add(24*time.Hour).Unix(), 10), "apns-collapse-id": collapse,
	}
	for k, v := range want {
		if got := p.Header.Get(k); got != v {
			t.Errorf("push %s = %q, want %q", k, got, v)
		}
	}
	if p.Env != "sandbox" || p.Token != tok {
		t.Errorf("push env = %s", p.Env)
	}
	wantPush := map[string]any{
		"aps": map[string]any{
			"alert":           map[string]any{"title": "Oryn", "body": "New notification"},
			"mutable-content": float64(1), "sound": "default",
		},
		"v": "2", "id": "n1", "e": ciph,
	}
	if !reflect.DeepEqual(p.Body, wantPush) {
		t.Errorf("push payload = %s", p.Raw)
	}
	if strings.Contains(p.Raw, "secret") {
		t.Error("push payload contains plaintext")
	}
	// provider token 缓存：10 分钟后仍是同一个
	if p.JWT != c.JWT {
		t.Error("provider token not cached")
	}

	// 撤回不联系 APNs
	expect(t, e.push(h, pushproto.PushRequest{Grant: gr.Grant, ID: "n1", Withdraw: true, E: ciph}), http.StatusAccepted, "")
	if n := len(e.apns.Requests()); n != 2 {
		t.Errorf("withdraw reached APNs: %d requests", n)
	}

	// 不在配置里的环境 / 别的 topic → app_mismatch
	sandboxOnly := newEnv(t, func(c *Config) { c.APNs.Environments = []string{"production"} })
	w = sandboxOnly.call(http.MethodPost, pushproto.PathChallenges, pushproto.ChallengeRequest{Channel: "apns", AppID: testApp, Environment: "sandbox", Token: tok}, asDevice(d))
	expect(t, w, http.StatusConflict, pushproto.CodeAppMismatch)
	w = e.call(http.MethodPost, pushproto.PathChallenges, pushproto.ChallengeRequest{Channel: "apns", AppID: "com.example.other", Token: tok}, asDevice(d))
	expect(t, w, http.StatusConflict, pushproto.CodeAppMismatch)
	noFCM := newEnv(t, func(c *Config) { c.FCM = FCM{} })
	w = noFCM.call(http.MethodPost, pushproto.PathChallenges, pushproto.ChallengeRequest{Channel: "fcm", AppID: testApp, Token: fcmToken(t)}, asDevice(d))
	expect(t, w, http.StatusConflict, pushproto.CodeAppMismatch)
	w = e.call(http.MethodPost, pushproto.PathChallenges, pushproto.ChallengeRequest{Channel: "apns", AppID: testApp, Token: "ABCDEF"}, asDevice(d))
	expect(t, w, http.StatusBadRequest, pushproto.CodeMalformed)
}

func TestGrantExclusivePerDevice(t *testing.T) {
	e := newEnv(t, nil)
	d, h1, h2 := newDevice(t), newHost(t), newHost(t)
	g1 := e.grant(d, h1, pushproto.ChannelFCM, "", fcmToken(t))
	other := e.grant(newDevice(t), h1, pushproto.ChannelFCM, "", fcmToken(t))
	g2 := e.grant(d, h2, pushproto.ChannelFCM, "", fcmToken(t))
	if g2 == g1 {
		t.Fatal("new host reused the grant id")
	}
	expect(t, e.verify(h1, g1), http.StatusGone, pushproto.CodeGrantRevoked)
	expect(t, e.verify(h2, g2), http.StatusNoContent, "")
	// 别的设备给同一主机的授权不受影响
	expect(t, e.verify(h1, other), http.StatusNoContent, "")

	// 同一主机再授权：原 id、令牌更新、创建时间不变
	e.clk.Add(time.Minute)
	before, _ := e.srv.store.Get(context.Background(), g2)
	newTok := fcmToken(t)
	id, nonce := e.challenge(d, pushproto.ChannelFCM, "", newTok)
	w := e.call(http.MethodPost, pushproto.PathGrants, pushproto.GrantRequest{ChallengeID: id, Nonce: nonce, HostKey: h2.pub}, asDevice(d))
	expect(t, w, http.StatusCreated, "")
	var gr pushproto.GrantResponse
	_ = json.Unmarshal(w.Body.Bytes(), &gr)
	if gr.Grant != g2 || gr.CreatedAt != before.CreatedAt {
		t.Fatalf("same host: grant %s created %d, want %s %d", gr.Grant, gr.CreatedAt, g2, before.CreatedAt)
	}
	ciph, _ := cipher(t, plaintext)
	expect(t, e.push(h2, pushproto.PushRequest{Grant: g2, ID: "n1", E: ciph}), http.StatusAccepted, "")
	_, sends := e.google.Snapshot()
	if got := sends[len(sends)-1].Body["message"].(map[string]any)["token"]; got != newTok {
		t.Error("push did not use the refreshed token")
	}
	var active int
	if err := e.srv.store.db.QueryRow(`SELECT count(*) FROM grants WHERE device_key = ? AND revoked_at IS NULL`, d.pub).Scan(&active); err != nil || active != 1 {
		t.Errorf("active grants for device = %d (%v)", active, err)
	}
}

func TestNonceSingleUseAndExpiry(t *testing.T) {
	e := newEnv(t, nil)
	d, h := newDevice(t), newHost(t)
	grantReq := func(dev device, id, nonce string) int {
		w := e.call(http.MethodPost, pushproto.PathGrants, pushproto.GrantRequest{ChallengeID: id, Nonce: nonce, HostKey: h.pub}, asDevice(dev))
		if w.Code != http.StatusCreated {
			expect(t, w, http.StatusConflict, pushproto.CodeChallengeInvalid)
		}
		return w.Code
	}
	wrongNonce := "AAAAAAAAAAAAAAAAAAAAAA"

	// 别的设备拿着 id 来取：拒绝，且不作废原 challenge
	id, nonce := e.challenge(d, pushproto.ChannelFCM, "", fcmToken(t))
	if grantReq(newDevice(t), id, nonce) != http.StatusConflict {
		t.Fatal("other device used the challenge")
	}
	if grantReq(d, id, nonce) != http.StatusCreated {
		t.Fatal("owner could not use the challenge")
	}
	// 用过一次即作废
	if grantReq(d, id, nonce) != http.StatusConflict {
		t.Fatal("challenge reused")
	}

	// nonce 错一次即作废
	id, nonce = e.challenge(d, pushproto.ChannelFCM, "", fcmToken(t))
	if grantReq(d, id, wrongNonce) != http.StatusConflict || grantReq(d, id, nonce) != http.StatusConflict {
		t.Fatal("challenge survived a wrong nonce")
	}

	// 5 分钟过期
	id, nonce = e.challenge(d, pushproto.ChannelFCM, "", fcmToken(t))
	e.clk.Add(5*time.Minute - time.Millisecond)
	id2, nonce2 := e.challenge(d, pushproto.ChannelFCM, "", fcmToken(t))
	e.clk.Add(time.Millisecond)
	if grantReq(d, id, nonce) != http.StatusConflict {
		t.Fatal("expired challenge accepted")
	}
	if grantReq(d, id2, nonce2) != http.StatusCreated {
		t.Fatal("fresh challenge rejected")
	}
	// 不存在的 id
	if grantReq(d, wrongNonce, wrongNonce) != http.StatusConflict {
		t.Fatal("unknown challenge accepted")
	}
	// 过期的 challenge 由清理丢掉
	e.challenge(d, pushproto.ChannelFCM, "", fcmToken(t))
	e.clk.Add(challengeTTL)
	e.srv.cleanup(context.Background())
	if n := len(e.srv.challenges.m); n != 0 {
		t.Errorf("challenges left after cleanup: %d", n)
	}
}

func TestHostMismatchAndRevoked(t *testing.T) {
	e := newEnv(t, nil)
	d, h, stranger := newDevice(t), newHost(t), newHost(t)
	g := e.grant(d, h, pushproto.ChannelFCM, "", fcmToken(t))
	ciph, _ := cipher(t, plaintext)
	expect(t, e.verify(stranger, g), http.StatusForbidden, pushproto.CodeHostMismatch)
	expect(t, e.push(stranger, pushproto.PushRequest{Grant: g, ID: "n1", E: ciph}), http.StatusForbidden, pushproto.CodeHostMismatch)
	unknown := "BBBBBBBBBBBBBBBBBBBBBB"
	expect(t, e.verify(h, unknown), http.StatusGone, pushproto.CodeGrantRevoked)
	expect(t, e.push(h, pushproto.PushRequest{Grant: unknown, ID: "n1", E: ciph}), http.StatusGone, pushproto.CodeGrantRevoked)

	expect(t, e.call(http.MethodDelete, pushproto.GrantPath(g), nil, asDevice(d)), http.StatusNoContent, "")
	expect(t, e.verify(h, g), http.StatusGone, pushproto.CodeGrantRevoked)
	expect(t, e.push(h, pushproto.PushRequest{Grant: g, ID: "n1", E: ciph}), http.StatusGone, pushproto.CodeGrantRevoked)
	_, sends := e.google.Snapshot()
	if len(sends) != 1 {
		t.Errorf("revoked grant reached FCM: %d sends", len(sends))
	}

	// 签名要求：设备不能 push / verify，主机不能申请 challenge
	expect(t, e.call(http.MethodPost, pushproto.PathVerify, pushproto.VerifyRequest{Grant: g}, asDevice(d)), http.StatusBadRequest, pushproto.CodeMalformed)
	expect(t, e.call(http.MethodPost, pushproto.PathChallenges, pushproto.ChallengeRequest{Channel: "fcm", AppID: testApp, Token: "x"}, asHost(h)), http.StatusBadRequest, pushproto.CodeMalformed)
	// 签名错误与时钟偏差
	expect(t, e.call(http.MethodPost, pushproto.PathVerify, pushproto.VerifyRequest{Grant: g}, asHost(h), func(r *http.Request) {
		r.Header.Set(pushproto.HeaderSignature, "AAAA")
	}), http.StatusUnauthorized, pushproto.CodeBadSignature)
	w := e.call(http.MethodPost, pushproto.PathVerify, pushproto.VerifyRequest{Grant: g}, func(e *env, r *http.Request, body []byte) {
		pushproto.SignHost(r, testOrigin, h.key, e.clk.Now().Add(-6*time.Minute), body)
	})
	expect(t, w, http.StatusUnauthorized, pushproto.CodeClockSkew)
	// 签名不覆盖别的 origin
	w = e.call(http.MethodPost, pushproto.PathVerify, pushproto.VerifyRequest{Grant: g}, func(e *env, r *http.Request, body []byte) {
		pushproto.SignHost(r, "https://other.test", h.key, e.clk.Now(), body)
	})
	expect(t, w, http.StatusUnauthorized, pushproto.CodeBadSignature)
}

func TestDeleteAuthorization(t *testing.T) {
	e := newEnv(t, nil)
	d, h := newDevice(t), newHost(t)
	del := func(id string, s signer) int {
		w := e.call(http.MethodDelete, pushproto.GrantPath(id), nil, s)
		if w.Code == http.StatusForbidden {
			expect(t, w, http.StatusForbidden, pushproto.CodeForbidden)
		}
		return w.Code
	}

	g := e.grant(d, h, pushproto.ChannelFCM, "", fcmToken(t))
	if del(g, asDevice(newDevice(t))) != http.StatusForbidden || del(g, asHost(newHost(t))) != http.StatusForbidden {
		t.Fatal("stranger could delete the grant")
	}
	expect(t, e.verify(h, g), http.StatusNoContent, "")
	if del(g, asDevice(d)) != http.StatusNoContent {
		t.Fatal("device could not delete")
	}
	expect(t, e.verify(h, g), http.StatusGone, pushproto.CodeGrantRevoked)
	// 已吊销：任何签名者都是 204
	if del(g, asHost(newHost(t))) != http.StatusNoContent {
		t.Fatal("revoked grant not 204")
	}

	g = e.grant(d, h, pushproto.ChannelAPNs, "", apnsToken(t))
	if del(g, asHost(h)) != http.StatusNoContent {
		t.Fatal("host could not delete")
	}
	expect(t, e.verify(h, g), http.StatusGone, pushproto.CodeGrantRevoked)

	if del("CCCCCCCCCCCCCCCCCCCCCC", asDevice(d)) != http.StatusNoContent {
		t.Fatal("unknown grant not 204")
	}
	expect(t, e.call(http.MethodDelete, pushproto.GrantPath("short"), nil, asDevice(d)), http.StatusBadRequest, pushproto.CodeMalformed)
	expect(t, e.call(http.MethodDelete, pushproto.GrantPath(g), nil, nil), http.StatusBadRequest, pushproto.CodeMalformed)
}

func TestUpstreamUnregisteredRevokes(t *testing.T) {
	e := newEnv(t, nil)
	ciph, _ := cipher(t, plaintext)
	fcmErr := func(status int, code string) fcmtest.Err {
		return fcmtest.Err{Status: status, Body: `{"error":{"code":` + strconv.Itoa(status) + `,"status":"X","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"` + code + `"}]}}`}
	}
	cases := []struct {
		name    string
		channel string
		set     func(tok string)
	}{
		{"fcm UNREGISTERED", "fcm", func(tok string) { e.google.SetErr(tok, fcmErr(404, "UNREGISTERED")) }},
		{"fcm SENDER_ID_MISMATCH", "fcm", func(tok string) { e.google.SetErr(tok, fcmErr(403, "SENDER_ID_MISMATCH")) }},
		{"apns 410", "apns", func(tok string) { e.apns.SetErr(tok, apnstest.Reply{Status: 410, Reason: "Unregistered"}) }},
		{"apns BadDeviceToken", "apns", func(tok string) { e.apns.SetErr(tok, apnstest.Reply{Status: 400, Reason: "BadDeviceToken"}) }},
		{"apns DeviceTokenNotForTopic", "apns", func(tok string) {
			e.apns.SetErr(tok, apnstest.Reply{Status: 400, Reason: "DeviceTokenNotForTopic"})
		}},
	}
	for _, tc := range cases {
		d, h := newDevice(t), newHost(t)
		tok := fcmToken(t)
		if tc.channel == "apns" {
			tok = apnsToken(t)
		}
		g := e.grant(d, h, tc.channel, "", tok)
		tc.set(tok)
		t.Log(tc.name)
		expect(t, e.push(h, pushproto.PushRequest{Grant: g, ID: "n1", E: ciph}), http.StatusGone, pushproto.CodeDeviceUnregistered)
		expect(t, e.verify(h, g), http.StatusGone, pushproto.CodeGrantRevoked)
	}

	// challenge 阶段：失效令牌 410，不属本 App 409，都不留下 challenge
	d := newDevice(t)
	e.google.SetErr("unreg-token", fcmErr(404, "UNREGISTERED"))
	e.google.SetErr("mismatch-token", fcmErr(403, "SENDER_ID_MISMATCH"))
	w := e.call(http.MethodPost, pushproto.PathChallenges, pushproto.ChallengeRequest{Channel: "fcm", AppID: testApp, Token: "unreg-token"}, asDevice(d))
	expect(t, w, http.StatusGone, pushproto.CodeDeviceUnregistered)
	w = e.call(http.MethodPost, pushproto.PathChallenges, pushproto.ChallengeRequest{Channel: "fcm", AppID: testApp, Token: "mismatch-token"}, asDevice(d))
	expect(t, w, http.StatusConflict, pushproto.CodeAppMismatch)
	at := apnsToken(t)
	e.apns.SetErr(at, apnstest.Reply{Status: 400, Reason: "DeviceTokenNotForTopic"})
	w = e.call(http.MethodPost, pushproto.PathChallenges, pushproto.ChallengeRequest{Channel: "apns", AppID: testApp, Token: at}, asDevice(d))
	expect(t, w, http.StatusConflict, pushproto.CodeAppMismatch)
	if n := len(e.srv.challenges.m); n != 0 {
		t.Errorf("failed challenges kept: %d", n)
	}

	// 其它上游失败：502，授权保留
	h := newHost(t)
	tok := fcmToken(t)
	g := e.grant(newDevice(t), h, "fcm", "", tok)
	e.google.SetErr(tok, fcmtest.Err{Status: 503, Body: `{"error":{"code":503,"status":"UNAVAILABLE"}}`})
	w = e.push(h, pushproto.PushRequest{Grant: g, ID: "n1", E: ciph})
	expect(t, w, http.StatusBadGateway, pushproto.CodeUpstream)
	expect(t, e.verify(h, g), http.StatusNoContent, "")
}

func TestRateLimits(t *testing.T) {
	ciph, _ := cipher(t, plaintext)
	retryAfter := func(t *testing.T, w interface{ Header() http.Header }, want int) {
		t.Helper()
		got, err := strconv.Atoi(w.Header().Get("Retry-After"))
		if err != nil || got != want {
			t.Errorf("Retry-After = %q, want %d", w.Header().Get("Retry-After"), want)
		}
	}

	t.Run("challenges per IP", func(t *testing.T) {
		e := newEnv(t, func(c *Config) { c.Limits.ChallengesPerIPHour = 2 })
		d := newDevice(t)
		from := func(ip string) func(*http.Request) {
			return func(r *http.Request) {
				r.RemoteAddr = "127.0.0.1:40000"
				r.Header.Set("X-Real-IP", ip)
			}
		}
		req := pushproto.ChallengeRequest{Channel: "fcm", AppID: testApp, Token: fcmToken(t)}
		for range 2 {
			expect(t, e.call(http.MethodPost, pushproto.PathChallenges, req, asDevice(d), from("198.51.100.7")), http.StatusAccepted, "")
		}
		w := e.call(http.MethodPost, pushproto.PathChallenges, req, asDevice(d), from("198.51.100.7"))
		expect(t, w, http.StatusTooManyRequests, pushproto.CodeRateLimited)
		retryAfter(t, w, 1800)
		expect(t, e.call(http.MethodPost, pushproto.PathChallenges, req, asDevice(d), from("198.51.100.8")), http.StatusAccepted, "")
		e.clk.Add(30 * time.Minute)
		expect(t, e.call(http.MethodPost, pushproto.PathChallenges, req, asDevice(d), from("198.51.100.7")), http.StatusAccepted, "")
	})

	t.Run("grant per minute and day", func(t *testing.T) {
		e := newEnv(t, func(c *Config) { c.Limits.GrantPerMinute, c.Limits.GrantPerDay = 2, 3 })
		h := newHost(t)
		g := e.grant(newDevice(t), h, "fcm", "", fcmToken(t))
		other := e.grant(newDevice(t), h, "fcm", "", fcmToken(t))
		for range 2 {
			expect(t, e.push(h, pushproto.PushRequest{Grant: g, ID: "n", E: ciph}), http.StatusAccepted, "")
		}
		w := e.push(h, pushproto.PushRequest{Grant: g, ID: "n", E: ciph})
		expect(t, w, http.StatusTooManyRequests, pushproto.CodeRateLimited)
		retryAfter(t, w, 30)
		// 别的授权不受影响
		expect(t, e.push(h, pushproto.PushRequest{Grant: other, ID: "n", E: ciph}), http.StatusAccepted, "")
		e.clk.Add(time.Minute)
		expect(t, e.push(h, pushproto.PushRequest{Grant: g, ID: "n", E: ciph}), http.StatusAccepted, "")
		// 每天 3 条：剩余补充时间 = 8 小时 - 已过的 1 分钟
		w = e.push(h, pushproto.PushRequest{Grant: g, ID: "n", E: ciph})
		expect(t, w, http.StatusTooManyRequests, pushproto.CodeRateLimited)
		retryAfter(t, w, 8*3600-60)
		_, sends := e.google.Snapshot()
		if len(sends) != 2+4 {
			t.Errorf("rate-limited pushes reached FCM: %d sends", len(sends))
		}
	})

	t.Run("host per minute", func(t *testing.T) {
		e := newEnv(t, func(c *Config) { c.Limits.HostPerMinute = 3 })
		h := newHost(t)
		g1 := e.grant(newDevice(t), h, "fcm", "", fcmToken(t))
		g2 := e.grant(newDevice(t), h, "fcm", "", fcmToken(t))
		for _, g := range []string{g1, g2, g1} {
			expect(t, e.push(h, pushproto.PushRequest{Grant: g, ID: "n", E: ciph}), http.StatusAccepted, "")
		}
		w := e.push(h, pushproto.PushRequest{Grant: g2, ID: "n", E: ciph})
		expect(t, w, http.StatusTooManyRequests, pushproto.CodeRateLimited)
		retryAfter(t, w, 20)
		// 别的主机不受影响
		h2 := newHost(t)
		g3 := e.grant(newDevice(t), h2, "fcm", "", fcmToken(t))
		expect(t, e.push(h2, pushproto.PushRequest{Grant: g3, ID: "n", E: ciph}), http.StatusAccepted, "")
	})
}

func TestTooBig(t *testing.T) {
	e := newEnv(t, nil)
	d, h := newDevice(t), newHost(t)
	g := e.grant(d, h, pushproto.ChannelFCM, "", fcmToken(t))
	expect(t, e.push(h, pushproto.PushRequest{Grant: g, ID: "n1", E: strings.Repeat("A", pushproto.MaxCipher+1)}), http.StatusRequestEntityTooLarge, pushproto.CodeTooBig)
	expect(t, e.push(h, pushproto.PushRequest{Grant: g, ID: "n1", E: strings.Repeat("A", pushproto.MaxCipher)}), http.StatusAccepted, "")
	big := []byte(`{"grant":"` + g + `","id":"n1","e":"` + strings.Repeat("A", pushproto.MaxBody) + `"}`)
	expect(t, e.call(http.MethodPost, pushproto.PathPush, big, asHost(h)), http.StatusRequestEntityTooLarge, pushproto.CodeTooBig)
	expect(t, e.push(h, pushproto.PushRequest{Grant: g, ID: "n1", E: "not base64!"}), http.StatusBadRequest, pushproto.CodeMalformed)
	expect(t, e.push(h, pushproto.PushRequest{Grant: g, ID: "n1", Collapse: strings.Repeat("c", 65), E: "AAAA"}), http.StatusBadRequest, pushproto.CodeMalformed)
}

func TestTokenEncryptedAtRest(t *testing.T) {
	e := newEnv(t, nil)
	h := newHost(t)
	ft, at := fcmToken(t), apnsToken(t)
	g1 := e.grant(newDevice(t), h, pushproto.ChannelFCM, "", ft)
	g2 := e.grant(newDevice(t), h, pushproto.ChannelAPNs, "", at)
	for id, tok := range map[string]string{g1: ft, g2: at} {
		got, err := e.srv.store.Get(context.Background(), id)
		if err != nil || got.Token != tok {
			t.Fatalf("Get(%s) = %+v, %v", id, got, err)
		}
	}
	// 密文绑定授权 id：挪到别的行解不开
	if _, err := e.srv.store.db.Exec(`UPDATE grants SET token_enc = (SELECT token_enc FROM grants WHERE id = ?) WHERE id = ?`, g1, g2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.store.Get(context.Background(), g2); err == nil {
		t.Error("token ciphertext moved between grants still decrypts")
	}
	if err := e.srv.Close(); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(e.cfg.DataDir, DBFile+"*"))
	if len(files) == 0 {
		t.Fatal("no database files")
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, tok := range []string{ft, at, strings.ToUpper(at)} {
			if bytes.Contains(raw, []byte(tok)) {
				t.Errorf("%s contains a push token", filepath.Base(f))
			}
		}
		if bytes.Contains(raw, mustHex(t, at)) {
			t.Errorf("%s contains the raw APNs token bytes", filepath.Base(f))
		}
	}
}

func TestRetentionCleanup(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	day := 24 * time.Hour
	h := newHost(t)
	ciph, _ := cipher(t, plaintext)
	exists := func(id string) bool {
		g, err := e.srv.store.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return g != nil
	}
	used := e.grant(newDevice(t), h, "fcm", "", fcmToken(t))
	idle := e.grant(newDevice(t), h, "fcm", "", fcmToken(t))
	revokedDev := newDevice(t)
	revoked := e.grant(revokedDev, h, "fcm", "", fcmToken(t))
	expect(t, e.call(http.MethodDelete, pushproto.GrantPath(revoked), nil, asDevice(revokedDev)), http.StatusNoContent, "")
	t0 := e.clk.Now().UnixMilli()

	// last_used_at 至多每小时写一次
	e.clk.Add(30 * time.Minute)
	expect(t, e.push(h, pushproto.PushRequest{Grant: used, ID: "n", E: ciph}), http.StatusAccepted, "")
	if g, _ := e.srv.store.Get(ctx, used); g.LastUsedAt != t0 {
		t.Errorf("last_used_at written within the hour: %d", g.LastUsedAt-t0)
	}
	e.clk.Add(31 * time.Minute)
	expect(t, e.push(h, pushproto.PushRequest{Grant: used, ID: "n", E: ciph}), http.StatusAccepted, "")
	if g, _ := e.srv.store.Get(ctx, used); g.LastUsedAt != e.clk.Now().UnixMilli() {
		t.Errorf("last_used_at not updated after an hour")
	}

	e.clk.Add(6 * day)
	e.srv.cleanup(ctx)
	if !exists(revoked) {
		t.Fatal("revoked grant deleted before 7 days")
	}
	e.clk.Add(day)
	e.srv.cleanup(ctx)
	if exists(revoked) || !exists(used) || !exists(idle) {
		t.Fatalf("after 7 days: revoked=%v used=%v idle=%v", exists(revoked), exists(used), exists(idle))
	}

	expect(t, e.push(h, pushproto.PushRequest{Grant: used, ID: "n", E: ciph}), http.StatusAccepted, "")
	lastUse := e.clk.Now()
	e.clk.Add(time.Duration(t0-lastUse.UnixMilli())*time.Millisecond + 90*day)
	e.srv.cleanup(ctx)
	if !exists(idle) {
		t.Fatal("idle grant deleted at exactly 90 days")
	}
	e.clk.Add(time.Millisecond)
	e.srv.cleanup(ctx)
	if exists(idle) || !exists(used) {
		t.Fatalf("after 90 days idle: idle=%v used=%v", exists(idle), exists(used))
	}
	e.clk.Add(lastUse.Add(90 * day).Sub(e.clk.Now()))
	e.srv.cleanup(ctx)
	if !exists(used) {
		t.Fatal("used grant deleted at exactly 90 days since last use")
	}
	e.clk.Add(time.Millisecond)
	e.srv.cleanup(ctx)
	if exists(used) {
		t.Fatal("used grant kept after 90 days idle")
	}
}

func TestLogsOmitSecrets(t *testing.T) {
	e := newEnv(t, nil)
	d, h := newDevice(t), newHost(t)
	ft, at := fcmToken(t), apnsToken(t)
	ciph, _ := cipher(t, plaintext)
	var nonces []string

	id, nonce := e.challenge(d, "fcm", "", ft)
	nonces = append(nonces, nonce)
	w := e.call(http.MethodPost, pushproto.PathGrants, pushproto.GrantRequest{ChallengeID: id, Nonce: nonce, HostKey: h.pub}, asDevice(d))
	var gr pushproto.GrantResponse
	_ = json.Unmarshal(w.Body.Bytes(), &gr)
	e.verify(h, gr.Grant)
	e.push(h, pushproto.PushRequest{Grant: gr.Grant, ID: "n1", Collapse: "c1", E: ciph})
	e.push(h, pushproto.PushRequest{Grant: gr.Grant, ID: "n1", Withdraw: true, E: ciph})
	e.google.SetErr(ft, fcmtest.Err{Status: 404, Body: `{"error":{"code":404,"status":"NOT_FOUND","details":[{"errorCode":"UNREGISTERED"}],"message":"` + ft + `"}}`})
	expect(t, e.push(h, pushproto.PushRequest{Grant: gr.Grant, ID: "n2", E: ciph}), http.StatusGone, pushproto.CodeDeviceUnregistered)

	d2 := newDevice(t)
	id, nonce = e.challenge(d2, "apns", "", at)
	nonces = append(nonces, nonce)
	e.call(http.MethodPost, pushproto.PathGrants, pushproto.GrantRequest{ChallengeID: id, Nonce: "wrongwrongwrongwrong00", HostKey: h.pub}, asDevice(d2))
	g2 := e.grant(d2, h, "apns", "", at)
	nonces = append(nonces, e.apns.Requests()[len(e.apns.Requests())-1].Body["nonce"].(string))
	e.apns.SetErr(at, apnstest.Reply{Status: 500, Reason: "InternalServerError"})
	expect(t, e.push(h, pushproto.PushRequest{Grant: g2, ID: "n3", E: ciph}), http.StatusBadGateway, pushproto.CodeUpstream)
	e.call(http.MethodPost, pushproto.PathChallenges, pushproto.ChallengeRequest{Channel: "fcm", AppID: testApp, Token: ft}, asDevice(d2), func(r *http.Request) {
		r.RemoteAddr = "127.0.0.1:1"
		r.Header.Set("X-Real-IP", "203.0.113.99")
	})

	logs := e.logs.String()
	secrets := map[string]string{"fcm token": ft, "apns token": at, "e": ciph, "device key": d.pub, "host key": h.pub, "client ip": "203.0.113.99", "plaintext": "secret"}
	for i, n := range nonces {
		secrets["nonce "+strconv.Itoa(i)] = n
	}
	for name, s := range secrets {
		if strings.Contains(logs, s) {
			t.Errorf("logs contain the %s", name)
		}
	}
	for _, want := range []string{"grant=" + gr.Grant, "grant=" + g2, "host=" + hostHash(h.pub), "upstream=UNREGISTERED", "upstream=InternalServerError", "status=410", `route="POST /v1/push"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %q", want)
		}
	}
	if t.Failed() {
		t.Log(logs)
	}
}

func TestConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, "gateway.yaml")
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	c, err := Load(write("listen: 127.0.0.1:17700\npublic_origin: http://localhost:17700\ndata_dir: ./data\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(c.DataDir) || c.Limits.GrantPerMinute != 60 || c.Limits.GrantPerDay != 2000 || c.Limits.HostPerMinute != 600 || c.Limits.ChallengesPerIPHour != 30 {
		t.Errorf("defaults: %+v", c)
	}
	bad := []string{
		"listen: x\npublic_origin: https://push.test\ndata_dir: d\nunknown: 1\n",
		"public_origin: http://push.test\ndata_dir: d\n",
		"public_origin: https://push.test/path\ndata_dir: d\n",
		"public_origin: ftp://push.test\ndata_dir: d\n",
		"public_origin: https://push.test\ndata_dir: d\napns: {key_file: k.p8}\n",
		"public_origin: https://push.test\ndata_dir: d\napns: {key_file: k.p8, key_id: K, team_id: T, topic: t, environments: [staging]}\n",
		"public_origin: https://push.test\ndata_dir: d\nfcm: {service_account_file: sa.json}\n",
		"public_origin: https://push.test\ndata_dir: d\nfcm: {upstream: http://x}\n",
		"public_origin: https://push.test\ndata_dir: d\nlimits: {grant_per_minute: 0}\n",
		"public_origin: https://push.test\ndata_dir: d\nlog_level: loud\n",
	}
	for _, s := range bad {
		if _, err := Load(write(s)); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
	for _, o := range []string{"https://push.oryn.best", "http://127.0.0.1:7700", "http://[::1]:7700", "http://localhost"} {
		if err := validOrigin(o); err != nil {
			t.Errorf("validOrigin(%s): %v", o, err)
		}
	}
}

func b64Decode(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
