package coord

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ownermem"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// fakeOwnerMemory 记录调用。
type fakeOwnerMemory struct {
	on       bool
	brief    string
	calls    []string
	queries  []string
	remember []string
}

func (f *fakeOwnerMemory) Enabled(context.Context) bool { return f.on }

func (f *fakeOwnerMemory) Brief(_ context.Context, threadID string) (string, error) {
	f.calls = append(f.calls, threadID)
	return f.brief, nil
}

func (f *fakeOwnerMemory) Recall(_ context.Context, q string) (ownermem.RecallResult, error) {
	f.queries = append(f.queries, q)
	return ownermem.RecallResult{Results: []ownermem.RecallHit{{Text: "主人住在新加坡", Type: "world", Source: "主人陈述"}}}, nil
}

func (f *fakeOwnerMemory) Remember(_ context.Context, threadID, content string, quote *string) (apigen.OwnerMemoryExplicit, error) {
	q := ""
	if quote != nil {
		q = *quote
	}
	f.remember = append(f.remember, threadID+"|"+content+"|"+q)
	st := apigen.OwnerMemoryExplicitPending
	if q != "" {
		st = apigen.OwnerMemoryExplicitActive
	}
	return apigen.OwnerMemoryExplicit{ID: "ome_1", Status: st}, nil
}

type fakeEngMemory struct {
	on       bool
	projects []string
}

func (f *fakeEngMemory) Enabled(context.Context) bool { return f.on }

func (f *fakeEngMemory) Search(_ context.Context, q string, projectID *string) (apigen.MemorySearchResult, error) {
	p := ""
	if projectID != nil {
		p = *projectID
	}
	f.projects = append(f.projects, p)
	return apigen.MemorySearchResult{Items: []apigen.MemorySearchHit{{Text: "约定：" + q}}}, nil
}

func toolNames(defs []apigen.OmpFrame) []string {
	var out []string
	for _, d := range defs {
		out = append(out, d["name"].(string))
	}
	return out
}

// TestProfileOwnerMemory：助手记忆没有运行时提示词与工具表与改动前逐字相同；运行时记忆段与用法接在末尾、多出记忆工具；
// Space 不注入。
func TestProfileOwnerMemory(t *testing.T) {
	e := newEnv(t, true)
	conv := e.assistant()
	before, err := e.c.AgentProfile(e.ctx, conv.ID)
	require.NoError(t, err)
	assert.Equal(t, SystemPrompt(nil, "", conv.ID, true), before.SystemPrompt)

	mem := &fakeOwnerMemory{brief: "Memory（记忆）：\n## 关于主人\n主人住在新加坡"}
	e.c.SetOwnerMemory(mem, nil)
	off, err := e.c.AgentProfile(e.ctx, conv.ID)
	require.NoError(t, err)
	assert.Equal(t, before.SystemPrompt, off.SystemPrompt, "助手记忆关闭时逐字不变")
	assert.Equal(t, toolNames(before.Tools), toolNames(off.Tools))
	assert.Empty(t, mem.calls)

	mem.on = true
	on, err := e.c.AgentProfile(e.ctx, conv.ID)
	require.NoError(t, err)
	assert.Equal(t, before.SystemPrompt+"\n\n"+mem.brief+"\n\n"+memoryGuide, on.SystemPrompt)
	names := toolNames(on.Tools)
	for _, n := range []string{ToolRecallMemory, ToolSearchHistory, ToolRecallEngineering, ToolRemember} {
		assert.Contains(t, names, n)
	}

	sp := e.space("s", "")
	spProf, err := e.c.AgentProfile(e.ctx, sp.ThreadID)
	require.NoError(t, err)
	assert.NotContains(t, spProf.SystemPrompt, "Memory")
	assert.NotContains(t, toolNames(spProf.Tools), ToolRecallMemory)
	assert.Equal(t, []string{conv.ID}, mem.calls, "Space 协调线程不生成记忆段")
}

