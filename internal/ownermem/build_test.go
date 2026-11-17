package ownermem

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/share"
	"github.com/Lynricsy/Oryn/internal/transcript"
)

// baseTS 是测试条目的起始时间（毫秒）；第 seq 条的时间是 baseTS + seq 秒。
const baseTS = int64(1_763_337_600_000) // 2025-11-17T00:00:00Z

// ent 构造一条主会话 message 条目；content 可为字符串或块数组。
func ent(seq int64, role string, content any) transcript.Entry {
	b, _ := json.Marshal(map[string]any{"type": "message", "message": map[string]any{"role": role, "content": content}})
	ts := baseTS + seq*1000
	return transcript.Entry{Seq: seq, File: "s.jsonl", EntryID: fmt.Sprintf("e%d", seq), Type: "message", TS: &ts, Payload: b}
}

func text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

func blocks(bs ...map[string]any) []map[string]any { return bs }

func thinking(s string) map[string]any { return map[string]any{"type": "thinking", "thinking": s} }

func toolCall(name string) map[string]any {
	return map[string]any{"type": "toolCall", "id": "c1", "name": name, "arguments": map[string]any{"cmd": "secret-tool-arg"}}
}

func contents(cs []Chunk) []string {
	var out []string
	for _, c := range cs {
		for _, it := range c.Items {
			out = append(out, it.Content)
		}
	}
	return out
}

// TestBuildConv：助手对话按主人回合切 item，带上一条助手回复与尾部回复；工具调用、结果、thinking、自动消息、标签块都不进入。
func TestBuildConv(t *testing.T) {
	branch := []transcript.Entry{
		ent(1, "user", "你好<system-reminder>\nreminder body\n</system-reminder>，帮我规划 Oryn 发版"),
		ent(2, "assistant", blocks(thinking("想一想"), text("先跑测试"), toolCall("bash"))),
		ent(3, "toolResult", blocks(text("tool output"))),
		ent(4, "assistant", blocks(text("测试通过，可以发版。"))),
		ent(5, "user", "[Oryn thread report · automatic] t9 is now idle."),
		ent(6, "assistant", blocks(text("收到回报，一切正常。"))),
		ent(7, "user", blocks(text("好的，就这么做"), map[string]any{"type": "image"})),
		ent(8, "assistant", blocks(toolCall("read"))),
		ent(9, "assistant", "已安排。"),
		{Seq: 10, File: "s/sub.jsonl", EntryID: "x", Type: "message", Payload: ent(10, "user", "子 agent 消息").Payload},
		{Seq: 11, File: "s.jsonl", EntryID: "e11", Type: "model_change", Payload: json.RawMessage(`{"type":"model_change"}`)},
	}
	meta := ThreadMeta{ID: "c1", Kind: "assistant", Title: "发版规划"}
	cs := Build(meta, branch, nil)
	require.Len(t, cs, 1)
	c := cs[0]
	assert.Equal(t, 0, c.Idx)
	assert.Equal(t, "conv:c1:0", c.DocumentID)
	assert.Equal(t, int64(1), c.SeqFrom)
	assert.Equal(t, int64(9), c.SeqTo)
	assert.Len(t, c.Hash, 64)
	assert.Equal(t, []string{
		"主人：你好，帮我规划 Oryn 发版",
		"助手（上一条）：收到回报，一切正常。\n主人：好的，就这么做",
		"助手：已安排。",
	}, contents(cs))

	assert.Equal(t, map[string]string{"seqs": "1", "roles": "owner", "thread": "c1"}, c.Items[0].Metadata)
	assert.Equal(t, map[string]string{"seqs": "6,7", "roles": "assistant,owner", "thread": "c1"}, c.Items[1].Metadata)
	assert.Equal(t, map[string]string{"seqs": "9", "roles": "assistant", "thread": "c1"}, c.Items[2].Metadata)
	for _, it := range c.Items {
		assert.Equal(t, "conv:c1:0", it.DocumentID)
		assert.Equal(t, []string{"src:assistant"}, it.Tags)
		assert.Equal(t, []Entity{{Text: "主人"}}, it.Entities)
		assert.Equal(t, "主人与全局助手的对话「发版规划」", it.Context)
	}
	assert.Equal(t, "2025-11-17T00:00:01Z", c.Items[0].Timestamp)
	assert.Equal(t, "2025-11-17T00:00:06Z", c.Items[1].Timestamp)

	all := strings.Join(contents(cs), "\n")
	for _, bad := range []string{"reminder", "想一想", "先跑测试", "tool output", "secret-tool-arg", "[Oryn", "子 agent"} {
		assert.NotContains(t, all, bad)
	}

	space := Build(ThreadMeta{ID: "s1", Kind: "space"}, branch[:1], nil)
	require.Len(t, space, 1)
	assert.Equal(t, "conv:s1:0", space[0].DocumentID)
	assert.Equal(t, "主人与 Space 协调助手的对话", space[0].Items[0].Context)
}

