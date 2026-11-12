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
)

// TokenKeyFile 是 data_dir 下 orb 令牌派生密钥的文件名。
const TokenKeyFile = "orb-token.key"

// LoadOrCreateTokenKey 读取（首次生成）<dataDir>/orb-token.key（32 字节随机，0600）。
//
// orb 令牌 = "oot_" + base64url(HMAC-SHA256(key, orbID))：控制面随时可以重新算出令牌
// （唤醒 / 重启 orb 时 spawn 任务需要它），库中仍只存其 SHA-256。
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

// orbToken 由密钥派生 orb 令牌。
func orbToken(key []byte, orbID string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("oryn-orb-token:" + orbID))
	return "oot_" + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
