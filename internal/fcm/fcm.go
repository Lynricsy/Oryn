// Package fcm 是 Firebase Cloud Messaging HTTP v1 的出站客户端：服务账号 RS256 JWT 换 OAuth access token（进程内缓存），
// 经 `messages:send` 发 data-only 消息。它只认「令牌 + data map」，不关心载荷怎么加密、订阅存在哪里；
// orynd 的直发通道与官方推送网关共用它。
package fcm

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
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// DefaultTokenURL 是 Google OAuth 2.0 令牌端点（同时是 JWT 的 aud）。
	DefaultTokenURL = "https://oauth2.googleapis.com/token"
	// DefaultAPIBase 是 FCM HTTP v1 API 的根地址。
	DefaultAPIBase = "https://fcm.googleapis.com"
	// MaxToken 是注册令牌的长度上限（字节）。
	MaxToken = 4096

	scope       = "https://www.googleapis.com/auth/firebase.messaging"
	tokenTTL    = 55 * time.Minute
	jwtLifetime = time.Hour
	maxResponse = 64 << 10
)

// ErrInvalidAccount 表示服务账号 JSON 不可用（不是 JSON、缺字段、私钥不是 RSA PEM）。
var ErrInvalidAccount = errors.New("invalid FCM service account")

// Account 是服务账号 JSON 里用到的字段与解析好的私钥。
type Account struct {
	ProjectID    string `json:"project_id"`
	ClientEmail  string `json:"client_email"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`

	key *rsa.PrivateKey
	// fingerprint 是服务账号 JSON 的 SHA-256：换了服务账号即作废缓存的 access token。
	fingerprint [32]byte
}

// ParseAccount 解析服务账号 JSON；失败时错误包装 ErrInvalidAccount。
func ParseAccount(raw []byte) (*Account, error) {
	var a Account
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("%w: service account is not valid JSON", ErrInvalidAccount)
	}
	if a.ProjectID == "" || a.ClientEmail == "" || a.PrivateKey == "" {
		return nil, fmt.Errorf("%w: service account lacks project_id / client_email / private_key", ErrInvalidAccount)
	}
	block, _ := pem.Decode([]byte(a.PrivateKey))
	if block == nil {
		return nil, fmt.Errorf("%w: service account private_key is not PEM", ErrInvalidAccount)
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%w: service account private_key is not RSA", ErrInvalidAccount)
		}
		a.key = rk
	} else if rk, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		a.key = rk
	} else {
		return nil, fmt.Errorf("%w: service account private_key cannot be parsed", ErrInvalidAccount)
	}
	a.fingerprint = sha256.Sum256(raw)
	return &a, nil
}

