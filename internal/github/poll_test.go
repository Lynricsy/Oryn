package github

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (e *testEnv) hookCursor() int64 {
	e.t.Helper()
	app, err := e.st.Q.GetGithubApp(e.ctx)
	require.NoError(e.t, err)
	return app.HookCursor
}

func (e *testEnv) setUnreachable(v bool) {
	e.fake.Lock()
	e.fake.Unreachable = v
	e.fake.Unlock()
}

func (e *testEnv) lastFakeDelivery() int64 {
	e.fake.Lock()
	defer e.fake.Unlock()
	return e.fake.Deliveries[len(e.fake.Deliveries)-1].ID
}

func TestPollOnce(t *testing.T) {
	e := newEnv(t)
	require.NoError(t, e.s.pollOnce(e.ctx)) // 没有 App：什么都不做
	e.installApp()
	hook := e.hookURL()

	// 首次只建立基线：已有的投递不回放
	e.setUnreachable(true)
	_, old := e.fake.Deliver(hook, "ping", map[string]any{"zen": "old"})
	require.NoError(t, e.s.pollOnce(e.ctx))
	e.drain()
	assert.Equal(t, e.lastFakeDelivery(), e.hookCursor())
	_, err := e.st.Q.GetGithubDelivery(e.ctx, old)
	assert.True(t, isNoRows(err))

	// 不可达的两条 → 轮询补齐，来源 poll
	_, g1 := e.fake.Deliver(hook, "ping", map[string]any{"zen": "a"})
	_, g2 := e.fake.Deliver(hook, "ping", map[string]any{"zen": "b"})
	// webhook 已处理过的一条不再取详情
	e.setUnreachable(false)
	status, g3 := e.fake.Deliver(hook, "ping", map[string]any{"zen": "c"})
	require.Equal(t, http.StatusAccepted, status)
	e.drain()
	e.fake.ResetCalls()
	require.NoError(t, e.s.pollOnce(e.ctx))
	e.drain()
	for _, g := range []string{g1, g2} {
		d := e.delivery(g)
		assert.Equal(t, "poll", d.Source)
		assert.Equal(t, "ignored:ping", d.Outcome)
	}
	assert.Equal(t, "webhook", e.delivery(g3).Source)
	assert.Len(t, e.fake.CallsTo("GET", "/app/hook/deliveries/*"), 2)
	assert.Equal(t, e.lastFakeDelivery(), e.hookCursor())
	app, err := e.st.Q.GetGithubApp(e.ctx)
	require.NoError(t, err)
	// 轮询在 webhook 的 g3 之后受理，最近投递来源记为 poll
	assert.Equal(t, "poll", *app.LastDeliverySrc)

	// 再来一轮：没有新投递，不取详情
	e.fake.ResetCalls()
	require.NoError(t, e.s.pollOnce(e.ctx))
	assert.Empty(t, e.fake.CallsTo("GET", "/app/hook/deliveries/*"))
}

func TestPollOnceDetailFailure(t *testing.T) {
	e := newEnv(t)
	e.installApp()
	hook := e.hookURL()
	require.NoError(t, e.s.pollOnce(e.ctx))
	assert.EqualValues(t, 1, e.hookCursor()) // 空日志：基线为 1

	e.setUnreachable(true)
	var guids []string
	var ids []int64
	for i := range 3 {
		_, g := e.fake.Deliver(hook, "ping", map[string]any{"n": i})
		guids = append(guids, g)
		ids = append(ids, e.lastFakeDelivery())
	}
	e.fake.Lock()
	e.fake.Fail = map[string]int{fmt.Sprintf("GET /app/hook/deliveries/%d", ids[1]): 500}
	e.fake.Unlock()
	require.NoError(t, e.s.pollOnce(e.ctx))
	e.drain()
	assert.Equal(t, "ignored:ping", e.delivery(guids[0]).Outcome)
	for _, g := range guids[1:] {
		_, err := e.st.Q.GetGithubDelivery(e.ctx, g)
		assert.True(t, isNoRows(err))
	}
	assert.Equal(t, ids[1]-1, e.hookCursor())

	e.fake.Lock()
	e.fake.Fail = nil
	e.fake.Unlock()
	e.fake.ResetCalls()
	require.NoError(t, e.s.pollOnce(e.ctx))
	e.drain()
	for _, g := range guids {
		assert.Equal(t, "ignored:ping", e.delivery(g).Outcome)
	}
	assert.Len(t, e.fake.CallsTo("GET", "/app/hook/deliveries/*"), 2)
	assert.Equal(t, ids[2], e.hookCursor())

	// 列表接口故障：游标不动
	e.fake.Lock()
	e.fake.Fail = map[string]int{"GET /app/hook/deliveries": 502}
	e.fake.Unlock()
	_, _ = e.fake.Deliver(hook, "ping", map[string]any{})
	require.Error(t, e.s.pollOnce(e.ctx))
	assert.Equal(t, ids[2], e.hookCursor())
}

func TestPollOncePaged(t *testing.T) {
	e := newEnv(t)
	e.installApp()
	hook := e.hookURL()
	e.setUnreachable(true)
	_, _ = e.fake.Deliver(hook, "ping", map[string]any{})
	require.NoError(t, e.s.pollOnce(e.ctx))
	base := e.hookCursor()
	var last string
	for range 130 {
		_, last = e.fake.Deliver(hook, "ping", map[string]any{})
	}
	e.fake.ResetCalls()
	require.NoError(t, e.s.pollOnce(e.ctx))
	e.drain()
	assert.Len(t, e.fake.CallsTo("GET", "/app/hook/deliveries"), 2)
	assert.Len(t, e.fake.CallsTo("GET", "/app/hook/deliveries/*"), 130)
	assert.Equal(t, "ignored:ping", e.delivery(last).Outcome)
	assert.Greater(t, e.hookCursor(), base)
}
