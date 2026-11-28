package auth

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"net/http"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

var recoveryEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newRecoveryCodes 生成 10 个恢复码，形如 abcd-efgh-ijkl-mnop（80 位熵）。
func newRecoveryCodes() []string {
	out := make([]string, recoveryCodeNum)
	for i := range out {
		b := make([]byte, 10)
		_, _ = rand.Read(b)
		s := strings.ToLower(recoveryEncoding.EncodeToString(b))
		out[i] = s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
	}
	return out
}

// normalizeRecoveryCode 去掉分隔符与空白并转小写。
func normalizeRecoveryCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(code) {
		if r == '-' || r == ' ' || r == '\t' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func hashRecoveryCode(code string) string {
	return ids.Hash("oryn-recovery:" + normalizeRecoveryCode(code))
}

// RecoveryResult 是恢复码兑换结果。
type RecoveryResult struct {
	Cookie    string
	Expires   time.Time
	Remaining int
}

// Recover 作废一个恢复码，签发只能重新注册 Passkey 或重设密码登录的一次性会话（15 分钟）。
func (s *Service) Recover(ctx context.Context, r *http.Request, code string) (RecoveryResult, error) {
	var res RecoveryResult
	if normalizeRecoveryCode(code) == "" {
		return res, newErr(http.StatusBadRequest, "invalid_request", "恢复码不能为空")
	}
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		now := time.Now().UnixMilli()
		n, err := q.UseRecoveryCode(ctx, db.UseRecoveryCodeParams{UsedAt: &now, CodeHash: hashRecoveryCode(code)})
		if err != nil {
			return err
		}
		if n == 0 {
			return newErr(http.StatusUnauthorized, "unauthorized", "恢复码无效或已使用")
		}
		res.Cookie, res.Expires, err = createSession(ctx, q, sessionKindRecovery, r.UserAgent(), RecoveryTTL, sessionAuth{})
		if err != nil {
			return err
		}
		left, err := q.CountUnusedRecoveryCodes(ctx)
		res.Remaining = int(left)
		return err
	})
	return res, err
}

// replaceRecoveryCodes 作废全部旧码并写入新码（只存哈希）；须在事务里调用。
func replaceRecoveryCodes(ctx context.Context, q *db.Queries, codes []string, now int64) error {
	if err := q.DeleteAllRecoveryCodes(ctx); err != nil {
		return err
	}
	for _, code := range codes {
		if err := q.InsertRecoveryCode(ctx, db.InsertRecoveryCodeParams{CodeHash: hashRecoveryCode(code), CreatedAt: now}); err != nil {
			return err
		}
	}
	return nil
}

// RecoveryStatus 是恢复码的剩余情况。
type RecoveryStatus struct {
	Total, Remaining int
	// GeneratedAt 是本批恢复码的生成时间（毫秒）；0 = 未知（升级前生成或还没有恢复码）。
	GeneratedAt int64
}

// RecoveryStatus 返回恢复码总数、未用数与生成时间。
func (s *Service) RecoveryStatus(ctx context.Context, p Principal) (RecoveryStatus, error) {
	if err := requireOwnerSession(p); err != nil {
		return RecoveryStatus{}, err
	}
	var st RecoveryStatus
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		total, err := q.CountRecoveryCodes(ctx)
		if err != nil {
			return err
		}
		left, err := q.CountUnusedRecoveryCodes(ctx)
		if err != nil {
			return err
		}
		at, err := q.RecoveryCodesGeneratedAt(ctx)
		if err != nil {
			return err
		}
		st = RecoveryStatus{Total: int(total), Remaining: int(left), GeneratedAt: at}
		return nil
	})
	return st, err
}

// RegenerateRecoveryCodes 作废全部旧码并生成 10 个新码，明文只返回这一次。要求 5 分钟内的 Passkey 验证。
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, p Principal) ([]string, error) {
	if err := requireOwnerSession(p); err != nil {
		return nil, err
	}
	if err := RequireRecentAuth(p, time.Now()); err != nil {
		return nil, err
	}
	codes := newRecoveryCodes()
	now := time.Now().UnixMilli()
	if err := s.st.Tx(ctx, func(q *db.Queries) error {
		return replaceRecoveryCodes(ctx, q, codes, now)
	}); err != nil {
		return nil, err
	}
	return codes, nil
}
