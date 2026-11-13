// Package webpush 实现 Web Push 的服务端（M5 计划 3.3）：RFC 8291 aes128gcm 载荷加密与 RFC 8292 VAPID 鉴权，
// 只用标准库。
package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	keyFile = "vapid.key"
	// recordSize 是单记录的 rs 字段值。
	recordSize = 4096
	// MaxPlaintext 是单记录可承载的明文上限（推送服务普遍限制请求体 4096 字节：去掉 86 字节头、16 字节标签与 1 字节分隔符）。
	MaxPlaintext = 3993
)

// ErrTooLarge 表示明文超过 MaxPlaintext。
var ErrTooLarge = errors.New("webpush: payload too large")

// Keys 是 VAPID 用的 P-256 私钥。
type Keys struct {
	priv *ecdsa.PrivateKey
	pub  string // base64url 无填充的 65 字节未压缩点
}

// LoadOrCreateKeys 读取 <dataDir>/vapid.key（32 字节私钥标量）；不存在时生成（0600）。
func LoadOrCreateKeys(dataDir string) (*Keys, error) {
	p := filepath.Join(dataDir, keyFile)
	raw, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		raw, err = priv.Bytes()
		if err != nil {
			return nil, err
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, fmt.Errorf("创建 VAPID 私钥: %w", err)
		}
		if _, err := f.Write(raw); err != nil {
			_ = f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("读取 VAPID 私钥: %w", err)
	}
	return NewKeys(raw)
}

// NewKeys 用 32 字节私钥标量构造 Keys。
func NewKeys(scalar []byte) (*Keys, error) {
	priv, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), scalar)
	if err != nil {
		return nil, fmt.Errorf("VAPID 私钥无效: %w", err)
	}
	pub, err := priv.PublicKey.Bytes()
	if err != nil {
		return nil, err
	}
	return &Keys{priv: priv, pub: b64(pub)}, nil
}

// PublicKey 返回 base64url（无填充）的 65 字节未压缩公钥点（applicationServerKey）。
func (k *Keys) PublicKey() string { return k.pub }

// Subscription 是浏览器 PushSubscription 的端点与密钥（base64url，带或不带填充均可）。
type Subscription struct{ Endpoint, P256dh, Auth string }

// Options 是一次推送的请求参数。
type Options struct {
	TTL     int
	Urgency string // very-low | low | normal | high；空 = 不发该头
	Topic   string // 空 = 不发该头
	Subject string // VAPID sub（https URL 或 mailto:）
}

// DecodeKey 解码 base64url（容忍填充与标准字母表）。
func DecodeKey(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	s = strings.NewReplacer("+", "-", "/", "_").Replace(s)
	return base64.RawURLEncoding.DecodeString(s)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// Encrypt 按 RFC 8291 以 aes128gcm 单记录加密明文。rnd 依次提供 32 字节临时私钥标量与 16 字节 salt。
func Encrypt(plaintext []byte, sub Subscription, rnd io.Reader) ([]byte, error) {
	if len(plaintext) > MaxPlaintext {
		return nil, ErrTooLarge
	}
	uaRaw, err := DecodeKey(sub.P256dh)
	if err != nil {
		return nil, fmt.Errorf("webpush: p256dh: %w", err)
	}
	uaPub, err := ecdh.P256().NewPublicKey(uaRaw)
	if err != nil {
		return nil, fmt.Errorf("webpush: p256dh: %w", err)
	}
	authSecret, err := DecodeKey(sub.Auth)
	if err != nil || len(authSecret) != 16 {
		return nil, errors.New("webpush: auth secret must be 16 bytes")
	}
	var asPriv *ecdh.PrivateKey
	for range 8 {
		scalar := make([]byte, 32)
		if _, err := io.ReadFull(rnd, scalar); err != nil {
			return nil, err
		}
		if asPriv, err = ecdh.P256().NewPrivateKey(scalar); err == nil {
			break
		}
	}
	if asPriv == nil {
		return nil, errors.New("webpush: cannot derive ephemeral key")
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rnd, salt); err != nil {
		return nil, err
	}
	secret, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, err
	}
	asPub := asPriv.PublicKey().Bytes()
	info := "WebPush: info\x00" + string(uaRaw) + string(asPub)
	ikm, err := hkdf.Key(sha256.New, secret, authSecret, info, 32)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 16+4+1+len(asPub)+len(plaintext)+1+gcm.Overhead())
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, recordSize)
	out = append(out, byte(len(asPub)))
	out = append(out, asPub...)
	padded := append(append(make([]byte, 0, len(plaintext)+1), plaintext...), 0x02)
	return gcm.Seal(out, nonce, padded, nil), nil
}

// Authorization 返回 VAPID 头值 `vapid t=<JWT>, k=<公钥>`；JWT 的 aud 为端点的 origin，有效期 12 小时。
func (k *Keys) Authorization(endpoint, subject string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("webpush: invalid endpoint %q", endpoint)
	}
	claims, err := json.Marshal(map[string]any{
		"aud": u.Scheme + "://" + u.Host,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": subject,
	})
	if err != nil {
		return "", err
	}
	input := b64([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + b64(claims)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, k.priv, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + input + "." + b64(sig) + ", k=" + k.pub, nil
}

// Send 加密载荷并 POST 到订阅端点，返回 HTTP 状态码（网络错误时为 0 与 err）。
func (k *Keys) Send(ctx context.Context, hc *http.Client, sub Subscription, payload []byte, o Options) (int, error) {
	body, err := Encrypt(payload, sub, rand.Reader)
	if err != nil {
		return 0, err
	}
	authz, err := k.Authorization(sub.Endpoint, o.Subject, time.Now())
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(o.TTL))
	if o.Urgency != "" {
		req.Header.Set("Urgency", o.Urgency)
	}
	if o.Topic != "" {
		req.Header.Set("Topic", o.Topic)
	}
	req.Header.Set("Authorization", authz)
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
