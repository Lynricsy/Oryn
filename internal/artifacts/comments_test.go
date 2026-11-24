package artifacts

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// codeOf 返回 err 的错误码（不是 *OpError 时为空）。
func codeOf(err error) string {
	if oe, ok := AsOpError(err); ok {
		return oe.Code
	}
	return ""
}

// commentsJSON 解出 read_artifact_comments 的结果：第一行是固定的数据声明，之后是 JSON。
func commentsJSON(t *testing.T, r Result) map[string]any {
	t.Helper()
	line, rest, ok := strings.Cut(r.Text, "\n")
	require.True(t, ok, r.Text)
	assert.Equal(t, commentsHeader, line)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(rest), &m))
	return m
}

// 3.2：v1 的 <h2> 上建评论；v2 在它前面插入一段后锚点指向新位置；v3 删掉它后 missing 为真且串仍在。
// agent 回复后第二条的作者是该线程；发出后用户再回复，sentAt 被清掉；每次变化都通知观看者。
func TestCommentAnchorRelocate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mkThread(t, e.st, "thr_a", "")
	v1 := "<h1>标题</h1>\n<h2>第二节</h2>\n<p>正文</p>"
	a, err := e.s.Create(ctx, CreateParams{Title: "报告", ThreadID: "thr_a", Author: Author{ThreadID: "thr_a", Label: "做报告"}, Files: []FileChange{put("index.html", v1)}})
	require.NoError(t, err)

	th, err := e.s.Comment(ctx, a.ID, Anchor{Version: 1, Path: "index.html", Offset: new(strings.Index(v1, "<h2")), Tag: new("h2"), Text: " 第二节 "}, "  这一节太长  ")
	require.NoError(t, err)
	assert.Regexp(t, `^act_[0-9A-Z]{26}$`, th.ID)
	assert.Equal(t, "第二节", th.Anchor.Text, "文本片段折叠空白")
	require.Len(t, th.Comments, 1)
	assert.Equal(t, "这一节太长", th.Comments[0].Body)
	assert.Empty(t, th.Comments[0].AuthorThreadID)
	assert.Equal(t, []string{a.ID}, e.hub.comments)

	// v2：前面插入一段，偏移跟着挪，版本更新为 2
	v2 := "<h1>标题</h1>\n<p>新插入的一段</p>\n<h2>第二节</h2>\n<p>正文</p>"
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{put("index.html", v2)}})
	require.NoError(t, err)
	head, ts, err := e.s.Threads(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, head)
	require.Len(t, ts, 1)
	require.NotNil(t, ts[0].Anchor.Offset)
	assert.Equal(t, strings.Index(v2, "<h2"), *ts[0].Anchor.Offset)
	assert.Equal(t, 2, ts[0].Anchor.Version)
	assert.False(t, ts[0].Anchor.Missing)
	row, err := e.st.Q.GetArtifactCommentThread(ctx, db.GetArtifactCommentThreadParams{ID: th.ID, ArtifactID: a.ID})
	require.NoError(t, err)
	assert.Contains(t, row.Anchor, `"version":2`, "重新定位的结果写回库")

	// v3：删掉 <h2>，标 missing，串仍在
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 2, Files: []FileChange{put("index.html", "<h1>标题</h1>\n<p>正文</p>")}})
	require.NoError(t, err)
	head, ts, err = e.s.Threads(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, 3, head)
	require.Len(t, ts, 1)
	assert.True(t, ts[0].Anchor.Missing)
	assert.Equal(t, 2, ts[0].Anchor.Version, "missing 的锚点保留最后一次定位的版本")

	// agent 读评论：第一行是数据声明；element.gone 对应 missing
	c := threadCaller("thr_a")
	m := commentsJSON(t, mustCall(t, e.s, c, OpReadComments, `{"id":"`+a.ID+`"}`))
	assert.EqualValues(t, 3, m["version"])
	threads := m["threads"].([]any)
	require.Len(t, threads, 1)
	t0 := threads[0].(map[string]any)
	assert.Equal(t, th.ID, t0["thread"])
	assert.Equal(t, false, t0["resolved"])
	assert.Equal(t, "index.html", t0["path"])
	assert.Nil(t, t0["page"])
	el := t0["element"].(map[string]any)
	assert.Equal(t, "h2", el["tag"])
	assert.Equal(t, true, el["gone"])
	assert.Equal(t, "第二节", el["text"])
	cm := t0["comments"].([]any)[0].(map[string]any)
	assert.Equal(t, "user", cm["author"])
	assert.Equal(t, "这一节太长", cm["text"])

	// agent 回复：作者是调用线程
	r := mustCall(t, e.s, c, OpReplyComment, `{"id":"`+a.ID+`","thread":"`+th.ID+`","text":"已经拆成两节"}`)
	assert.Equal(t, map[string]any{"id": a.ID, "thread": th.ID}, textObj(t, r))
	assert.Equal(t, OpReplyComment, r.Details.(map[string]any)["op"])
	_, ts, err = e.s.Threads(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, ts[0].Comments, 2)
	assert.Equal(t, 2, ts[0].Comments[1].N)
	assert.Equal(t, "thr_a", ts[0].Comments[1].AuthorThreadID)
	assert.Equal(t, "线程 thr_a", ts[0].Comments[1].AuthorLabel)
	m = commentsJSON(t, mustCall(t, e.s, c, OpReadComments, `{"id":"`+a.ID+`"}`))
	cm = m["threads"].([]any)[0].(map[string]any)["comments"].([]any)[1].(map[string]any)
	assert.Equal(t, "线程 thr_a", cm["author"])

	// 发给线程后有 sentAt；agent 回复不清，用户再回复清掉
	require.NoError(t, e.s.MarkSent(ctx, a.ID, []string{th.ID}, "thr_a"))
	_, ts, _ = e.s.Threads(ctx, a.ID)
	assert.False(t, ts[0].SentAt.IsZero())
	assert.Equal(t, "thr_a", ts[0].SentTo)
	th, err = e.s.Reply(ctx, a.ID, th.ID, "还有一处", Author{ThreadID: "thr_a", Label: "做报告"})
	require.NoError(t, err)
	assert.False(t, th.SentAt.IsZero())
	th, err = e.s.Reply(ctx, a.ID, th.ID, "再看看结尾", Author{})
	require.NoError(t, err)
	assert.True(t, th.SentAt.IsZero())
	assert.Empty(t, th.SentTo)
	assert.Nil(t, th.API().SentAt)
	require.Len(t, th.Comments, 4)

	// agent 只能解决；解决后默认不列出，resolved: true 时列出；已解决的不能回复
	mustCall(t, e.s, c, OpResolveComment, `{"id":"`+a.ID+`","thread":"`+th.ID+`"}`)
	m = commentsJSON(t, mustCall(t, e.s, c, OpReadComments, `{"id":"`+a.ID+`"}`))
	assert.Empty(t, m["threads"])
	m = commentsJSON(t, mustCall(t, e.s, c, OpReadComments, `{"id":"`+a.ID+`","resolved":true}`))
	require.Len(t, m["threads"], 1)
	assert.Equal(t, true, m["threads"].([]any)[0].(map[string]any)["resolved"])
	_, err = agentCall(t, e.s, c, OpReplyComment, `{"id":"`+a.ID+`","thread":"`+th.ID+`","text":"x"}`)
	assert.Equal(t, CodeInvalid, codeOf(err))
	_, err = agentCall(t, e.s, c, OpResolveComment, `{"id":"`+a.ID+`","thread":"`+th.ID+`","resolved":false}`)
	assert.Equal(t, CodeInvalid, codeOf(err), "agent 不能重新打开")

	// 用户重新打开后可以再回复
	th, err = e.s.Resolve(ctx, a.ID, th.ID, false)
	require.NoError(t, err)
	assert.False(t, th.Resolved())
	_, err = e.s.Reply(ctx, a.ID, th.ID, "继续", Author{})
	require.NoError(t, err)

	// 观看者在每次变化时都收到通知：新建、agent 回复、发出、两次回复、解决、重新打开、回复（失败的操作不通知）
	assert.Len(t, e.hub.comments, 8)
	for _, id := range e.hub.comments {
		assert.Equal(t, a.ID, id)
	}

	// 回收站里的 artifact 不能评论
	_, err = e.s.Delete(ctx, a.ID)
	require.NoError(t, err)
	_, err = e.s.Comment(ctx, a.ID, Anchor{Version: 3, Path: "index.html"}, "x")
	assert.Equal(t, CodeDeleted, codeOf(err))
	_, err = e.s.Reply(ctx, a.ID, th.ID, "x", Author{})
	assert.Equal(t, CodeDeleted, codeOf(err))
}