// TestRecallMemoryTool：结果用不可信包裹；Space 与助手记忆关闭时没有这个工具。
func TestRecallMemoryTool(t *testing.T) {
	e := newEnv(t, true)
	conv := e.assistant()
	sp := e.spaceChat(e.space("s", ""))
	mem := &fakeOwnerMemory{on: true}
	e.c.SetOwnerMemory(mem, nil)

	r := e.call(conv, ToolRecallMemory, `{"query":"我住在哪"}`, true)
	require.False(t, r.IsError, r.Text)
	assert.True(t, strings.HasPrefix(r.Text, "<untrusted-thread-content>\n"), r.Text)
	assert.Contains(t, r.Text, "主人住在新加坡")
	assert.Equal(t, []string{"我住在哪"}, mem.queries)

	r = e.call(sp, ToolRecallMemory, `{"query":"x"}`, true)
	assert.True(t, strings.HasPrefix(r.Text, "unknown_tool:"), r.Text)
	mem.on = false
	r = e.call(conv, ToolRecallMemory, `{"query":"x"}`, true)
	assert.True(t, strings.HasPrefix(r.Text, "unknown_tool:"), r.Text)
}

// TestRememberTool：把本对话 id、内容与主人原话交给主人记忆，报告是否已生效。
func TestRememberTool(t *testing.T) {
	e := newEnv(t, true)
	conv := e.assistant()
	mem := &fakeOwnerMemory{on: true}
	e.c.SetOwnerMemory(mem, nil)

	r := e.call(conv, ToolRemember, `{"content":"主人的猫叫团子","ownerQuote":"记住我的猫叫团子"}`, true)
	require.False(t, r.IsError, r.Text)
	assert.Contains(t, r.Text, `"status":"active"`)
	r = e.call(conv, ToolRemember, `{"content":"x"}`, true)
	require.False(t, r.IsError, r.Text)
	assert.Contains(t, r.Text, "approval")
	assert.Equal(t, []string{conv.ID + "|主人的猫叫团子|记住我的猫叫团子", conv.ID + "|x|"}, mem.remember)
}

// TestRecallEngineeringTool：M16 未启用时 memory_disabled；给了项目时带上项目。
func TestRecallEngineeringTool(t *testing.T) {
	e := newEnv(t, true)
	conv := e.assistant()
	eng := &fakeEngMemory{}
	e.c.SetOwnerMemory(&fakeOwnerMemory{on: true}, eng)
	r := e.call(conv, ToolRecallEngineering, `{"query":"约定"}`, true)
	assert.True(t, strings.HasPrefix(r.Text, "memory_disabled:"), r.Text)

	eng.on = true
	p := e.project("app")
	r = e.call(conv, ToolRecallEngineering, `{"query":"测试约定","projectId":"`+p+`"}`, true)
	require.False(t, r.IsError, r.Text)
	assert.Contains(t, r.Text, "约定：测试约定")
	assert.True(t, strings.HasPrefix(r.Text, "<untrusted-thread-content>"))
	r = e.call(conv, ToolRecallEngineering, `{"query":"x","projectId":"prj_missing"}`, true)
	assert.True(t, strings.HasPrefix(r.Text, CodeNotFound+":"), r.Text)
	assert.Equal(t, []string{p}, eng.projects)
}

// addMsgs 给线程写 n 条交替的 user / assistant 消息（条目 id 按线程区分并连成一条链），返回各条的 seq。
func (e *env) addMsgs(threadID string, n int, text func(i int) string) []int64 {
	e.t.Helper()
	var seqs []int64
	eid := func(i int) string { return fmt.Sprintf("%s-m%d", threadID, i) }
	for i := range n {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		var parent *string
		if i > 0 {
			parent = new(eid(i - 1))
		}
		ts := int64(1000 + i)
		payload, _ := json.Marshal(map[string]any{"type": "message", "id": eid(i), "timestamp": ts,
			"message": map[string]any{"role": role, "content": text(i)}})
		seq, err := e.st.Q.UpsertEntry(e.ctx, db.UpsertEntryParams{ThreadID: threadID, File: "s.jsonl", EntryID: eid(i),
			ParentID: parent, Type: "message", Ts: &ts, Payload: payload})
		require.NoError(e.t, err)
		seqs = append(seqs, seq)
	}
	return seqs
}

