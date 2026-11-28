package auth

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// RFC 6238 附录 B 的 SHA1 测试向量（8 位结果取末 6 位）。
func TestTOTPRFCVectors(t *testing.T) {
	key := []byte("12345678901234567890")
	for unix, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		assert.Equal(t, want, totpCode(key, unix/totpPeriod), unix)
		step, ok := matchTOTP(key, want, time.Unix(unix, 0))
		assert.True(t, ok, unix)
		assert.Equal(t, unix/totpPeriod, step)
	}
	_, ok := matchTOTP(key, "287082", time.Unix(59+3*totpPeriod, 0))
	assert.False(t, ok, "超出前后一个时间步的容忍窗口")
}

func TestPasswordHash(t *testing.T) {
	h, err := hashPassword("correct horse battery")
	assert.NoError(t, err)
	assert.True(t, verifyPassword(h, "correct horse battery"))
	assert.False(t, verifyPassword(h, "correct horse batterY"))
	assert.False(t, verifyPassword("$argon2id$garbage", "correct horse battery"))
	h2, err := hashPassword("correct horse battery")
	assert.NoError(t, err)
	assert.NotEqual(t, h, h2, "每次加盐")
}

// setTestPassword 直接写入一套密码 + TOTP，返回明文 TOTP 密钥。
func setTestPassword(t *testing.T, svc *Service, pw string) []byte {
	t.Helper()
	key := make([]byte, totpSecretBytes)
	key[0] = byte(time.Now().UnixNano())
	hash, err := hashPassword(pw)
	require.NoError(t, err)
	enc, err := svc.box.Seal(totpAAD, key)
	require.NoError(t, err)
	require.NoError(t, svc.st.Q.SetOwnerPassword(context.Background(), db.SetOwnerPasswordParams{PasswordHash: &hash, TotpSecretEnc: enc}))
	return key
}

// 校验通过之后、用掉时间步之前，凭据被重设或关闭（时间步随之归零）：旧快照不能再签出会话。
func TestPasswordCheckRejectsStaleSnapshot(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	require.NoError(t, svc.st.Q.CreateOwner(ctx, db.CreateOwnerParams{DisplayName: "Owner", UserHandle: []byte("h"), CreatedAt: 1}))
	const pw = "correct horse battery"
	step := time.Now().Unix() / totpPeriod
	ran := 0
	then := func(*db.Queries) error { ran++; return nil }

	key := setTestPassword(t, svc, pw)
	c, err := svc.verifyPasswordLogin(ctx, pw, totpCode(key, step))
	require.NoError(t, err)
	setTestPassword(t, svc, "another passphrase")
	assert.ErrorIs(t, svc.consumePasswordCheck(ctx, c, then), errBadPassword, "重设之后")

	key = setTestPassword(t, svc, pw)
	c, err = svc.verifyPasswordLogin(ctx, pw, totpCode(key, step))
	require.NoError(t, err)
	require.NoError(t, svc.st.Q.ClearOwnerPassword(ctx))
	assert.ErrorIs(t, svc.consumePasswordCheck(ctx, c, then), errBadPassword, "关闭之后")
	assert.Zero(t, ran)

	key = setTestPassword(t, svc, pw)
	c, err = svc.verifyPasswordLogin(ctx, pw, totpCode(key, step))
	require.NoError(t, err)
	require.NoError(t, svc.consumePasswordCheck(ctx, c, then))
	assert.ErrorIs(t, svc.consumePasswordCheck(ctx, c, then), errBadPassword, "同一时间步只能用一次")
	assert.Equal(t, 1, ran)
}

// 两场仪式持同一个恢复会话：只有先消耗它的那场能写凭据、签发会话。
func TestCompleteSetupConsumesRecoveryOnce(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	require.NoError(t, svc.st.Q.CreateOwner(ctx, db.CreateOwnerParams{DisplayName: "Owner", UserHandle: []byte("h"), CreatedAt: 1}))
	tok, _, err := createSession(ctx, svc.st.Q, sessionKindRecovery, "", RecoveryTTL, sessionAuth{})
	require.NoError(t, err)
	c := &ceremony{RecoverySession: ids.Hash(tok)}
	writes := 0
	write := func(*db.Queries) ([]string, error) { writes++; return nil, nil }
	r := httptest.NewRequest("POST", "/", nil)

	res, err := svc.completeSetup(ctx, r, c, sessionAuth{password: true}, write)
	require.NoError(t, err)
	assert.NotEmpty(t, res.Cookie)
	_, err = svc.completeSetup(ctx, r, c, sessionAuth{password: true}, write)
	var ae *Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, 401, ae.Status)
	assert.Equal(t, 1, writes)

	// 过期的恢复会话同样不能消耗
	expired, _, err := createSession(ctx, svc.st.Q, sessionKindRecovery, "", -time.Second, sessionAuth{})
	require.NoError(t, err)
	_, err = svc.completeSetup(ctx, r, &ceremony{RecoverySession: ids.Hash(expired)}, sessionAuth{}, write)
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, 1, writes)
}
