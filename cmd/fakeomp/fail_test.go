package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// provider 失败的回合与 omp 同形（oryn-core rpc-prompt-results.ts）：agent_end.messages 末尾的 assistant 消息
// stopReason=error 带 errorMessage，随后 prompt_result{id, status:"error", error.message}。
func TestFailedTurnShape(t *testing.T) {
	for _, tc := range []struct {
		name, model, prompt, want string
	}{
		{"keyword", "", "please fakeomp:error now", "fakeomp: provider request failed"},
		{"model", failModelID, "x", "fakeomp: model does-not-exist not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			h := start(t, []string{"--session-dir", dir, "--delay", "0s"}, nil)
			require.Equal(t, "ready", h.next()["type"])
			r, _ := h.call(map[string]any{"id": "n1", "type": "negotiate_protocol", "protocolVersion": 2})
			require.Equal(t, true, r["success"], r)
			open := map[string]any{"id": "o1", "type": "open_session", "sessionDir": dir}
			if tc.model != "" {
				open["provider"], open["modelId"] = "fake", tc.model
			}
			r, _ = h.call(open)
			require.Equal(t, true, r["success"], r)

			h.send(map[string]any{"id": "p1", "type": "prompt", "message": tc.prompt})
			frames := h.until("session_settled")
			end := findType(frames, "agent_end")
			require.NotNil(t, end)
			msgs := end["messages"].([]any)
			last := msgs[len(msgs)-1].(map[string]any)
			assert.Equal(t, "assistant", last["role"])
			assert.Equal(t, "error", last["stopReason"])
			assert.Equal(t, tc.want, last["errorMessage"])
			pr := findType(frames, "prompt_result")
			require.NotNil(t, pr)
			assert.Equal(t, "p1", pr["id"])
			assert.Equal(t, "error", pr["status"])
			assert.Equal(t, true, pr["agentInvoked"])
			assert.Equal(t, map[string]any{"message": tc.want, "retryable": false}, pr["error"])
			assert.Contains(t, readSessionFiles(t, dir), `"stopReason":"error"`, "失败的 assistant 消息写入会话文件")
		})
	}
}
