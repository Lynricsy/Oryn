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

// Recover 作废一个恢复码，签发只能重新注册 Passkey 的一次性会话（15 分钟）。
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
		res.Cookie, res.Expires, err = createSession(ctx, q, sessionKindRecovery, r.UserAgent(), RecoveryTTL)
		if err != nil {
			return err
		}
		left, err := q.CountUnusedRecoveryCodes(ctx)
		res.Remaining = int(left)
		return err
	})
	return res, err
}
