package api_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts"
)

// 评论 REST：新建（锚点校验）、列表（重新定位）、回复、标为已解决 / 重新打开、记为已发送（用户回复后清掉）。
func TestArtifactCommentsREST(t *testing.T) {
	e, svc := newArtifactsEnv(t)
	ctx := t.Context()
	creator := artifactThread(t, e.st, "做报告")
	v1 := "<h1>标题</h1><h2>第二节</h2>"
	a, err := svc.Create(ctx, artifacts.CreateParams{Title: "周报", ThreadID: creator, Author: artifacts.Author{ThreadID: creator, Label: "做报告"},
		Files: artFiles("index.html", v1)})
	require.NoError(t, err)
	base := "/api/v1/artifacts/" + a.ID + "/comments"
	h2 := strings.Index(v1, "<h2")

	// 偏移处不是该标签 → 400；正常新建 → 201
	code, body := e.do("POST", base, map[string]any{"anchor": map[string]any{"version": 1, "path": "index.html", "offset": h2, "tag": "h1", "text": "第二节"}, "body": "x"})
	assert.Equal(t, 400, code, string(body))
	code, body = e.do("POST", base, map[string]any{"anchor": map[string]any{"version": 1, "path": "index.html", "offset": h2, "tag": "h2", "text": "第二节"}, "body": "太长"})
	require.Equal(t, 201, code, string(body))
	th := decodeAs[apigen.ArtifactCommentThread](t, body)
	require.Len(t, th.Comments, 1)
	assert.Nil(t, th.Comments[0].AuthorThreadID)
	assert.Equal(t, "太长", th.Comments[0].Body)
	assert.Nil(t, th.ResolvedAt)

	// 新版本在前面插入内容后，列表里的锚点指向新位置
	v2 := "<h1>标题</h1><p>新的一段</p><h2>第二节</h2>"
	_, err = svc.Write(ctx, artifacts.WriteParams{ID: a.ID, BaseVersion: 1, Files: artFiles("index.html", v2)})
	require.NoError(t, err)
	code, body = e.do("GET", base, nil)
	require.Equal(t, 200, code, string(body))
	list := decodeAs[apigen.ArtifactCommentThreadList](t, body)
	assert.Equal(t, 2, list.Version)
	require.Len(t, list.Threads, 1)
	require.NotNil(t, list.Threads[0].Anchor.Offset)
	assert.Equal(t, strings.Index(v2, "<h2"), *list.Threads[0].Anchor.Offset)
	assert.Equal(t, 2, list.Threads[0].Anchor.Version)

	// agent 回复、发出、用户再回复：sentAt 被清掉
	_, err = svc.Reply(ctx, a.ID, th.ID, "已拆分", artifacts.Author{ThreadID: creator, Label: "做报告"})
	require.NoError(t, err)
	code, body = e.do("POST", base+"/sent", map[string]any{"threadIds": []string{th.ID}, "sentTo": creator})
	require.Equal(t, 204, code, string(body))
	code, body = e.do("GET", base, nil)
	require.Equal(t, 200, code, string(body))
	list = decodeAs[apigen.ArtifactCommentThreadList](t, body)
	require.NotNil(t, list.Threads[0].SentAt)
	assert.Equal(t, creator, *list.Threads[0].SentTo)
	require.Len(t, list.Threads[0].Comments, 2)
	assert.Equal(t, creator, *list.Threads[0].Comments[1].AuthorThreadID)
	assert.Equal(t, "做报告", list.Threads[0].Comments[1].AuthorLabel)

	code, body = e.do("POST", base+"/"+th.ID+"/replies", map[string]any{"body": "还有结尾"})
	require.Equal(t, 200, code, string(body))
	th = decodeAs[apigen.ArtifactCommentThread](t, body)
	assert.Nil(t, th.SentAt)
	assert.Nil(t, th.SentTo)
	require.Len(t, th.Comments, 3)
	assert.Equal(t, 3, th.Comments[2].N)

	// 已解决的不能回复；重新打开后可以
	code, body = e.do("PATCH", base+"/"+th.ID, map[string]any{"resolved": true})
	require.Equal(t, 200, code, string(body))
	assert.NotNil(t, decodeAs[apigen.ArtifactCommentThread](t, body).ResolvedAt)
	code, _ = e.do("POST", base+"/"+th.ID+"/replies", map[string]any{"body": "x"})
	assert.Equal(t, 400, code)
	code, body = e.do("PATCH", base+"/"+th.ID, map[string]any{"resolved": false})
	require.Equal(t, 200, code, string(body))
	assert.Nil(t, decodeAs[apigen.ArtifactCommentThread](t, body).ResolvedAt)

	// 不存在的串与 artifact → 404；空正文 → 400；sentTo 不是线程 → 400
	code, _ = e.do("POST", base+"/act_00000000000000000000000000/replies", map[string]any{"body": "x"})
	assert.Equal(t, 404, code)
	code, _ = e.do("GET", "/api/v1/artifacts/art_00000000000000000000000000/comments", nil)
	assert.Equal(t, 404, code)
	code, _ = e.do("POST", base+"/"+th.ID+"/replies", map[string]any{"body": "  "})
	assert.Equal(t, 400, code)
	code, _ = e.do("POST", base+"/sent", map[string]any{"threadIds": []string{th.ID}, "sentTo": "T-nope"})
	assert.Equal(t, 400, code)

	// 回收站里的不能评论 → 410
	_, err = svc.Delete(ctx, a.ID)
	require.NoError(t, err)
	code, _ = e.do("POST", base, map[string]any{"anchor": map[string]any{"version": 2, "path": "index.html", "text": ""}, "body": "x"})
	assert.Equal(t, 410, code)
}
