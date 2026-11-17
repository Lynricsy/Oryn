package threads

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// M19 4.3：线程不能挂到助手的电脑上 → orb_is_computer。
func TestCreateOnComputerOrbRejected(t *testing.T) {
	f := newFixture(t, Config{})
	id := f.orb("ready")
	_, err := f.st.DB.Exec(`UPDATE orbs SET computer = 1 WHERE id = ?`, id)
	require.NoError(t, err)
	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "hi", OrbID: id})
	var te *Error
	require.ErrorAs(t, err, &te)
	assert.Equal(t, CodeOrbIsComputer, te.Code)
}

// updatingCoord 在调用里先交出 HostToolCall（测试借它的 Update 发部分结果），等 release 后返回 res。
type updatingCoord struct {
	*fakeCoord
	release chan struct{}
}

func (c *updatingCoord) HostToolCall(_ context.Context, call HostToolCall) HostToolResult {
	c.calls <- call
	<-c.release
	return c.res
}

// M19 4.3：在途调用的 Update 发 host_tool_update{id, partialResult}；结果写回之后的 Update 丢弃。Raw 结果（含 image 块）
// 原样作为 result，其 isError 决定帧的 isError。
func TestHostToolUpdateAndRawResult(t *testing.T) {
	f := newFixture(t, Config{})
	coord := &updatingCoord{fakeCoord: newFakeCoord(), release: make(chan struct{})}
	raw := `{"content":[{"type":"text","text":"Linux"},{"type":"image","data":"iVBORw0KGgo=","mimeType":"image/png"}],"details":{"exitCode":0}}`
	coord.res = HostToolResult{Raw: json.RawMessage(raw), Text: "ignored"}
	f.s.SetCoordinator(coord)
	th := f.coordThread(f.orb("ready"), KindAssistant, StateIdle)
	a := fakeAgent(16)
	attach(th, a, apigen.AgentStatusReady)

	f.s.onFrame(f.ctx, th, th.orbID, []byte(`{"type":"host_tool_call","id":"h-1","toolCallId":"tc-1","toolName":"computer_bash","arguments":{"command":"uname"}}`))
	call := <-coord.calls
	require.NotNil(t, call.Update)
	call.Update(json.RawMessage(`{"content":[{"type":"text","text":"Lin"}]}`))
	up := recvJSON(t, a)
	assert.Equal(t, "host_tool_update", up["type"])
	assert.Equal(t, "h-1", up["id"])
	assert.Equal(t, map[string]any{"content": []any{map[string]any{"type": "text", "text": "Lin"}}}, up["partialResult"])

	close(coord.release)
	res := recvJSON(t, a)
	assert.Equal(t, "host_tool_result", res["type"])
	assert.Equal(t, false, res["isError"])
	b, err := json.Marshal(res["result"])
	require.NoError(t, err)
	assert.JSONEq(t, raw, string(b), "Raw 原样透传")

	call.Update(json.RawMessage(`{"content":[{"type":"text","text":"late"}]}`))
	assert.Empty(t, sent(a), "调用结束后的部分结果丢弃")

	frame, err := hostToolResultFrame("h-2", HostToolResult{Raw: json.RawMessage(`{"content":[{"type":"text","text":"boom"}],"isError":true}`)})
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(frame, &m))
	assert.Equal(t, true, m["isError"])
}
