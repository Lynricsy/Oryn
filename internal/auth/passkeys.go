package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// passkeyNameMax 是 Passkey 显示名的最大字符数。
const passkeyNameMax = 80

var errPasskeyNotFound = newErr(http.StatusNotFound, "not_found", "Passkey 不存在")

// PasskeyInfo 是所有者的一把 Passkey。
type PasskeyInfo struct {
	// ID 是 credential id 的无填充 base64url。
	ID         string
	Name       string
	CreatedAt  int64
	LastUsedAt *int64
	Transports []string
	// BackupEligible / BackedUp 取自凭据 Flags（BE / BS）。
	BackupEligible, BackedUp bool
	// Current 为真表示当前会话由这把登录。
	Current bool
}

// passkeyInfo 把库里的一行转成 PasskeyInfo。
func passkeyInfo(pk db.Passkey, p Principal) (PasskeyInfo, error) {
	var c webauthn.Credential
	if err := json.Unmarshal([]byte(pk.Credential), &c); err != nil {
		return PasskeyInfo{}, fmt.Errorf("解析 Passkey 记录: %w", err)
	}
	transports := []string{}
	if pk.Transports != "" {
		if err := json.Unmarshal([]byte(pk.Transports), &transports); err != nil {
			return PasskeyInfo{}, fmt.Errorf("解析 Passkey transports: %w", err)
		}
		if transports == nil {
			transports = []string{}
		}
	}
	id := base64.RawURLEncoding.EncodeToString(pk.ID)
	return PasskeyInfo{
		ID: id, Name: pk.Name, CreatedAt: pk.CreatedAt, LastUsedAt: pk.LastUsedAt, Transports: transports,
		BackupEligible: c.Flags.BackupEligible, BackedUp: c.Flags.BackupState,
		Current: p.PasskeyID != "" && p.PasskeyID == id,
	}, nil
}

// decodePasskeyID 解码路径里的 credential id；无效时按不存在处理。
func decodePasskeyID(id string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(b) == 0 {
		return nil, errPasskeyNotFound
	}
	return b, nil
}

// ListPasskeys 按创建时间列出所有者的 Passkey。
func (s *Service) ListPasskeys(ctx context.Context, p Principal) ([]PasskeyInfo, error) {
	if err := requireOwnerSession(p); err != nil {
		return nil, err
	}
	rows, err := s.st.Q.ListPasskeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PasskeyInfo, 0, len(rows))
	for _, pk := range rows {
		info, err := passkeyInfo(pk, p)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

// RenamePasskey 给 Passkey 改名（1–80 个字符）。不要求重新验证。
func (s *Service) RenamePasskey(ctx context.Context, p Principal, id, name string) (PasskeyInfo, error) {
	if err := requireOwnerSession(p); err != nil {
		return PasskeyInfo{}, err
	}
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n == 0 || n > passkeyNameMax {
		return PasskeyInfo{}, newErr(http.StatusBadRequest, "invalid_request", fmt.Sprintf("名称须为 1–%d 个字符", passkeyNameMax))
	}
	raw, err := decodePasskeyID(id)
	if err != nil {
		return PasskeyInfo{}, err
	}
	n, err := s.st.Q.RenamePasskey(ctx, db.RenamePasskeyParams{Name: name, ID: raw})
	if err != nil {
		return PasskeyInfo{}, err
	}
	if n == 0 {
		return PasskeyInfo{}, errPasskeyNotFound
	}
	pk, err := s.st.Q.GetPasskey(ctx, raw)
	if errors.Is(err, sql.ErrNoRows) {
		return PasskeyInfo{}, errPasskeyNotFound
	}
	if err != nil {
		return PasskeyInfo{}, err
	}
	return passkeyInfo(pk, p)
}

// DeletePasskey 删除一把 Passkey，并吊销用它登录的其它会话（当前会话保留）。
// 要求 5 分钟内的 Passkey 验证；最后一把不能删（409 last_passkey）。
func (s *Service) DeletePasskey(ctx context.Context, p Principal, id string) error {
	if err := requireOwnerSession(p); err != nil {
		return err
	}
	if err := RequireRecentAuth(p, time.Now()); err != nil {
		return err
	}
	raw, err := decodePasskeyID(id)
	if err != nil {
		return err
	}
	var revoked []string
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.GetPasskey(ctx, raw); errors.Is(err, sql.ErrNoRows) {
			return errPasskeyNotFound
		} else if err != nil {
			return err
		}
		n, err := q.CountPasskeys(ctx)
		if err != nil {
			return err
		}
		if n <= 1 {
			return newErr(http.StatusConflict, "last_passkey", "不能删除最后一把 Passkey")
		}
		if _, err := q.DeletePasskey(ctx, raw); err != nil {
			return err
		}
		// 吊销靠删行：用这把登录的会话立即失效（关联的 portal 会话等级联删除）
		revoked, err = q.DeleteWebSessionsByPasskey(ctx, db.DeleteWebSessionsByPasskeyParams{PasskeyID: raw, KeepIDHash: p.ID})
		return err
	})
	if err != nil {
		return err
	}
	// publish 只负责通知已建立的 WebSocket 断开
	for _, h := range revoked {
		s.revocations.publish(KindWeb, h)
	}
	return nil
}
