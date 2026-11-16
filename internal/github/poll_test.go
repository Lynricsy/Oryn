package github

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (e *testEnv) setUnreachable(v bool) {
	e.fake.Lock()
	e.fake.Unreachable = v
	e.fake.Unlock()
}

func (e *testEnv) deliveriesUsed() int {
	e.fake.Lock()
	defer e.fake.Unlock()
	return e.fake.DeliveriesUsed
}

// pollEnv 建好 Acme/Widgets 上的 active hook，返回 hook 的投递日志列表路径。
func (e *testEnv) pollEnv() string {
	e.t.Helper()
	e.hookEnv()
	e.bindWidgets()
	return fmt.Sprintf("/repos/Acme/Widgets/hooks/%d/deliveries", *e.hookRow(42).HookID)
}

func TestPollHookBaselineAndBackfill(t *testing.T) {
	e := newEnv(t)
	e.pollEnv()

	// 首次只建立基线：已有的投递不回放
	e.setUnreachable(true)
	_, old := e.fake.DeliverHook("Acme/Widgets", "issues", map[string]any{"action": "old"})
	e.s.pollHooks(e.ctx)
	e.drain()
	row := e.hookRow(42)
	assert.Equal(t, e.fake.LastRepoDelivery(), row.DeliveryCursor)
	require.NotNil(t, row.DeliveriesEtag)
	_, err := e.st.Q.GetGithubDelivery(e.ctx, old)
	assert.True(t, isNoRows(err))

	// 不可达的两条 → 轮询补齐，来源 poll；webhook 已处理过的一条不取详情
	_, g1 := e.fake.DeliverHook("Acme/Widgets", "issues", map[string]any{"action": "a"})
	_, g2 := e.fake.DeliverHook("Acme/Widgets", "ping", map[string]any{"zen": "b"})
	e.setUnreachable(false)
	status, g3 := e.fake.DeliverHook("Acme/Widgets", "issues", map[string]any{"action": "c"})
	require.Equal(t, http.StatusAccepted, status)
	e.drain()
	e.fake.ResetCalls()
	e.clock.Add(hookPollRelaxed) // 刚收到过 webhook：放宽到 5 分钟
	e.s.pollHooks(e.ctx)
	e.drain()
	assert.Equal(t, "poll", e.delivery(g1).Source)
	assert.Equal(t, "issues", e.delivery(g1).Event)
	assert.Equal(t, "poll", e.delivery(g2).Source)
	assert.Equal(t, "ping", e.delivery(g2).Event)
	assert.Equal(t, "ignored:ping", e.delivery(g2).Outcome)
	assert.JSONEq(t, `{"zen":"b"}`, string(e.delivery(g2).Payload))
	assert.Equal(t, "webhook", e.delivery(g3).Source)
	assert.Len(t, e.fake.CallsTo("GET", "/repos/Acme/Widgets/hooks/*"), 3, "列表 1 次 + 两条详情")
	row = e.hookRow(42)
	assert.Equal(t, e.fake.LastRepoDelivery(), row.DeliveryCursor)
	require.NotNil(t, row.LastDeliverySrc)
	assert.Equal(t, "poll", *row.LastDeliverySrc)
}

func TestPollHookNotModified(t *testing.T) {
	e := newEnv(t)
	path := e.pollEnv()
	e.s.pollHooks(e.ctx) // 基线
	etag := *e.hookRow(42).DeliveriesEtag

	used := e.deliveriesUsed()
	e.fake.ResetCalls()
	e.clock.Add(hookPollInterval)
	e.s.pollHooks(e.ctx)
	calls := e.fake.CallsTo("GET", path)
	require.Len(t, calls, 1)
	assert.Equal(t, used, e.deliveriesUsed(), "304 不计额度")
	assert.Equal(t, etag, *e.hookRow(42).DeliveriesEtag)

	// 有新投递后 ETag 变化
	e.setUnreachable(true)
	_, g := e.fake.DeliverHook("Acme/Widgets", "issues", map[string]any{"action": "x"})
	e.s.pollHooks(e.ctx)
	e.drain()
	assert.Equal(t, "poll", e.delivery(g).Source)
	assert.NotEqual(t, etag, *e.hookRow(42).DeliveriesEtag)
}

