package transcript

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "重写 testdata 下的金文件")

// msg 构造 message 条目的 payload；content 可为字符串或块数组。
func msg(role string, content any, extra map[string]any) json.RawMessage {
	m := map[string]any{"role": role, "content": content}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(map[string]any{"type": "message", "message": m})
	return b
}

func text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

func ids(es []Entry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.EntryID)
	}
	return out
}

func TestIsMainFile(t *testing.T) {
	assert.True(t, IsMainFile("2026-01-01_abc.jsonl"))
	assert.False(t, IsMainFile("2026-01-01_abc/sub.jsonl"))
	assert.False(t, IsMainFile("notes.txt"))
}

func TestActiveBranch(t *testing.T) {
	entries := []Entry{
		{Seq: 1, File: "a.jsonl", EntryID: "__title__", Type: "title"},
		{Seq: 2, File: "a.jsonl", EntryID: "__session__", Type: "session"},
		{Seq: 3, File: "a.jsonl", EntryID: "u1", Type: "message"},
		{Seq: 4, File: "a.jsonl", EntryID: "x1", ParentID: new("u1"), Type: "message"},
		{Seq: 5, File: "a.jsonl", EntryID: "x2", ParentID: new("u1"), Type: "message"},
		{Seq: 6, File: "a/sub.jsonl", EntryID: "s1", Type: "message"},
		{Seq: 7, File: "b.jsonl", EntryID: "v1", Type: "message"},
		{Seq: 8, File: "b.jsonl", EntryID: "v2", ParentID: new("v1"), Type: "message"},
		{Seq: 9, File: "b.jsonl", EntryID: "v3", ParentID: new("v1"), Type: "message"},
		{Seq: 10, File: "a.jsonl", EntryID: "x3", ParentID: new("x1"), Type: "message"}, // a 文件的最后一条：旧分支上的迟到条目
	}

	// 叶子在第一个文件：该文件从叶子回溯，b 文件从最后一条回溯；文件按首次出现顺序拼接。
	assert.Equal(t, []string{"u1", "x2", "v1", "v3"}, ids(ActiveBranch(entries, "x2")))
	// 叶子在第二个文件：a 文件从其最后一条（x3）回溯。
	assert.Equal(t, []string{"u1", "x1", "x3", "v1", "v2"}, ids(ActiveBranch(entries, "v2")))
	// 无叶子 / 未知叶子：各文件都从最后一条回溯。
	assert.Equal(t, []string{"u1", "x1", "x3", "v1", "v3"}, ids(ActiveBranch(entries, "")))
	assert.Equal(t, ids(ActiveBranch(entries, "")), ids(ActiveBranch(entries, "nope")))

	// 环：遇到已访问的 id 即停止。
	cyc := []Entry{
		{Seq: 1, File: "a.jsonl", EntryID: "c1", ParentID: new("c2")},
		{Seq: 2, File: "a.jsonl", EntryID: "c2", ParentID: new("c1")},
	}
	assert.Equal(t, []string{"c1", "c2"}, ids(ActiveBranch(cyc, "")))
	// 断链：父不在本文件里即停止。
	broken := []Entry{
		{Seq: 1, File: "a.jsonl", EntryID: "r1"},
		{Seq: 2, File: "a.jsonl", EntryID: "r3", ParentID: new("gone")},
	}
	assert.Equal(t, []string{"r3"}, ids(ActiveBranch(broken, "")))
	assert.Empty(t, ActiveBranch(nil, ""))
}

func TestParseMessageAndLineText(t *testing.T) {
	e := Entry{Type: "message", Payload: msg("assistant", []any{
		map[string]any{"type": "thinking", "thinking": "hmm"},
		text("one"),
		map[string]any{"type": "toolCall", "id": "tc1", "name": "bash", "arguments": map[string]any{"command": "ls"}},
		map[string]any{"type": "image", "data": "x", "mimeType": "image/png"},
		text("two"),
	}, nil)}
	m, ok := ParseMessage(e)
	require.True(t, ok)
	assert.Equal(t, "assistant", m.Role)
	assert.Equal(t, "one\ntwo", m.Text)
	assert.Equal(t, "hmm", m.Thinking)
	assert.Equal(t, 1, m.Images)
	require.Len(t, m.ToolCalls, 1)
	assert.Equal(t, "tc1", m.ToolCalls[0].ID)
	assert.Equal(t, "bash", m.ToolCalls[0].Name)
	assert.JSONEq(t, `{"command":"ls"}`, string(m.ToolCalls[0].Args))
	role, txt := LineText(e)
	assert.Equal(t, "assistant", role)
	assert.Equal(t, "one\n<tool bash {\"command\":\"ls\"}>\n<image>\ntwo", txt)

	tr := Entry{Type: "message", Payload: msg("toolResult", []any{text("out")}, map[string]any{"toolName": "bash", "toolCallId": "tc1", "isError": true})}
	m, ok = ParseMessage(tr)
	require.True(t, ok)
	assert.Equal(t, "tc1", m.ToolCallID)
	assert.Equal(t, "bash", m.ToolName)
	assert.True(t, m.IsError)
	role, txt = LineText(tr)
	assert.Equal(t, "toolResult:bash", role)
	assert.Equal(t, "out", txt)

	role, txt = LineText(Entry{Type: "message", Payload: json.RawMessage(`{"message":{"role":"user","content":"plain"}}`)})
	assert.Equal(t, "user", role)
	assert.Equal(t, "plain", txt)

	_, ok = ParseMessage(Entry{Type: "compaction", Payload: json.RawMessage(`{}`)})
	assert.False(t, ok)
	_, ok = ParseMessage(Entry{Type: "message", Payload: json.RawMessage(`{"type":"message"}`)}) // 已被 Public 清空
	assert.False(t, ok)
	role, _ = LineText(Entry{Type: "model_change"})
	assert.Empty(t, role)
}

