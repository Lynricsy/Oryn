package api_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	h2PublicURL = "https://oryn.test"
	h2RPID      = "oryn.test"
)

// h2Env 是一套进程内控制面（只装配 H2 依赖）。
type h2Env struct {
	t       *testing.T
	srv     *httptest.Server
	st      *store.Store
	dataDir string
	vault   *secrets.Vault
	tickets *auth.Tickets
	svc     *auth.Service
}

func newH2Env(t *testing.T) *h2Env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.LoadOrCreate(dir)
	require.NoError(t, err)
	home, err := agentcfg.NewHome(dir)
	require.NoError(t, err)
	projectHomes, err := agentcfg.NewProjectHomes(dir)
	require.NoError(t, err)
	svc, err := auth.NewService(st, dir, h2PublicURL)
	require.NoError(t, err)
	cfg := config.DefaultOrynd()
	cfg.PublicURL, cfg.DataDir = h2PublicURL, dir
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st, AuthMiddleware: auth.Middleware(st)}}
	e := &h2Env{t: t, st: st, dataDir: dir, vault: secrets.NewVault(st, box), tickets: auth.NewTickets(), svc: svc}
	s.Auth = &api.AuthDeps{Service: svc, Tickets: e.tickets, Vault: e.vault, Home: home, ProjectHomes: projectHomes}
	s.Mounts = map[string]http.Handler{api.AppSessionPath: s.AppSessionHandler()}
	e.srv = httptest.NewServer(api.NewRouter(s))
	t.Cleanup(e.srv.Close)
	return e
}

// h2Cred 描述一次请求使用的凭据。
type h2Cred struct {
	bearer string
	cookie string
	csrf   bool
}

type h2Resp struct {
	status  int
	body    []byte
	cookies []*http.Cookie
}

func (r h2Resp) json(t *testing.T, v any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(r.body, v), string(r.body))
}

func (r h2Resp) code(t *testing.T) string {
	t.Helper()
	var e struct{ Code string }
	_ = json.Unmarshal(r.body, &e)
	return e.Code
}

func (r h2Resp) session() string {
	for _, c := range r.cookies {
		if c.Name == auth.CookieName {
			return c.Value
		}
	}
	return ""
}

func (e *h2Env) do(method, path string, cred h2Cred, body any) h2Resp {
	e.t.Helper()
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case []byte:
		rd, ct = bytes.NewReader(b), "application/octet-stream"
	default:
		buf, err := json.Marshal(b)
		require.NoError(e.t, err)
		rd, ct = bytes.NewReader(buf), "application/json"
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	require.NoError(e.t, err)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if cred.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+cred.bearer)
	}
	if cred.cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cred.cookie})
	}
	if cred.csrf {
		req.Header.Set(auth.CSRFHeader, "1")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(e.t, err)
	return h2Resp{status: resp.StatusCode, body: b, cookies: resp.Cookies()}
}

func (e *h2Env) webSession() string {
	e.t.Helper()
	tok, err := auth.CreateWebSession(context.Background(), e.st, "test")
	require.NoError(e.t, err)
	return tok
}

