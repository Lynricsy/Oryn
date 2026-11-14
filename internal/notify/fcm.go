package notify

// FCM 发送器（M7 附录 B）：Android `gms` 包在没有 UnifiedPush 分发器时登记 FCM 注册令牌，
// 服务端以服务账号做 RS256 JWT 换 OAuth access token（缓存 55 分钟），经 FCM HTTP v1 `messages:send`
// 发 data-only 高优先级消息（data 的键与 Web Push 载荷同名、值为字符串）；FCM 回 UNREGISTERED 即删除订阅。
// 服务账号 JSON 存在密钥库里（须为 proxy 模式：env 模式的密钥会注入每台 orb），配置只引用密钥名。

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	// DefaultFCMTokenURL 是 Google OAuth 2.0 令牌端点（同时是 JWT 的 aud）。
	DefaultFCMTokenURL = "https://oauth2.googleapis.com/token"
	// DefaultFCMAPIBase 是 FCM HTTP v1 API 的根地址。
	DefaultFCMAPIBase = "https://fcm.googleapis.com"

	fcmScope       = "https://www.googleapis.com/auth/firebase.messaging"
	fcmTokenTTL    = 55 * time.Minute
	fcmJWTLifetime = time.Hour
	fcmMaxToken    = 4096
	maxFCMResponse = 64 << 10
)

// SecretGetter 按名称读取密钥（secrets.Vault 实现）。
type SecretGetter interface {
	Get(ctx context.Context, name string) (value string, meta secrets.Meta, ok bool, err error)
}

// FCMConfig 是 FCM 发送参数。
type FCMConfig struct {
	// Secret 是存服务账号 JSON 的密钥名；空 = 关闭 FCM。
	Secret string
	// Secrets 读取密钥（Secret 非空时必填）。
	Secrets SecretGetter
	// TokenURL / APIBase 默认 Google 的正式地址；测试指向 httptest。
	TokenURL, APIBase string
}

// fcmAccount 是服务账号 JSON 里用到的字段。
type fcmAccount struct {
	ProjectID    string `json:"project_id"`
	ClientEmail  string `json:"client_email"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`

	key *rsa.PrivateKey
	// fingerprint 是服务账号 JSON 的 SHA-256：换了服务账号即作废缓存的 access token。
	fingerprint [32]byte
}

// fcmTokenCache 缓存 access token。
type fcmTokenCache struct {
	mu          sync.Mutex
	fingerprint [32]byte
	token       string
	expires     time.Time
}

func (s *Service) fcmEnabled() bool { return s.cfg.FCM.Secret != "" && s.cfg.FCM.Secrets != nil }

func (s *Service) fcmTokenURL() string {
	if s.cfg.FCM.TokenURL != "" {
		return s.cfg.FCM.TokenURL
	}
	return DefaultFCMTokenURL
}

func (s *Service) fcmAPIBase() string {
	if s.cfg.FCM.APIBase != "" {
		return strings.TrimRight(s.cfg.FCM.APIBase, "/")
	}
	return DefaultFCMAPIBase
}

// errFCMUnavailable 是 409 fcm_unavailable。
func errFCMUnavailable(msg string) error {
	return &Error{Status: http.StatusConflict, Code: "fcm_unavailable", Message: msg}
}

// fcmAccount 读取并解析服务账号；未配置、密钥缺失、模式不对或内容无效时返回 fcm_unavailable。
func (s *Service) fcmAccount(ctx context.Context) (*fcmAccount, error) {
	if !s.fcmEnabled() {
		return nil, errFCMUnavailable("FCM is not configured on this server")
	}
	val, meta, ok, err := s.cfg.FCM.Secrets.Get(ctx, s.cfg.FCM.Secret)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errFCMUnavailable(fmt.Sprintf("secret %q not found", s.cfg.FCM.Secret))
	}
	if meta.Mode != secrets.ModeProxy {
		// env 模式的密钥会注入每台 orb 的 agent 进程：服务账号不得以此形态保存
		return nil, errFCMUnavailable(fmt.Sprintf("secret %q must use mode proxy", s.cfg.FCM.Secret))
	}
	return parseFCMAccount([]byte(val))
}

