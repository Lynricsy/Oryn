// Package apns 是 Apple Push Notification service 的出站客户端：HTTP/2 + ES256 provider token（.p8 key）。
// 它只认「设备令牌 + 头 + JSON 载荷」，不关心载荷怎么加密、授权存在哪里；官方推送网关用它。
package apns

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// ProductionURL 与 SandboxURL 是 APNs 两个环境的根地址。
	ProductionURL = "https://api.push.apple.com"
	SandboxURL    = "https://api.sandbox.push.apple.com"

	// EnvProduction 与 EnvSandbox 是环境名（与 aps-environment 权利值一致）。
	EnvProduction = "production"
	EnvSandbox    = "sandbox"

	// MaxPayload 是单条通知载荷的字节上限。
	MaxPayload = 4096

	// tokenTTL 是 provider token 的缓存时长：Apple 要求 20–60 分钟内刷新，且刷新过频会回 TooManyProviderTokenUpdates。
	tokenTTL    = 50 * time.Minute
	maxResponse = 16 << 10
)

// ErrInvalidKey 表示 .p8 key 不可用（不是 PEM、不是 PKCS#8、不是 P-256 ECDSA）。
var ErrInvalidKey = errors.New("invalid APNs auth key")

// ParseKey 解析 Apple Developer 下载的 AuthKey_<KeyID>.p8（PKCS#8 PEM 的 P-256 私钥）。
func ParseKey(raw []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("%w: not PEM", ErrInvalidKey)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: not PKCS#8", ErrInvalidKey)
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok || ek.Curve.Params().Name != "P-256" {
		return nil, fmt.Errorf("%w: not a P-256 ECDSA key", ErrInvalidKey)
	}
	return ek, nil
}

// Notification 是一条通知。
type Notification struct {
	// Token 是设备令牌（小写十六进制）。
	Token string
	// Environment 为 EnvProduction 或 EnvSandbox，决定发往哪个根地址。
	Environment string
	// Topic 是 apns-topic（App 的 bundle id）。
	Topic string
	// PushType 是 apns-push-type（alert / background …）。
	PushType string
	// Priority 是 apns-priority（10 立即、5 省电）。
	Priority int
	// Expiration 是 apns-expiration；零值发送 "0"（只尝试一次、不离线存储）。
	Expiration time.Time
	// CollapseID 是 apns-collapse-id；空则不发该头。
	CollapseID string
	// Payload 是 JSON 载荷。
	Payload []byte
}

// Result 是一次发送的结果。
type Result struct {
	// Status 是 APNs 的 HTTP 状态码；请求没有得到响应时为 0。
	Status int
	// Reason 是 APNs 错误体里的 reason（如 BadDeviceToken）；成功或没有响应时为空。
	Reason string
	// Unregistered 表示令牌已失效：410，或 400 的 BadDeviceToken（令牌无效或不属于该环境）。
	Unregistered bool
	// TopicMismatch 表示令牌不属于该 topic（400 DeviceTokenNotForTopic）。
	TopicMismatch bool
	// Retry 表示可重试的失败（网络错误、429、5xx）。
	Retry      bool
	RetryAfter time.Duration
	// Err 为空表示成功；错误文本不含设备令牌与 provider token。
	Err error
}

// Client 发送 APNs 通知并缓存 provider token（并发安全）。HTTP、Key、KeyID、TeamID 必填。
type Client struct {
	// HTTP 必须能走 HTTP/2（APNs 只接受 HTTP/2）。
	HTTP *http.Client
	Key  *ecdsa.PrivateKey
	// KeyID 是 .p8 key 的 Key ID（JWT 头 kid），TeamID 是开发者团队 ID（JWT 声明 iss）。
	KeyID, TeamID string
	// ProductionURL / SandboxURL 为空时用 Apple 的正式地址；测试指向 httptest。
	ProductionURL, SandboxURL string
	// Now 为空时用 time.Now。
	Now func() time.Time

	mu     sync.Mutex
	token  string
	issued time.Time
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) base(env string) (string, error) {
	switch env {
	case EnvProduction:
		if c.ProductionURL != "" {
			return strings.TrimRight(c.ProductionURL, "/"), nil
		}
		return ProductionURL, nil
	case EnvSandbox:
		if c.SandboxURL != "" {
			return strings.TrimRight(c.SandboxURL, "/"), nil
		}
		return SandboxURL, nil
	}
	return "", fmt.Errorf("apns: unknown environment %q", env)
}