func TestDeviceTokenAndRevoke(t *testing.T) {
	e := newH2Env(t)
	ctx := context.Background()
	// 与 `orynd admin create-device` 同一路径
	dev, tok, err := auth.CreateDevice(ctx, e.st, "verify", "cli", "")
	require.NoError(t, err)

	r := e.do("GET", "/api/v1/me", h2Cred{bearer: tok}, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var me struct {
		Auth struct{ Kind, DeviceID string }
	}
	r.json(t, &me)
	assert.Equal(t, "device", me.Auth.Kind)
	assert.Equal(t, dev.ID, me.Auth.DeviceID)

	// 设备令牌做变更请求无需 CSRF 头
	r = e.do("POST", "/api/v1/ws-ticket", h2Cred{bearer: tok}, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var tk struct {
		Ticket    string
		ExpiresIn int
	}
	r.json(t, &tk)
	assert.Equal(t, 30, tk.ExpiresIn)
	who, ok := e.tickets.Redeem(tk.Ticket)
	require.True(t, ok)
	assert.Equal(t, auth.Principal{Kind: auth.KindDevice, ID: dev.ID}, who, "票据记录签发主体")

	r = e.do("GET", "/api/v1/devices", h2Cred{bearer: tok}, nil)
	require.Equal(t, 200, r.status)
	var list struct {
		Devices []struct {
			ID      string
			Current bool
		}
	}
	r.json(t, &list)
	require.Len(t, list.Devices, 1)
	assert.True(t, list.Devices[0].Current)

	assert.Equal(t, 404, e.do("DELETE", "/api/v1/devices/dev_01JAAAAAAAAAAAAAAAAAAAAAAA", h2Cred{bearer: tok}, nil).status)
	require.Equal(t, 204, e.do("DELETE", "/api/v1/devices/"+dev.ID, h2Cred{bearer: tok}, nil).status)
	r = e.do("GET", "/api/v1/me", h2Cred{bearer: tok}, nil)
	assert.Equal(t, 401, r.status)
	assert.Equal(t, "unauthorized", r.code(t))

	// 其它令牌形态一律 401
	assert.Equal(t, 401, e.do("GET", "/api/v1/me", h2Cred{bearer: "opv_xxx"}, nil).status)
	assert.Equal(t, 401, e.do("GET", "/api/v1/me", h2Cred{}, nil).status)
}

func TestDevicePairing(t *testing.T) {
	e := newH2Env(t)
	r := e.do("POST", "/api/v1/devices/pair/start", h2Cred{}, map[string]string{"name": "laptop", "kind": "cli", "platform": "linux-x64"})
	require.Equal(t, 200, r.status, string(r.body))
	var start struct {
		Code, Verifier, VerifyURL string
		ExpiresAt                 int64
		Interval                  int
	}
	r.json(t, &start)
	assert.Regexp(t, `^[A-Z2-9]{4}-[A-Z2-9]{4}$`, start.Code)
	assert.Equal(t, h2PublicURL+"/pair", start.VerifyURL, "不预填配对码")
	assert.Equal(t, 2, start.Interval)

	poll := map[string]string{"code": start.Code, "verifier": start.Verifier}
	assert.Equal(t, 202, e.do("POST", "/api/v1/devices/pair/poll", h2Cred{}, poll).status)
	assert.Equal(t, 403, e.do("POST", "/api/v1/devices/pair/poll", h2Cred{}, map[string]string{"code": start.Code, "verifier": "wrong"}).status)

	// 批准需要网页会话：设备令牌不行，缺 CSRF 头也不行
	_, devTok, err := auth.CreateDevice(context.Background(), e.st, "other", "cli", "")
	require.NoError(t, err)
	approve := map[string]string{"code": start.Code}
	assert.Equal(t, 403, e.do("POST", "/api/v1/devices/pair/approve", h2Cred{bearer: devTok}, approve).status)
	web := e.webSession()

	// 批准前可查看发起方信息（仅网页会话）
	assert.Equal(t, 403, e.do("GET", "/api/v1/devices/pair/"+start.Code, h2Cred{bearer: devTok}, nil).status)
	assert.Equal(t, 401, e.do("GET", "/api/v1/devices/pair/"+start.Code, h2Cred{}, nil).status)
	assert.Equal(t, 404, e.do("GET", "/api/v1/devices/pair/ZZZZ-ZZZZ", h2Cred{cookie: web}, nil).status)
	r = e.do("GET", "/api/v1/devices/pair/"+strings.ToLower(start.Code), h2Cred{cookie: web}, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var pending struct {
		Code, Name, Kind, Platform, IP, UserAgent string
		CreatedAt, ExpiresAt                      int64
	}
	r.json(t, &pending)
	assert.Equal(t, start.Code, pending.Code)
	assert.Equal(t, "laptop", pending.Name)
	assert.Equal(t, "127.0.0.1", pending.IP)
	assert.Contains(t, pending.UserAgent, "Go-http-client")
	assert.Equal(t, start.ExpiresAt, pending.ExpiresAt)

	r = e.do("POST", "/api/v1/devices/pair/approve", h2Cred{cookie: web}, approve)
	assert.Equal(t, 403, r.status)
	assert.Equal(t, "forbidden", r.code(t))
	// 小写、无分隔符的输入同样有效
	r = e.do("POST", "/api/v1/devices/pair/approve", h2Cred{cookie: web, csrf: true}, map[string]string{"code": "  " + start.Code[:4] + start.Code[5:]})
	require.Equal(t, 200, r.status, string(r.body))
	assert.Equal(t, 409, e.do("POST", "/api/v1/devices/pair/approve", h2Cred{cookie: web, csrf: true}, approve).status)
	assert.Equal(t, 409, e.do("GET", "/api/v1/devices/pair/"+start.Code, h2Cred{cookie: web}, nil).status)

	r = e.do("POST", "/api/v1/devices/pair/poll", h2Cred{}, poll)
	require.Equal(t, 200, r.status, string(r.body))
	var res struct {
		DeviceToken string
		Device      struct{ ID, Name, Kind string }
	}
	r.json(t, &res)
	assert.Equal(t, "laptop", res.Device.Name)
	// 令牌只交付一次
	assert.Equal(t, 404, e.do("POST", "/api/v1/devices/pair/poll", h2Cred{}, poll).status)

	r = e.do("GET", "/api/v1/me", h2Cred{bearer: res.DeviceToken}, nil)
	require.Equal(t, 200, r.status)
	assert.Contains(t, string(r.body), res.Device.ID)
}

// h2Authenticator 是手工构造的 ES256 平台认证器（attestation=none，可发现凭据）。
type h2Authenticator struct {
	t      *testing.T
	key    *ecdsa.PrivateKey
	credID []byte
	handle []byte
	count  uint32
}

func newH2Authenticator(t *testing.T) *h2Authenticator {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &h2Authenticator{t: t, key: key, credID: id}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (a *h2Authenticator) authData(flags protocol.AuthenticatorFlags, attested []byte) []byte {
	h := sha256.Sum256([]byte(h2RPID))
	d := append([]byte{}, h[:]...)
	d = append(d, byte(flags))
	d = binary.BigEndian.AppendUint32(d, a.count)
	return append(d, attested...)
}

func (a *h2Authenticator) clientData(typ string, options map[string]any) []byte {
	b, err := json.Marshal(map[string]any{"type": typ, "challenge": options["challenge"], "origin": h2PublicURL, "crossOrigin": false})
	require.NoError(a.t, err)
	return b
}

// create 模拟 navigator.credentials.create，返回 RegistrationResponseJSON。
func (a *h2Authenticator) create(options map[string]any) map[string]any {
	user := options["user"].(map[string]any)
	h, err := base64.RawURLEncoding.DecodeString(user["id"].(string))
	require.NoError(a.t, err)
	a.handle = h
	pub, err := webauthncbor.Marshal(map[int64]any{
		1: int64(webauthncose.EllipticKey), 3: int64(webauthncose.AlgES256), -1: int64(webauthncose.P256),
		-2: a.key.X.FillBytes(make([]byte, 32)), -3: a.key.Y.FillBytes(make([]byte, 32)),
	})
	require.NoError(a.t, err)
	attested := make([]byte, 16) // AAGUID 全零
	attested = binary.BigEndian.AppendUint16(attested, uint16(len(a.credID)))
	attested = append(append(attested, a.credID...), pub...)
	authData := a.authData(protocol.FlagUserPresent|protocol.FlagUserVerified|protocol.FlagAttestedCredentialData, attested)
	att, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData})
	require.NoError(a.t, err)
	return map[string]any{
		"id": b64(a.credID), "rawId": b64(a.credID), "type": "public-key",
		"response": map[string]any{
			"attestationObject": b64(att), "clientDataJSON": b64(a.clientData("webauthn.create", options)),
			"transports": []string{"internal"},
		},
		"clientExtensionResults": map[string]any{},
	}
}

// get 模拟 navigator.credentials.get，返回 AuthenticationResponseJSON。
func (a *h2Authenticator) get(options map[string]any) map[string]any {
	a.count++
	authData := a.authData(protocol.FlagUserPresent|protocol.FlagUserVerified, nil)
	cd := a.clientData("webauthn.get", options)
	cdh := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, authData...), cdh[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	require.NoError(a.t, err)
	return map[string]any{
		"id": b64(a.credID), "rawId": b64(a.credID), "type": "public-key",
		"response": map[string]any{
			"authenticatorData": b64(authData), "clientDataJSON": b64(cd),
			"signature": b64(sig), "userHandle": b64(a.handle),
		},
		"clientExtensionResults": map[string]any{},
	}
}

type h2Ceremony struct {
	CeremonyID string         `json:"ceremonyId"`
	Options    map[string]any `json:"options"`
}

func TestPasskeySetupLoginRecovery(t *testing.T) {
	e := newH2Env(t)
	ctx := context.Background()

	// 未初始化时不能登录
	assert.Equal(t, 409, e.do("POST", "/api/v1/auth/login/begin", h2Cred{}, nil).status)

	setupURL, created, err := auth.EnsureSetupToken(ctx, e.st, e.dataDir, h2PublicURL)
	require.NoError(t, err)
	require.True(t, created)
	u, err := url.Parse(setupURL)
	require.NoError(t, err)
	require.Empty(t, u.RawQuery, "初始化令牌不进查询串（反代访问日志）")
	frag, err := url.ParseQuery(u.Fragment)
	require.NoError(t, err)
	token := frag.Get("token")

	r := e.do("POST", "/api/v1/auth/setup/begin", h2Cred{}, map[string]string{"token": "ost_bad"})
	assert.Equal(t, 401, r.status)
	r = e.do("POST", "/api/v1/auth/setup/begin", h2Cred{}, map[string]string{})
	assert.Equal(t, 401, r.status, "既无令牌也无恢复会话")

	r = e.do("POST", "/api/v1/auth/setup/begin", h2Cred{}, map[string]string{"token": token, "name": "MacBook"})
	require.Equal(t, 200, r.status, string(r.body))
	var cer h2Ceremony
	r.json(t, &cer)
	assert.Equal(t, h2RPID, cer.Options["rp"].(map[string]any)["id"])
	assert.Equal(t, "required", cer.Options["authenticatorSelection"].(map[string]any)["residentKey"])

	authn := newH2Authenticator(t)
	cred := authn.create(cer.Options)

	// 错误的仪式句柄
	assert.Equal(t, 400, e.do("POST", "/api/v1/auth/setup/finish", h2Cred{}, map[string]any{"ceremonyId": "nope", "credential": cred}).status)

	r = e.do("POST", "/api/v1/auth/setup/finish", h2Cred{}, map[string]any{"ceremonyId": cer.CeremonyID, "credential": cred})
	require.Equal(t, 200, r.status, string(r.body))
	var fin struct {
		Me struct {
			Auth         struct{ Kind string }
			PasskeyCount int
		}
		RecoveryCodes []string
	}
	r.json(t, &fin)
	assert.Equal(t, "web", fin.Me.Auth.Kind)
	assert.Equal(t, 1, fin.Me.PasskeyCount)
	require.Len(t, fin.RecoveryCodes, 10)
	sess := r.session()
	require.NotEmpty(t, sess)
	for _, c := range r.cookies {
		if c.Name == auth.CookieName {
			assert.True(t, c.HttpOnly)
			assert.True(t, c.Secure)
			assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
		}
	}
	// 仪式一次性
	assert.Equal(t, 400, e.do("POST", "/api/v1/auth/setup/finish", h2Cred{}, map[string]any{"ceremonyId": cer.CeremonyID, "credential": cred}).status)
	// 初始化令牌已作废，且已有所有者
	assert.Equal(t, 409, e.do("POST", "/api/v1/auth/setup/begin", h2Cred{}, map[string]string{"token": token}).status)
	_, created, err = auth.EnsureSetupToken(ctx, e.st, e.dataDir, h2PublicURL)
	require.NoError(t, err)
	assert.False(t, created)

	// Cookie 会话：GET 免 CSRF，变更类要求 CSRF
	assert.Equal(t, 200, e.do("GET", "/api/v1/me", h2Cred{cookie: sess}, nil).status)
	assert.Equal(t, 403, e.do("POST", "/api/v1/ws-ticket", h2Cred{cookie: sess}, nil).status)
	assert.Equal(t, 200, e.do("POST", "/api/v1/ws-ticket", h2Cred{cookie: sess, csrf: true}, nil).status)

	// 注销后会话失效
	require.Equal(t, 204, e.do("POST", "/api/v1/auth/logout", h2Cred{cookie: sess, csrf: true}, nil).status)
	assert.Equal(t, 401, e.do("GET", "/api/v1/me", h2Cred{cookie: sess}, nil).status)

	// 登录
	r = e.do("POST", "/api/v1/auth/login/begin", h2Cred{}, nil)
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &cer)
	assert.Equal(t, h2RPID, cer.Options["rpId"])
	r = e.do("POST", "/api/v1/auth/login/finish", h2Cred{}, map[string]any{"ceremonyId": cer.CeremonyID, "credential": authn.get(cer.Options)})
	require.Equal(t, 200, r.status, string(r.body))
	sess = r.session()
	require.NotEmpty(t, sess)
	assert.Equal(t, 200, e.do("GET", "/api/v1/me", h2Cred{cookie: sess}, nil).status)

	// 其它密钥签名的断言被拒
	r = e.do("POST", "/api/v1/auth/login/begin", h2Cred{}, nil)
	r.json(t, &cer)
	forged := newH2Authenticator(t)
	forged.credID, forged.handle = authn.credID, authn.handle
	r = e.do("POST", "/api/v1/auth/login/finish", h2Cred{}, map[string]any{"ceremonyId": cer.CeremonyID, "credential": forged.get(cer.Options)})
	assert.Equal(t, 401, r.status)

	// 恢复码：换一次性会话 → 只能 me 与 setup → 重新注册新 Passkey
	assert.Equal(t, 401, e.do("POST", "/api/v1/auth/recovery", h2Cred{}, map[string]string{"code": "aaaa-bbbb-cccc-dddd"}).status)
	r = e.do("POST", "/api/v1/auth/recovery", h2Cred{}, map[string]string{"code": fin.RecoveryCodes[3]})
	require.Equal(t, 200, r.status, string(r.body))
	var rec struct{ RemainingCodes int }
	r.json(t, &rec)
	assert.Equal(t, 9, rec.RemainingCodes)
	recSess := r.session()
	var revoked []string
	e.svc.Subscribe(func(kind, id string) { revoked = append(revoked, kind+":"+id) })
	assert.Equal(t, 401, e.do("POST", "/api/v1/auth/recovery", h2Cred{}, map[string]string{"code": fin.RecoveryCodes[3]}).status, "恢复码只能用一次")

	r = e.do("GET", "/api/v1/me", h2Cred{cookie: recSess}, nil)
	require.Equal(t, 200, r.status)
	assert.Contains(t, string(r.body), `"recovery"`)
	assert.Equal(t, 403, e.do("GET", "/api/v1/devices", h2Cred{cookie: recSess}, nil).status)
	assert.Equal(t, 403, e.do("POST", "/api/v1/auth/setup/begin", h2Cred{cookie: recSess}, map[string]string{}).status, "缺 CSRF")

	r = e.do("POST", "/api/v1/auth/setup/begin", h2Cred{cookie: recSess, csrf: true}, map[string]string{"name": "新手机"})
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &cer)
	excl := cer.Options["excludeCredentials"].([]any)
	assert.Len(t, excl, 1)
	phone := newH2Authenticator(t)
	r = e.do("POST", "/api/v1/auth/setup/finish", h2Cred{cookie: recSess, csrf: true}, map[string]any{"ceremonyId": cer.CeremonyID, "credential": phone.create(cer.Options)})
	require.Equal(t, 200, r.status, string(r.body))
	fin.RecoveryCodes = nil
	r.json(t, &fin)
	assert.Empty(t, fin.RecoveryCodes)
	assert.Equal(t, 2, fin.Me.PasskeyCount)
	assert.Equal(t, 401, e.do("GET", "/api/v1/me", h2Cred{cookie: recSess}, nil).status, "恢复会话已删除")
	assert.Equal(t, []string{"web:" + ids.Hash(recSess)}, revoked, "删除恢复会话发布吊销事件")
	assert.Equal(t, 200, e.do("GET", "/api/v1/me", h2Cred{cookie: r.session()}, nil).status)

	// 新 Passkey 可以登录
	r = e.do("POST", "/api/v1/auth/login/begin", h2Cred{}, nil)
	r.json(t, &cer)
	r = e.do("POST", "/api/v1/auth/login/finish", h2Cred{}, map[string]any{"ceremonyId": cer.CeremonyID, "credential": phone.get(cer.Options)})
	require.Equal(t, 200, r.status, string(r.body))
}

