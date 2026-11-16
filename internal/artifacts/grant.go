package artifacts

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// GrantTTL 是发布页授权的有效期。
const GrantTTL = 12 * time.Hour

// DeriveGrantKey 由控制面的持久密钥（orb-token.key）派生发布页授权的 HMAC 密钥：HMAC 加用途标签做域分离，
// 与 share.DeriveRoleKeySecret 同构，两个用途互不可推。
func DeriveGrantKey(master []byte) []byte {
	m := hmac.New(sha256.New, master)
	m.Write([]byte("oryn-artifact-grant"))
	return m.Sum(nil)
}

// grantClaims 是授权的内容：发布 slug、签发时的访问码代数、过期时间（毫秒）。
type grantClaims struct {
	S string `json:"s"`
	G int64  `json:"g"`
	E int64  `json:"e"`
}

var grantEncoding = base64.RawURLEncoding

func grantSign(key []byte, payload string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(payload))
	return grantEncoding.EncodeToString(m.Sum(nil))
}

// MakeGrant 签发授权：`base64url(JSON{s, g, e}) + "." + base64url(HMAC-SHA256(key, 前一段))`。
func MakeGrant(key []byte, slug string, gen int64, exp time.Time) string {
	b, _ := json.Marshal(grantClaims{S: slug, G: gen, E: exp.UnixMilli()})
	payload := grantEncoding.EncodeToString(b)
	return payload + "." + grantSign(key, payload)
}

// VerifyGrant 校验授权：签名正确、属于 slug、代数等于当前的 gen、在 now 时尚未过期。
// 签名按编码后的文本比较，任何一个字符被改动都校验失败。
func VerifyGrant(key []byte, token, slug string, gen int64, now time.Time) bool {
	payload, sig, ok := strings.Cut(token, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(grantSign(key, payload))) {
		return false
	}
	b, err := grantEncoding.DecodeString(payload)
	if err != nil {
		return false
	}
	var c grantClaims
	if json.Unmarshal(b, &c) != nil {
		return false
	}
	return c.S == slug && c.G == gen && now.UnixMilli() < c.E
}