// Send 发送一条通知。provider token 被拒（403 ExpiredProviderToken / InvalidProviderToken）时重签并重试一次。
func (c *Client) Send(ctx context.Context, n Notification) Result {
	if len(n.Payload) > MaxPayload {
		return Result{Err: fmt.Errorf("apns: payload is %d bytes, limit %d", len(n.Payload), MaxPayload)}
	}
	base, err := c.base(n.Environment)
	if err != nil {
		return Result{Err: err}
	}
	jwt, err := c.providerToken("")
	if err != nil {
		return Result{Err: err}
	}
	r := c.do(ctx, base, n, jwt)
	if r.Status == http.StatusForbidden && (r.Reason == "ExpiredProviderToken" || r.Reason == "InvalidProviderToken") {
		if jwt, err = c.providerToken(jwt); err != nil {
			return Result{Err: err}
		}
		r = c.do(ctx, base, n, jwt)
	}
	return r
}

func (c *Client) do(ctx context.Context, base string, n Notification, jwt string) Result {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/3/device/"+n.Token, bytes.NewReader(n.Payload))
	if err != nil {
		return Result{Err: errors.New("apns: build request")}
	}
	exp := "0"
	if !n.Expiration.IsZero() {
		exp = strconv.FormatInt(n.Expiration.Unix(), 10)
	}
	req.Header.Set("Authorization", "bearer "+jwt)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apns-topic", n.Topic)
	req.Header.Set("apns-push-type", n.PushType)
	req.Header.Set("apns-priority", strconv.Itoa(n.Priority))
	req.Header.Set("apns-expiration", exp)
	if n.CollapseID != "" {
		req.Header.Set("apns-collapse-id", n.CollapseID)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// *url.Error 的文本含 URL（即设备令牌）：只保留是否取消
		if errors.Is(err, context.Canceled) {
			return Result{Err: errors.New("apns: request canceled")}
		}
		return Result{Retry: true, Err: errors.New("apns: request failed")}
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if resp.StatusCode == http.StatusOK {
		return Result{Status: resp.StatusCode}
	}
	var e struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(rb, &e)
	r := Result{Status: resp.StatusCode, Reason: e.Reason, Err: fmt.Errorf("apns %s (HTTP %d)", e.Reason, resp.StatusCode)}
	switch {
	case resp.StatusCode == http.StatusGone,
		resp.StatusCode == http.StatusBadRequest && e.Reason == "BadDeviceToken":
		r.Unregistered = true
	case resp.StatusCode == http.StatusBadRequest && e.Reason == "DeviceTokenNotForTopic":
		r.TopicMismatch = true
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		r.Retry = true
		if sec, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && sec > 0 {
			r.RetryAfter = time.Duration(sec) * time.Second
		}
	}
	return r
}

// providerToken 返回缓存的 provider token（未满 50 分钟），否则签发新的并缓存。
// rejected 非空表示它刚被 APNs 拒绝：缓存的仍是它时强制重签。
func (c *Client) providerToken(rejected string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.token != "" && c.token != rejected && now.Sub(c.issued) < tokenTTL && !now.Before(c.issued) {
		return c.token, nil
	}
	t, err := c.sign(now)
	if err != nil {
		return "", err
	}
	c.token, c.issued = t, now
	return t, nil
}

// sign 构造 ES256 JWT：头 {alg: ES256, kid}，声明 {iss: TeamID, iat}；签名是 32 字节 r ‖ 32 字节 s。
func (c *Client) sign(now time.Time) (string, error) {
	h, err := json.Marshal(map[string]string{"alg": "ES256", "kid": c.KeyID})
	if err != nil {
		return "", err
	}
	cl, err := json.Marshal(map[string]any{"iss": c.TeamID, "iat": now.Unix()})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding.EncodeToString
	signing := enc(h) + "." + enc(cl)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, c.Key, sum[:])
	if err != nil {
		return "", fmt.Errorf("apns: sign provider token: %w", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + enc(sig), nil
}