// ValidToken：非空、≤ MaxToken 字节、只含可见 ASCII（不含空白与控制字符）。
func ValidToken(t string) bool {
	if t == "" || len(t) > MaxToken {
		return false
	}
	for _, r := range t {
		if r > unicode.MaxASCII || r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// Message 是一条 data-only 消息。
type Message struct {
	// Token 是注册令牌。
	Token string
	Data  map[string]string
	// High 为 true 时 android.priority = HIGH（会显示通知的消息）；否则 NORMAL（不弹通知的消息，避免被 FCM 降级）。
	High bool
	// TTL 是离线保留时长（秒级精度）。
	TTL time.Duration
}

// Result 是一次发送的结果。
type Result struct {
	// Retry 表示可重试的失败（网络错误、408 / 429 / 5xx、access token 被拒）。
	Retry      bool
	RetryAfter time.Duration
	// Unregistered 表示令牌已失效（FCM errorCode UNREGISTERED）：调用方应删除它。
	Unregistered bool
	// AccessRejected 表示 access token 被拒（HTTP 401）：缓存已作废，重试会重新换取。
	AccessRejected bool
	// Code 是可写进日志的失败概述：FCM errorCode、"oauth_token"、"HTTP <状态>"、"timeout" 或 "network"。
	Code string
	// Err 为空表示成功；错误文本不含令牌与 access token。
	Err error
}

// Client 发送 FCM 消息并缓存 access token（并发安全）。零值不可用：HTTP 必填。
type Client struct {
	HTTP *http.Client
	// TokenURL / APIBase 为空时用 Google 的正式地址；测试指向 httptest。
	TokenURL, APIBase string
	// Now 为空时用 time.Now。
	Now func() time.Time

	mu          sync.Mutex
	fingerprint [32]byte
	token       string
	expires     time.Time
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) tokenURL() string {
	if c.TokenURL != "" {
		return c.TokenURL
	}
	return DefaultTokenURL
}

func (c *Client) apiBase() string {
	if c.APIBase != "" {
		return strings.TrimRight(c.APIBase, "/")
	}
	return DefaultAPIBase
}

// Send 以服务账号 acct 发送一条消息。不设 android.collapse_key：折叠消息每个令牌最多保留 4 个 collapse key，
// 超出的会被不确定地丢弃且不通知 App（见 docs/protocol.md「M7：移动推送」）。
func (c *Client) Send(ctx context.Context, acct *Account, m Message) Result {
	priority := "NORMAL"
	if m.High {
		priority = "HIGH"
	}
	android := map[string]any{"priority": priority, "ttl": fmt.Sprintf("%ds", int64(m.TTL/time.Second))}
	body, err := json.Marshal(map[string]any{"message": map[string]any{"token": m.Token, "data": m.Data, "android": android}})
	if err != nil {
		return Result{Err: err}
	}
	access, r := c.accessToken(ctx, acct)
	if r != nil {
		return *r
	}
	endpoint := c.apiBase() + "/v1/projects/" + url.PathEscape(acct.ProjectID) + "/messages:send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return netResult(err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return Result{}
	}
	code := errorCode(rb)
	switch {
	case code == "UNREGISTERED":
		return Result{Unregistered: true, Code: code, Err: fmt.Errorf("fcm token unregistered (HTTP %d)", resp.StatusCode)}
	case resp.StatusCode == http.StatusUnauthorized:
		// access token 被拒（吊销 / 提前过期）：作废缓存，下次重试重新换取
		c.dropToken(access)
		return Result{Retry: true, AccessRejected: true, Code: "UNAUTHENTICATED", Err: fmt.Errorf("fcm rejected access token (HTTP %d)", resp.StatusCode)}
	}
	r2 := httpResult(resp, rb)
	if code != "" {
		r2.Code = code
		r2.Err = fmt.Errorf("fcm %s (HTTP %d)", code, resp.StatusCode)
	}
	return r2
}

// errorCode 取 FCM 错误体里 FcmError 的 errorCode（没有则取 error.status）。
func errorCode(body []byte) string {
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

// accessToken 返回缓存的 access token（未过期且属于同一服务账号），否则以 JWT 断言换取新的并缓存 55 分钟。
// 失败时返回结果：令牌端点 4xx（服务账号无效 / 被禁用）不重试，网络错误与 5xx 重试。
func (c *Client) accessToken(ctx context.Context, acct *Account) (string, *Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.token != "" && c.fingerprint == acct.fingerprint && now.Before(c.expires) {
		return c.token, nil
	}
	c.token = ""
	assertion, err := assertion(acct, c.tokenURL(), now)
	if err != nil {
		return "", &Result{Err: err}
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", &Result{Err: err}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		r := netResult(err)
		return "", &r
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		r := httpResult(resp, rb)
		r.Code, r.Err = "oauth_token", fmt.Errorf("fcm oauth token: %w", r.Err)
		return "", &r
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(rb, &tr); err != nil || tr.AccessToken == "" {
		return "", &Result{Code: "oauth_token", Err: errors.New("fcm oauth token: response has no access_token")}
	}
	ttl := tokenTTL
	if tr.ExpiresIn > 0 {
		// 端点给的有效期短于 55 分钟时留 1 分钟余量
		ttl = min(ttl, time.Duration(tr.ExpiresIn)*time.Second-time.Minute)
	}
	c.fingerprint, c.token, c.expires = acct.fingerprint, tr.AccessToken, now.Add(ttl)
	return c.token, nil
}

// dropToken 作废缓存（仅当缓存的仍是被拒的那一个）。
func (c *Client) dropToken(token string) {
	c.mu.Lock()
	if c.token == token {
		c.token = ""
	}
	c.mu.Unlock()
}

// assertion 构造 RS256 签名的 JWT 断言（RFC 7523）：iss = 服务账号邮箱，aud = 令牌端点，有效 1 小时。
func assertion(acct *Account, aud string, now time.Time) (string, error) {
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	if acct.PrivateKeyID != "" {
		header["kid"] = acct.PrivateKeyID
	}
	claims := map[string]any{
		"iss": acct.ClientEmail, "scope": scope, "aud": aud,
		"iat": now.Unix(), "exp": now.Add(jwtLifetime).Unix(),
	}
	h, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	cl, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding.EncodeToString
	signing := enc(h) + "." + enc(cl)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, acct.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc(sig), nil
}

// httpResult 把非 2xx 响应归类：408 / 429 / 5xx 可重试（尊重 Retry-After 秒数），其余不重试。
func httpResult(resp *http.Response, body []byte) Result {
	msg := strings.TrimSpace(string(body))
	if len(msg) > 200 {
		msg = msg[:200]
	}
	r := Result{Code: fmt.Sprintf("HTTP %d", resp.StatusCode), Err: fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)}
	switch {
	case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		r.Retry = true
		if sec, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && sec > 0 {
			r.RetryAfter = time.Duration(sec) * time.Second
		}
	}
	return r
}

// netResult 是请求未得到响应（网络错误 / 超时）的结果：除调用方取消外可重试。
func netResult(err error) Result {
	code := "network"
	var te interface{ Timeout() bool }
	if errors.As(err, &te) && te.Timeout() {
		code = "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return Result{Code: code, Err: err}
	}
	return Result{Code: code, Err: err, Retry: true}
}
