package orbs

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// TokenKeyFile 是 data_dir 下 orb 令牌派生密钥的文件名。
const TokenKeyFile = "orb-token.key"

// LoadOrCreateTokenKey 读取（首次生成）<dataDir>/orb-token.key（32 字节随机，0600）。
//
// orb 令牌 = "oot_" + base64url(HMAC-SHA256(key, orbID ":" gen))：gen 是 orbs.token_gen，每次入队 spawn 时 +1，
// 旧令牌随即失效（泄露的令牌活不过下一次唤醒）。库中只存当前令牌的 SHA-256。
func LoadOrCreateTokenKey(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, TokenKeyFile)
	b, err := os.ReadFile(path)
	if err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("%s: 长度 %d，应为 32 字节", path, len(b))
		}
		return b, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return LoadOrCreateTokenKey(dataDir)
	}
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		return nil, err
	}
	return key, f.Close()
}

// orbToken 由密钥派生 orb 第 gen 代令牌（gen 0 = 引入轮换之前的格式，已有 orb 的令牌保持有效直到下一次 spawn）。
func orbToken(key []byte, orbID string, gen int64) string {
	msg := "oryn-orb-token:" + orbID
	if gen > 0 {
		msg += ":" + strconv.FormatInt(gen, 10)
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return "oot_" + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