func TestPublic(t *testing.T) {
	cleared := func(typ string) string { return `{"type":"` + typ + `"}` }

	// 1. 子 agent 文件不公开。
	_, ok := Public(Entry{File: "a/sub.jsonl", Type: "message", Payload: msg("user", "x", nil)})
	assert.False(t, ok)

	// 2. 渲染类型原样保留。
	for _, e := range []Entry{
		{File: "a.jsonl", Type: "message", Payload: msg("assistant", "hi", nil)},
		{File: "a.jsonl", Type: "message", Payload: msg("custom", "shown", map[string]any{"display": true})},
		{File: "a.jsonl", Type: "compaction", Payload: json.RawMessage(`{"summary":"s"}`)},
		{File: "a.jsonl", Type: "custom_message", Payload: json.RawMessage(`{"content":"c","display":true}`)},
		{File: "a.jsonl", Type: "model_change", Payload: json.RawMessage(`{"model":"m"}`)},
	} {
		got, ok := Public(e)
		require.True(t, ok)
		assert.Equal(t, string(e.Payload), string(got.Payload), e.Type)
	}

	// 3. 隐藏类型（含合成的标题槽与会话头）：保留条目与链，清空内容。
	in := Entry{Seq: 3, File: "a.jsonl", EntryID: "l1", ParentID: new("u1"), Type: "label", Payload: json.RawMessage(`{"label":"secret"}`)}
	got, ok := Public(in)
	require.True(t, ok)
	assert.Equal(t, cleared("label"), string(got.Payload))
	assert.Equal(t, in.EntryID, got.EntryID)
	assert.Equal(t, "u1", *got.ParentID)
	assert.Equal(t, `{"label":"secret"}`, string(in.Payload), "不得改写入参")
	got, _ = Public(Entry{File: "a.jsonl", EntryID: "__session__", Type: "session", Payload: json.RawMessage(`{"cwd":"/home/user"}`)})
	assert.Equal(t, cleared("session"), string(got.Payload))

	// 4. display:false 的 custom / hookMessage 消息。
	for _, role := range []string{"custom", "hookMessage"} {
		got, ok = Public(Entry{File: "a.jsonl", Type: "message", Payload: msg(role, "inject", map[string]any{"display": false})})
		require.True(t, ok)
		assert.Equal(t, cleared("message"), string(got.Payload))
	}
	// display:false 只对注入类角色生效。
	got, _ = Public(Entry{File: "a.jsonl", Type: "message", Payload: msg("user", "u", map[string]any{"display": false})})
	assert.NotEqual(t, cleared("message"), string(got.Payload))

	// 5. display:false 的 custom_message。
	got, ok = Public(Entry{File: "a.jsonl", Type: "custom_message", Payload: json.RawMessage(`{"content":"c","display":false}`)})
	require.True(t, ok)
	assert.Equal(t, cleared("custom_message"), string(got.Payload))
}

func TestSnippet(t *testing.T) {
	assert.Equal(t, "Title Done: item one item two quoted", Snippet("# Title\n\nDone:\n- item one\n* item two\n> quoted", 200))
	assert.Equal(t, "see code after", Snippet("see\n```go\ncode\n```\nafter", 200))
	assert.Equal(t, "#hashtag **bold** -5", Snippet("  #hashtag\n**bold**\n-5", 200))
	assert.Equal(t, "a b c", Snippet("a\t\tb\n\n\nc", 200))
	assert.Equal(t, "nested", Snippet("> - nested", 200))
	assert.Equal(t, "abcd…", Snippet("abcdefgh", 5))
	assert.Equal(t, "你好世…", Snippet("你好世界和平", 4))
	assert.Equal(t, "ab…", Snippet("ab cdef", 4), "截断处的空格被去掉")
	assert.Equal(t, "abcd", Snippet("abcd", 4))
	assert.Empty(t, Snippet("```\n```", 10))
	assert.Empty(t, Snippet("x", 0))
}