// TestBuildThread：工作线程一块一个 item，自动消息标为「自动消息」并截断，块头含标题、项目、状态与 PR。
func TestBuildThread(t *testing.T) {
	code := "```go\n" + strings.Repeat("x := 1\n", 10) + "```"
	auto := "[Oryn GitHub event on a/b#1 — delivered automatically, not typed by the owner] " + strings.Repeat("事", 400)
	branch := []transcript.Entry{
		ent(1, "user", auto),
		ent(2, "assistant", "我看到了事件"),
		ent(3, "user", "修一下这个 bug"),
		ent(4, "assistant", blocks(toolCall("edit"))),
		ent(5, "toolResult", "ok"),
		ent(6, "assistant", "已修复，代码如下：\n"+code+"\n短的：\n```\na\nb\n```\n完成"),
	}
	meta := ThreadMeta{ID: "w1", Kind: "work", Title: "修 bug", ProjectID: "PRJ1", ProjectName: "Oryn", Shipped: true, Archived: true, Pulls: []int64{12, 15}}
	cs := Build(meta, branch, nil)
	require.Len(t, cs, 1)
	require.Len(t, cs[0].Items, 1)
	it := cs[0].Items[0]
	autoLine := strings.SplitN(it.Content, "\n", 2)[0]
	assert.True(t, strings.HasPrefix(autoLine, "自动消息：[Oryn GitHub event"))
	assert.True(t, strings.HasSuffix(autoLine, "……（截断）"))
	assert.Equal(t, 300, utf8.RuneCountInString(strings.TrimSuffix(strings.TrimPrefix(autoLine, "自动消息："), "……（截断）")))
	assert.True(t, strings.HasSuffix(it.Content,
		"\nagent：我看到了事件\n主人：修一下这个 bug\nagent：已修复，代码如下：\n（代码 10 行，已省略）\n短的：\n```\na\nb\n```\n完成"), it.Content)
	assert.Equal(t, map[string]string{"seq_from": "1", "seq_to": "6", "roles": "owner,agent,auto", "thread": "w1"}, it.Metadata)
	assert.Equal(t, []string{"src:thread", "project:prj1"}, it.Tags)
	assert.Equal(t, "线程「修 bug」 · 项目 Oryn · 已交付 · PR #12, #15", it.Context)
	assert.Equal(t, "thread:w1:0", it.DocumentID)
	assert.Equal(t, "2025-11-17T00:00:01Z", it.Timestamp)
	assert.NotContains(t, it.Content, "x := 1")

	id := func(s string) string { return s }
	assert.Equal(t, "线程 · 已归档", threadContext(ThreadMeta{Archived: true}, id, true))
	assert.Equal(t, "线程「a」 · 项目 p1 · 出错", threadContext(ThreadMeta{Title: "a", ProjectID: "p1", State: "error"}, id, true))
	assert.Equal(t, "线程「a」 · 项目 p1", threadContext(ThreadMeta{Title: "a", ProjectID: "p1", State: "error"}, id, false))
	noProj := Build(ThreadMeta{ID: "w2", Kind: "work"}, branch[2:3], nil)
	require.Len(t, noProj, 1)
	assert.Equal(t, []string{"src:thread"}, noProj[0].Items[0].Tags)
	assert.Equal(t, "线程 · 进行中", noProj[0].Items[0].Context)
}

