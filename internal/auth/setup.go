package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
)

// setupTokenFile 保存当前有效初始化令牌（ost_…）的 SHA-256，位于 data_dir 下（0600）。
// 用文件而不是表：`orynd setup-token` 在独立进程中重新生成后，运行中的 orynd 下次校验即生效。
const setupTokenFile = "setup-token.sha256"

// ErrAlreadyInitialized 表示已存在所有者，不再签发初始化令牌。
var ErrAlreadyInitialized = errors.New("已完成初始化；丢失 Passkey 请在 /login 使用恢复码")

// EnsureSetupToken 在尚无所有者时生成新的 ost_ 初始化令牌（覆盖旧令牌）并返回
// `<public_url>/setup#token=…`（令牌放在 URL 片段里，不进反代访问日志；页面从 location.hash 读出后照旧放进请求体），created=true；已有所有者时返回 created=false 且不改动任何东西。
func EnsureSetupToken(ctx context.Context, st *store.Store, dataDir, publicURL string) (string, bool, error) {
	if _, err := st.Q.GetOwner(ctx); err == nil {
		return "", false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	u, err := RegenerateSetupToken(dataDir, publicURL)
	return u, err == nil, err
}

// RegenerateSetupToken 无条件生成新的初始化令牌并返回 setup 链接（调用方负责确认尚无所有者）。
func RegenerateSetupToken(dataDir, publicURL string) (string, error) {
	tok := ids.Token("ost")
	p := filepath.Join(dataDir, setupTokenFile)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(ids.Hash(tok)+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("写入初始化令牌: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return "", fmt.Errorf("写入初始化令牌: %w", err)
	}
	return strings.TrimSuffix(publicURL, "/") + "/setup#token=" + url.QueryEscape(tok), nil
}

// currentSetupTokenHash 读取当前初始化令牌哈希；不存在返回空串。
func currentSetupTokenHash(dataDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, setupTokenFile))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// checkSetupToken 校验明文初始化令牌，返回其哈希。
func checkSetupToken(dataDir, tok string) (string, error) {
	cur, err := currentSetupTokenHash(dataDir)
	if err != nil {
		return "", err
	}
	h := ids.Hash(tok)
	if cur == "" || ids.TokenPrefix(tok) != "ost" || subtle.ConstantTimeCompare([]byte(cur), []byte(h)) != 1 {
		return "", newErr(401, "unauthorized", "初始化令牌无效或已被重新生成")
	}
	return h, nil
}

// consumeSetupToken 在初始化完成后删除令牌文件（仅当仍是本次使用的令牌）。
func consumeSetupToken(dataDir, hash string) {
	if cur, _ := currentSetupTokenHash(dataDir); cur == hash {
		_ = os.Remove(filepath.Join(dataDir, setupTokenFile))
	}
}
