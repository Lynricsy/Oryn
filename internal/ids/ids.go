// Package ids 生成 Oryn 的各类 ID 与令牌。
package ids

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/oklog/ulid/v2"
)

// Thread 返回 T-<uuidv7>。
func Thread() string { return "T-" + uuid.Must(uuid.NewV7()).String() }

// Orb 返回 orb-<26 位小写 ULID>（同时用作容器名与 hostname）。
func Orb() string { return "orb-" + strings.ToLower(newULID()) }

// Snapshot 返回 oryn-snap-<小写 ULID>（Docker 镜像名）。
func Snapshot() string { return "oryn-snap-" + strings.ToLower(newULID()) }

// New 返回 <prefix>_<ULID>，如 prj_01J…。
func New(prefix string) string { return prefix + "_" + newULID() }

// newULID 用 crypto/rand 生成单调 ULID：熵源不可预测（ulid.Make 的熵源可预测，而部分 id 在 orb 内可见），
// 同一毫秒内仍严格递增（任务队列等按 id 排序依赖这一点）。
func newULID() string {
	ulidMu.Lock()
	defer ulidMu.Unlock()
	return ulid.MustNew(ulid.Now(), ulidEntropy).String()
}

var (
	ulidMu      sync.Mutex
	ulidEntropy = ulid.Monotonic(rand.Reader, 0)
)

// Token 返回 <prefix>_<32 字节随机 base64url>，如 opv_…。
func Token(prefix string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(b)
}

// Hash 返回令牌的 SHA-256 十六进制（库中只存它）。
func Hash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// TokenPrefix 返回令牌前缀（"opv"、"oot"、"odt"、"ost"），格式不符时返回空串。
func TokenPrefix(token string) string {
	p, _, ok := strings.Cut(token, "_")
	if !ok || len(p) != 3 {
		return ""
	}
	return p
}
