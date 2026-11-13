// Package webpushtest 提供 Web Push 的测试辅助：生成用户代理密钥、按 RFC 8291 解密载荷、校验 VAPID 头。仅供测试使用。
package webpushtest

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/Lynricsy/Oryn/internal/webpush"
)

// Client 是一个模拟浏览器的订阅方：持有用户代理私钥与 auth secret。
type Client struct {
	UA   *ecdh.PrivateKey
	Auth []byte
	Sub  webpush.Subscription
}

// NewClient 生成随机的用户代理密钥与 auth secret。
func NewClient(endpoint string) (*Client, error) {
	ua, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		return nil, err
	}
	enc := base64.RawURLEncoding.EncodeToString
	return &Client{UA: ua, Auth: auth, Sub: webpush.Subscription{Endpoint: endpoint, P256dh: enc(ua.PublicKey().Bytes()), Auth: enc(auth)}}, nil
}

// Decrypt 解密 aes128gcm 单记录载荷。
func (c *Client) Decrypt(body []byte) ([]byte, error) { return Decrypt(body, c.UA, c.Auth) }

// Decrypt 是 webpush.Encrypt 的逆过程（不支持多记录）。
func Decrypt(body []byte, ua *ecdh.PrivateKey, authSecret []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("webpushtest: body too short")
	}
	salt := body[:16]
	idLen := int(body[20])
	if len(body) < 21+idLen {
		return nil, errors.New("webpushtest: body too short")
	}
	asRaw := body[21 : 21+idLen]
	ct := body[21+idLen:]
	asPub, err := ecdh.P256().NewPublicKey(asRaw)
	if err != nil {
		return nil, err
	}
	secret, err := ua.ECDH(asPub)
	if err != nil {
		return nil, err
	}
	info := "WebPush: info\x00" + string(ua.PublicKey().Bytes()) + string(asRaw)
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
	p, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, err
	}
	p = bytes.TrimRight(p, "\x00")
	if len(p) == 0 || p[len(p)-1] != 0x02 {
		return nil, errors.New("webpushtest: missing last-record delimiter")
	}
	return p[:len(p)-1], nil
}

// VerifyVAPID 解析 `vapid t=<JWT>, k=<公钥>`：k 须等于 pub，JWT 头须为 ES256 且签名有效；返回 JWT 声明。
func VerifyVAPID(header, pub string) (map[string]any, error) {
	rest, ok := strings.CutPrefix(header, "vapid t=")
	if !ok {
		return nil, fmt.Errorf("webpushtest: not a vapid header: %q", header)
	}
	jwt, k, ok := strings.Cut(rest, ", k=")
	if !ok || k != pub {
		return nil, fmt.Errorf("webpushtest: k mismatch: %q", header)
	}
	seg := strings.Split(jwt, ".")
	if len(seg) != 3 {
		return nil, errors.New("webpushtest: malformed JWT")
	}
	dec := base64.RawURLEncoding.DecodeString
	hdr, err := dec(seg[0])
	if err != nil {
		return nil, err
	}
	var h struct{ Typ, Alg string }
	if err := json.Unmarshal(hdr, &h); err != nil || h.Typ != "JWT" || h.Alg != "ES256" {
		return nil, fmt.Errorf("webpushtest: bad JWT header %s", hdr)
	}
	sig, err := dec(seg[2])
	if err != nil || len(sig) != 64 {
		return nil, errors.New("webpushtest: bad signature encoding")
	}
	raw, err := webpush.DecodeKey(pub)
	if err != nil {
		return nil, errors.New("webpushtest: bad public key")
	}
	pk, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	if err != nil {
		return nil, fmt.Errorf("webpushtest: bad public key: %w", err)
	}
	digest := sha256.Sum256([]byte(seg[0] + "." + seg[1]))
	if !ecdsa.Verify(pk, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return nil, errors.New("webpushtest: signature verification failed")
	}
	cb, err := dec(seg[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(cb, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}