// 设置页的 Passkey 管理与恢复码重新生成：9 个接口全部走真实路由。
func TestPasskeyManage(t *testing.T) {
	e := newH2Env(t)
	ctx := context.Background()

	type passkey struct {
		ID      string
		Name    string
		Current bool
	}
	type passkeyList struct {
		Items            []passkey
		ReauthValidUntil *int64
	}
	list := func(sess string) passkeyList {
		t.Helper()
		r := e.do("GET", "/api/v1/auth/passkeys", h2Cred{cookie: sess}, nil)
		require.Equal(t, 200, r.status, string(r.body))
		var l passkeyList
		r.json(t, &l)
		return l
	}
	login := func(a *h2Authenticator) string {
		t.Helper()
		r := e.do("POST", "/api/v1/auth/login/begin", h2Cred{}, nil)
		require.Equal(t, 200, r.status, string(r.body))
		var cer h2Ceremony
		r.json(t, &cer)
		r = e.do("POST", "/api/v1/auth/login/finish", h2Cred{}, map[string]any{"ceremonyId": cer.CeremonyID, "credential": a.get(cer.Options)})
		require.Equal(t, 200, r.status, string(r.body))
		require.NotEmpty(t, r.session())
		return r.session()
	}

	// 1. 初始化并登录得到 S1
	setupURL, _, err := auth.EnsureSetupToken(ctx, e.st, e.dataDir, h2PublicURL)
	require.NoError(t, err)
	u, err := url.Parse(setupURL)
	require.NoError(t, err)
	frag, err := url.ParseQuery(u.Fragment)
	require.NoError(t, err)
	r := e.do("POST", "/api/v1/auth/setup/begin", h2Cred{}, map[string]string{"token": frag.Get("token"), "name": "MacBook"})
	require.Equal(t, 200, r.status, string(r.body))
	var cer h2Ceremony
	r.json(t, &cer)
	laptop := newH2Authenticator(t)
	r = e.do("POST", "/api/v1/auth/setup/finish", h2Cred{}, map[string]any{"ceremonyId": cer.CeremonyID, "credential": laptop.create(cer.Options)})
	require.Equal(t, 200, r.status, string(r.body))
	var fin struct{ RecoveryCodes []string }
	r.json(t, &fin)
	require.Len(t, fin.RecoveryCodes, 10)

	s1 := login(laptop)
	s1c := h2Cred{cookie: s1, csrf: true}
	l := list(s1)
	require.Len(t, l.Items, 1)
	assert.Equal(t, "MacBook", l.Items[0].Name)
	assert.True(t, l.Items[0].Current)
	assert.NotNil(t, l.ReauthValidUntil, "登录即算一次验证")
	laptopID := l.Items[0].ID

	// 2. 验证过期：新增、删除、重新生成恢复码都要求重新验证；改名不要求
	stale := time.Now().Add(-6 * time.Minute).UnixMilli()
	require.NoError(t, e.st.Q.TouchWebSessionVerified(ctx, db.TouchWebSessionVerifiedParams{VerifiedAt: &stale, IDHash: ids.Hash(s1)}))
	assert.Nil(t, list(s1).ReauthValidUntil)
	r = e.do("POST", "/api/v1/auth/passkeys/begin", s1c, map[string]string{"name": "手机"})
	assert.Equal(t, 403, r.status)
	assert.Equal(t, "reauth_required", r.code(t))
	r = e.do("DELETE", "/api/v1/auth/passkeys/"+laptopID, s1c, nil)
	assert.Equal(t, 403, r.status)
	assert.Equal(t, "reauth_required", r.code(t))
	r = e.do("POST", "/api/v1/auth/recovery-codes", s1c, nil)
	assert.Equal(t, 403, r.status)
	assert.Equal(t, "reauth_required", r.code(t))
	r = e.do("PATCH", "/api/v1/auth/passkeys/"+laptopID, s1c, map[string]string{"name": "  工作电脑 "})
	require.Equal(t, 200, r.status, string(r.body))
	var renamed passkey
	r.json(t, &renamed)
	assert.Equal(t, "工作电脑", renamed.Name)
	assert.Equal(t, "工作电脑", list(s1).Items[0].Name)
	assert.Equal(t, 400, e.do("PATCH", "/api/v1/auth/passkeys/"+laptopID, s1c, map[string]string{"name": " "}).status)
	assert.Equal(t, 404, e.do("PATCH", "/api/v1/auth/passkeys/"+b64([]byte("nope")), s1c, map[string]string{"name": "x"}).status)

	// 3. 重新验证；先用未登记的认证器：403 不是 401，会话不受影响，同一仪式还能再用
	r = e.do("POST", "/api/v1/auth/reauth/begin", s1c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &cer)
	stranger := newH2Authenticator(t)
	stranger.handle = laptop.handle
	r = e.do("POST", "/api/v1/auth/reauth/finish", s1c, map[string]any{"ceremonyId": cer.CeremonyID, "credential": stranger.get(cer.Options)})
	assert.Equal(t, 403, r.status, string(r.body))
	assert.Equal(t, "passkey_reauth_failed", r.code(t))
	assert.Equal(t, 200, e.do("GET", "/api/v1/me", h2Cred{cookie: s1}, nil).status)
	allow := cer.Options["allowCredentials"].([]any)
	require.Len(t, allow, 1)
	assert.Equal(t, laptopID, allow[0].(map[string]any)["id"])
	r = e.do("POST", "/api/v1/auth/reauth/finish", s1c, map[string]any{"ceremonyId": cer.CeremonyID, "credential": laptop.get(cer.Options)})
	require.Equal(t, 200, r.status, string(r.body))
	var re struct{ VerifiedAt, ValidUntil int64 }
	r.json(t, &re)
	assert.Equal(t, int64(300000), re.ValidUntil-re.VerifiedAt)
	l = list(s1)
	require.NotNil(t, l.ReauthValidUntil)
	assert.Equal(t, re.ValidUntil, *l.ReauthValidUntil)
	// 签名计数已写回：再登录不会被当成克隆
	_ = login(laptop)

	// 4. 追加第二把
	r = e.do("POST", "/api/v1/auth/passkeys/begin", s1c, map[string]string{"name": "手机"})
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &cer)
	assert.Len(t, cer.Options["excludeCredentials"].([]any), 1)
	phone := newH2Authenticator(t)
	r = e.do("POST", "/api/v1/auth/passkeys/finish", s1c, map[string]any{"ceremonyId": cer.CeremonyID, "credential": phone.create(cer.Options)})
	require.Equal(t, 200, r.status, string(r.body))
	var added passkey
	r.json(t, &added)
	assert.Equal(t, "手机", added.Name)
	assert.False(t, added.Current)
	assert.Len(t, list(s1).Items, 2)
	r = e.do("GET", "/api/v1/me", h2Cred{cookie: s1}, nil)
	require.Equal(t, 200, r.status)
	var me struct {
		PasskeyCount           int
		RecoveryCodesRemaining *int
	}
	r.json(t, &me)
	assert.Equal(t, 2, me.PasskeyCount)

	// 5. 用第二把登录得到 S2；S1 删掉第二把 → S2 立即失效并发布吊销
	s2 := login(phone)
	assert.True(t, func() bool {
		for _, it := range list(s2).Items {
			if it.ID == added.ID {
				return it.Current
			}
		}
		return false
	}())
	var revoked []string
	e.svc.Subscribe(func(kind, id string) { revoked = append(revoked, kind+":"+id) })
	require.Equal(t, 204, e.do("DELETE", "/api/v1/auth/passkeys/"+added.ID, s1c, nil).status)
	assert.Equal(t, 401, e.do("GET", "/api/v1/me", h2Cred{cookie: s2}, nil).status)
	assert.Equal(t, []string{"web:" + ids.Hash(s2)}, revoked)
	assert.Len(t, list(s1).Items, 1)

	// 6. 最后一把不能删；不存在的 id → 404
	r = e.do("DELETE", "/api/v1/auth/passkeys/"+laptopID, s1c, nil)
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "last_passkey", r.code(t))
	assert.Equal(t, 404, e.do("DELETE", "/api/v1/auth/passkeys/"+added.ID, s1c, nil).status)

	// 7. 重新生成恢复码：旧码全部失效，新码可用
	r = e.do("POST", "/api/v1/auth/recovery-codes", s1c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var regen struct{ RecoveryCodes []string }
	r.json(t, &regen)
	require.Len(t, regen.RecoveryCodes, 10)
	assert.Equal(t, 401, e.do("POST", "/api/v1/auth/recovery", h2Cred{}, map[string]string{"code": fin.RecoveryCodes[0]}).status)
	require.Equal(t, 200, e.do("POST", "/api/v1/auth/recovery", h2Cred{}, map[string]string{"code": regen.RecoveryCodes[0]}).status)
	r = e.do("GET", "/api/v1/auth/recovery-codes", h2Cred{cookie: s1}, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var rs struct {
		Total, Remaining int
		GeneratedAt      *int64
	}
	r.json(t, &rs)
	assert.Equal(t, 10, rs.Total)
	assert.Equal(t, 9, rs.Remaining)
	assert.NotNil(t, rs.GeneratedAt)
	r = e.do("GET", "/api/v1/me", h2Cred{cookie: s1}, nil)
	r.json(t, &me)
	require.NotNil(t, me.RecoveryCodesRemaining)
	assert.Equal(t, 9, *me.RecoveryCodesRemaining)

	// 8. 设备令牌不能管理 Passkey
	_, tok, err := auth.CreateDevice(ctx, e.st, "verify", "cli", "")
	require.NoError(t, err)
	r = e.do("GET", "/api/v1/auth/passkeys", h2Cred{bearer: tok}, nil)
	assert.Equal(t, 403, r.status)
	assert.Equal(t, "forbidden", r.code(t))
}

func TestPublicEndpointRateLimit(t *testing.T) {
	e := newH2Env(t)
	body := map[string]string{"name": "x", "kind": "cli", "platform": "linux"}
	for i := range 5 {
		require.Equal(t, 200, e.do("POST", "/api/v1/devices/pair/start", h2Cred{}, body).status, i)
	}
	r := e.do("POST", "/api/v1/devices/pair/start", h2Cred{}, body)
	assert.Equal(t, 429, r.status)
	assert.Equal(t, "rate_limited", r.code(t))
	// 其它端点各自计数
	assert.Equal(t, 409, e.do("POST", "/api/v1/auth/login/begin", h2Cred{}, nil).status)
	for range 5 {
		e.do("POST", "/api/v1/auth/recovery", h2Cred{}, map[string]string{"code": "x"})
	}
	assert.Equal(t, 429, e.do("POST", "/api/v1/auth/recovery", h2Cred{}, map[string]string{"code": "x"}).status)
}

// appGet 以不跟随跳转的客户端请求 App 会话兑换端点。
func (e *h2Env) appGet(method, rawQuery string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+api.AppSessionPath+"?"+rawQuery, nil)
	require.NoError(e.t, err)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	require.NoError(e.t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp
}

func sessionCookie(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	return nil
}

// M6：App 会话——desktop 设备令牌换一次性码，顶层跳转兑换成绑定设备的网页会话。
func TestAppSession(t *testing.T) {
	e := newH2Env(t)
	ctx := context.Background()
	dev, tok, err := auth.CreateDevice(ctx, e.st, "laptop", "desktop", "linux")
	require.NoError(t, err)
	_, _, err = auth.CreateDevice(ctx, e.st, "phone", "mobile", "android")
	require.NoError(t, err)
	_, cliTok, err := auth.CreateDevice(ctx, e.st, "cli", "cli", "linux")
	require.NoError(t, err)

	mint := func() string {
		t.Helper()
		r := e.do("POST", "/api/v1/auth/app-session", h2Cred{bearer: tok}, nil)
		require.Equal(t, 200, r.status, string(r.body))
		var c struct {
			Code      string
			ExpiresIn int
		}
		r.json(t, &c)
		assert.True(t, strings.HasPrefix(c.Code, "oac_"), c.Code)
		assert.Equal(t, 30, c.ExpiresIn)
		return c.Code
	}

	// 只有 desktop / mobile 设备令牌能换：网页会话与 cli 令牌 403
	r := e.do("POST", "/api/v1/auth/app-session", h2Cred{cookie: e.webSession(), csrf: true}, nil)
	assert.Equal(t, 403, r.status)
	assert.Equal(t, "app_session_unsupported", r.code(t))
	r = e.do("POST", "/api/v1/auth/app-session", h2Cred{bearer: cliTok}, nil)
	assert.Equal(t, 403, r.status)
	assert.Equal(t, "app_session_unsupported", r.code(t))
	assert.Equal(t, 401, e.do("POST", "/api/v1/auth/app-session", h2Cred{}, nil).status)

	code := mint()
	resp := e.appGet("GET", url.Values{"code": {code}, "next": {"/t/x"}}.Encode())
	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "/t/x", resp.Header.Get("Location"))
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	assert.Equal(t, "no-referrer", resp.Header.Get("Referrer-Policy"))
	c := sessionCookie(resp)
	require.NotNil(t, c, "兑换成功写会话 Cookie")
	assert.True(t, c.Secure)
	assert.True(t, c.HttpOnly)
	assert.Equal(t, "/", c.Path)
	// 会话级 Cookie：不落 WebView 磁盘
	assert.Zero(t, c.MaxAge, "不带 Max-Age")
	assert.True(t, c.Expires.IsZero(), "不带 Expires")
	app := c.Value

	// 同一个码再兑换一次：照样 302，但没有 Set-Cookie
	resp = e.appGet("GET", url.Values{"code": {code}, "next": {"/t/x"}}.Encode())
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "/t/x", resp.Header.Get("Location"))
	assert.Nil(t, sessionCookie(resp))

	// App 会话：kind 仍为 web，并给出设备
	r = e.do("GET", "/api/v1/me", h2Cred{cookie: app}, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var me struct {
		Auth struct{ Kind, DeviceID string }
	}
	r.json(t, &me)
	assert.Equal(t, "web", me.Auth.Kind)
	assert.Equal(t, dev.ID, me.Auth.DeviceID)

	// 普通网页会话不带 deviceId
	r = e.do("GET", "/api/v1/me", h2Cred{cookie: e.webSession()}, nil)
	require.Equal(t, 200, r.status)
	me.Auth.DeviceID = ""
	r.json(t, &me)
	assert.Empty(t, me.Auth.DeviceID)

	// 设备换来的会话不能批准配对（设备令牌不能再签发设备），也不能查看待批准请求
	r = e.do("POST", "/api/v1/devices/pair/approve", h2Cred{cookie: app, csrf: true}, map[string]string{"code": "ABCD-EFGH"})
	assert.Equal(t, 403, r.status)
	assert.Equal(t, "forbidden", r.code(t))
	assert.Equal(t, 403, e.do("GET", "/api/v1/devices/pair/ABCD-EFGH", h2Cred{cookie: app}, nil).status)

	// 设备列表里该设备 current == true，其余为 false
	r = e.do("GET", "/api/v1/devices", h2Cred{cookie: app}, nil)
	require.Equal(t, 200, r.status)
	var list struct {
		Devices []struct {
			ID      string
			Current bool
		}
	}
	r.json(t, &list)
	require.Len(t, list.Devices, 3)
	for _, d := range list.Devices {
		assert.Equal(t, d.ID == dev.ID, d.Current, d.ID)
	}

	// 仍是 Cookie 会话：变更请求要 CSRF 头
	r = e.do("POST", "/api/v1/ws-ticket", h2Cred{cookie: app}, nil)
	assert.Equal(t, 403, r.status)
	r = e.do("POST", "/api/v1/ws-ticket", h2Cred{cookie: app, csrf: true}, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var tk struct{ Ticket string }
	r.json(t, &tk)
	who, ok := e.tickets.Redeem(tk.Ticket)
	require.True(t, ok)
	assert.Equal(t, auth.KindWeb, who.Kind)
	assert.Equal(t, dev.ID, who.DeviceID)

	// 跳转目标只放行站内路径
	for _, next := range []string{"//evil.example", "https://evil.example", `/\evil`, "/\t/evil.example", "evil", ""} {
		resp = e.appGet("GET", url.Values{"code": {"oac_invalid"}, "next": {next}}.Encode())
		assert.Equal(t, http.StatusFound, resp.StatusCode, next)
		assert.Equal(t, "/", resp.Header.Get("Location"), next)
		assert.Nil(t, sessionCookie(resp), "无效码不写 Cookie")
	}

	// 只接受 GET / HEAD
	req, err := http.NewRequest("POST", e.srv.URL+api.AppSessionPath, nil)
	require.NoError(t, err)
	pr, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = pr.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, pr.StatusCode)
	assert.Equal(t, "GET, HEAD", pr.Header.Get("Allow"))

	// HEAD 同样可兑换；新码兑换后旧 App 会话失效
	resp = e.appGet("HEAD", url.Values{"code": {mint()}, "next": {"/settings/devices"}}.Encode())
	assert.Equal(t, "/settings/devices", resp.Header.Get("Location"))
	c2 := sessionCookie(resp)
	require.NotNil(t, c2)
	assert.Equal(t, 401, e.do("GET", "/api/v1/me", h2Cred{cookie: app}, nil).status, "每台设备只保留最新的 App 会话")
	app = c2.Value
	require.Equal(t, 200, e.do("GET", "/api/v1/me", h2Cred{cookie: app}, nil).status)

	// 吊销设备后其 App 会话立即失效
	require.Equal(t, 204, e.do("DELETE", "/api/v1/devices/"+dev.ID, h2Cred{bearer: cliTok}, nil).status)
	r = e.do("GET", "/api/v1/me", h2Cred{cookie: app}, nil)
	assert.Equal(t, 401, r.status)
	assert.Equal(t, "unauthorized", r.code(t))
}
