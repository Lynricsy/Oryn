package threads

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTakeAttachments(t *testing.T) {
	ids, rest, err := takeAttachments("prompt", json.RawMessage(`{"type":"prompt","message":"hi","orynAttachments":["att_A","att_B"]}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"att_A", "att_B"}, ids)
	assert.JSONEq(t, `{"type":"prompt","message":"hi"}`, string(rest), "omp 看不到 orynAttachments")

	// 空数组：照样删除字段，走普通路径
	ids, rest, err = takeAttachments("steer", json.RawMessage(`{"type":"steer","message":"x","orynAttachments":[]}`))
	require.NoError(t, err)
	assert.Empty(t, ids)
	assert.JSONEq(t, `{"type":"steer","message":"x"}`, string(rest))

	frame := json.RawMessage(`{"type":"prompt","message":"x"}`)
	ids, rest, err = takeAttachments("prompt", frame)
	require.NoError(t, err)
	assert.Nil(t, ids)
	assert.Equal(t, string(frame), string(rest))

	for _, bad := range []string{
		`{"type":"get_state","orynAttachments":["att_A"]}`,
		`{"type":"prompt","message":"x","orynAttachments":"att_A"}`,
		`{"type":"prompt","message":"x","orynAttachments":["att_A","att_A"]}`,
	} {
		var typ struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(bad), &typ)
		_, _, err := takeAttachments(typ.Type, json.RawMessage(bad))
		assert.Error(t, err, bad)
	}
}

func TestAppendAttachmentBlock(t *testing.T) {
	block := attachmentBlock([]DeliveredAttachment{{ID: "att_A", Name: "a b.pdf", Mime: "application/pdf", Size: 12, Path: "/home/user/.oryn/attachments/T-1/att_A/a b.pdf"}})
	out, err := appendMessage([]byte(`{"type":"prompt","message":"look","images":[]}`), block)
	require.NoError(t, err)
	var f struct {
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(out, &f))
	assert.Equal(t, "look\n\n<oryn-attachments>\n"+
		"The user attached these files. They are stored outside the repository; read them when relevant and do not copy them into the repository unless asked.\n"+
		"- /home/user/.oryn/attachments/T-1/att_A/a b.pdf (application/pdf, 12 bytes)\n"+
		"</oryn-attachments>", f.Message)

	// 只有附件、没有文字：不留前导空行
	out, err = appendMessage([]byte(`{"type":"prompt"}`), block)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(out, &f))
	assert.Equal(t, "<oryn-attachments>\n", f.Message[:len("<oryn-attachments>\n")])
}
