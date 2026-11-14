package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// expireAppCode 把一次性码的过期时间改到过去（测试不 sleep）。
func expireAppCode(t *testing.T, svc *Service, code string) {
	t.Helper()
	svc.appCodes.mu.Lock()
	defer svc.appCodes.mu.Unlock()
	v, ok := svc.appCodes.m[code]
	require.True(t, ok)
	v.exp = time.Now().Add(-time.Second)
	svc.appCodes.m[code] = v
}

func TestIssueAppCode(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	cli, _, err := CreateDevice(ctx, svc.st, "c", "cli", "linux")
	require.NoError(t, err)
	_, _, err = svc.IssueAppCode(ctx, Principal{Kind: KindDevice, ID: cli.ID})
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, 403, e.Status)
	assert.Equal(t, "app_session_unsupported", e.Code)

	// 网页会话（含 App 会话）不能再换
	_, _, err = svc.IssueAppCode(ctx, Principal{Kind: KindWeb, ID: "h"})
	require.ErrorAs(t, err, &e)
	assert.Equal(t, "app_session_unsupported", e.Code)

	for _, kind := range []string{"desktop", "mobile"} {
		dev, _, err := CreateDevice(ctx, svc.st, kind, kind, "")
		require.NoError(t, err)
		code, exp, err := svc.IssueAppCode(ctx, Principal{Kind: KindDevice, ID: dev.ID})
		require.NoError(t, err)
		assert.Equal(t, "oac", ids.TokenPrefix(code))
		assert.Equal(t, 30, exp)
	}
}

func TestRedeemAppCode(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	var events []string
	svc.Subscribe(func(kind, id string) { events = append(events, kind+":"+id) })

	dev, _, err := CreateDevice(ctx, svc.st, "d", "desktop", "linux")
	require.NoError(t, err)
	who := Principal{Kind: KindDevice, ID: dev.ID}

	code, _, err := svc.IssueAppCode(ctx, who)
	require.NoError(t, err)
	cookie, exp, ok, err := svc.RedeemAppCode(ctx, code, "OrynDesktop/0.1.0 (linux-x86_64)")
	require.NoError(t, err)
	require.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(SessionTTL), exp, time.Minute)

	// 码只能兑换一次
	_, _, ok, err = svc.RedeemAppCode(ctx, code, "")
	require.NoError(t, err)
	assert.False(t, ok)
	_, _, ok, err = svc.RedeemAppCode(ctx, "oac_unknown", "")
	require.NoError(t, err)
	assert.False(t, ok)
	_, _, ok, err = svc.RedeemAppCode(ctx, "", "")
	require.NoError(t, err)
	assert.False(t, ok)

	ws, err := svc.st.Q.GetWebSession(ctx, ids.Hash(cookie))
	require.NoError(t, err)
	require.NotNil(t, ws.DeviceID)
	assert.Equal(t, dev.ID, *ws.DeviceID)
	assert.Equal(t, sessionKindFull, ws.Kind)
	assert.Equal(t, "OrynDesktop/0.1.0 (linux-x86_64)", ws.UserAgent)
	d, err := svc.st.Q.GetDevice(ctx, dev.ID)
	require.NoError(t, err)
	assert.NotNil(t, d.LastSeenAt, "兑换时更新设备最近活跃")
	assert.Empty(t, events)

	// 过期的码不能兑换
	expired, _, err := svc.IssueAppCode(ctx, who)
	require.NoError(t, err)
	expireAppCode(t, svc, expired)
	_, _, ok, err = svc.RedeemAppCode(ctx, expired, "")
	require.NoError(t, err)
	assert.False(t, ok)

	// 再兑换一次新码：旧会话被删并发布 (web, 旧哈希)；每台设备只有一条 App 会话
	code2, _, err := svc.IssueAppCode(ctx, who)
	require.NoError(t, err)
	cookie2, _, ok, err := svc.RedeemAppCode(ctx, code2, "")
	require.NoError(t, err)
	require.True(t, ok)
	assert.NotEqual(t, cookie, cookie2)
	_, err = svc.st.Q.GetWebSession(ctx, ids.Hash(cookie))
	assert.Error(t, err, "旧会话已删除")
	_, err = svc.st.Q.GetWebSession(ctx, ids.Hash(cookie2))
	require.NoError(t, err)
	assert.Equal(t, []string{"web:" + ids.Hash(cookie)}, events)
}

