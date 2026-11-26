package threads

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agent_end 之后控制面刷新 get_state，omp 可能在「已发 agent_end、尚未清 isStreaming」的窗口里作答：
// 这份过时的 {isStreaming:true,isSettled:false} 不得把线程拉回 working 或让它不再静止——之后不会再有帧来纠正，
// 线程会永远停在 working、orb 也永远不能冻结。应答在 session_settled 之前或之后到达两种顺序都要覆盖。
func TestStaleStreamingStateAfterAgentEndDoesNotWedgeWorking(t *testing.T) {
	for _, replyAfterSettled := range []bool{false, true} {
		name := "reply-before-settled"
		if replyAfterSettled {
			name = "reply-after-settled"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, Config{})
			th, a, _ := readyThread(f)

			f.frame(th, `{"type":"agent_start"}`)
			f.frame(th, `{"type":"agent_end","messages":[],"isTerminal":true,"yielded":true}`)
			var getState string
			for _, fr := range sent(a) {
				if strings.Contains(fr, `"get_state"`) {
					getState = fr
				}
			}
			require.NotEmpty(t, getState, "agent_end 后刷新 state")
			stale := func() {
				f.respond(th, getState, true, `{"isStreaming":true,"isSettled":false,"hasPendingAsyncWork":false}`)
			}
			if !replyAfterSettled {
				stale()
			}
			f.frame(th, `{"type":"prompt_result","agentInvoked":true,"status":"completed","sessionSettled":true}`)
			f.frame(th, `{"type":"session_settled"}`)
			if replyAfterSettled {
				stale()
			}

			th.mu.Lock()
			defer th.mu.Unlock()
			assert.Equal(t, StateIdle, f.s.status(th))
			assert.True(t, th.quiescentLocked(), "session_settled 之后静止")
		})
	}
}

// 首 token 到达前重新订阅（切换对话 / 重载）：agent_start 只置了 Live.working，缓存的 get_state 仍是上一轮结束时的
// 空闲态（isStreaming=false, isSettled=true）。Subscribe 快照必须透传 Live.working，同时在仅有后台异步任务
// （pendingAsync=true、Live.working=false）时不把 working 标成真。
func TestSubscribeSnapshotCarriesWorkingWithoutPendingAsyncConflation(t *testing.T) {
	f := newFixture(t, Config{})
	th, a, _ := readyThread(f)

	// 先跑完一轮并缓存空闲态 get_state（还原生产中「上一轮结束后留有空闲 state 缓存」的前提）
	f.frame(th, `{"type":"agent_start"}`)
	f.frame(th, `{"type":"agent_end","messages":[],"isTerminal":true,"yielded":true}`)
	var idleGetState string
	for _, fr := range sent(a) {
		if strings.Contains(fr, `"get_state"`) {
			idleGetState = fr
		}
	}
	require.NotEmpty(t, idleGetState, "上一轮 agent_end 后应刷新 get_state")
	f.respond(th, idleGetState, true, `{"isStreaming":false,"isSettled":true,"hasPendingAsyncWork":false}`)
	f.frame(th, `{"type":"session_settled"}`)
	// 新回合开始、尚无 message_start（等待慢 TTFT 期间）
	f.frame(th, `{"type":"agent_start"}`)
	var midTurn Snapshot
	require.NoError(t, f.s.Subscribe(f.ctx, th.id, func(s Snapshot) { midTurn = s }))
	assert.Equal(t, apigen.ThreadStateWorking, midTurn.Thread.State)
	assert.True(t, midTurn.Agent.Working, "回合进行中（即使 get_state 仍是旧空闲缓存）快照应透传 working=true")
	assert.Empty(t, midTurn.Agent.Live)

	// 回合结束但仍有后台异步任务（hasPendingAsyncWork=true）：摘要状态仍是 working，但 Live.working 必须为 false
	f.frame(th, `{"type":"agent_end","messages":[],"isTerminal":true,"yielded":true}`)
	var afterEndGetState string
	for _, fr := range sent(a) {
		if strings.Contains(fr, `"get_state"`) {
			afterEndGetState = fr
		}
	}
	require.NotEmpty(t, afterEndGetState, "agent_end 后应刷新 get_state")
	f.respond(th, afterEndGetState, true, `{"isStreaming":false,"isSettled":false,"hasPendingAsyncWork":true}`)

	var asyncOnly Snapshot
	require.NoError(t, f.s.Subscribe(f.ctx, th.id, func(s Snapshot) { asyncOnly = s }))
	assert.Equal(t, apigen.ThreadStateWorking, asyncOnly.Thread.State)
	assert.False(t, asyncOnly.Agent.Working, "仅有后台异步任务未静止时，agent.working 必须为 false")
	rawAgent, err := json.Marshal(asyncOnly.Agent)
	require.NoError(t, err)
	assert.Contains(t, string(rawAgent), `"working":false`, "快照必须显式发送 working:false，便于前端直接覆盖滞后的 state")
}
