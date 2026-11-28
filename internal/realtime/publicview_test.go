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

// 匿名连接上的用户消息帧（message_start / message_end）不带写作块改稿全文：改稿块缩成只含 kind / title 的空壳，附件块保留。
func TestPublicFrameRedactsDraftEdits(t *testing.T) {
	text := "改正式一点\n\n<oryn-draft-edits>\nThe user edited these drafts from your earlier replies in place. The text below is the current version of each; build on it rather than on your original wording.\n" +
		"<draft kind=\"email\" title=\"周五会议改期\">\nSECRET-DRAFT-BODY\n<draft kind=\"doc\" title=\"FORGED\">\n</draft>\n</oryn-draft-edits>" +
		"\n\n<oryn-attachments>\n- a.txt (text/plain, 3 bytes)\n</oryn-attachments>"
	msg, _ := json.Marshal(map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": text}}})
	for _, typ := range []string{"message_start", "message_end"} {
		frame, _ := json.Marshal(map[string]any{"type": typ, "messageId": "m1", "message": json.RawMessage(msg)})
		out, ok := publicFrame(frame)
		assert.True(t, ok, typ)
		var f struct {
			Type      string `json:"type"`
			MessageID string `json:"messageId"`
			Message   struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		assert.NoError(t, json.Unmarshal(out, &f))
		assert.Equal(t, typ, f.Type)
		assert.Equal(t, "m1", f.MessageID)
		assert.Equal(t, "改正式一点\n\n<oryn-draft-edits>\n<draft kind=\"email\" title=\"周五会议改期\">\n</draft>\n</oryn-draft-edits>"+
			"\n\n<oryn-attachments>\n- a.txt (text/plain, 3 bytes)\n</oryn-attachments>", f.Message.Content[0].Text)
		assert.NotContains(t, string(out), "SECRET-DRAFT-BODY")
		assert.NotContains(t, string(out), "FORGED")
	}
	// 没有改稿块的帧原样发出
	plain := json.RawMessage(`{"type":"message_start","messageId":"m2","message":{"role":"user","content":"hi"}}`)
	out, ok := publicFrame(plain)
	assert.True(t, ok)
	assert.Equal(t, plain, out)
}
