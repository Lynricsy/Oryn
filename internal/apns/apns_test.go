package apns_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Lynricsy/Oryn/internal/apns"
	"github.com/Lynricsy/Oryn/internal/apns/apnstest"
)

const tok = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func setup(t *testing.T) (*apns.Client, *apnstest.Server, *clock) {
	t.Helper()
	pemKey, priv := apnstest.Key(t)
	key, err := apns.ParseKey(pemKey)
	if err != nil {
		t.Fatal(err)
	}
	if !key.Equal(priv) {
		t.Fatal("ParseKey returned a different key")
	}
	srv := apnstest.New(t, &priv.PublicKey)
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	c := &apns.Client{
		HTTP: srv.Client(), Key: key, KeyID: apnstest.KeyID, TeamID: apnstest.TeamID,
		ProductionURL: srv.URL("prod"), SandboxURL: srv.URL("sandbox"), Now: clk.Now,
	}
	return c, srv, clk
}

func note(env string) apns.Notification {
	return apns.Notification{
		Token: tok, Environment: env, Topic: "com.lynricsy.oryn", PushType: "alert", Priority: 10,
		Payload: []byte(`{"aps":{"alert":{"title":"Oryn","body":"x"}}}`),
	}
}

func TestSendHeadersAndProviderToken(t *testing.T) {
	c, srv, clk := setup(t)
	n := note(apns.EnvProduction)
	n.Expiration = clk.Now().Add(24 * time.Hour)
	n.CollapseID = "abc"
	if r := c.Send(context.Background(), n); r.Err != nil {
		t.Fatalf("send: %+v", r)
	}
	if r := c.Send(context.Background(), note(apns.EnvSandbox)); r.Err != nil {
		t.Fatalf("send sandbox: %+v", r)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d", len(reqs))
	}
	p := reqs[0]
	if p.Env != "prod" || p.Token != tok || p.Proto != 2 {
		t.Errorf("env=%s token=%s proto=%d", p.Env, p.Token, p.Proto)
	}
	want := map[string]string{
		"apns-topic": "com.lynricsy.oryn", "apns-push-type": "alert", "apns-priority": "10",
		"apns-expiration": "1800086400", "apns-collapse-id": "abc", "Content-Type": "application/json",
	}
	for k, v := range want {
		if got := p.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if p.Claims["iss"] != apnstest.TeamID || p.Claims["iat"] != float64(1_800_000_000) {
		t.Errorf("claims = %v", p.Claims)
	}
	s := reqs[1]
	if s.Env != "sandbox" || s.Header.Get("apns-expiration") != "0" || s.Header.Values("apns-collapse-id") != nil {
		t.Errorf("sandbox request: env=%s exp=%q collapse=%v", s.Env, s.Header.Get("apns-expiration"), s.Header.Values("apns-collapse-id"))
	}
	if s.JWT != p.JWT {
		t.Error("provider token was not cached")
	}

	clk.Add(49 * time.Minute)
	c.Send(context.Background(), note(apns.EnvProduction))
	clk.Add(2 * time.Minute)
	c.Send(context.Background(), note(apns.EnvProduction))
	reqs = srv.Requests()
	if reqs[2].JWT != p.JWT {
		t.Error("provider token renewed before 50 minutes")
	}
	if reqs[3].JWT == p.JWT || reqs[3].Claims["iat"] != float64(1_800_000_000+51*60) {
		t.Errorf("provider token not renewed after 50 minutes: %v", reqs[3].Claims)
	}
}

func TestProviderTokenRejectedRetriesOnce(t *testing.T) {
	for _, reason := range []string{"ExpiredProviderToken", "InvalidProviderToken"} {
		t.Run(reason, func(t *testing.T) {
			c, srv, clk := setup(t)
			c.Send(context.Background(), note(apns.EnvProduction))
			clk.Add(time.Second)
			srv.RejectProviderToken(1, reason)
			if r := c.Send(context.Background(), note(apns.EnvProduction)); r.Err != nil {
				t.Fatalf("send after rejection: %+v", r)
			}
			reqs := srv.Requests()
			if len(reqs) != 3 || reqs[1].JWT != reqs[0].JWT || reqs[2].JWT == reqs[1].JWT {
				t.Fatalf("want cached → rejected → re-signed, got %d requests", len(reqs))
			}
			// 两次都被拒：只重试一次
			srv.RejectProviderToken(2, reason)
			r := c.Send(context.Background(), note(apns.EnvProduction))
			if r.Status != http.StatusForbidden || r.Reason != reason || len(srv.Requests()) != 5 {
				t.Fatalf("second rejection: %+v, requests=%d", r, len(srv.Requests()))
			}
		})
	}
}

func TestSendClassifiesErrors(t *testing.T) {
	c, srv, _ := setup(t)
	cases := []struct {
		token        string
		reply        apnstest.Reply
		unregistered bool
		mismatch     bool
		retry        bool
	}{
		{strings.Repeat("a", 64), apnstest.Reply{Status: 410, Reason: "Unregistered"}, true, false, false},
		{strings.Repeat("b", 64), apnstest.Reply{Status: 400, Reason: "BadDeviceToken"}, true, false, false},
		{strings.Repeat("c", 64), apnstest.Reply{Status: 400, Reason: "DeviceTokenNotForTopic"}, false, true, false},
		{strings.Repeat("d", 64), apnstest.Reply{Status: 429, Reason: "TooManyRequests"}, false, false, true},
		{strings.Repeat("e", 64), apnstest.Reply{Status: 503, Reason: "ServiceUnavailable"}, false, false, true},
		{strings.Repeat("f", 64), apnstest.Reply{Status: 400, Reason: "PayloadTooLarge"}, false, false, false},
	}
	for _, tc := range cases {
		srv.SetErr(tc.token, tc.reply)
		n := note(apns.EnvProduction)
		n.Token = tc.token
		r := c.Send(context.Background(), n)
		if r.Err == nil || r.Reason != tc.reply.Reason || r.Unregistered != tc.unregistered || r.TopicMismatch != tc.mismatch || r.Retry != tc.retry {
			t.Errorf("%s: %+v", tc.reply.Reason, r)
		}
		if strings.Contains(r.Err.Error(), tc.token) {
			t.Errorf("%s: error contains device token", tc.reply.Reason)
		}
	}
}

func TestSendNetworkErrorOmitsToken(t *testing.T) {
	c, srv, _ := setup(t)
	srv.Close()
	r := c.Send(context.Background(), note(apns.EnvProduction))
	if r.Err == nil || !r.Retry || r.Status != 0 || strings.Contains(r.Err.Error(), tok) {
		t.Fatalf("network error: %+v", r)
	}
}

func TestSendRejectsUnknownEnvironmentAndBigPayload(t *testing.T) {
	c, _, _ := setup(t)
	if r := c.Send(context.Background(), note("staging")); r.Err == nil {
		t.Error("unknown environment accepted")
	}
	n := note(apns.EnvProduction)
	n.Payload = make([]byte, apns.MaxPayload+1)
	if r := c.Send(context.Background(), n); r.Err == nil {
		t.Error("oversized payload accepted")
	}
}

func TestParseKeyRejects(t *testing.T) {
	for _, raw := range []string{"", "not pem", "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"} {
		if _, err := apns.ParseKey([]byte(raw)); !errors.Is(err, apns.ErrInvalidKey) {
			t.Errorf("ParseKey(%q) = %v", raw, err)
		}
	}
}
