package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// uploadFile 以原始字节调用 uploadAssistantFile。
func (e *threadsEnv) uploadFile(name, threadID string, body []byte) (int, []byte) {
	e.t.Helper()
	q := url.Values{"name": {name}}
	if threadID != "" {
		q.Set("threadId", threadID)
	}
	req, err := http.NewRequest("POST", e.srv.URL+"/api/v1/assistant/files?"+q.Encode(), bytes.NewReader(body))
	require.NoError(e.t, err)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (e *threadsEnv) listFiles(query string) []apigen.AssistantFile {
	e.t.Helper()
	code, body := e.do("GET", "/api/v1/assistant/files"+query, nil)
	require.Equal(e.t, 200, code, string(body))
	var list apigen.AssistantFileList
	require.NoError(e.t, json.Unmarshal(body, &list))
	return list.Files
}

func fileIDs(list []apigen.AssistantFile) []string {
	out := make([]string, len(list))
	for i, f := range list {
		out[i] = f.ID
	}
	return out
}

// 文件库 REST：上传、带文件新建助手对话（首条消息与绑定）、列出与过滤、改 scope、下载（含 Range）、删除。
func TestAssistantFilesREST(t *testing.T) {
	e, _ := newCoordEnv(t)

	// 不带请求体：行为不变，没有首批命令
	code, body := e.do("POST", "/api/v1/assistant/conversations", nil)
	require.Equal(t, 201, code, string(body))
	var asst apigen.ThreadEnvelope
	require.NoError(t, json.Unmarshal(body, &asst))
	row, err := e.st.Q.GetThread(t.Context(), asst.Thread.ID)
	require.NoError(t, err)
	assert.Empty(t, row.StartupFrames)

	// 上传：threadId 须是助手对话
	work := ids.Thread()
	require.NoError(t, e.st.Q.CreateThread(t.Context(), db.CreateThreadParams{Kind: "work", ID: work, TitleSource: "auto", State: "idle", SessionDir: "/s/" + work, CreatedAt: 1, UpdatedAt: 1}))
	code, body = e.uploadFile("a.txt", work, []byte("hi"))
	assert.Equal(t, 400, code, string(body))
	code, _ = e.uploadFile("a.txt", ids.Thread(), []byte("hi"))
	assert.Equal(t, 404, code)

	code, body = e.uploadFile("notes.txt", "", []byte("hello library"))
	require.Equal(t, 201, code, string(body))
	var f1 apigen.AssistantFile
	require.NoError(t, json.Unmarshal(body, &f1))
	assert.Equal(t, apigen.Conversation, f1.Scope)
	assert.Equal(t, apigen.AssistantFileSourceUpload, f1.Source)
	assert.Nil(t, f1.ThreadID)
	code, body = e.uploadFile("mine.txt", asst.Thread.ID, []byte("x"))
	require.Equal(t, 201, code, string(body))
	var f2 apigen.AssistantFile
	require.NoError(t, json.Unmarshal(body, &f2))
	require.NotNil(t, f2.ThreadID)
	assert.Equal(t, asst.Thread.ID, *f2.ThreadID)
	assert.Empty(t, e.listFiles(""), "未随消息发送的上传不列出")

	// 带 prompt + files 新建对话：文件绑定到新对话，首条消息 = 提示词 + 沙箱路径块
	code, body = e.do("POST", "/api/v1/assistant/conversations", map[string]any{
		"prompt": "总结一下", "files": []string{f1.ID}, "model": map[string]any{"provider": "p", "id": "m"}, "thinkingLevel": "low",
	})
	require.Equal(t, 201, code, string(body))
	var conv apigen.ThreadEnvelope
	require.NoError(t, json.Unmarshal(body, &conv))
	row, err = e.st.Q.GetThread(t.Context(), conv.Thread.ID)
	require.NoError(t, err)
	require.NotNil(t, row.Model)
	assert.Equal(t, "p/m", *row.Model)
	require.NotNil(t, row.ThinkingLevel)
	assert.Equal(t, "low", *row.ThinkingLevel)
	var frames []map[string]any
	require.NoError(t, json.Unmarshal(row.StartupFrames, &frames))
	require.Len(t, frames, 1)
	assert.Equal(t, "prompt", frames[0]["type"])
	assert.Equal(t, "总结一下\n\n<oryn-attachments>\nThe user attached these files. They are stored outside the repository; "+
		"read them when relevant and do not copy them into the repository unless asked.\n"+
		"- ~/library/threads/"+conv.Thread.ID+"/"+f1.ID+"/notes.txt (text/plain; charset=utf-8, 13 bytes)\n</oryn-attachments>", frames[0]["message"])

	list := e.listFiles("")
	require.Len(t, list, 1)
	require.NotNil(t, list[0].ThreadID)
	assert.Equal(t, conv.Thread.ID, *list[0].ThreadID, "绑定到新对话")
	assert.Equal(t, []string{f1.ID}, fileIDs(e.listFiles("?threadId="+conv.Thread.ID)))
	assert.Empty(t, e.listFiles("?threadId="+asst.Thread.ID), "别的对话看不到未共享的文件")

	// 别的对话的未共享文件、不存在的文件：建对话失败
	code, body = e.do("POST", "/api/v1/assistant/conversations", map[string]any{"prompt": "x", "files": []string{f2.ID}})
	assert.Equal(t, 400, code, string(body))
	code, body = e.do("POST", "/api/v1/assistant/conversations", map[string]any{"prompt": "x", "files": []string{"afl_00000000000000000000000000"}})
	assert.Equal(t, 404, code, string(body))

	// 共享后所有对话可见
	code, body = e.do("PATCH", "/api/v1/assistant/files/"+f1.ID, map[string]any{"scope": "shared"})
	require.Equal(t, 200, code, string(body))
	var patched apigen.AssistantFile
	require.NoError(t, json.Unmarshal(body, &patched))
	assert.Equal(t, apigen.Shared, patched.Scope)
	assert.Equal(t, []string{f1.ID}, fileIDs(e.listFiles("?threadId="+asst.Thread.ID)))
	assert.Empty(t, e.listFiles("?scope=conversation"))
	assert.Equal(t, []string{f1.ID}, fileIDs(e.listFiles("?scope=shared")))
	code, _ = e.do("PATCH", "/api/v1/assistant/files/"+f1.ID, map[string]any{"scope": "everyone"})
	assert.Equal(t, 400, code)
	code, _ = e.do("PATCH", "/api/v1/assistant/files/afl_00000000000000000000000000", map[string]any{"scope": "shared"})
	assert.Equal(t, 404, code)

	// 下载：响应头同附件；支持 Range
	resp, err := http.Get(e.srv.URL + "/api/v1/assistant/files/" + f1.ID + "/content")
	require.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "hello library", string(b))
	assert.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"))
	assert.Contains(t, resp.Header.Get("Content-Disposition"), "attachment")
	assert.Equal(t, "sandbox", resp.Header.Get("Content-Security-Policy"))
	assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	req, err := http.NewRequest("GET", e.srv.URL+"/api/v1/assistant/files/"+f1.ID+"/content", nil)
	require.NoError(t, err)
	req.Header.Set("Range", "bytes=0-4")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, 206, resp.StatusCode)
	assert.Equal(t, "hello", string(b))

	// 删除
	code, _ = e.do("DELETE", "/api/v1/assistant/files/"+f1.ID, nil)
	assert.Equal(t, 204, code)
	code, _ = e.do("DELETE", "/api/v1/assistant/files/"+f1.ID, nil)
	assert.Equal(t, 404, code)
	resp, err = http.Get(e.srv.URL + "/api/v1/assistant/files/" + f1.ID + "/content")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 404, resp.StatusCode)
	assert.Empty(t, e.listFiles(""))
}
