package share

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/transcript"
)

var hex16 = regexp.MustCompile(`^[0-9a-f]{16}$`)

// testRoleKeySecret 是测试用的服务端密钥。
var testRoleKeySecret = []byte("test-role-key-secret")

func keysOf(shareID string) roleKeys { return roleKeys{secret: testRoleKeySecret, shareID: shareID} }

func TestRoleKey(t *testing.T) {
	k := keysOf("shr_A").of("Mare")
	assert.Regexp(t, hex16, k)
	assert.Equal(t, k, keysOf("shr_A").of("Mare"), "确定性")
	assert.NotEqual(t, k, keysOf("shr_B").of("Mare"), "不同分享的键不同")
	assert.NotEqual(t, k, keysOf("shr_A").of("Cyrene"))
}

// 只知道 shareID（所有者 API、日志、由 sharedAt 推出的时间戳）不足以由 role 名字典反推键：键依赖服务端密钥。
func TestRoleKeyNotDerivableFromShareID(t *testing.T) {
	k := keysOf("shr_A").of("Mare")
	plain := sha256.Sum256([]byte("shr_A\x00Mare"))
	assert.NotEqual(t, hex.EncodeToString(plain[:])[:16], k, "键不能只由 shareID 与 role 算出")
	other := roleKeys{secret: []byte("another-server"), shareID: "shr_A"}
	assert.NotEqual(t, k, other.of("Mare"), "换一个服务端密钥，键随之改变")

	// 派生密钥与 orb 令牌密钥本身不同（域分离），且确定（快照固化的键跨重启有效）
	master := []byte("0123456789abcdef0123456789abcdef")
	d := DeriveRoleKeySecret(master)
	assert.Equal(t, d, DeriveRoleKeySecret(master))
	assert.NotEqual(t, master, d)
}

// 来源记录：role / alias 换成不透明键，未知字段与条目头字段之外的内容被丢弃；其它 custom 条目仍被清空；子 agent 文件不公开。
func TestPublicEntry(t *testing.T) {
	raw := `{"type":"custom","id":"r1","parentId":"u0","timestamp":"2026-11-15T00:00:00.000Z","customType":"oryn-model-source","secret":"sk-leak",` +
		`"data":{"v":1,"source":"startup","model":"openai/gpt-6.1-sol","role":"default","alias":"Cyrene","note":"Nahida"}}`
	e := transcript.Entry{Seq: 3, File: "a.jsonl", EntryID: "r1", Type: "custom", Payload: json.RawMessage(raw)}
	pe, ok := publicEntry(keysOf("shr_A"), e)
	require.True(t, ok)
	assert.Equal(t, int64(3), pe.Seq)
	assert.Equal(t, "r1", pe.EntryID)
	var p map[string]any
	require.NoError(t, json.Unmarshal(pe.Payload, &p))
	assert.Equal(t, map[string]any{
		"type": "custom", "customType": "oryn-model-source",
		"data": map[string]any{"v": 1.0, "source": "startup", "model": "openai/gpt-6.1-sol", "role": keysOf("shr_A").of("default"), "alias": keysOf("shr_A").of("Cyrene")},
	}, p)
	for _, leak := range []string{"Cyrene", "Nahida", "sk-leak", `"default"`} {
		assert.NotContains(t, string(pe.Payload), leak)
	}
	pb, _ := publicEntry(keysOf("shr_B"), e)
	assert.NotEqual(t, string(pe.Payload), string(pb.Payload), "不同分享的键不同")

	// manual：没有 role / alias 键
	man := transcript.Entry{File: "a.jsonl", EntryID: "r2", Type: "custom", Payload: json.RawMessage(`{"type":"custom","customType":"oryn-model-source","data":{"v":1,"source":"manual","model":"p/m"}}`)}
	pe, ok = publicEntry(keysOf("shr_A"), man)
	require.True(t, ok)
	assert.JSONEq(t, `{"type":"custom","customType":"oryn-model-source","data":{"v":1,"source":"manual","model":"p/m"}}`, string(pe.Payload))

	// 其它 custom、非法记录（v:2）→ 清空
	for _, payload := range []string{
		`{"type":"custom","customType":"other","data":{"role":"Mare"}}`,
		`{"type":"custom","customType":"oryn-model-source","data":{"v":2,"source":"role","model":"p/m","role":"Mare"}}`,
	} {
		pe, ok := publicEntry(keysOf("shr_A"), transcript.Entry{File: "a.jsonl", EntryID: "x", Type: "custom", Payload: json.RawMessage(payload)})
		require.True(t, ok)
		assert.JSONEq(t, `{"type":"custom"}`, string(pe.Payload), payload)
	}

	// 子 agent 文件的记录不公开
	_, ok = publicEntry(keysOf("shr_A"), transcript.Entry{File: "a/task.jsonl", EntryID: "r1", Type: "custom", Payload: json.RawMessage(raw)})
	assert.False(t, ok)

	// 其余条目与 transcript.Public 相同
	msg := transcript.Entry{File: "a.jsonl", EntryID: "m", Type: "message", Payload: json.RawMessage(`{"type":"message","message":{"role":"user","content":"hi"}}`)}
	want, _ := transcript.Public(msg)
	got, ok := publicEntry(keysOf("shr_A"), msg)
	require.True(t, ok)
	assert.Equal(t, want, got)
}

