package threads

import (
	"strings"
	"testing"

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