// TestSearchHistoryTool：按种类与项目过滤，结果带 seq 与不可信包裹。
func TestSearchHistoryTool(t *testing.T) {
	e := newEnv(t, true)
	conv := e.assistant()
	e.c.SetOwnerMemory(&fakeOwnerMemory{on: true}, nil)
	other := e.assistant()
	p := e.project("app")
	w := e.work("w")
	_, err := e.st.DB.ExecContext(e.ctx, `UPDATE threads SET project_id = ? WHERE id = ?`, p, w.ID)
	require.NoError(t, err)
	e.addMsgs(other.ID, 2, func(i int) string { return fmt.Sprintf("关于部署方案的讨论 %d", i) })
	e.addMsgs(w.ID, 2, func(i int) string { return fmt.Sprintf("部署方案 落地 %d", i) })

	hits := func(args string) []historyHit {
		t.Helper()
		r := e.call(conv, ToolSearchHistory, args, true)
		require.False(t, r.IsError, r.Text)
		assert.True(t, strings.HasPrefix(r.Text, "<untrusted-thread-content>"))
		var v struct{ Hits []historyHit }
		raw, _ := json.Marshal(r.Details)
		require.NoError(t, json.Unmarshal(raw, &v))
		return v.Hits
	}
	assert.Len(t, hits(`{"query":"部署方案"}`), 4)
	h := hits(`{"query":"部署方案","kind":"assistant"}`)
	require.Len(t, h, 2)
	assert.Equal(t, other.ID, h[0].ThreadID)
	assert.Positive(t, h[0].Seq)
	h = hits(`{"query":"部署方案","projectId":"` + p + `"}`)
	require.Len(t, h, 2)
	assert.Equal(t, w.ID, h[0].ThreadID)
	assert.Len(t, hits(`{"query":"部署方案","before":1001}`), 2)

	r := e.call(conv, ToolSearchHistory, `{"query":"部署方案","kind":"space"}`, true)
	assert.True(t, strings.HasPrefix(r.Text, CodeInvalid+":"), r.Text)
}

// TestReadThreadSeq：全局助手可以读别的助手对话；aroundSeq / beforeSeq 读指定位置，每条带 seq；Space 对话不可读。
func TestReadThreadSeq(t *testing.T) {
	e := newEnv(t, true)
	conv := e.assistant()
	other := e.assistant()
	seqs := e.addMsgs(other.ID, 12, func(i int) string { return fmt.Sprintf("消息%d", i) })

	read := func(args string) []textMessage {
		t.Helper()
		r := e.call(conv, ToolReadThread, args, true)
		require.False(t, r.IsError, r.Text)
		var v struct{ Messages []textMessage }
		raw, _ := json.Marshal(r.Details)
		require.NoError(t, json.Unmarshal(raw, &v))
		return v.Messages
	}
	texts := func(ms []textMessage) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.Text)
		}
		return out
	}
	last := read(`{"threadId":"` + other.ID + `","tail":3}`)
	assert.Equal(t, []string{"消息9", "消息10", "消息11"}, texts(last))
	assert.Equal(t, seqs[9], last[0].Seq)
	assert.Equal(t, []string{"消息6", "消息7", "消息8"},
		texts(read(fmt.Sprintf(`{"threadId":"%s","tail":3,"beforeSeq":%d}`, other.ID, seqs[9]))))
	assert.Equal(t, []string{"消息3", "消息4", "消息5", "消息6"},
		texts(read(fmt.Sprintf(`{"threadId":"%s","tail":4,"aroundSeq":%d}`, other.ID, seqs[5]))))

	sp := e.space("s", "")
	r := e.call(conv, ToolReadThread, `{"threadId":"`+sp.ThreadID+`"}`, true)
	assert.True(t, strings.HasPrefix(r.Text, CodeInvalidTarget+":"), r.Text)
	// Space 协调者仍只能读工作线程
	r = e.call(e.spaceChat(sp), ToolReadThread, `{"threadId":"`+other.ID+`"}`, true)
	assert.True(t, strings.HasPrefix(r.Text, CodeInvalidTarget+":"), r.Text)
}
