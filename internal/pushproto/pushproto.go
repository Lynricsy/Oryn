// Package pushproto 是官方推送网关的线上协议（docs/protocol.md「官方推送网关」）：请求签名、主机密钥、
// 折叠键与 JSON 结构。orynd（主机侧）与网关（验签侧）共用；壳的 Rust 实现以 schema/pushgw/sign-vectors.json 对齐。
package pushproto

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
)

// 请求头。
const (
	HeaderHostKey   = "Oryn-Host-Key"
	HeaderDeviceKey = "Oryn-Device-Key"
	HeaderTimestamp = "Oryn-Timestamp"
	HeaderSignature = "Oryn-Signature"
)

const (
	// Context 是签名输入的第一行。
	Context = "oryn-push-gw/1"
	// MaxClockSkew 是请求时间与网关时钟的最大偏差。
	MaxClockSkew = 5 * time.Minute
	// MaxBody 是请求体上限。
	MaxBody = 8 << 10
)

// 签名者种类。
const (
	SignerHost   = "host"
	SignerDevice = "device"
)

// 验签错误。
var (
	ErrMalformed    = errors.New("pushproto: malformed signed request")
	ErrBadSignature = errors.New("pushproto: bad signature")
	ErrClockSkew    = errors.New("pushproto: clock skew")
)

// Signer 是验签通过的签名者：Kind 为 SignerHost / SignerDevice，Key 为请求头里的公钥原文（base64url）。
type Signer struct {
	Kind string
	Key  string
}

var b64 = base64.RawURLEncoding

// SigningInput 是两种身份共用的签名输入：
// "oryn-push-gw/1\n" + METHOD + "\n" + origin + path + "\n" + timestamp + "\n" + hex(sha256(body))。
func SigningInput(method, origin, path string, timestampMs int64, body []byte) []byte {
	sum := sha256.Sum256(body)
	var b bytes.Buffer
	b.WriteString(Context)
	b.WriteByte('\n')
	b.WriteString(method)
	b.WriteByte('\n')
	b.WriteString(origin)
	b.WriteString(path)
	b.WriteByte('\n')
	b.WriteString(strconv.FormatInt(timestampMs, 10))
	b.WriteByte('\n')
	b.WriteString(hex.EncodeToString(sum[:]))
	return b.Bytes()
}

// SignHost 给发往网关 origin 的请求加主机签名头；body 必须与请求体逐字节相同。
func SignHost(req *http.Request, origin string, key ed25519.PrivateKey, now time.Time, body []byte) {
	ts := now.UnixMilli()
	sig := ed25519.Sign(key, SigningInput(req.Method, origin, req.URL.EscapedPath(), ts, body))
	req.Header.Set(HeaderHostKey, HostKeyString(key.Public().(ed25519.PublicKey)))
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderSignature, b64.EncodeToString(sig))
}

// HostKeyString 是主机公钥的线上形态：base64url 无填充的 32 字节。
func HostKeyString(pub ed25519.PublicKey) string { return b64.EncodeToString(pub) }

// ParseDeviceKey 解析设备公钥（base64url 无填充的 65 字节未压缩 P-256 点）。
func ParseDeviceKey(s string) (*ecdsa.PublicKey, error) {
	raw, err := b64.DecodeString(s)
	if err != nil || len(raw) != 65 || raw[0] != 4 {
		return nil, ErrMalformed
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	if err != nil {
		return nil, ErrMalformed
	}
	return pub, nil
}

// ParseHostKey 解析主机公钥。
func ParseHostKey(s string) (ed25519.PublicKey, error) {
	raw, err := b64.DecodeString(s)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, ErrMalformed
	}
	return ed25519.PublicKey(raw), nil
}

// ReadBody 读取至多 MaxBody 字节的请求体；超出返回 ErrTooBig（调用方回 413）。
func ReadBody(r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxBody {
		return nil, ErrTooBig
	}
	return b, nil
}

// ErrTooBig 表示请求体超过 MaxBody。
var ErrTooBig = errors.New("pushproto: body too large")

// Verify 校验请求签名：恰有主机或设备两种身份头之一、不带查询串、时间在 MaxClockSkew 内、签名覆盖 origin + 路径 + 请求体。
func Verify(r *http.Request, origin string, body []byte, now time.Time) (Signer, error) {
	host, device := r.Header.Get(HeaderHostKey), r.Header.Get(HeaderDeviceKey)
	if (host == "") == (device == "") || r.URL.RawQuery != "" {
		return Signer{}, ErrMalformed
	}
	ts, err := strconv.ParseInt(r.Header.Get(HeaderTimestamp), 10, 64)
	if err != nil {
		return Signer{}, ErrMalformed
	}
	sig, err := b64.DecodeString(r.Header.Get(HeaderSignature))
	if err != nil || len(sig) == 0 {
		return Signer{}, ErrMalformed
	}
	if d := now.Sub(time.UnixMilli(ts)); d > MaxClockSkew || d < -MaxClockSkew {
		return Signer{}, ErrClockSkew
	}
	input := SigningInput(r.Method, origin, r.URL.EscapedPath(), ts, body)
	if host != "" {
		pub, err := ParseHostKey(host)
		if err != nil {
			return Signer{}, err
		}
		if !ed25519.Verify(pub, input, sig) {
			return Signer{}, ErrBadSignature
		}
		return Signer{Kind: SignerHost, Key: host}, nil
	}
	pub, err := ParseDeviceKey(device)
	if err != nil {
		return Signer{}, err
	}
	sum := sha256.Sum256(input)
	if !ecdsa.VerifyASN1(pub, sum[:], sig) {
		return Signer{}, ErrBadSignature
	}
	return Signer{Kind: SignerDevice, Key: device}, nil
}

// Collapse 是 APNs 的 apns-collapse-id：base64url(sha256(grant + tag)) 的前 22 个字符，网关看不出线程。
func Collapse(grant, tag string) string {
	sum := sha256.Sum256([]byte(grant + tag))
	return b64.EncodeToString(sum[:])[:22]
}
