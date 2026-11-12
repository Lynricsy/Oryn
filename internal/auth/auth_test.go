package auth

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func TestTickets(t *testing.T) {
	tk := NewTickets()
	who := Principal{Kind: KindDevice, ID: "dev_1"}
	a, exp := tk.Issue(who)
	assert.Equal(t, 30, exp)
	assert.True(t, strings.HasPrefix(a, "owt_"))
	p, ok := tk.Redeem(a)
	assert.True(t, ok)
	assert.Equal(t, who, p)
	_, ok = tk.Redeem(a)
	assert.False(t, ok, "一次性")
	_, ok = tk.Redeem("owt_unknown")
	assert.False(t, ok)

	b, _ := tk.Issue(who)
	tk.mu.Lock()
	v := tk.m[b]
	v.exp = time.Now().Add(-time.Second)
	tk.m[b] = v
	tk.mu.Unlock()
	_, ok = tk.Redeem(b)
	assert.False(t, ok, "过期")
}

func TestLimiter(t *testing.T) {
	l := newLimiter()
	lim := Limit{Name: "x", Burst: 3, Every: 10 * time.Second}
	now := time.Now()
	for range 3 {
		ok, _ := l.allow(lim, "1.2.3.4", now)
		require.True(t, ok)
	}
	ok, wait := l.allow(lim, "1.2.3.4", now)
	assert.False(t, ok)
	assert.InDelta(t, 10*time.Second, wait, float64(time.Millisecond))
	ok, _ = l.allow(lim, "5.6.7.8", now)
	assert.True(t, ok, "按 IP 隔离")
	ok, _ = l.allow(Limit{Name: "y", Burst: 1, Every: time.Second}, "1.2.3.4", now)
	assert.True(t, ok, "按端点隔离")
	ok, _ = l.allow(lim, "1.2.3.4", now.Add(10*time.Second))
	assert.True(t, ok, "按时间补充")
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	svc, err := NewService(st, dir, "https://oryn.test")
	require.NoError(t, err)
	return svc
}

func TestSealedCeremony(t *testing.T) {
	svc := newTestService(t)
	c := &ceremony{Kind: ceremonyLogin, PasskeyName: "x"}
	id, err := svc.putCeremony(c)
	require.NoError(t, err)

	got, err := svc.takeCeremony(id, ceremonyLogin)
	require.NoError(t, err)
	assert.Equal(t, "x", got.PasskeyName)
	_, err = svc.takeCeremony(id, ceremonySetup)
	assert.Error(t, err, "类型不符")

	// 篡改任一字节都无法解封
	b := []byte(id)
	b[len(b)-2] ^= 1
	_, err = svc.takeCeremony(string(b), ceremonyLogin)
	assert.Error(t, err)
	_, err = svc.takeCeremony("wac_!!", ceremonyLogin)
	assert.Error(t, err)

	// 另一进程（另一把密钥）签发的不被接受
	other := newTestService(t)
	_, err = other.takeCeremony(id, ceremonyLogin)
	assert.Error(t, err)

	// 成功使用后不可重放
	require.NoError(t, svc.markUsed(id, got))
	assert.Error(t, svc.markUsed(id, got))
	_, err = svc.takeCeremony(id, ceremonyLogin)
	assert.Error(t, err)

	// 已用记录随仪式过期被清理
	svc.pruneUsedCeremonies(time.Now().Add(time.Hour))
	assert.Empty(t, svc.used)
}

func TestRevocations(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	var got []string
	cancel := svc.Subscribe(func(kind, id string) { got = append(got, kind+":"+id) })

	dev, _, err := CreateDevice(ctx, svc.st, "d", "cli", "")
	require.NoError(t, err)
	require.NoError(t, svc.RevokeDevice(ctx, dev.ID))
	var e *Error
	require.ErrorAs(t, svc.RevokeDevice(ctx, "dev_missing"), &e)
	assert.Equal(t, 404, e.Status)

	live, _, err := createSession(ctx, svc.st.Q, sessionKindFull, "", SessionTTL)
	require.NoError(t, err)
	require.NoError(t, svc.Logout(ctx, Principal{Kind: KindWeb, ID: ids.Hash(live)}))

	old, _, err := createSession(ctx, svc.st.Q, sessionKindFull, "", -time.Second)
	require.NoError(t, err)
	require.NoError(t, svc.sweep(ctx, time.Now()))

	assert.Equal(t, []string{"device:" + dev.ID, "web:" + ids.Hash(live), "web:" + ids.Hash(old)}, got)
	_, err = svc.st.Q.GetWebSession(ctx, ids.Hash(old))
	assert.Error(t, err, "过期会话已删除")

	cancel()
	require.NoError(t, svc.RevokeDevice(ctx, dev.ID))
	assert.Len(t, got, 3, "取消订阅后不再回调")
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
