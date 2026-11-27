package coord

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// viewOf 返回结果的 details.view（JSON 往返后的形状，与写进 host_tool_result 的一致）。
func viewOf(t *testing.T, res threads.HostToolResult) map[string]any {
	t.Helper()
	b, err := json.Marshal(res.Details)
	require.NoError(t, err)
	var d map[string]map[string]any
	require.NoError(t, json.Unmarshal(b, &d))
	require.Contains(t, d, "view")
	return d["view"]
}

// specJSON 返回 details.view.spec 的 JSON 文本。
func specJSON(t *testing.T, view map[string]any) string {
	t.Helper()
	b, err := json.Marshal(view["spec"])
	require.NoError(t, err)
	return string(b)
}

// 视图工具与指引只给生成式 UI 开启的全局助手：Space 协调者、研究员、受限的一段与开关关闭时都没有。
func TestViewToolsInProfile(t *testing.T) {
	e := newEnv(t, true)
	conv := e.assistant()
	hasViews := func(p threads.AgentProfile) bool {
		names := toolNames(p.Tools)
		ui, html := false, false
		for _, n := range names {
			ui = ui || n == ToolRenderUI
			html = html || n == ToolRenderHTML
		}
		assert.Equal(t, ui, html, "两个工具同进同出")
		assert.Equal(t, ui, strings.Contains(p.SystemPrompt, viewGuide()), "工具与指引同进同出")
		return ui
	}

	p, err := e.c.AgentProfile(e.ctx, conv.ID)
	require.NoError(t, err)
	assert.True(t, hasViews(p), "所有者未初始化时按开启")
	assert.True(t, strings.HasPrefix(viewGuide(), "# Views (render_ui / render_html)"))
	for _, d := range p.Tools {
		if n := frameName(d); n == ToolRenderUI || n == ToolRenderHTML {
			assert.Equal(t, "essential", d["loadMode"], n)
			assert.NotEmpty(t, d["description"], n)
			assert.Equal(t, "object", d["parameters"].(obj)["type"], n)
		}
	}

	sp := e.space("s", "")
	sprof, err := e.c.AgentProfile(e.ctx, sp.ThreadID)
	require.NoError(t, err)
	assert.False(t, hasViews(sprof), "Space 协调者没有视图")

	resID, err := e.c.EnsureResearcher(e.ctx, "private")
	require.NoError(t, err)
	rp, err := e.c.AgentProfile(e.ctx, resID)
	require.NoError(t, err)
	assert.False(t, hasViews(rp), "研究员没有视图")

	main, err := e.c.EnsureMain(e.ctx)
	require.NoError(t, err)
	mp, err := e.c.AgentProfile(e.ctx, main.ID)
	require.NoError(t, err)
	assert.True(t, hasViews(mp), "主线有视图")
	e.c.mu.Lock()
	e.c.restrict.want = true
	e.c.mu.Unlock()
	mp, err = e.c.AgentProfile(e.ctx, main.ID)
	require.NoError(t, err)
	assert.False(t, hasViews(mp), "受限的一段没有视图")
	e.c.mu.Lock()
	e.c.restrict.want = false
	e.c.mu.Unlock()

	require.NoError(t, e.st.Q.CreateOwner(e.ctx, db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))
	n, err := e.st.Q.SetOwnerGenerativeUI(e.ctx, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	p, err = e.c.AgentProfile(e.ctx, conv.ID)
	require.NoError(t, err)
	assert.False(t, hasViews(p), "开关关闭后没有视图")
}

// 非 await 与参数检查：文本按 C3，details.view.spec 是去掉 await 的参数（HTML 只留 libs / height）。
func TestViewRenderedResults(t *testing.T) {
	e := newEnv(t, true)
	asst := e.assistant()

	res := e.call(asst, ToolRenderUI, `{"title":"T","blocks":[{"type":"heading","text":"Hi"}],"await":true}`, true)
	require.False(t, res.IsError)
	assert.Equal(t, viewTextRendered, res.Text, "没有交互块时 await 被忽略")
	v := viewOf(t, res)
	assert.Equal(t, "rendered", v["outcome"])
	assert.JSONEq(t, `{"title":"T","blocks":[{"type":"heading","text":"Hi"}]}`, specJSON(t, v))
	assert.NotContains(t, v, "answer")

	// 嵌在 columns → card 里的表单也算交互块；form 自己的 columns 是数字
	nested := `{"blocks":[{"type":"columns","columns":[{"blocks":[{"type":"text","text":"a"}]},{"blocks":[{"type":"card","blocks":[{"type":"form","columns":2,"fields":[{"name":"n","label":"N","kind":"text"}]}]}]}]}]}`
	res = e.call(asst, ToolRenderUI, nested, true)
	require.False(t, res.IsError)
	assert.Equal(t, "Rendered. The user's input will arrive as their next message; end this turn now.", res.Text)
	assert.JSONEq(t, nested, specJSON(t, viewOf(t, res)))
	res = e.call(asst, ToolRenderUI, `{"blocks":[{"type":"tabs","tabs":[{"label":"a","blocks":[{"type":"divider"}]},{"label":"b","blocks":[{"type":"actions","buttons":[{"label":"Go","prompt":"go"}]}]}]}],"await":false}`, true)
	assert.Equal(t, viewTextRenderedInteractive, res.Text)

	res = e.call(asst, ToolRenderHTML, `{"title":"H","html":"<p>hi</p>","libs":["chartjs"],"height":300}`, true)
	require.False(t, res.IsError)
	assert.Equal(t, "Rendered.", res.Text)
	v = viewOf(t, res)
	assert.Equal(t, "rendered", v["outcome"])
	assert.JSONEq(t, `{"libs":["chartjs"],"height":300}`, specJSON(t, v))
	assert.JSONEq(t, `{}`, specJSON(t, viewOf(t, e.call(asst, ToolRenderHTML, `{"html":"x"}`, true))))

	blocks41 := "[" + strings.TrimSuffix(strings.Repeat(`{"type":"divider"},`, 41), ",") + "]"
	for _, bad := range []struct{ name, args string }{
		{ToolRenderUI, `{"blocks":[]}`},
		{ToolRenderUI, `{"blocks":` + blocks41 + `}`},
		{ToolRenderUI, `{"blocks":"[]"}`},
		{ToolRenderUI, `[]`},
		{ToolRenderHTML, `{"html":""}`},
		{ToolRenderHTML, `{"html":"` + strings.Repeat("字", viewMaxHTML+1) + `"}`},
	} {
		res := e.call(asst, bad.name, bad.args, true)
		assert.True(t, res.IsError, bad.args)
		assert.True(t, strings.HasPrefix(res.Text, CodeInvalid+": "), res.Text)
		assert.Nil(t, res.Details, "参数不合格时没有 details.view")
	}

	sp := e.space("s", "")
	errText(t, e.call(e.spaceChat(sp), ToolRenderUI, `{"blocks":[{"type":"divider"}]}`, true), "unknown_tool")
	assert.Equal(t, viewToolTimeout, e.c.HostToolTimeout(ToolRenderUI))
	assert.Equal(t, viewToolTimeout, e.c.HostToolTimeout(ToolRenderHTML))
}

// viewAwaitCall 在后台以对话 conv 的身份调用视图工具（工具调用 id 为 tc-1），返回结果通道。
func viewAwaitCall(ctx context.Context, e *env, conv apigen.Thread, name, args string) <-chan threads.HostToolResult {
	out := make(chan threads.HostToolResult, 1)
	go func() {
		out <- e.c.HostToolCall(ctx, threads.HostToolCall{ThreadID: conv.ID, Kind: threads.KindAssistant, CallID: "h-1",
			ToolCallID: "tc-1", Name: name, Args: json.RawMessage(args), OwnerTurn: true})
	}()
	return out
}

// waitViewRequest 等对话里挂出视图作答请求，返回解析后的请求帧。
func waitViewRequest(t *testing.T, e *env, conv apigen.Thread) map[string]any {
	t.Helper()
	var frames []json.RawMessage
	require.Eventually(t, func() bool {
		frames = pending(t, e.th, conv.ID)
		return len(frames) == 1
	}, 5*time.Second, 10*time.Millisecond, "视图作答请求挂出")
	var f map[string]any
	require.NoError(t, json.Unmarshal(frames[0], &f))
	return f
}

func viewResultOf(t *testing.T, ch <-chan threads.HostToolResult) threads.HostToolResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("视图调用没有返回")
		return threads.HostToolResult{}
	}
}