// 签发后吊销设备：未兑换的码作废；之后签发也不行。
func TestRedeemAppCodeAfterRevoke(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	dev, _, err := CreateDevice(ctx, svc.st, "d", "mobile", "android")
	require.NoError(t, err)
	who := Principal{Kind: KindDevice, ID: dev.ID}
	code, _, err := svc.IssueAppCode(ctx, who)
	require.NoError(t, err)

	require.NoError(t, svc.RevokeDevice(ctx, dev.ID))
	_, _, ok, err := svc.RedeemAppCode(ctx, code, "")
	require.NoError(t, err)
	assert.False(t, ok)

	// 即便码表未被清理（例如吊销与签发竞争），兑换也在事务内复核设备状态
	svc.appCodes.mu.Lock()
	svc.appCodes.m["oac_raced"] = ticket{p: who, exp: time.Now().Add(TicketTTL)}
	svc.appCodes.mu.Unlock()
	_, _, ok, err = svc.RedeemAppCode(ctx, "oac_raced", "")
	require.NoError(t, err)
	assert.False(t, ok)

	_, _, err = svc.IssueAppCode(ctx, who)
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, 401, e.Status)
}

// RevokeDevice 删除该设备换来的全部网页会话，先发布 (device, id) 再逐个发布 (web, 哈希)；
// 其它设备与普通网页会话不受影响。
func TestRevokeDeviceDeletesAppSessions(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	dev, _, err := CreateDevice(ctx, svc.st, "d", "desktop", "")
	require.NoError(t, err)
	other, _, err := CreateDevice(ctx, svc.st, "o", "desktop", "")
	require.NoError(t, err)

	code, _, err := svc.IssueAppCode(ctx, Principal{Kind: KindDevice, ID: dev.ID})
	require.NoError(t, err)
	cookie, _, ok, err := svc.RedeemAppCode(ctx, code, "")
	require.NoError(t, err)
	require.True(t, ok)
	code, _, err = svc.IssueAppCode(ctx, Principal{Kind: KindDevice, ID: other.ID})
	require.NoError(t, err)
	otherCookie, _, ok, err := svc.RedeemAppCode(ctx, code, "")
	require.NoError(t, err)
	require.True(t, ok)
	web, err := CreateWebSession(ctx, svc.st, "")
	require.NoError(t, err)

	var events []string
	svc.Subscribe(func(kind, id string) { events = append(events, kind+":"+id) })
	require.NoError(t, svc.RevokeDevice(ctx, dev.ID))
	assert.Equal(t, []string{"device:" + dev.ID, "web:" + ids.Hash(cookie)}, events)

	_, err = svc.st.Q.GetWebSession(ctx, ids.Hash(cookie))
	assert.Error(t, err)
	valid, err := svc.PrincipalValid(ctx, Principal{Kind: KindWeb, ID: ids.Hash(cookie), DeviceID: dev.ID})
	require.NoError(t, err)
	assert.False(t, valid)
	_, err = svc.st.Q.GetWebSession(ctx, ids.Hash(otherCookie))
	require.NoError(t, err)
	_, err = svc.st.Q.GetWebSession(ctx, ids.Hash(web))
	require.NoError(t, err)

	// 幂等：再吊销一次不再有会话可删
	events = nil
	require.NoError(t, svc.RevokeDevice(ctx, dev.ID))
	assert.Equal(t, []string{"device:" + dev.ID}, events)
}

// appSessionReq 构造带 App 会话 Cookie 的只读请求。
func appSessionReq(cookie string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Cookie", CookieName+"="+cookie)
	return req
}

