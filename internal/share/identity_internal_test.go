package share

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/transcript"
)

var hex16 = regexp.MustCompile(`^[0-9a-f]{16}$`)

func TestRoleKey(t *testing.T) {
	k := roleKey("shr_A", "Mare")
	assert.Regexp(t, hex16, k)
	assert.Equal(t, k, roleKey("shr_A", "Mare"), "确定性")
	assert.NotEqual(t, k, roleKey("shr_B", "Mare"), "不同分享的键不同")
	assert.NotEqual(t, k, roleKey("shr_A", "Cyrene"))
}

// 来源记录：role / alias 换成不透明键，未知字段与条目头字段之外的内容被丢弃；其它 custom 条目仍被清空；子 agent 文件不公开。
func TestPublicEntry(t *testing.T) {
	raw := `{"type":"custom","id":"r1","parentId":"u0","timestamp":"2026-11-15T00:00:00.000Z","customType":"oryn-model-source","secret":"sk-leak",` +
		`"data":{"v":1,"source":"startup","model":"openai/gpt-6.1-sol","role":"default","alias":"Cyrene","note":"Nahida"}}`
	e := transcript.Entry{Seq: 3, File: "a.jsonl", EntryID: "r1", Type: "custom", Payload: json.RawMessage(raw)}
	pe, ok := publicEntry("shr_A", e)
	require.True(t, ok)
	assert.Equal(t, int64(3), pe.Seq)
	assert.Equal(t, "r1", pe.EntryID)
	var p map[string]any
	require.NoError(t, json.Unmarshal(pe.Payload, &p))
	assert.Equal(t, map[string]any{
		"type": "custom", "customType": "oryn-model-source",
		"data": map[string]any{"v": 1.0, "source": "startup", "model": "openai/gpt-6.1-sol", "role": roleKey("shr_A", "default"), "alias": roleKey("shr_A", "Cyrene")},
	}, p)
	for _, leak := range []string{"Cyrene", "Nahida", "sk-leak", `"default"`} {
		assert.NotContains(t, string(pe.Payload), leak)
	}
	pb, _ := publicEntry("shr_B", e)
	assert.NotEqual(t, string(pe.Payload), string(pb.Payload), "不同分享的键不同")

	// manual：没有 role / alias 键
	man := transcript.Entry{File: "a.jsonl", EntryID: "r2", Type: "custom", Payload: json.RawMessage(`{"type":"custom","customType":"oryn-model-source","data":{"v":1,"source":"manual","model":"p/m"}}`)}
	pe, ok = publicEntry("shr_A", man)
	require.True(t, ok)
	assert.JSONEq(t, `{"type":"custom","customType":"oryn-model-source","data":{"v":1,"source":"manual","model":"p/m"}}`, string(pe.Payload))

	// 其它 custom、非法记录（v:2）→ 清空
	for _, payload := range []string{
		`{"type":"custom","customType":"other","data":{"role":"Mare"}}`,
		`{"type":"custom","customType":"oryn-model-source","data":{"v":2,"source":"role","model":"p/m","role":"Mare"}}`,
	} {
		pe, ok := publicEntry("shr_A", transcript.Entry{File: "a.jsonl", EntryID: "x", Type: "custom", Payload: json.RawMessage(payload)})
		require.True(t, ok)
		assert.JSONEq(t, `{"type":"custom"}`, string(pe.Payload), payload)
	}

	// 子 agent 文件的记录不公开
	_, ok = publicEntry("shr_A", transcript.Entry{File: "a/task.jsonl", EntryID: "r1", Type: "custom", Payload: json.RawMessage(raw)})
	assert.False(t, ok)

	// 其余条目与 transcript.Public 相同
	msg := transcript.Entry{File: "a.jsonl", EntryID: "m", Type: "message", Payload: json.RawMessage(`{"type":"message","message":{"role":"user","content":"hi"}}`)}
	want, _ := transcript.Public(msg)
	got, ok := publicEntry("shr_A", msg)
	require.True(t, ok)
	assert.Equal(t, want, got)
}