func parseFCMAccount(raw []byte) (*fcmAccount, error) {
	var a fcmAccount
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, errFCMUnavailable("service account is not valid JSON")
	}
	if a.ProjectID == "" || a.ClientEmail == "" || a.PrivateKey == "" {
		return nil, errFCMUnavailable("service account lacks project_id / client_email / private_key")
	}
	block, _ := pem.Decode([]byte(a.PrivateKey))
	if block == nil {
		return nil, errFCMUnavailable("service account private_key is not PEM")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errFCMUnavailable("service account private_key is not RSA")
		}
		a.key = rk
	} else if rk, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		a.key = rk
	} else {
		return nil, errFCMUnavailable("service account private_key cannot be parsed")
	}
	a.fingerprint = sha256.Sum256(raw)
	return &a, nil
}

// createFCMSubscription 按令牌 upsert 一条 FCM 订阅（endpoint 列存令牌）。
func (s *Service) createFCMSubscription(ctx context.Context, in apigen.PushSubscriptionInput, owner Owner, userAgent string) (apigen.PushSubscription, error) {
	if in.Endpoint != nil || in.Keys != nil {
		return apigen.PushSubscription{}, errPushInvalid("kind fcm takes only token")
	}
	if in.Token == nil || !validFCMToken(*in.Token) {
		return apigen.PushSubscription{}, errPushInvalid("token must be a non-empty FCM registration token")
	}
	if _, err := s.fcmAccount(ctx); err != nil {
		return apigen.PushSubscription{}, err
	}
	row, err := s.st.Q.UpsertPushSubscription(ctx, db.UpsertPushSubscriptionParams{
		ID: ids.New("psub"), Kind: "fcm", Endpoint: *in.Token,
		WebSessionHash: owner.WebSessionHash, DeviceID: owner.DeviceID, UserAgent: truncate(userAgent, 300), CreatedAt: s.now(),
	})
	if err != nil {
		return apigen.PushSubscription{}, err
	}
	if row.Kind != "fcm" {
		// 同一字符串已作为 Web Push 端点登记（不可能是合法令牌，仅防御）
		return apigen.PushSubscription{}, errPushInvalid("token collides with an existing subscription")
	}
	return subscriptionAPI(row), nil
}

// validFCMToken：非空、≤4096 字节、只含可见 ASCII（不含空白与控制字符）。
func validFCMToken(t string) bool {
	if t == "" || len(t) > fcmMaxToken {
		return false
	}
	for _, r := range t {
		if r > unicode.MaxASCII || r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

type fcmSender struct {
	s     *Service
	token string
}

func (s *Service) fcmSender(sub db.PushSubscription) Sender { return fcmSender{s, sub.Endpoint} }

// fcmData 把 Web Push 载荷转成 FCM 的 data（值一律为字符串）。
func fcmData(m Message) (map[string]string, error) {
	payload, err := pushPayload(m)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(v))
	for k, x := range v {
		out[k] = fmt.Sprint(x)
	}
	return out, nil
}

func (f fcmSender) Send(ctx context.Context, m Message) Result {
	acct, err := f.s.fcmAccount(ctx)
	if err != nil {
		return Result{Err: err}
	}
	data, err := fcmData(m)
	if err != nil {
		return Result{Err: err}
	}
	android := map[string]any{"priority": "HIGH", "ttl": fmt.Sprintf("%ds", pushTTL)}
	if tag := data["tag"]; tag != "" {
		android["collapse_key"] = tag
	}
	body, err := json.Marshal(map[string]any{"message": map[string]any{"token": f.token, "data": data, "android": android}})
	if err != nil {
		return Result{Err: err}
	}
	access, r := f.s.fcmAccessToken(ctx, acct)
	if r != nil {
		return *r
	}
	endpoint := f.s.fcmAPIBase() + "/v1/projects/" + url.PathEscape(acct.ProjectID) + "/messages:send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := f.s.cfg.HTTP.Do(req)
	if err != nil {
		return netResult(err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, maxFCMResponse))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return Result{}
	}
	code := fcmErrorCode(rb)
	switch {
	case code == "UNREGISTERED":
		return Result{Gone: true, Err: fmt.Errorf("fcm token unregistered (HTTP %d)", resp.StatusCode)}
	case resp.StatusCode == http.StatusUnauthorized:
		// access token 被拒（吊销 / 提前过期）：作废缓存，下次重试重新换取
		f.s.dropFCMToken(access)
		return Result{Retry: true, Err: fmt.Errorf("fcm rejected access token (HTTP %d)", resp.StatusCode)}
	}
	r2 := httpResult(resp, rb)
	if code != "" {
		r2.Err = fmt.Errorf("fcm %s (HTTP %d)", code, resp.StatusCode)
	}
	return r2
}