func TestPollHookRelaxedAfterWebhook(t *testing.T) {
	e := newEnv(t)
	path := e.pollEnv()
	e.s.pollHooks(e.ctx)
	status, _ := e.fake.DeliverHook("Acme/Widgets", "ping", map[string]any{})
	require.Equal(t, http.StatusAccepted, status)
	e.drain()

	e.fake.ResetCalls()
	e.clock.Add(time.Minute)
	e.s.pollHooks(e.ctx)
	assert.Empty(t, e.fake.CallsTo("GET", path), "10 分钟内收到过 webhook：5 分钟内不再轮询")
	e.clock.Add(hookPollRelaxed)
	e.s.pollHooks(e.ctx)
	assert.Len(t, e.fake.CallsTo("GET", path), 1)
	e.clock.Add(hookRecentWebhook)
	e.fake.ResetCalls()
	e.s.pollHooks(e.ctx)
	assert.Len(t, e.fake.CallsTo("GET", path), 1, "webhook 久未到达：恢复 30 秒周期")
}

func TestPollHookDuplicateProcessedOnce(t *testing.T) {
	e := newEnv(t)
	e.pollEnv()
	var calls atomic.Int32
	e.s.handlers["test_event"] = func(context.Context, string, []byte) (string, error) {
		calls.Add(1)
		return "handled", nil
	}
	e.s.pollHooks(e.ctx)
	e.fake.Lock()
	for _, h := range e.fake.RepoHooks {
		h.Events = append(h.Events, "test_event")
	}
	e.fake.Unlock()

	// webhook 先到，轮询看到同一 guid 不再取详情
	status, g1 := e.fake.DeliverHook("Acme/Widgets", "test_event", map[string]any{"action": "x"})
	require.Equal(t, http.StatusAccepted, status)
	e.drain()
	e.fake.ResetCalls()
	e.clock.Add(hookPollRelaxed)
	e.s.pollHooks(e.ctx)
	e.drain()
	assert.Empty(t, e.fake.CallsTo("GET", "/repos/Acme/Widgets/hooks/*/deliveries/*"))
	assert.EqualValues(t, 1, calls.Load())

	// 轮询先补到，随后 GitHub 重投同一 guid 到 webhook：只处理一次
	e.setUnreachable(true)
	_, g2 := e.fake.DeliverHook("Acme/Widgets", "test_event", map[string]any{"action": "y"})
	e.clock.Add(hookPollRelaxed)
	e.s.pollHooks(e.ctx)
	e.drain()
	e.setUnreachable(false)
	status, _ = e.fake.DeliverHookGUID("Acme/Widgets", "test_event", map[string]any{"action": "y"}, g2)
	require.Equal(t, http.StatusAccepted, status)
	e.drain()
	assert.EqualValues(t, 2, calls.Load())
	assert.Equal(t, "webhook", e.delivery(g1).Source)
	assert.Equal(t, "poll", e.delivery(g2).Source)
}