// model_change：role 是用户配置的 role 名时换成本分享的不透明键（点过却没用的 role 名不得出现在公开条目里）；
// omp 的保留值原样保留（客户端靠它们识别回退 / 临时切换）；其余字段不变；子 agent 文件不公开。
func TestPublicModelChange(t *testing.T) {
	mc := func(role string) transcript.Entry {
		p := map[string]any{"type": "model_change", "id": "c1", "parentId": "u0", "model": "openai/gpt-6.1-sol", "resolvedModelIsFallback": false}
		if role != "" {
			p["role"] = role
		}
		b, _ := json.Marshal(p)
		return transcript.Entry{File: "a.jsonl", EntryID: "c1", Type: "model_change", Payload: b}
	}
	roleOf := func(e transcript.Entry) any {
		var p map[string]any
		require.NoError(t, json.Unmarshal(e.Payload, &p))
		assert.Equal(t, "openai/gpt-6.1-sol", p["model"], "其余字段不变")
		assert.Equal(t, false, p["resolvedModelIsFallback"])
		return p["role"]
	}

	pe, ok := publicEntry(keysOf("shr_A"), mc("Nahida"))
	require.True(t, ok)
	assert.NotContains(t, string(pe.Payload), "Nahida")
	assert.Equal(t, keysOf("shr_A").of("Nahida"), roleOf(pe))

	for _, r := range []string{"default", "fallback", "prewalk", "temporary"} {
		pe, ok := publicEntry(keysOf("shr_A"), mc(r))
		require.True(t, ok)
		assert.Equal(t, r, roleOf(pe), "保留值原样")
	}
	pe, ok = publicEntry(keysOf("shr_A"), mc(""))
	require.True(t, ok)
	assert.Nil(t, roleOf(pe), "没有 role 键时不添加")

	pe, ok = publicEntry(keysOf("shr_A"), transcript.Entry{File: "a.jsonl", EntryID: "c2", Type: "model_change", Payload: json.RawMessage(`not json`)})
	require.True(t, ok)
	assert.JSONEq(t, `{"type":"model_change"}`, string(pe.Payload), "坏载荷只留类型")

	_, ok = publicEntry(keysOf("shr_A"), transcript.Entry{File: "a/sub.jsonl", EntryID: "c3", Type: "model_change", Payload: mc("Nahida").Payload})
	assert.False(t, ok, "子 agent 文件不公开")
}