// await：作答（带 {v:1} 的 JSON 与旧客户端的纯文本）、主人改为打字（cancelled）、10 分钟到时、调用取消。
func TestViewAwait(t *testing.T) {
	ce := newConfirmEnv(t)
	e := ce.env
	asst := e.assistant()
	const form = `{"title":"Pick a plan","blocks":[{"type":"form","fields":[{"name":"plan","label":"Plan","kind":"text"}]}],"await":true}`
	respond := func(id string, body map[string]any) {
		t.Helper()
		body["type"], body["id"] = "extension_ui_response", id
		b, err := json.Marshal(body)
		require.NoError(t, err)
		require.NoError(t, e.th.UIResponse(e.ctx, asst.ID, b))
	}

	t.Run("answered", func(t *testing.T) {
		ch := viewAwaitCall(e.ctx, e, asst, ToolRenderUI, form)
		f := waitViewRequest(t, e, asst)
		id, _ := f["id"].(string)
		assert.True(t, strings.HasPrefix(id, "vwa_"), id)
		assert.Equal(t, "input", f["method"])
		assert.Equal(t, "Pick a plan", f["title"])
		assert.Equal(t, "oryn-view:tc-1", f["placeholder"])
		require.Eventually(t, func() bool {
			got, err := e.th.Get(e.ctx, asst.ID)
			return err == nil && string(got.State) == threads.StateAwaitingInput
		}, 10*time.Second, 20*time.Millisecond, "等待作答时对话处于 awaiting_input")

		text := "Pick a plan\n- Plan: B\n\n```json\n{\"plan\":\"B\"}\n```"
		value, err := json.Marshal(map[string]any{"v": 1, "answer": map[string]any{"block": "0", "kind": "form", "values": map[string]any{"plan": "B"}}, "text": text})
		require.NoError(t, err)
		respond(id, map[string]any{"value": string(value)})
		res := viewResultOf(t, ch)
		require.False(t, res.IsError)
		assert.Equal(t, "The user answered in the view:\n"+text, res.Text)
		v := viewOf(t, res)
		assert.Equal(t, "answered", v["outcome"])
		assert.Equal(t, map[string]any{"block": "0", "kind": "form", "values": map[string]any{"plan": "B"}}, v["answer"])
		assert.JSONEq(t, `{"title":"Pick a plan","blocks":[{"type":"form","fields":[{"name":"plan","label":"Plan","kind":"text"}]}]}`, specJSON(t, v))
		assert.Empty(t, pending(t, e.th, asst.ID))
	})

	t.Run("plain text answer", func(t *testing.T) {
		ch := viewAwaitCall(e.ctx, e, asst, ToolRenderHTML, `{"html":"<button>x</button>","libs":["tailwind"],"await":true}`)
		f := waitViewRequest(t, e, asst)
		assert.Equal(t, viewWaitingTitleZh, f["title"], "没有 title 时按语言取缺省标题")
		respond(f["id"].(string), map[string]any{"value": "typed in the dock"})
		res := viewResultOf(t, ch)
		assert.Equal(t, "The user answered in the view:\ntyped in the dock", res.Text)
		v := viewOf(t, res)
		assert.Equal(t, "answered", v["outcome"])
		assert.NotContains(t, v, "answer")
		assert.JSONEq(t, `{"libs":["tailwind"]}`, specJSON(t, v))
	})

	t.Run("dismissed", func(t *testing.T) {
		ch := viewAwaitCall(e.ctx, e, asst, ToolRenderUI, form)
		f := waitViewRequest(t, e, asst)
		respond(f["id"].(string), map[string]any{"cancelled": true})
		res := viewResultOf(t, ch)
		require.False(t, res.IsError)
		assert.Equal(t, "The user chose to reply in text instead; their message follows.", res.Text)
		assert.Equal(t, "dismissed", viewOf(t, res)["outcome"])
	})

	t.Run("timeout", func(t *testing.T) {
		old := viewAwaitTimeout
		viewAwaitTimeout = 200 * time.Millisecond
		t.Cleanup(func() { viewAwaitTimeout = old })
		ch := viewAwaitCall(e.ctx, e, asst, ToolRenderUI, form)
		f := waitViewRequest(t, e, asst)
		res := viewResultOf(t, ch)
		require.False(t, res.IsError)
		assert.Equal(t, "No response within 10 minutes. The view stays usable: a later answer arrives as a user message. Continue without it or end the turn.", res.Text)
		assert.Equal(t, "timeout", viewOf(t, res)["outcome"])
		assert.Empty(t, pending(t, e.th, asst.ID), "到时撤回请求")
		assert.ErrorIs(t, e.th.UIResponse(e.ctx, asst.ID, json.RawMessage(`{"type":"extension_ui_response","id":"`+f["id"].(string)+`","value":"late"}`)), threads.ErrUIResolved)
	})

	t.Run("aborted", func(t *testing.T) {
		ctx, cancel := context.WithCancel(e.ctx)
		ch := viewAwaitCall(ctx, e, asst, ToolRenderUI, form)
		waitViewRequest(t, e, asst)
		cancel()
		res := viewResultOf(t, ch)
		assert.True(t, res.IsError)
		assert.Equal(t, "Aborted.", res.Text)
		assert.Equal(t, "aborted", viewOf(t, res)["outcome"])
		assert.Empty(t, pending(t, e.th, asst.ID), "取消时撤回请求")
	})

	t.Run("english title", func(t *testing.T) {
		require.NoError(t, e.st.Q.CreateOwner(e.ctx, db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))
		_, err := e.st.Q.SetOwnerLanguage(e.ctx, string(apigen.En))
		require.NoError(t, err)
		ch := viewAwaitCall(e.ctx, e, asst, ToolRenderUI, `{"blocks":[{"type":"actions","buttons":[{"label":"A","prompt":"a"}]}],"await":true}`)
		f := waitViewRequest(t, e, asst)
		assert.Equal(t, viewWaitingTitleEn, f["title"])
		respond(f["id"].(string), map[string]any{"cancelled": true})
		viewResultOf(t, ch)
	})
}
