package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// fakeAPI 记录收到的请求，按 "方法 路径" 分派到 routes。
type fakeAPI struct {
	t      *testing.T
	mu     sync.Mutex
	reqs   []recordedReq
	routes map[string]func(w http.ResponseWriter, body []byte)
	srv    *httptest.Server
}

type recordedReq struct {
	Method, Path, RawQuery string
	Body                   []byte
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{t: t, routes: map[string]func(http.ResponseWriter, []byte){}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		assert.Equal(t, "Bearer odt_test", r.Header.Get("Authorization"))
		f.mu.Lock()
		f.reqs = append(f.reqs, recordedReq{r.Method, r.URL.Path, r.URL.RawQuery, b})
		h := f.routes[r.Method+" "+r.URL.Path]
		f.mu.Unlock()
		if h == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"not_found","message":"no route"}`))
			return
		}
		h(w, b)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) client() *apiClient {
	return &apiClient{base: f.srv.URL, token: "odt_test", http: f.srv.Client()}
}

func (f *fakeAPI) json(route string, status int, v any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[route] = func(w http.ResponseWriter, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
}

func (f *fakeAPI) requests() []recordedReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedReq(nil), f.reqs...)
}

func sampleThread(id string) apigen.Thread {
	return apigen.Thread{ID: id, Title: "t", State: apigen.ThreadStateIdle, Labels: []string{}, TitleSource: "auto"}
}

func TestThreadNewRequestBody(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST /api/v1/threads", 201, apigen.ThreadEnvelope{Thread: sampleThread("T-1")})
	f.json("GET /api/v1/projects", 200, apigen.ProjectList{Projects: []apigen.Project{{ID: "prj_A", Name: "demo"}}})
	ctx := context.Background()

	th, err := threadNew(ctx, f.client(), "hi", threadNewOptions{orb: "orb-x", title: "T", labels: []string{"a", "b"}, project: "demo"})
	require.NoError(t, err)
	assert.Equal(t, "T-1", th.ID)
	reqs := f.requests()
	require.Len(t, reqs, 2)
	assert.JSONEq(t, `{"prompt":"hi","title":"T","labels":["a","b"],"projectId":"prj_A","executor":{"kind":"orb","orbId":"orb-x"}}`, string(reqs[1].Body))

	_, err = threadNew(ctx, f.client(), "hi", threadNewOptions{provider: "local", orbType: "small"})
	require.NoError(t, err)
	reqs = f.requests()
	assert.JSONEq(t, `{"prompt":"hi","executor":{"kind":"new_orb","provider":"local","type":"small"}}`, string(reqs[len(reqs)-1].Body))

	_, err = threadNew(ctx, f.client(), "hi", threadNewOptions{orb: "orb-x", provider: "local"})
	assert.ErrorContains(t, err, "不能同时使用")
	_, err = threadNew(ctx, f.client(), "hi", threadNewOptions{project: "demo", noProject: true})
	assert.Error(t, err)
	assert.Len(t, f.requests(), 3, "参数冲突时不发请求")
}

func TestThreadListQuery(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /api/v1/threads", 200, apigen.ThreadList{Threads: []apigen.Thread{sampleThread("T-1")}})
	f.json("GET /api/v1/projects", 200, apigen.ProjectList{Projects: []apigen.Project{{ID: "prj_A", Name: "demo"}}})
	ctx := context.Background()

	list, err := threadList(ctx, f.client(), threadListOptions{})
	require.NoError(t, err)
	assert.Len(t, list, 1)
	_, err = threadList(ctx, f.client(), threadListOptions{archived: true, state: "idle", project: "demo", query: "fix bug"})
	require.NoError(t, err)
	_, err = threadList(ctx, f.client(), threadListOptions{project: "none"})
	require.NoError(t, err)
	_, err = threadList(ctx, f.client(), threadListOptions{state: "bogus"})
	assert.ErrorContains(t, err, "未知状态")

	var queries []string
	for _, r := range f.requests() {
		if r.Path == "/api/v1/threads" {
			queries = append(queries, r.RawQuery)
		}
	}
	assert.Equal(t, []string{"", "archived=true&project=prj_A&q=fix+bug&state=idle", "project=none"}, queries)
}

func TestThreadArchive(t *testing.T) {
	f := newFakeAPI(t)
	for _, id := range []string{"T-1", "T-2"} {
		f.json("PATCH /api/v1/threads/"+id, 200, sampleThread(id))
	}
	var errb bytes.Buffer
	out, err := threadArchive(context.Background(), f.client(), []string{"T-1", "T-2"}, false, &errb)
	require.NoError(t, err)
	assert.Len(t, out, 2)
	assert.Equal(t, "已归档 T-1\n已归档 T-2\n", errb.String())
	errb.Reset()
	_, err = threadArchive(context.Background(), f.client(), []string{"T-1", "T-404"}, true, &errb)
	assert.ErrorContains(t, err, "T-404")
	assert.Equal(t, "已取消归档 T-1\n", errb.String())

	reqs := f.requests()
	require.Len(t, reqs, 4)
	assert.Equal(t, http.MethodPatch, reqs[0].Method)
	assert.JSONEq(t, `{"archived":true}`, string(reqs[0].Body))
	assert.JSONEq(t, `{"archived":false}`, string(reqs[2].Body))
}

func TestProviderRm(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /api/v1/providers", 200, apigen.ProviderList{Providers: []apigen.Provider{{ID: "prov_1", Name: "local"}, {ID: "prov_2", Name: "tmp"}}})
	f.mu.Lock()
	f.routes["DELETE /api/v1/providers/prov_2"] = func(w http.ResponseWriter, _ []byte) { w.WriteHeader(http.StatusNoContent) }
	f.mu.Unlock()
	var errb bytes.Buffer
	require.NoError(t, providerRm(context.Background(), f.client(), "tmp", &errb))
	assert.Equal(t, "已删除 tmp\n", errb.String())
	require.NoError(t, providerRm(context.Background(), f.client(), "prov_2", &errb), "也接受 id")
	assert.ErrorContains(t, providerRm(context.Background(), f.client(), "nope", &errb), "不存在")
	var deletes int
	for _, r := range f.requests() {
		if r.Method == http.MethodDelete {
			deletes++
			assert.Equal(t, "/api/v1/providers/prov_2", r.Path)
		}
	}
	assert.Equal(t, 2, deletes)
}

func TestNormalizeRepoURL(t *testing.T) {
	want := "github.com/Lynricsy/Oryn"
	for _, s := range []string{
		"https://github.com/Lynricsy/Oryn.git", "git@github.com:Lynricsy/Oryn.git", "ssh://git@GitHub.com/Lynricsy/Oryn",
		"https://user:tok@github.com/Lynricsy/Oryn/", "http://127.0.0.1:7811/git/github.com/Lynricsy/Oryn.git",
	} {
		got := normalizeRepoURL(s)
		if strings.HasPrefix(s, "http://127.0.0.1") {
			assert.NotEqual(t, want, got, s)
			continue
		}
		assert.Equal(t, want, got, s)
	}
}

func TestThreadNewBrowserProfile(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST /api/v1/threads", 201, apigen.ThreadEnvelope{Thread: sampleThread("T-1")})
	f.json("GET /api/v1/browser-profiles", 200, apigen.BrowserProfileList{Profiles: []apigen.BrowserProfile{{ID: "bpf_A", Name: "工作"}}})
	ctx := context.Background()
	lastBody := func() string {
		reqs := f.requests()
		return string(reqs[len(reqs)-1].Body)
	}

	_, err := threadNew(ctx, f.client(), "hi", threadNewOptions{browserProfile: "工作"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"prompt":"hi","executor":{"kind":"new_orb"},"browserProfileId":"bpf_A"}`, lastBody())
	_, err = threadNew(ctx, f.client(), "hi", threadNewOptions{browserProfile: "bpf_A", readonly: true})
	require.NoError(t, err)
	assert.JSONEq(t, `{"prompt":"hi","executor":{"kind":"new_orb"},"browserProfileId":"bpf_A","profileMode":"readonly"}`, lastBody())
	_, err = threadNew(ctx, f.client(), "hi", threadNewOptions{browserProfile: "工作", takeover: true})
	require.NoError(t, err)
	assert.JSONEq(t, `{"prompt":"hi","executor":{"kind":"new_orb"},"browserProfileId":"bpf_A","profileMode":"takeover"}`, lastBody())
	_, err = threadNew(ctx, f.client(), "hi", threadNewOptions{browserProfile: "none"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"prompt":"hi","executor":{"kind":"new_orb"},"browserProfileId":""}`, lastBody())

	n := len(f.requests())
	_, err = threadNew(ctx, f.client(), "hi", threadNewOptions{browserProfile: "nope"})
	assert.ErrorContains(t, err, "不存在")
	_, err = threadNew(ctx, f.client(), "hi", threadNewOptions{browserProfile: "工作", readonly: true, takeover: true})
	assert.ErrorContains(t, err, "不能同时使用")
	_, err = threadNew(ctx, f.client(), "hi", threadNewOptions{orb: "orb-x", browserProfile: "工作"})
	assert.ErrorContains(t, err, "--orb")
	assert.Len(t, f.requests(), n+1, "只有解析名字发了请求")

	// 409 profile_in_use：提示 --readonly / --takeover
	f.json("POST /api/v1/threads", 409, apigen.ProfileInUseConflict{Code: apigen.ProfileInUseConflictCodeProfileInUse, Message: "Profile「工作」正在线程「T」里使用"})
	_, err = threadNew(ctx, f.client(), "hi", threadNewOptions{browserProfile: "工作"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "正在线程「T」里使用")
	assert.Contains(t, err.Error(), "--takeover")
}

// wsClient 是指向假 WS 服务器的客户端。
func wsClient(ws *fakeWS) *apiClient {
	return &apiClient{base: ws.srv.URL, token: "odt_test", http: ws.srv.Client()}
}

// goErr 在后台执行 fn，返回接收其错误的通道。
func goErr(fn func() error) chan error {
	ch := make(chan error, 1)
	go func() { ch <- fn() }()
	return ch
}

func recvErr(t *testing.T, ch chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("命令没有返回")
		return nil
	}
}

// answerCmd 在新连接上读一条 cmd，断言其 threadId 与 frame，回以 reply（只需填 type / response / code 等，id 自动带上）。
func answerCmd(t *testing.T, ws *fakeWS, thread string, wantFrame map[string]any, reply map[string]any) {
	t.Helper()
	conn := ws.accept()
	m := conn.next()
	assert.Equal(t, "cmd", m["type"])
	assert.Equal(t, thread, m["threadId"])
	assert.Equal(t, wantFrame, m["frame"])
	reply["id"], reply["threadId"] = m["id"], thread
	conn.send(reply)
}

func TestThreadRPC(t *testing.T) {
	ws := newFakeWS(t)
	ctx := context.Background()

	// 成功：frame 原样下发，应答缩进打印
	var out bytes.Buffer
	done := goErr(func() error {
		return threadRPC(ctx, wsClient(ws), "T-1", `{"type":"set_thinking_level", "level":"high"}`, &out)
	})
	answerCmd(t, ws, "T-1", map[string]any{"type": "set_thinking_level", "level": "high"},
		map[string]any{"type": "res", "response": map[string]any{"type": "response", "command": "get_state", "success": true, "data": map[string]any{"sessionId": "s1"}}})
	require.NoError(t, recvErr(t, done))
	assert.Equal(t, "{\n  \"command\": \"get_state\",\n  \"data\": {\n    \"sessionId\": \"s1\"\n  },\n  \"success\": true,\n  \"type\": \"response\"\n}\n", out.String())

	// success: false：应答照常打印，返回错误（main 打到 stderr、退出码 1）
	out.Reset()
	done = goErr(func() error { return threadRPC(ctx, wsClient(ws), "T-1", `{"type":"compact"}`, &out) })
	answerCmd(t, ws, "T-1", map[string]any{"type": "compact"},
		map[string]any{"type": "res", "response": map[string]any{"type": "response", "command": "compact", "success": false, "error": "nothing to compact"}})
	err := recvErr(t, done)
	require.EqualError(t, err, "agent 拒绝了命令: nothing to compact")
	var ec *exitCodeError
	assert.False(t, errors.As(err, &ec), "agent 拒绝走默认退出码 1")
	assert.Contains(t, out.String(), `"success": false`)

	// 托管命令：控制面以 WS error 拒绝
	done = goErr(func() error { return threadRPC(ctx, wsClient(ws), "T-1", `{"type":"new_session"}`, &out) })
	answerCmd(t, ws, "T-1", map[string]any{"type": "new_session"},
		map[string]any{"type": "error", "code": "managed_by_oryn", "message": "new_session is managed by Oryn"})
	assert.ErrorContains(t, recvErr(t, done), "managed_by_oryn")

	// 非法帧：不发任何请求，退出码 2
	f := newFakeAPI(t)
	for _, raw := range []string{`get_state`, `[]`, `null`, `{}`, `{"type":1}`, `{"type":""}`, `{"type":"x"`} {
		err := threadRPC(ctx, f.client(), "T-1", raw, &out)
		require.EqualError(t, err, "帧必须是带 type 的 JSON 对象", raw)
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, 2, ec.code)
	}
	assert.Empty(t, f.requests(), "非法帧不发请求")
}

func TestThreadAbort(t *testing.T) {
	ws := newFakeWS(t)
	ctx := context.Background()

	done := goErr(func() error { return threadAbort(ctx, wsClient(ws), "T-1") })
	answerCmd(t, ws, "T-1", map[string]any{"type": "abort"},
		map[string]any{"type": "res", "response": map[string]any{"type": "response", "command": "abort", "success": true}})
	require.NoError(t, recvErr(t, done))

	done = goErr(func() error { return threadAbort(ctx, wsClient(ws), "T-1") })
	answerCmd(t, ws, "T-1", map[string]any{"type": "abort"},
		map[string]any{"type": "res", "response": map[string]any{"type": "response", "command": "abort", "success": false, "error": "not running"}})
	assert.EqualError(t, recvErr(t, done), "agent 拒绝了命令: not running")
}

func TestThreadFork(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST /api/v1/threads/T-1/fork", 201, apigen.ThreadEnvelope{Thread: sampleThread("T-2")})
	ctx := context.Background()

	// --env 缺省 share，不带 entryId
	env := threadForkCmd.Flags().Lookup("env").DefValue
	assert.Equal(t, "share", env)
	th, err := threadFork(ctx, f.client(), "T-1", "", env)
	require.NoError(t, err)
	assert.Equal(t, "T-2", th.ID)
	_, err = threadFork(ctx, f.client(), "T-1", "e-9", "copy")
	require.NoError(t, err)
	_, err = threadFork(ctx, f.client(), "T-1", "", "bogus")
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 2, ec.code)

	reqs := f.requests()
	require.Len(t, reqs, 2, "非法 --env 不发请求")
	for _, r := range reqs {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/v1/threads/T-1/fork", r.Path)
	}
	assert.JSONEq(t, `{"env":"share"}`, string(reqs[0].Body))
	assert.JSONEq(t, `{"env":"copy","entryId":"e-9"}`, string(reqs[1].Body))
}