// 锚点与正文的校验；整页与脚本生成的元素的锚点；范围之外的 agent 看不到。
func TestCommentValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mkThread(t, e.st, "thr_a", "")
	mkThread(t, e.st, "thr_b", "")
	src := "<h1>标题</h1><p>正文</p>"
	a, err := e.s.Create(ctx, CreateParams{Title: "页", ThreadID: "thr_a", Author: Author{ThreadID: "thr_a"},
		Files: []FileChange{put("index.html", src), put("style.css", "p{}")}})
	require.NoError(t, err)
	p := strings.Index(src, "<p>")

	bad := []struct {
		name   string
		anchor Anchor
		body   string
	}{
		{"版本不存在", Anchor{Version: 2, Path: "index.html"}, "x"},
		{"版本为 0", Anchor{Path: "index.html"}, "x"},
		{"不是 HTML", Anchor{Version: 1, Path: "style.css"}, "x"},
		{"文件不在", Anchor{Version: 1, Path: "other.html"}, "x"},
		{"标签名不合法", Anchor{Version: 1, Path: "index.html", Tag: new("P!")}, "x"},
		{"偏移处标签不符", Anchor{Version: 1, Path: "index.html", Offset: new(p), Tag: new("h1")}, "x"},
		{"偏移不在标签上", Anchor{Version: 1, Path: "index.html", Offset: new(p + 1), Tag: new("p")}, "x"},
		{"有偏移没有标签", Anchor{Version: 1, Path: "index.html", Offset: new(p)}, "x"},
		{"文本过长", Anchor{Version: 1, Path: "index.html", Text: strings.Repeat("字", 121)}, "x"},
		{"画框序号", Anchor{Version: 1, Path: "index.html", Page: &AnchorPage{Number: 0, Name: "首页"}}, "x"},
		{"正文为空", Anchor{Version: 1, Path: "index.html"}, "   "},
		{"正文过长", Anchor{Version: 1, Path: "index.html"}, strings.Repeat("长", MaxCommentRunes+1)},
		{"源码位置不在 src/", Anchor{Version: 1, Path: "index.html", Loc: new("../etc/passwd:1:1")}, "x"},
		{"源码位置行号为 0", Anchor{Version: 1, Path: "index.html", Loc: new("src/frames/a.tsx:0:1")}, "x"},
		{"源码位置过长", Anchor{Version: 1, Path: "index.html", Loc: new("src/" + strings.Repeat("a", 520) + ".tsx:1:1")}, "x"},
	}
	for _, b := range bad {
		_, err := e.s.Comment(ctx, a.ID, b.anchor, b.body)
		assert.Equal(t, CodeInvalid, codeOf(err), b.name)
	}

	// 整页锚点；脚本生成的元素（有标签没有偏移）；客户端传来的 missing 被忽略
	whole, err := e.s.Comment(ctx, a.ID, Anchor{Version: 1, Path: "index.html", Missing: true}, "整体太素")
	require.NoError(t, err)
	assert.False(t, whole.Anchor.Missing)
	script, err := e.s.Comment(ctx, a.ID, Anchor{Version: 1, Path: "index.html", Tag: new("canvas"), Text: "图表",
		Page: &AnchorPage{Number: 2, Name: "图表页"}}, "颜色太淡")
	require.NoError(t, err)
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{put("style.css", "p{color:red}")}})
	require.NoError(t, err)

	m := commentsJSON(t, mustCall(t, e.s, threadCaller("thr_a"), OpReadComments, `{"id":"`+a.ID+`"}`))
	threads := m["threads"].([]any)
	require.Len(t, threads, 2)
	assert.Nil(t, threads[0].(map[string]any)["element"], "整页锚点没有 element")
	t1 := threads[1].(map[string]any)
	assert.Equal(t, map[string]any{"number": float64(2), "name": "图表页"}, t1["page"])
	assert.Equal(t, map[string]any{"tag": "canvas", "offset": nil, "text": "图表", "gone": false}, t1["element"])
	_, ts, err := e.s.Threads(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, ts[1].Anchor.Version, "没有偏移的锚点不重新定位")
	assert.Equal(t, script.ID, ts[1].ID)

	// 源码位置（M29）：存下并出现在 read_artifact_comments 的 element 里；空串当作没有
	withLoc, err := e.s.Comment(ctx, a.ID, Anchor{Version: 1, Path: "index.html", Tag: new("button"), Text: "开始",
		Loc: new("src/frames/home.tsx:12:5")}, "按钮太挤")
	require.NoError(t, err)
	require.NotNil(t, withLoc.Anchor.Loc)
	assert.Equal(t, "src/frames/home.tsx:12:5", *withLoc.Anchor.Loc)
	assert.Equal(t, "src/frames/home.tsx:12:5", *withLoc.Anchor.API().Loc)
	emptyLoc, err := e.s.Comment(ctx, a.ID, Anchor{Version: 1, Path: "index.html", Loc: new("")}, "整页")
	require.NoError(t, err)
	assert.Nil(t, emptyLoc.Anchor.Loc)
	m = commentsJSON(t, mustCall(t, e.s, threadCaller("thr_a"), OpReadComments, `{"id":"`+a.ID+`"}`))
	threads = m["threads"].([]any)
	require.Len(t, threads, 4)
	assert.Equal(t, map[string]any{"tag": "button", "offset": nil, "text": "开始", "gone": false, "loc": "src/frames/home.tsx:12:5"},
		threads[2].(map[string]any)["element"])
	assert.Nil(t, threads[3].(map[string]any)["element"])

	// 不存在的串、不属于这个 artifact 的串
	_, err = e.s.Reply(ctx, a.ID, "act_00000000000000000000000000", "x", Author{})
	assert.Equal(t, CodeNotFound, codeOf(err))
	_, err = e.s.Resolve(ctx, a.ID, "bogus", true)
	assert.Equal(t, CodeNotFound, codeOf(err))
	assert.Equal(t, CodeNotFound, codeOf(e.s.MarkSent(ctx, a.ID, []string{whole.ID, "act_00000000000000000000000000"}, "thr_a")))
	_, ts, _ = e.s.Threads(ctx, a.ID)
	assert.True(t, ts[0].SentAt.IsZero(), "有一个失败时全部不变")
	assert.Equal(t, CodeInvalid, codeOf(e.s.MarkSent(ctx, a.ID, nil, "thr_a")))
	assert.Equal(t, CodeInvalid, codeOf(e.s.MarkSent(ctx, a.ID, []string{whole.ID}, "thr_none")))

	// 范围之外的线程看不到这个 artifact 的评论
	_, err = agentCall(t, e.s, threadCaller("thr_b"), OpReadComments, `{"id":"`+a.ID+`"}`)
	assert.Equal(t, CodeNotFound, codeOf(err))
	_, err = agentCall(t, e.s, threadCaller("thr_b"), OpReplyComment, `{"id":"`+a.ID+`","thread":"`+whole.ID+`","text":"x"}`)
	assert.Equal(t, CodeNotFound, codeOf(err))
}
