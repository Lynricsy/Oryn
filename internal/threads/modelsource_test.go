package threads

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/omprpc"
)

// 就绪时写入 agent 流的第一帧是 oryn_mark_session_start，排队的 prompt 在它之后；explicitModel 随线程行的模型取值。
func TestMarkSessionStartFirstOnReady(t *testing.T) {
	for name, model := range map[string]*string{"无模型": nil, "空串": new(""), "显式模型": new("openai/gpt-6.1-sol")} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, Config{})
			orb := f.orb("ready")
			th := f.thread(orb, StateProvisioning)
			th.mu.Lock()
			th.model = model
			th.queue = append(th.queue, []byte(`{"id":"q1","type":"prompt","message":"first"}`))
			th.mu.Unlock()
			a := fakeAgent(16)
			attach(th, a, apigen.AgentStatusReady)
			th.mu.Lock()
			f.s.onReadyLocked(f.ctx, th)
			th.mu.Unlock()

			all := sent(a)
			rest := afterMark(t, all)
			var mark struct {
				Type          string `json:"type"`
				ID            string `json:"id"`
				ExplicitModel *bool  `json:"explicitModel"`
			}
			require.NoError(t, json.Unmarshal([]byte(all[0]), &mark))
			require.NotNil(t, mark.ExplicitModel, "显式给出 explicitModel")
			assert.Equal(t, parseModel(model) != nil, *mark.ExplicitModel)
			assert.NotEmpty(t, mark.ID, "注入了命令 id")
			require.NotEmpty(t, rest)
			assert.Contains(t, rest[0], `"first"`, "排队的 prompt 紧随其后")
			for _, fr := range rest {
				assert.NotContains(t, fr, "oryn_mark_session_start", "每次就绪只发一次")
			}
		})
	}
}

// 冻结闸门已提交时就绪：不发标记（命令写进流也要等解冻才处理，且之后进程不重启就不再有记录 → 首回合回落）。
func TestMarkSessionStartSkippedWhileFreezing(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("freezing")
	th := f.thread(orb, StateIdle)
	a := fakeAgent(8)
	attach(th, a, apigen.AgentStatusReady)
	th.mu.Lock()
	f.s.onReadyLocked(f.ctx, th)
	th.mu.Unlock()
	assert.Empty(t, sent(a))
}

// 旧 Core 应答 Unknown command：线程状态不变，不进 error，未决命令清除；之后的命令照常下发。
func TestMarkSessionStartUnknownCommandIgnored(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)
	a := fakeAgent(16)
	attach(th, a, apigen.AgentStatusReady)
	th.mu.Lock()
	f.s.onReadyLocked(f.ctx, th)
	before := th.state
	th.mu.Unlock()
	all := sent(a)
	_ = afterMark(t, all)

	env := struct {
		ID string `json:"id"`
	}{}
	require.NoError(t, json.Unmarshal([]byte(all[0]), &env))
	resp := `{"type":"response","id":"` + env.ID + `","command":"oryn_mark_session_start","success":false,"error":"Unknown command: oryn_mark_session_start"}`
	renv, err := omprpc.ParseEnvelope([]byte(resp))
	require.NoError(t, err)
	f.s.onResponse(f.ctx, th, renv, json.RawMessage(resp))
	th.mu.Lock()
	assert.Equal(t, before, th.state, "失败应答不改变线程状态")
	assert.NotEqual(t, StateError, th.state)
	assert.False(t, th.failed)
	_, still := th.pending[env.ID]
	assert.False(t, still, "未决命令已结算")
	th.mu.Unlock()

	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"after"}`), noReply))
	frames := sent(a)
	require.NotEmpty(t, frames)
	assert.Contains(t, frames[0], `"after"`)
}

// 客户端发 oryn_mark_session_start → managed_by_oryn，不写进 agent 流；oryn_select_model 与 set_model 放行。
func TestMarkSessionStartManaged(t *testing.T) {
	route, typ := RouteCommand(json.RawMessage(`{"type":"oryn_mark_session_start","explicitModel":false}`))
	assert.Equal(t, RouteManaged, route)
	assert.Equal(t, "oryn_mark_session_start", typ)
	for _, frame := range []string{`{"type":"oryn_select_model","role":"Mare"}`, `{"type":"set_model","provider":"a","modelId":"b"}`} {
		route, _ := RouteCommand(json.RawMessage(frame))
		assert.Equal(t, RoutePass, route, frame)
	}

	f := newFixture(t, Config{})
	th := f.thread(f.orb("ready"), StateIdle)
	a := fakeAgent(8)
	attach(th, a, apigen.AgentStatusReady)
	err := f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"oryn_mark_session_start"}`), noReply)
	var te *Error
	require.ErrorAs(t, err, &te)
	assert.Equal(t, RouteManaged, te.Code)
	assert.Empty(t, sent(a), "被拒的命令不写进 agent 流")

	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"oryn_select_model","provider":"a","modelId":"b"}`), noReply))
	frames := sent(a)
	require.Len(t, frames, 1)
	assert.Contains(t, frames[0], `"oryn_select_model"`)
}
