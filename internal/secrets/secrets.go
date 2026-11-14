// Package secrets 负责密钥值的加密存储：主密钥文件 + AES-256-GCM。
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

const keyFile = "master.key"

// Box 用主密钥加解密密钥值；大文件用 SealStream / OpenStream 分块流式加密。
type Box struct {
	aead cipher.AEAD
	// key 是主密钥的副本，供流式加密经 HKDF 派生每个文件的子密钥。
	key [32]byte
}

// LoadOrCreate 读取 <dataDir>/master.key；不存在时生成 32 字节随机密钥（0600）。
func LoadOrCreate(dataDir string) (*Box, error) {
	p := filepath.Join(dataDir, keyFile)
	key, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, fmt.Errorf("创建主密钥: %w", err)
		}
		if _, err := f.Write(key); err != nil {
			_ = f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("读取主密钥: %w", err)
	}
	return New(key)
}

// New 用给定 32 字节密钥构造 Box。
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("主密钥长度应为 32 字节，实际 %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	box := &Box{aead: aead}
	copy(box.key[:], key)
	return box, nil
}

// Seal 加密明文；输出 = nonce || 密文。name 作为附加数据绑定，防止密文在记录间挪用。
func (b *Box) Seal(name string, plain []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plain, []byte(name)), nil
}

// Open 解密 Seal 的输出。
func (b *Box) Open(name string, sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("密文过短")
	}
	return b.aead.Open(nil, sealed[:n], sealed[n:], []byte(name))
}

// 密钥模式。
const (
	ModeProxy = "proxy"
	ModeEnv   = "env"
)

var (
	envNameRe    = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	secretNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]{0,199}$`)
)

// ValidName 校验密钥名；env 模式下名称必须是合法环境变量名。
func ValidName(name, mode string) error {
	switch mode {
	case ModeProxy:
		if !secretNameRe.MatchString(name) {
			return fmt.Errorf("密钥名 %q 无效", name)
		}
	case ModeEnv:
		if !envNameRe.MatchString(name) {
			return fmt.Errorf("env 模式的密钥名必须匹配 [A-Z_][A-Z0-9_]*：%q", name)
		}
	default:
		return fmt.Errorf("未知模式 %q", mode)
	}
	return nil
}

// IsEnvName 判断字符串是否是环境变量名形态。
func IsEnvName(s string) bool { return envNameRe.MatchString(s) }
