package pushgw

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Lynricsy/Oryn/internal/apns/apnstest"
	"github.com/Lynricsy/Oryn/internal/fcm/fcmtest"
	"github.com/Lynricsy/Oryn/internal/pushproto"
	"github.com/Lynricsy/Oryn/internal/webpush"
	"github.com/Lynricsy/Oryn/internal/webpush/webpushtest"
)

const (
	testOrigin = "https://push.test"
	testApp    = "com.lynricsy.oryn"
)

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

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// env 是一个网关实例与它的假上游。
type env struct {
	t      *testing.T
	cfg    Config
	srv    *Server
	h      http.Handler
	clk    *clock
	google *fcmtest.Google
	apns   *apnstest.Server
	logs   *syncBuffer
}

func newEnv(t *testing.T, mod func(*Config)) *env {
	t.Helper()
	dir := t.TempDir()
	sa, rsaPub := fcmtest.ServiceAccount(t)
	saFile := filepath.Join(dir, "fcm.json")
	p8, ecKey := apnstest.Key(t)
	p8File := filepath.Join(dir, "AuthKey_"+apnstest.KeyID+".p8")
	if err := os.WriteFile(saFile, []byte(sa), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p8File, p8, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.PublicOrigin = testOrigin + "/"
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.LogLevel = "debug"
	cfg.APNs = APNs{KeyFile: p8File, KeyID: apnstest.KeyID, TeamID: apnstest.TeamID, Topic: testApp}
	cfg.FCM = FCM{ServiceAccountFile: saFile, AppID: testApp}
	if mod != nil {
		mod(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	e := &env{
		t: t, cfg: cfg, clk: &clock{t: time.UnixMilli(1_800_000_000_000)}, logs: &syncBuffer{},
		google: fcmtest.NewGoogle(t, rsaPub), apns: apnstest.New(t, &ecKey.PublicKey),
	}
	srv, err := New(context.Background(), cfg, Options{
		Log: slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now: e.clk.Now,
		// 假 Google 是明文 HTTP，假 APNs 是 HTTP/2 TLS
		FCMHTTP: e.google.Server.Client(), FCMTokenURL: e.google.TokenURL(), FCMAPIBase: e.google.Server.URL,
		APNsHTTP: e.apns.Client(), APNsProductionURL: e.apns.URL("prod"), APNsSandboxURL: e.apns.URL("sandbox"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	e.srv, e.h = srv, srv.Handler()
	return e
}

type device struct {
	key *ecdsa.PrivateKey
	pub string
}

func newDevice(t *testing.T) device {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := k.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return device{key: k, pub: base64.RawURLEncoding.EncodeToString(raw)}
}

type host struct {
	key ed25519.PrivateKey
	pub string
}

func newHost(t *testing.T) host {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return host{key: k, pub: pushproto.HostKeyString(k.Public().(ed25519.PublicKey))}
}

// signer 给请求加签名头。
type signer func(e *env, r *http.Request, body []byte)

// asDevice 以设备密钥现签（ECDSA P-256 / SHA-256，DER）。
func asDevice(d device) signer {
	return func(e *env, r *http.Request, body []byte) {
		ts := e.clk.Now().UnixMilli()
		sum := sha256.Sum256(pushproto.SigningInput(r.Method, testOrigin, r.URL.EscapedPath(), ts, body))
		sig, err := ecdsa.SignASN1(rand.Reader, d.key, sum[:])
		if err != nil {
			e.t.Fatal(err)
		}
		r.Header.Set(pushproto.HeaderDeviceKey, d.pub)
		r.Header.Set(pushproto.HeaderTimestamp, strconv.FormatInt(ts, 10))
		r.Header.Set(pushproto.HeaderSignature, base64.RawURLEncoding.EncodeToString(sig))
	}
}

func asHost(h host) signer {
	return func(e *env, r *http.Request, body []byte) {
		pushproto.SignHost(r, testOrigin, h.key, e.clk.Now(), body)
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// call 发一个请求；body 为 nil 时请求体为空，为 []byte 时原样发送，否则编码为 JSON。
func (e *env) call(method, path string, body any, sign signer, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	e.t.Helper()
	var raw []byte
	switch b := body.(type) {
	case nil:
	case []byte:
		raw = b
	default:
		raw = mustJSON(b)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if sign != nil {
		sign(e, r, raw)
	}
	for _, m := range mod {
		m(r)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

// expect 断言状态码与（非空时）错误码。
func expect(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d; body %s", w.Code, status, w.Body.String())
	}
	if code == "" {
		return
	}
	var eb pushproto.ErrorBody
	if err := json.Unmarshal(w.Body.Bytes(), &eb); err != nil || eb.Code != code || eb.Message == "" {
		t.Fatalf("error body = %s, want code %q", w.Body.String(), code)
	}
}

// challenge 发起 challenge 并从假上游取回推送里的 challengeId 与 nonce。
func (e *env) challenge(d device, channel, environment, token string) (id, nonce string) {
	e.t.Helper()
	w := e.call(http.MethodPost, pushproto.PathChallenges, pushproto.ChallengeRequest{
		Channel: channel, AppID: testApp, Environment: environment, Token: token,
	}, asDevice(d))
	expect(e.t, w, http.StatusAccepted, "")
	var cr pushproto.ChallengeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &cr); err != nil || !pushproto.IDPattern.MatchString(cr.ChallengeID) {
		e.t.Fatalf("challenge response %s", w.Body.String())
	}
	var data map[string]any
	if channel == pushproto.ChannelFCM {
		_, sends := e.google.Snapshot()
		data = sends[len(sends)-1].Body["message"].(map[string]any)["data"].(map[string]any)
	} else {
		reqs := e.apns.Requests()
		data = reqs[len(reqs)-1].Body
	}
	if data["challengeId"] != cr.ChallengeID {
		e.t.Fatalf("pushed challengeId %v, response %s", data["challengeId"], cr.ChallengeID)
	}
	return cr.ChallengeID, data["nonce"].(string)
}

// grant 走完整的 challenge → grant，返回授权 id。
func (e *env) grant(d device, h host, channel, environment, token string) string {
	e.t.Helper()
	id, nonce := e.challenge(d, channel, environment, token)
	w := e.call(http.MethodPost, pushproto.PathGrants, pushproto.GrantRequest{ChallengeID: id, Nonce: nonce, HostKey: h.pub}, asDevice(d))
	expect(e.t, w, http.StatusCreated, "")
	var gr pushproto.GrantResponse
	if err := json.Unmarshal(w.Body.Bytes(), &gr); err != nil || !pushproto.IDPattern.MatchString(gr.Grant) || gr.CreatedAt != e.clk.Now().UnixMilli() {
		e.t.Fatalf("grant response %s", w.Body.String())
	}
	return gr.Grant
}

func (e *env) push(h host, req pushproto.PushRequest) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.call(http.MethodPost, pushproto.PathPush, req, asHost(h))
}

func (e *env) verify(h host, grant string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.call(http.MethodPost, pushproto.PathVerify, pushproto.VerifyRequest{Grant: grant}, asHost(h))
}

func fcmToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "fcm-" + base64.RawURLEncoding.EncodeToString(b) + ":APA91b" + hex.EncodeToString(b)
}

func apnsToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// cipher 用 webpush 造一段 RFC 8291 密文（e），返回 e 与能解开它的接收端。
func cipher(t *testing.T, plain string) (string, *webpushtest.Client) {
	t.Helper()
	cl, err := webpushtest.NewClient("https://push.test/v1/grants/x")
	if err != nil {
		t.Fatal(err)
	}
	ct, err := webpush.Encrypt([]byte(plain), cl.Sub, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(ct), cl
}