func TestMarkdownGolden(t *testing.T) {
	toolResult := func(id, name, out string, isErr bool) json.RawMessage {
		return msg("toolResult", []any{text(out)}, map[string]any{"toolCallId": id, "toolName": name, "isError": isErr})
	}
	att := "please check\n\n<oryn-attachments>\nThe user attached these files. They are stored outside the repository; read them when relevant and do not copy them into the repository unless asked.\n" +
		"- /home/user/.oryn/attachments/T-x/att_01HZX3J9Q4W8E5R6T7Y8U9I0OP/report.pdf (application/pdf, 1234 bytes)\n</oryn-attachments>"
	long := strings.Repeat("x", 4100)
	branch := []Entry{
		{Seq: 1, File: "a.jsonl", EntryID: "__title__", Type: "title", Payload: json.RawMessage(`{"type":"title"}`)},
		{Seq: 2, File: "a.jsonl", EntryID: "u1", Type: "message", Payload: msg("user", []any{text(att), map[string]any{"type": "image", "data": "blob:sha256:00", "mimeType": "image/png"}}, nil)},
		{Seq: 3, File: "a.jsonl", EntryID: "a1", Type: "message", Payload: msg("assistant", []any{
			map[string]any{"type": "thinking", "thinking": "Let me look."},
			text("I'll run a command."),
			map[string]any{"type": "toolCall", "id": "t1", "name": "bash", "arguments": map[string]any{"command": "cat notes.md\n| head", "i": "Reading notes"}},
		}, nil)},
		{Seq: 4, File: "a.jsonl", EntryID: "r1", Type: "message", Payload: toolResult("t1", "bash", "Use ```go fences``` and `x`.\n````\nquad\n````", false)},
		{Seq: 5, File: "a.jsonl", EntryID: "a2", Type: "message", Payload: msg("assistant", []any{
			map[string]any{"type": "toolCall", "id": "t2", "name": "read", "arguments": map[string]any{"path": "`odd`.txt"}},
			map[string]any{"type": "toolCall", "id": "t3", "name": "grep", "arguments": map[string]any{"pattern": "TODO"}},
			map[string]any{"type": "toolCall", "id": "t4", "name": "todo", "arguments": map[string]any{}},
		}, nil)},
		{Seq: 6, File: "a.jsonl", EntryID: "r2", Type: "message", Payload: toolResult("t2", "read", "no such file", true)},
		{Seq: 7, File: "a.jsonl", EntryID: "r3", Type: "message", Payload: toolResult("t3", "grep", long, false)},
		{Seq: 8, File: "a.jsonl", EntryID: "m1", Type: "model_change", Payload: json.RawMessage(`{"model":"x"}`)},
		{Seq: 9, File: "a.jsonl", EntryID: "h1", Type: "message", Payload: json.RawMessage(`{"type":"message"}`)}, // 已清空
		{Seq: 10, File: "a.jsonl", EntryID: "a3", Type: "message", Payload: msg("assistant", "Done. All good.", nil)},
		{Seq: 11, File: "a.jsonl", EntryID: "c1", Type: "compaction", Payload: json.RawMessage(`{"summary":"s"}`)},
		{Seq: 12, File: "b.jsonl", EntryID: "u2", Type: "message", Payload: msg("user", "next session", nil)},
		{Seq: 13, File: "b.jsonl", EntryID: "cm", Type: "custom_message", Payload: json.RawMessage(`{"content":"Reminder:\n\nstay on task","display":true}`)},
		{Seq: 14, File: "b.jsonl", EntryID: "b1", Type: "branch_summary", Payload: json.RawMessage(`{"summary":"s"}`)},
		{Seq: 15, File: "b.jsonl", EntryID: "a4", Type: "message", Payload: msg("assistant", []any{text("Answer.")}, nil)},
	}
	var buf bytes.Buffer
	require.NoError(t, Markdown(&buf, MarkdownMeta{Title: "Fix\nthe build", Mode: "snapshot", SharedAt: time.Date(2026, 11, 13, 8, 30, 0, 0, time.FixedZone("CST", 8*3600))}, branch))
	golden := filepath.Join("testdata", "markdown.golden.md")
	if *update {
		require.NoError(t, os.WriteFile(golden, buf.Bytes(), 0o644))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err)
	assert.Equal(t, string(want), buf.String())

	buf.Reset()
	require.NoError(t, Markdown(&buf, MarkdownMeta{Mode: "live", SharedAt: time.UnixMilli(0)}, nil))
	assert.Equal(t, "# Untitled\n\n> Shared from Oryn · live · 1970-01-01T00:00:00Z\n", buf.String())
}
