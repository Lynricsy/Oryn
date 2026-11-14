package transcript

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// src 构造来源记录条目。
func src(id string, data map[string]any) Entry {
	b, _ := json.Marshal(map[string]any{"type": "custom", "customType": ModelSourceType, "data": data})
	return Entry{File: "a.jsonl", EntryID: id, Type: "custom", Payload: b}
}

func mc(id, role string) Entry {
	p := map[string]any{"type": "model_change", "model": "openai/gpt-6.1-sol"}
	if role != "" {
		p["role"] = role
	}
	b, _ := json.Marshal(p)
	return Entry{File: "a.jsonl", EntryID: id, Type: "model_change", Payload: b}
}

func m(id string) Entry {
	return Entry{File: "a.jsonl", EntryID: id, Type: "message", Payload: msg("user", "hi", nil)}
}

func TestParseModelSource(t *testing.T) {
	ok := src("r", map[string]any{"v": 1, "source": "role", "model": "openai/gpt-6.1-sol", "role": "Mare", "alias": "Cyrene", "extra": true})
	ms, good := ParseModelSource(ok)
	require.True(t, good)
	assert.Equal(t, ModelSource{Source: "role", Model: "openai/gpt-6.1-sol", Role: "Mare", Alias: "Cyrene"}, ms)

	ms, good = ParseModelSource(src("s", map[string]any{"v": 1, "source": "startup", "model": "p/m", "role": 3}))
	require.True(t, good)
	assert.Equal(t, "", ms.Role, "非字符串 role 按空处理")

	bad := map[string]Entry{
		"v2":          src("x", map[string]any{"v": 2, "source": "role", "model": "p/m"}),
		"v 字符串":       src("x", map[string]any{"v": "1", "source": "role", "model": "p/m"}),
		"缺 v":         src("x", map[string]any{"source": "role", "model": "p/m"}),
		"未知 source":   src("x", map[string]any{"v": 1, "source": "guess", "model": "p/m"}),
		"缺 model":     src("x", map[string]any{"v": 1, "source": "role"}),
		"空 model":     src("x", map[string]any{"v": 1, "source": "manual", "model": ""}),
		"model 非字符串":  src("x", map[string]any{"v": 1, "source": "manual", "model": 1}),
		"缺 data":      {Type: "custom", Payload: json.RawMessage(`{"type":"custom","customType":"oryn-model-source"}`)},
		"别的 custom":   {Type: "custom", Payload: json.RawMessage(`{"type":"custom","customType":"other","data":{"v":1,"source":"role","model":"p/m"}}`)},
		"不是 custom":   {Type: "model_change", Payload: json.RawMessage(`{"customType":"oryn-model-source","data":{"v":1,"source":"role","model":"p/m"}}`)},
		"payload 非对象": {Type: "custom", Payload: json.RawMessage(`[1]`)},
		"payload 清空":  {Type: "custom", Payload: json.RawMessage(`{"type":"custom"}`)},
	}
	for name, e := range bad {
		_, good := ParseModelSource(e)
		assert.False(t, good, name)
	}
}

func TestUsedRoles(t *testing.T) {
	role := func(id, r string) Entry {
		return src(id, map[string]any{"v": 1, "source": "role", "model": "openai/gpt-6.1-sol", "role": r})
	}
	cases := []struct {
		name   string
		branch []Entry
		want   [][]string
	}{
		{"记录后有消息 → 用到", []Entry{role("r1", "Mare"), m("u1")}, [][]string{{"Mare"}}},
		{"点了就换走 → 不在结果里", []Entry{role("r1", "Cyrene"), role("r2", "Mare"), m("u1")}, [][]string{{"Mare"}}},
		{"消息在记录之前不算", []Entry{m("u0"), role("r1", "Mare")}, nil},
		{"manual 不计", []Entry{src("r1", map[string]any{"v": 1, "source": "manual", "model": "p/m", "role": "Mare"}), m("u1")}, nil},
		{"fallback 的 model_change 不截断", []Entry{role("r1", "Mare"), mc("c1", "fallback"), m("u1")}, [][]string{{"Mare"}}},
		{"prewalk 的 model_change 不截断", []Entry{role("r1", "Mare"), mc("c1", "prewalk"), m("u1")}, [][]string{{"Mare"}}},
		{"default 的 model_change 截断", []Entry{role("r1", "Mare"), mc("c1", "default"), m("u1")}, nil},
		{"无 role 的 model_change 截断", []Entry{role("r1", "Mare"), mc("c1", ""), m("u1")}, nil},
		{"startup 带 alias → 两项候选", []Entry{
			src("r1", map[string]any{"v": 1, "source": "startup", "model": "p/m", "role": "default", "alias": "Cyrene"}), m("u1"),
		}, [][]string{{"default", "Cyrene"}}},
		{"非法记录不截断前一条", []Entry{role("r1", "Mare"), src("r2", map[string]any{"v": 2, "source": "role", "model": "p/m", "role": "Nahida"}), m("u1")}, [][]string{{"Mare"}}},
		{"多条各自计入", []Entry{role("r1", "Mare"), m("u1"), mc("c1", "default"), m("u2"), role("r2", "Hyacinthia"), m("u3")}, [][]string{{"Mare"}, {"Hyacinthia"}}},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, UsedRoles(c.branch), c.name)
	}
}

// 进程在首条回复前重启留下的启动文件（只有标题槽、会话头、model_change 与来源记录）不产生输出块：不出现多余的 `---`；
// 来源记录与身份都不进 Markdown。
func TestMarkdownStartupOnlyFile(t *testing.T) {
	startup := func(file string, seq int64) []Entry {
		return []Entry{
			{Seq: seq, File: file, EntryID: "__title__", Type: "title", Payload: json.RawMessage(`{"type":"title"}`)},
			{Seq: seq + 1, File: file, EntryID: "mc" + file, Type: "model_change", Payload: json.RawMessage(`{"type":"model_change","model":"p/m"}`)},
			{Seq: seq + 2, File: file, EntryID: "s" + file, Type: "custom", Payload: json.RawMessage(`{"type":"custom","customType":"oryn-model-source","data":{"v":1,"source":"role","model":"p/m","role":"Mare"}}`)},
		}
	}
	var branch []Entry
	branch = append(branch, Entry{Seq: 1, File: "a.jsonl", EntryID: "u1", Type: "message", Payload: msg("user", "first", nil)})
	branch = append(branch, startup("b.jsonl", 10)...)
	branch = append(branch, Entry{Seq: 20, File: "c.jsonl", EntryID: "u2", Type: "message", Payload: msg("user", "second", nil)})
	branch = append(branch, startup("d.jsonl", 30)...)
	var buf bytes.Buffer
	require.NoError(t, Markdown(&buf, MarkdownMeta{Mode: "snapshot", SharedAt: time.UnixMilli(0)}, branch))
	out := buf.String()
	assert.Equal(t, 1, strings.Count(out, "\n---\n"), out)
	assert.True(t, strings.HasSuffix(out, "second\n"), "末尾的启动文件不留分隔线：%q", out)
	assert.NotContains(t, out, "Mare")
	assert.NotContains(t, out, ModelSourceType)
}
