package pushproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// HostKeyFile 是 orynd 数据目录里的主机密钥文件名（与 vapid.key 同一约定：原始字节、0600）。
const HostKeyFile = "push-gateway.key"

// LoadOrCreateHostKey 读取 <dataDir>/push-gateway.key（32 字节 Ed25519 种子）；不存在时生成（0600）。
func LoadOrCreateHostKey(dataDir string) (ed25519.PrivateKey, error) {
	p := filepath.Join(dataDir, HostKeyFile)
	seed, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		seed = make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, fmt.Errorf("创建推送网关主机密钥: %w", err)
		}
		if _, err := f.Write(seed); err != nil {
			_ = f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("读取推送网关主机密钥: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("推送网关主机密钥 %s 不是 %d 字节", p, ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