// 审查 #1：删除 App 会话失败（库内触发器注入，或请求 ctx 已取消）时，吊销整体回滚：
// 设备不得处于「已吊销但 App 会话仍可用」的状态，也不发布任何吊销事件；故障消除后再吊销则一并失效。
func TestRevokeDeviceAtomicOnSessionDeleteFailure(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	dev, devTok, err := CreateDevice(ctx, svc.st, "d", "desktop", "linux")
	require.NoError(t, err)
	who := Principal{Kind: KindDevice, ID: dev.ID}
	code, _, err := svc.IssueAppCode(ctx, who)
	require.NoError(t, err)
	cookie, _, ok, err := svc.RedeemAppCode(ctx, code, "")
	require.NoError(t, err)
	require.True(t, ok)
	pending, _, err := svc.IssueAppCode(ctx, who)
	require.NoError(t, err)

	var events []string
	svc.Subscribe(func(kind, id string) { events = append(events, kind+":"+id) })

	// 断言当前状态一致：设备未吊销，令牌与 App 会话都仍有效
	assertIntact := func() {
		t.Helper()
		got, err := svc.st.Q.GetDevice(ctx, dev.ID)
		require.NoError(t, err)
		assert.Nil(t, got.RevokedAt, "失败的吊销必须回滚设备标记")
		_, ok := DeviceByToken(ctx, svc.st, devTok)
		assert.True(t, ok)
		p, err := Authenticate(httptest.NewRecorder(), appSessionReq(cookie), svc.st)
		require.NoError(t, err)
		assert.Equal(t, dev.ID, p.DeviceID)
		assert.Empty(t, events, "失败时不发布吊销事件")
	}

	// 1. 删除会话这一步失败：设备标记已在同一事务里执行，必须一并回滚
	_, err = svc.st.DB.ExecContext(ctx, `CREATE TRIGGER inject_app_session_delete_failure
BEFORE DELETE ON web_sessions WHEN OLD.device_id IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'injected delete failure'); END`)
	require.NoError(t, err)
	err = svc.RevokeDevice(ctx, dev.ID)
	require.ErrorContains(t, err, "injected delete failure")
	assertIntact()

	// 2. 请求 ctx 已取消：同样不留下任何持久变化
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	require.Error(t, svc.RevokeDevice(cctx, dev.ID))
	assertIntact()

	// 3. 故障消除后吊销成功：设备、令牌、App 会话与未兑换的码一并失效，事件按序发布
	_, err = svc.st.DB.ExecContext(ctx, `DROP TRIGGER inject_app_session_delete_failure`)
	require.NoError(t, err)
	require.NoError(t, svc.RevokeDevice(ctx, dev.ID))
	assert.Equal(t, []string{"device:" + dev.ID, "web:" + ids.Hash(cookie)}, events)
	_, ok = DeviceByToken(ctx, svc.st, devTok)
	assert.False(t, ok)
	_, err = Authenticate(httptest.NewRecorder(), appSessionReq(cookie), svc.st)
	assert.ErrorIs(t, err, errUnauthorized)
	_, _, ok, err = svc.RedeemAppCode(ctx, pending, "")
	require.NoError(t, err)
	assert.False(t, ok)
}

// 纵深防御：即便库里残留了已吊销设备的 App 会话行（绕过 RevokeDevice 直接标记吊销模拟），
// Cookie 鉴权与长连接复核都不再接受它；普通网页会话不受影响。
func TestAppSessionRejectedWhenDeviceRevoked(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	dev, _, err := CreateDevice(ctx, svc.st, "d", "mobile", "android")
	require.NoError(t, err)
	code, _, err := svc.IssueAppCode(ctx, Principal{Kind: KindDevice, ID: dev.ID})
	require.NoError(t, err)
	cookie, _, ok, err := svc.RedeemAppCode(ctx, code, "")
	require.NoError(t, err)
	require.True(t, ok)
	web, err := CreateWebSession(ctx, svc.st, "")
	require.NoError(t, err)

	now := time.Now().UnixMilli()
	_, err = svc.st.Q.RevokeDevice(ctx, db.RevokeDeviceParams{RevokedAt: &now, ID: dev.ID})
	require.NoError(t, err)
	var rows int
	require.NoError(t, svc.st.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM web_sessions WHERE id_hash = ?`, ids.Hash(cookie)).Scan(&rows))
	require.Equal(t, 1, rows, "会话行仍在，验证的是鉴权侧的设备复核")

	_, err = Authenticate(httptest.NewRecorder(), appSessionReq(cookie), svc.st)
	assert.ErrorIs(t, err, errUnauthorized)
	valid, err := svc.PrincipalValid(ctx, Principal{Kind: KindWeb, ID: ids.Hash(cookie), DeviceID: dev.ID})
	require.NoError(t, err)
	assert.False(t, valid)

	p, err := Authenticate(httptest.NewRecorder(), appSessionReq(web), svc.st)
	require.NoError(t, err)
	assert.Equal(t, ids.Hash(web), p.ID)
}
