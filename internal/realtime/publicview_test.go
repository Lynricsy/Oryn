package realtime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

// share.snapshot 的在途帧与实时帧走同一过滤（F2）：流式中途加入的匿名连接也拿不到隐藏的注入消息。
func TestPublicLiveFiltersHiddenMessages(t *testing.T) {
	in := []json.RawMessage{
		json.RawMessage(`{"type":"message_start","messageId":"m1","message":{"role":"custom","display":false,"content":"HIDDEN"}}`),
		json.RawMessage(`{"type":"message_start","messageId":"m2","message":{"role":"assistant","content":[]}}`),
		json.RawMessage(`{"type":"message_update","messageId":"m2","message":{"role":"assistant"},"assistantMessageEvent":{"type":"text_delta","delta":"hi"}}`),
		json.RawMessage(`{"type":"tool_execution_start","toolCallId":"t1","toolName":"task","args":{}}`),
		json.RawMessage(`{"type":"tool_execution_update","toolCallId":"t1","toolName":"task","partialResult":{"details":"HIDDEN"}}`),
	}
	out := publicLive(in)
	assert.Equal(t, []json.RawMessage{in[1], in[2], in[3]}, out)
}
