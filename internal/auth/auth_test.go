package auth

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func TestTickets(t *testing.T) {
	tk := NewTickets()
	a, exp := tk.Issue()
	assert.Equal(t, 30, exp)
	assert.True(t, strings.HasPrefix(a, "owt_"))
	assert.True(t, tk.Redeem(a))
	assert.False(t, tk.Redeem(a), "一次性")
	assert.False(t, tk.Redeem("owt_unknown"))

	b, _ := tk.Issue()
	tk.mu.Lock()
	tk.m[b] = time.Now().Add(-time.Second)
	tk.mu.Unlock()
	assert.False(t, tk.Redeem(b), "过期")
}

func TestPairCodeAndRecoveryCodes(t *testing.T) {
	for range 50 {
		c := newPairCode()
		assert.Regexp(t, `^[A-Z2-9]{4}-[A-Z2-9]{4}$`, c)
		assert.NotContains(t, c, "0")
		assert.NotContains(t, c, "O")
		assert.NotContains(t, c, "I")
		assert.NotContains(t, c, "1")
		assert.Equal(t, c, NormalizePairCode(strings.ToLower(strings.ReplaceAll(c, "-", " "))))
	}
	codes := newRecoveryCodes()
	require.Len(t, codes, 10)
	seen := map[string]bool{}
	for _, c := range codes {
		assert.Regexp(t, `^[a-z2-7]{4}-[a-z2-7]{4}-[a-z2-7]{4}-[a-z2-7]{4}$`, c)
		assert.False(t, seen[c])
		seen[c] = true
		assert.Equal(t, hashRecoveryCode(c), hashRecoveryCode(" "+strings.ToUpper(c)+" "))
	}
}

func TestEnsureSetupToken(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	defer st.Close()

	u1, created, err := EnsureSetupToken(ctx, st, dir, "https://oryn.test/")
	require.NoError(t, err)
	require.True(t, created)
	assert.True(t, strings.HasPrefix(u1, "https://oryn.test/setup?token=ost_"))
	tok1 := tokenOf(t, u1)
	h1, err := checkSetupToken(dir, tok1)
	require.NoError(t, err)

	// 重新生成后旧令牌失效
	u2, created, err := EnsureSetupToken(ctx, st, dir, "https://oryn.test")
	require.NoError(t, err)
	require.True(t, created)
	_, err = checkSetupToken(dir, tok1)
	assert.Error(t, err)
	h2, err := checkSetupToken(dir, tokenOf(t, u2))
	require.NoError(t, err)
	assert.NotEqual(t, h1, h2)
	_, err = checkSetupToken(dir, "opv_wrongprefix")
	assert.Error(t, err)

	consumeSetupToken(dir, h1) // 不是当前令牌，不删除
	_, err = checkSetupToken(dir, tokenOf(t, u2))
	require.NoError(t, err)
	consumeSetupToken(dir, h2)
	_, err = checkSetupToken(dir, tokenOf(t, u2))
	assert.Error(t, err)

	require.NoError(t, st.Q.CreateOwner(ctx, db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte{1}, CreatedAt: 1}))
	u3, created, err := EnsureSetupToken(ctx, st, dir, "https://oryn.test")
	require.NoError(t, err)
	assert.False(t, created)
	assert.Empty(t, u3)
}

func tokenOf(t *testing.T, setupURL string) string {
	t.Helper()
	u, err := url.Parse(setupURL)
	require.NoError(t, err)
	return u.Query().Get("token")
}