// TestBuildTruncation：主人消息截到 2000 字，回复截到 600 字，均加截断标记；标签块各种写法都被去掉。
func TestBuildTruncation(t *testing.T) {
	branch := []transcript.Entry{
		ent(1, "user", "<system-notice kind=\"x\">n</system-notice>\n<skill name=\"go\">\nskill body\n</skill>\n"+strings.Repeat("主", 2100)+"<advisory>a</advisory><task-result id=\"1\">r</task-result><system>s</system>"),
		ent(2, "assistant", strings.Repeat("答", 700)),
	}
	cs := Build(ThreadMeta{ID: "c", Kind: "assistant"}, branch, nil)
	got := contents(cs)
	require.Len(t, got, 2)
	assert.Equal(t, "主人："+strings.Repeat("主", 2000)+"……（截断）", got[0])
	assert.Equal(t, "助手："+strings.Repeat("答", 600)+"……（截断）", got[1])

	got = contents(Build(ThreadMeta{ID: "c", Kind: "assistant"}, []transcript.Entry{
		ent(1, "user", "<system-notice>n</system-notice>看看<advisory>a</advisory>这个<task-result>r</task-result>"),
	}, nil))
	assert.Equal(t, []string{"主人：看看这个"}, got)
}

// TestBuildNoOwner：没有主人消息（纯自动线程、只剩标签块的消息）或 kind 不认识时返回 nil。
func TestBuildNoOwner(t *testing.T) {
	branch := []transcript.Entry{
		ent(1, "user", "[Oryn scheduled run 3 · automatic]\n跑一下日报"),
		ent(2, "assistant", "日报已生成"),
		ent(3, "user", "<system-reminder>only</system-reminder>"),
	}
	assert.Nil(t, Build(ThreadMeta{ID: "w", Kind: "work"}, branch, nil))
	assert.Nil(t, Build(ThreadMeta{ID: "c", Kind: "assistant"}, branch, nil))
	assert.Nil(t, Build(ThreadMeta{ID: "x", Kind: "other"}, []transcript.Entry{ent(1, "user", "hi")}, nil))
	assert.Equal(t, "conv", SourceKindOf("assistant"))
	assert.Equal(t, "conv", SourceKindOf("space"))
	assert.Equal(t, "thread", SourceKindOf("work"))
	assert.Empty(t, SourceKindOf("other"))
}

// turns 构造 n 个主人回合（seq 从 start 开始），每回合：主人消息、工具调用、自动消息、agent 回复。
func turns(start int64, n int) []transcript.Entry {
	var out []transcript.Entry
	seq := start
	for i := range n {
		out = append(out,
			ent(seq, "user", fmt.Sprintf("第 %d 个问题：", i)+strings.Repeat("问", 300)),
			ent(seq+1, "assistant", blocks(toolCall("bash"))),
			ent(seq+2, "user", "[Oryn thread report · automatic] 子线程完成"),
			ent(seq+3, "assistant", fmt.Sprintf("第 %d 个回答：", i)+strings.Repeat("答", 400)),
		)
		seq += 4
	}
	return out
}

// TestBuildChunks：分块不拆开主人回合，超长单元单独成块；末尾追加消息只改变最后一块（前面块的哈希不变）。
func TestBuildChunks(t *testing.T) {
	meta := ThreadMeta{ID: "w", Kind: "work", Title: "长线程"}
	branch := turns(1, 8)
	cs := Build(meta, branch, nil)
	require.Greater(t, len(cs), 2)
	for i, c := range cs {
		assert.Equal(t, i, c.Idx)
		assert.Equal(t, fmt.Sprintf("thread:w:%d", i), c.DocumentID)
		require.Len(t, c.Items, 1)
		content := c.Items[0].Content
		assert.True(t, strings.HasPrefix(content, "主人：第 "), "块必须从主人回合开头开始")
		// 每个回合的三行都在同一块里。
		assert.Equal(t, strings.Count(content, "主人："), strings.Count(content, "agent："))
		assert.Equal(t, strings.Count(content, "主人："), strings.Count(content, "自动消息："))
		assert.Equal(t, fmt.Sprint(c.SeqFrom), c.Items[0].Metadata["seq_from"])
		assert.Equal(t, fmt.Sprint(c.SeqTo), c.Items[0].Metadata["seq_to"])
		if strings.Count(content, "主人：") > 1 {
			assert.LessOrEqual(t, utf8.RuneCountInString(content), chunkRunes+1)
		}
	}
	assert.Equal(t, cs, Build(meta, branch, nil), "构造是确定的")

	// 追加：最后一个回合多一条 agent 回复，再加两个新回合。
	more := append(append([]transcript.Entry{}, branch...), ent(100, "assistant", "补充说明"))
	more = append(more, turns(101, 2)...)
	cs2 := Build(meta, more, nil)
	require.GreaterOrEqual(t, len(cs2), len(cs))
	for i := 0; i < len(cs)-1; i++ {
		assert.Equal(t, cs[i].Hash, cs2[i].Hash, "块 %d 不应变化", i)
	}
	assert.NotEqual(t, cs[len(cs)-1].Hash, cs2[len(cs)-1].Hash)

	// 交付并关联 PR：状态只写在最后一块，前面的块不变。
	shipped := meta
	shipped.Shipped, shipped.Pulls = true, []int64{7}
	cs3 := Build(shipped, branch, nil)
	require.Len(t, cs3, len(cs))
	for i := range len(cs) - 1 {
		assert.Equal(t, cs[i].Hash, cs3[i].Hash, "块 %d 不应变化", i)
		assert.Equal(t, "线程「长线程」", cs3[i].Items[0].Context)
	}
	assert.Equal(t, "线程「长线程」 · 已交付 · PR #7", cs3[len(cs3)-1].Items[0].Context)

	// 超长单元（主人 2000 字 + 回复 600 字）单独成块。
	big := []transcript.Entry{
		ent(1, "user", "短问题"),
		ent(2, "user", strings.Repeat("长", 2100)),
		ent(3, "assistant", strings.Repeat("答", 700)),
		ent(4, "user", "再问"),
	}
	bcs := Build(meta, big, nil)
	require.Len(t, bcs, 3)
	assert.Equal(t, "主人：短问题", bcs[0].Items[0].Content)
	assert.Equal(t, int64(2), bcs[1].SeqFrom)
	assert.Equal(t, int64(3), bcs[1].SeqTo)
	assert.Equal(t, "主人：再问", bcs[2].Items[0].Content)
}

