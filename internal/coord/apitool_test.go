package coord

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAPI 记录进程内 REST 请求并返回预设响应。
type fakeAPI struct {
	mu     sync.Mutex
	calls  []fakeAPICall
	status int
	ctype  string
	body   []byte
}

type fakeAPICall struct {
	thread, method, target, ctype string
	body                          []byte
}

func (f *fakeAPI) Do(_ context.Context, threadID, method, target, contentType string, body []byte, max int) (int, http.Header, []byte, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeAPICall{threadID, method, target, contentType, append([]byte(nil), body...)})
	status, ctype, out := f.status, f.ctype, f.body
	if status == 0 {
		status, ctype, out = 200, "application/json", []byte(`{"ok":true}`)
	}
	total := len(out)
	if len(out) > max {
		out = out[:max]
	}
	return status, http.Header{"Content-Type": {ctype}}, out, total
}

func (f *fakeAPI) last(t *testing.T) fakeAPICall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.NotEmpty(t, f.calls, "fake API 未被调用")
	return f.calls[len(f.calls)-1]
}

func (f *fakeAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func orynArgs(op string, args any) string {
	b, _ := json.Marshal(map[string]any{"op": op, "args": args})
	return string(b)
}

func TestOrynWriteAudited(t *testing.T) {
	e := newEnv(t, true)
	fake := &fakeAPI{}
	e.c.SetAPI(fake)
	conv := e.assistant()

	res := e.call(conv, ToolOryn, `{"op":"updateProject","args":{"id":"prj_1","body":{"name":"N"}}}`, true)
	require.False(t, res.IsError, res.Text)
	got := fake.last(t)
	assert.Equal(t, "PATCH", got.method)
	assert.Equal(t, "/api/v1/projects/prj_1", got.target)
	assert.Equal(t, "application/json", got.ctype)
	assert.JSONEq(t, `{"name":"N"}`, string(got.body))
	assert.Equal(t, conv.ID, got.thread)
	assert.Contains(t, res.Text, "<untrusted-thread-content>")

	acts := e.actions()
	require.Len(t, acts, 1)
	assert.Equal(t, actionAPICall, acts[0].Action)
	assert.True(t, strings.HasPrefix(acts[0].Summary, "updateProject /api/v1/projects/prj_1"), acts[0].Summary)
	assert.True(t, acts[0].Ok)

	// 拼错的请求体字段被拒绝，不发请求
	n := fake.count()
	res = e.call(conv, ToolOryn, `{"op":"updateProject","args":{"id":"prj_1","body":{"nmae":"N"}}}`, true)
	require.True(t, res.IsError)
	assert.True(t, strings.HasPrefix(res.Text, `invalid_request: unknown body field "nmae"`), res.Text)
	assert.Equal(t, n, fake.count())

	// 未知参数
	res = e.call(conv, ToolOryn, `{"op":"getProject","args":{"id":"prj_1","x":1}}`, true)
	require.True(t, res.IsError)
	assert.Contains(t, res.Text, `unknown argument "x"`)

	// read：query 参数展开，不审计
	res = e.call(conv, ToolOryn, `{"op":"listThreads","args":{"archived":true,"q":"a b","state":null}}`, true)
	require.False(t, res.IsError, res.Text)
	assert.Contains(t, fake.last(t).target, "archived=true")
	assert.Contains(t, fake.last(t).target, "q=a+b")
	assert.NotContains(t, fake.last(t).target, "state=")
	assert.Len(t, e.actions(), 1)
}

func TestOrynConditionalConfirm(t *testing.T) {
	e := newEnv(t, true)
	fake := &fakeAPI{}
	e.c.SetAPI(fake)
	conv := e.assistant()

	res := e.call(conv, ToolOryn, `{"op":"publishArtifact","args":{"id":"a1","body":{"access":"private"}}}`, true)
	require.False(t, res.IsError, res.Text)
	assert.Equal(t, 1, fake.count())

	// access public 改走确认卡：hold 环境里没有 agent 挂不上卡，但请求不会发出
	res = e.call(conv, ToolOryn, `{"op":"publishArtifact","args":{"id":"a1","body":{"access":"public"}}}`, true)
	assert.Equal(t, 1, fake.count(), res.Text)
}

func TestOrynGuardsAndDenials(t *testing.T) {
	e := newEnv(t, true)
	fake := &fakeAPI{}
	e.c.SetAPI(fake)
	conv := e.assistant()

	res := e.call(conv, ToolOryn, orynArgs("deleteThread", map[string]any{"id": conv.ID}), true)
	require.True(t, res.IsError)
	assert.True(t, strings.HasPrefix(res.Text, CodeInvalidTarget+":"), res.Text)

	res = e.call(conv, ToolOryn, orynArgs("pauseOrb", map[string]any{"id": e.coordOrb, "body": map[string]any{"mode": "freeze"}}), true)
	require.True(t, res.IsError)
	assert.True(t, strings.HasPrefix(res.Text, CodeInvalidTarget+":"), res.Text)

	res = e.call(conv, ToolOryn, `{"op":"createProvider","args":{"body":{"name":"x"}}}`, true)
	require.True(t, res.IsError)
	assert.True(t, strings.HasPrefix(res.Text, CodeNotAvailable+":"), res.Text)

	res = e.call(conv, ToolOryn, `{"op":"getAgentConfigFile","args":{"path":"mcp.json"}}`, true)
	require.True(t, res.IsError)
	assert.True(t, strings.HasPrefix(res.Text, CodeNotAvailable+":"), res.Text)

	res = e.call(conv, ToolOryn, `{"op":"nope"}`, true)
	require.True(t, res.IsError)
	assert.True(t, strings.HasPrefix(res.Text, CodeUnknownOp+":"), res.Text)
	assert.Zero(t, fake.count())

	// 守卫失败的写操作也进审计
	acts := e.actions()
	require.Len(t, acts, 2)
	assert.False(t, acts[0].Ok)

	sp := e.space("S", "")
	res = e.call(e.spaceChat(sp), ToolOryn, `{"op":"listSpaces"}`, true)
	require.True(t, res.IsError)
	assert.True(t, strings.HasPrefix(res.Text, "unknown_tool:"), res.Text)
}

func TestOrynResults(t *testing.T) {
	e := newEnv(t, true)
	fake := &fakeAPI{}
	e.c.SetAPI(fake)
	conv := e.assistant()

	fake.status, fake.ctype, fake.body = 409, "application/json", []byte(`{"code":"secrets_found","message":"x","scan":{"findings":[{"rule":"aws"},{"rule":"gh"}]}}`)
	res := e.call(conv, ToolOryn, orynArgs("revokeShare", map[string]any{"id": "shr_1"}), true)
	require.True(t, res.IsError)
	assert.Contains(t, res.Text, "2 secret-like string(s)")
	assert.NotContains(t, res.Text, "findings")
	assert.NotContains(t, res.Text, "aws")

	fake.status, fake.body = 404, []byte(`{"code":"not_found","message":"no such share"}`)
	res = e.call(conv, ToolOryn, orynArgs("getSpace", map[string]any{"id": "sp_1"}), true)
	assert.Equal(t, "not_found: no such share", res.Text)

	big := `{"items":"` + strings.Repeat("a", 100<<10) + `"}`
	fake.status, fake.body = 200, []byte(big)
	res = e.call(conv, ToolOryn, `{"op":"listSpaces"}`, true)
	require.False(t, res.IsError)
	assert.Contains(t, res.Text, "[truncated: ")
	assert.Less(t, len(res.Text), apiResultCap+1024)
	assert.Nil(t, res.Details)

	fake.status, fake.body = 204, nil
	res = e.call(conv, ToolOryn, orynArgs("startBox", map[string]any{"box": "b1"}), true)
	require.False(t, res.IsError, res.Text)
	assert.JSONEq(t, `{"ok":true,"status":204}`, res.Text)

	fake.status, fake.ctype, fake.body = 200, "application/octet-stream", []byte{0xff, 0xfe, 0x00, 0x01}
	res = e.call(conv, ToolOryn, `{"op":"listSpaces"}`, true)
	require.True(t, res.IsError)
	assert.True(t, strings.HasPrefix(res.Text, CodeBinaryResult+":"), res.Text)
}

// confirm 类：挂卡不执行；卡片正文打码；批准后执行并以自动消息回到对话。
func TestOrynConfirmFlow(t *testing.T) {
	ce := newConfirmEnv(t)
	fake := &fakeAPI{}
	ce.c.SetAPI(fake)
	conv := ce.assistant()

	res := ce.call(conv, ToolOryn, `{"op":"putSecret","args":{"name":"A/B","body":{"value":"s3cret","mode":"env"}}}`, true)
	require.False(t, res.IsError, res.Text)
	var v map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.Text), &v))
	assert.Equal(t, "awaiting_owner", v["status"])
	assert.Zero(t, fake.count())
	id := v["confirmationId"].(string)

	row, err := ce.st.Q.GetAssistantConfirmation(ce.ctx, id)
	require.NoError(t, err)
	assert.Equal(t, actionAPICall, row.Action)
	assert.Equal(t, "putSecret /api/v1/secrets/A/B", row.Target)
	assert.Contains(t, row.Summary, "***")
	assert.NotContains(t, row.Summary, "s3cret")
	assert.Contains(t, row.Params, "Allow the assistant to run putSecret on A/B?")

	ce.c.UIAnswered(ce.ctx, conv.ID, id, json.RawMessage(`{"confirmed":true}`))
	got := fake.last(t)
	assert.Equal(t, "PUT", got.method)
	assert.Equal(t, "/api/v1/secrets/A%2FB", got.target)
	assert.JSONEq(t, `{"value":"s3cret","mode":"env"}`, string(got.body))
	assert.True(t, strings.HasPrefix(ce.result(), "[Oryn confirmation · automatic] api_call putSecret /api/v1/secrets/A/B: approved and done."))

	acts := ce.actions()
	require.Len(t, acts, 1)
	assert.Equal(t, actionAPICall, acts[0].Action)
	assert.Equal(t, "owner", acts[0].Origin)
	assert.NotContains(t, acts[0].Summary, "s3cret")
}