func TestPollHookDetailFailure(t *testing.T) {
	e := newEnv(t)
	path := e.pollEnv()
	e.s.pollHooks(e.ctx)
	base := e.hookRow(42).DeliveryCursor
	assert.EqualValues(t, 1, base) // 空日志：基线为 1

	e.setUnreachable(true)
	var guids []string
	var ids []int64
	for i := range 3 {
		_, g := e.fake.DeliverHook("Acme/Widgets", "ping", map[string]any{"n": i})
		guids = append(guids, g)
		ids = append(ids, e.fake.LastRepoDelivery())
	}
	e.fake.Lock()
	e.fake.Fail = map[string]int{fmt.Sprintf("GET %s/%d", path, ids[1]): 500}
	e.fake.Unlock()
	row := e.hookRow(42)
	require.Error(t, e.s.pollHook(e.ctx, row))
	e.drain()
	assert.Equal(t, "ignored:ping", e.delivery(guids[0]).Outcome)
	for _, g := range guids[1:] {
		_, err := e.st.Q.GetGithubDelivery(e.ctx, g)
		assert.True(t, isNoRows(err))
	}
	row = e.hookRow(42)
	assert.Equal(t, ids[0], row.DeliveryCursor)
	assert.Nil(t, row.DeliveriesEtag, "部分失败：清掉 ETag，下轮重取列表")

	e.fake.Lock()
	e.fake.Fail = nil
	e.fake.Unlock()
	e.fake.ResetCalls()
	require.NoError(t, e.s.pollHook(e.ctx, e.hookRow(42)))
	e.drain()
	for _, g := range guids {
		assert.Equal(t, "ignored:ping", e.delivery(g).Outcome)
	}
	assert.Len(t, e.fake.CallsTo("GET", path+"/*"), 2)
	assert.Equal(t, ids[2], e.hookRow(42).DeliveryCursor)

	// 列表接口故障：游标不动
	e.fake.Lock()
	e.fake.Fail = map[string]int{"GET " + path: 502}
	e.fake.Unlock()
	_, _ = e.fake.DeliverHook("Acme/Widgets", "ping", map[string]any{})
	require.Error(t, e.s.pollHook(e.ctx, e.hookRow(42)))
	assert.Equal(t, ids[2], e.hookRow(42).DeliveryCursor)
}

func TestPollHookPaged(t *testing.T) {
	e := newEnv(t)
	path := e.pollEnv()
	e.setUnreachable(true)
	_, _ = e.fake.DeliverHook("Acme/Widgets", "ping", map[string]any{})
	e.s.pollHooks(e.ctx)
	base := e.hookRow(42).DeliveryCursor
	var last string
	for range 130 {
		_, last = e.fake.DeliverHook("Acme/Widgets", "ping", map[string]any{})
	}
	e.fake.ResetCalls()
	require.NoError(t, e.s.pollHook(e.ctx, e.hookRow(42)))
	e.drain()
	assert.Len(t, e.fake.CallsTo("GET", path), 2)
	assert.Len(t, e.fake.CallsTo("GET", path+"/*"), 130)
	assert.Equal(t, "ignored:ping", e.delivery(last).Outcome)
	assert.Greater(t, e.hookRow(42).DeliveryCursor, base)
}

func TestPollHookRateLimitPause(t *testing.T) {
	e := newEnv(t)
	path := e.pollEnv()
	e.s.pollHooks(e.ctx)
	e.setUnreachable(true)
	_, g := e.fake.DeliverHook("Acme/Widgets", "ping", map[string]any{})
	e.fake.Lock()
	e.fake.DeliveriesUsed = 460 // 列表这次计数后剩 39 < 50
	e.fake.Unlock()
	e.fake.ResetCalls()
	e.clock.Add(hookPollInterval)
	e.s.pollHooks(e.ctx)
	e.drain()
	assert.Len(t, e.fake.CallsTo("GET", path), 1)
	assert.Empty(t, e.fake.CallsTo("GET", path+"/*"), "额度见底：不再取详情")
	_, err := e.st.Q.GetGithubDelivery(e.ctx, g)
	assert.True(t, isNoRows(err))

	// 暂停到 X-RateLimit-Reset
	e.fake.ResetCalls()
	e.clock.Add(hookPollInterval)
	e.s.pollHooks(e.ctx)
	assert.Empty(t, e.fake.CallsTo("GET", "/repos/*"))
	e.clock.Add(time.Hour)
	e.fake.Lock()
	e.fake.DeliveriesUsed = 0
	e.fake.Unlock()
	e.s.pollHooks(e.ctx)
	e.drain()
	assert.Equal(t, "poll", e.delivery(g).Source)
}

func TestPollHooksNoIdentity(t *testing.T) {
	e := newEnv(t)
	path := e.pollEnv()
	tok, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)
	require.NoError(t, e.s.tokenRejected(e.ctx, tok, slotUser))
	e.fake.ResetCalls()
	e.s.pollHooks(e.ctx)
	assert.Empty(t, e.fake.CallsTo("GET", path))
}