// TestBuildConvChunks：助手对话的块由多个 item 组成；追加主人消息时尾部回复 item 变成新 item 的「上一条」，只影响最后一块。
func TestBuildConvChunks(t *testing.T) {
	meta := ThreadMeta{ID: "c", Kind: "assistant"}
	var branch []transcript.Entry
	for i := range 10 {
		seq := int64(i*2 + 1)
		branch = append(branch, ent(seq, "user", strings.Repeat("问", 300)), ent(seq+1, "assistant", strings.Repeat("答", 500)))
	}
	cs := Build(meta, branch, nil)
	require.Greater(t, len(cs), 2)
	n := 0
	for _, c := range cs {
		for _, it := range c.Items {
			assert.Equal(t, c.DocumentID, it.DocumentID)
			n++
		}
	}
	assert.Equal(t, 11, n, "10 个主人 item + 1 个尾部回复 item")
	last := cs[len(cs)-1].Items
	assert.True(t, strings.HasPrefix(last[len(last)-1].Content, "助手："))

	cs2 := Build(meta, append(append([]transcript.Entry{}, branch...), ent(21, "user", "收到")), nil)
	require.Len(t, cs2, len(cs))
	for i := 0; i < len(cs)-1; i++ {
		assert.Equal(t, cs[i].Hash, cs2[i].Hash)
	}
	tail := cs2[len(cs2)-1].Items
	assert.Equal(t, "助手（上一条）："+strings.Repeat("答", 500)+"\n主人：收到", tail[len(tail)-1].Content)
	assert.Equal(t, "20,21", tail[len(tail)-1].Metadata["seqs"])
}

// TestBuildRedact：全部文本（含标题）先过脱敏；配合 share.RedactText 替换 sk- 密钥、bearer 头与密钥库值。
func TestBuildRedact(t *testing.T) {
	svc := &share.Service{}
	redact := func(s string) string {
		out, _ := svc.RedactText(context.Background(), s)
		return strings.ReplaceAll(out, "hunter2-vault-value", share.RedactPlaceholder)
	}
	branch := []transcript.Entry{
		ent(1, "user", "我的 key 是 sk-test-abcdefghijklmnopqrstuvwx0123，密码 hunter2-vault-value"),
		ent(2, "assistant", `用 curl -H "Authorization: Bearer abcdefghijklmnopqrstuvwxyz012345" 调用`),
	}
	cs := Build(ThreadMeta{ID: "c", Kind: "assistant", Title: "配置 sk-test-abcdefghijklmnopqrstuvwx0123"}, branch, redact)
	got := contents(cs)
	require.Len(t, got, 2)
	assert.Equal(t, "主人：我的 key 是 〔已脱敏〕，密码 〔已脱敏〕", got[0])
	assert.Equal(t, `助手：用 curl -H "〔已脱敏〕" 调用`, got[1])
	assert.Equal(t, "主人与全局助手的对话「配置 〔已脱敏〕」", cs[0].Items[0].Context)
}