// fcmErrorCode 取 FCM 错误体里 FcmError 的 errorCode（没有则取 error.status）。
func fcmErrorCode(body []byte) string {
	var e struct {
		Error struct {
			Status  string `json:"status"`
			Details []struct {
				Type      string `json:"@type"`
				ErrorCode string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	for _, d := range e.Error.Details {
		if d.ErrorCode != "" {
			return d.ErrorCode
		}
	}
	return e.Error.Status
}

// fcmAccessToken 返回缓存的 access token（未过期且属于同一服务账号），否则以 JWT 断言换取新的并缓存 55 分钟。
// 失败时返回投递结果：令牌端点 4xx（服务账号无效 / 被禁用）不重试，网络错误与 5xx 重试。
func (s *Service) fcmAccessToken(ctx context.Context, acct *fcmAccount) (string, *Result) {
	c := &s.fcm
	c.mu.Lock()
	defer c.mu.Unlock()
	now := s.cfg.Now()
	if c.token != "" && c.fingerprint == acct.fingerprint && now.Before(c.expires) {
		return c.token, nil
	}
	c.token = ""
	assertion, err := fcmAssertion(acct, s.fcmTokenURL(), now)
	if err != nil {
		return "", &Result{Err: err}
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.fcmTokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", &Result{Err: err}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		r := netResult(err)
		return "", &r
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, maxFCMResponse))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		r := httpResult(resp, rb)
		r.Err = fmt.Errorf("fcm oauth token: %w", r.Err)
		return "", &r
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(rb, &tr); err != nil || tr.AccessToken == "" {
		return "", &Result{Err: errors.New("fcm oauth token: response has no access_token")}
	}
	ttl := fcmTokenTTL
	if tr.ExpiresIn > 0 {
		// 端点给的有效期短于 55 分钟时留 1 分钟余量
		ttl = min(ttl, time.Duration(tr.ExpiresIn)*time.Second-time.Minute)
	}
	c.fingerprint, c.token, c.expires = acct.fingerprint, tr.AccessToken, now.Add(ttl)
	return c.token, nil
}

// dropFCMToken 作废缓存（仅当缓存的仍是被拒的那一个）。
func (s *Service) dropFCMToken(token string) {
	s.fcm.mu.Lock()
	if s.fcm.token == token {
		s.fcm.token = ""
	}
	s.fcm.mu.Unlock()
}

// fcmAssertion 构造 RS256 签名的 JWT 断言（RFC 7523）：iss = 服务账号邮箱，aud = 令牌端点，有效 1 小时。
func fcmAssertion(acct *fcmAccount, aud string, now time.Time) (string, error) {
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	if acct.PrivateKeyID != "" {
		header["kid"] = acct.PrivateKeyID
	}
	claims := map[string]any{
		"iss": acct.ClientEmail, "scope": fcmScope, "aud": aud,
		"iat": now.Unix(), "exp": now.Add(fcmJWTLifetime).Unix(),
	}
	h, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	c, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding.EncodeToString
	signing := enc(h) + "." + enc(c)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, acct.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc(sig), nil
}
