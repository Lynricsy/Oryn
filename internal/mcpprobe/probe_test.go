package mcpprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeServer 是最小的 Streamable HTTP MCP 服务器：记下每个请求的方法、鉴权头与会话头。
type fakeServer struct {
	sse     bool   // 用 text/event-stream 应答
	session string // 非空时 initialize 应答带 Mcp-Session-Id
	pages   int    // tools/list 的页数；< 0 = 永远给下一页
	errMsg  string // 非空时 tools/list 回 JSON-RPC 错误

	mu   sync.Mutex
	log  []string // 方法名；DELETE 记作 "DELETE"
	auth []string
	sess []string // initialize 之后各请求的 Mcp-Session-Id
	ver  []string // initialize 之后各请求的 Mcp-Protocol-Version
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	if r.Method == http.MethodDelete {
		f.record("DELETE", r)
		w.WriteHeader(http.StatusOK)
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Cursor string `json:"cursor"`
		} `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	f.record(req.Method, r)
	var result any
	switch req.Method {
	case "initialize":
		if f.session != "" {
			w.Header().Set("Mcp-Session-Id", f.session)
		}
		result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fake"}}
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
		return
	case "tools/list":
		if f.errMsg != "" {
			f.reply(w, req.ID, nil, f.errMsg)
			return
		}
		page := 0
		if req.Params.Cursor != "" {
			_, _ = fmt.Sscanf(req.Params.Cursor, "p%d", &page)
		}
		yes, no := true, false
		var tools []map[string]any
		if page == 0 {
			tools = []map[string]any{
				{"name": "list_items", "description": "List items", "annotations": map[string]any{"readOnlyHint": yes}},
				{"name": "send_item", "description": "Send an item", "annotations": map[string]any{"readOnlyHint": no}},
			}
		} else {
			tools = []map[string]any{{"name": fmt.Sprintf("tool_p%d", page)}}
		}
		res := map[string]any{"tools": tools}
		if f.pages < 0 || page+1 < f.pages {
			res["nextCursor"] = fmt.Sprintf("p%d", page+1)
		}
		result = res
	default:
		f.reply(w, req.ID, nil, "method not found")
		return
	}
	f.reply(w, req.ID, result, "")
}

func (f *fakeServer) record(method string, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if method != "initialize" {
		f.sess = append(f.sess, r.Header.Get("Mcp-Session-Id"))
		f.ver = append(f.ver, r.Header.Get("Mcp-Protocol-Version"))
	}
	f.log = append(f.log, method)
}

// calls 在锁下取记录的快照。
func (f *fakeServer) calls() (log, auth, sess, ver []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.log), slices.Clone(f.auth), slices.Clone(f.sess), slices.Clone(f.ver)
}

func (f *fakeServer) reply(w http.ResponseWriter, id json.RawMessage, result any, errMsg string) {
	msg := map[string]any{"jsonrpc": "2.0", "id": id}
	if errMsg != "" {
		msg["error"] = map[string]any{"code": -32601, "message": errMsg}
	} else {
		msg["result"] = result
	}
	b, _ := json.Marshal(msg)
	if !f.sse {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	// 应答之前先来一条服务器通知与一条别的 id 的消息：客户端要跳过它们
	fmt.Fprint(w, ": keep-alive\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{}}\n\n")
	fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":999,\"result\":{}}\n\n")
	// 应答本身在第一个逗号后拆成两行 data（多行 data 以换行拼接，落在记号之间仍是合法 JSON）
	s := string(b)
	cut := strings.Index(s, ",") + 1
	fmt.Fprintf(w, "event: message\r\ndata: %s\r\ndata: %s\r\n\r\n", s[:cut], s[cut:])
}

func toolNames(tools []Tool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}

var authHdr = map[string]string{"Authorization": "Bearer probe-secret-token"}

func TestListToolsJSONWithSession(t *testing.T) {
	f := &fakeServer{session: "sess-1", pages: 2}
	srv := httptest.NewServer(f)
	defer srv.Close()
	tools, err := ListTools(context.Background(), srv.Client(), srv.URL+"/mcp", authHdr)
	require.NoError(t, err)
	assert.Equal(t, []string{"list_items", "send_item", "tool_p1"}, toolNames(tools))
	require.NotNil(t, tools[0].ReadOnly)
	assert.True(t, *tools[0].ReadOnly)
	require.NotNil(t, tools[1].ReadOnly)
	assert.False(t, *tools[1].ReadOnly)
	assert.Nil(t, tools[2].ReadOnly, "没有自报时为 nil")
	assert.Equal(t, "List items", tools[0].Description)

	log, auth, sess, ver := f.calls()
	assert.Equal(t, []string{"initialize", "notifications/initialized", "tools/list", "tools/list", "DELETE"}, log)
	for _, a := range auth {
		assert.Equal(t, "Bearer probe-secret-token", a, "每个请求都带鉴权头")
	}
	assert.Equal(t, []string{"sess-1", "sess-1", "sess-1", "sess-1"}, sess, "initialize 之后的请求都带会话 id")
	assert.Equal(t, []string{"2025-06-18", "2025-06-18", "2025-06-18", "2025-06-18"}, ver)
}

func TestListToolsSSEWithoutSession(t *testing.T) {
	f := &fakeServer{sse: true, pages: 2}
	srv := httptest.NewServer(f)
	defer srv.Close()
	tools, err := ListTools(context.Background(), nil, srv.URL, authHdr)
	require.NoError(t, err)
	assert.Equal(t, []string{"list_items", "send_item", "tool_p1"}, toolNames(tools))
	require.NotNil(t, tools[0].ReadOnly)
	assert.True(t, *tools[0].ReadOnly)
	log, auth, sess, _ := f.calls()
	assert.Equal(t, []string{"initialize", "notifications/initialized", "tools/list", "tools/list"}, log, "没有会话 id 时不 DELETE")
	assert.Equal(t, []string{"", "", ""}, sess)
	for _, a := range auth {
		assert.Equal(t, "Bearer probe-secret-token", a)
	}
}

func TestListToolsPageCap(t *testing.T) {
	f := &fakeServer{pages: -1}
	srv := httptest.NewServer(f)
	defer srv.Close()
	tools, err := ListTools(context.Background(), nil, srv.URL, nil)
	require.NoError(t, err)
	n := 0
	log, _, _, _ := f.calls()
	for _, m := range log {
		if m == "tools/list" {
			n++
		}
	}
	assert.Equal(t, pagesMax, n)
	assert.Len(t, tools, 2+pagesMax-1)
}

// 跨主机重定向不跟随：第二个服务器收不到请求，也就拿不到鉴权头。
func TestListToolsRefusesCrossHostRedirect(t *testing.T) {
	var evil int
	var mu sync.Mutex
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		evil++
		mu.Unlock()
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(other.URL, "127.0.0.1", "localhost", 1)+"/mcp", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	_, err := ListTools(context.Background(), nil, srv.URL+"/mcp?key=secret-in-url", authHdr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cross-host redirect")
	assert.NotContains(t, err.Error(), "secret-in-url", "错误里不带请求地址")
	mu.Lock()
	assert.Zero(t, evil)
	mu.Unlock()

	// 同主机的重定向照常跟随
	f := &fakeServer{pages: 1}
	mux := http.NewServeMux()
	mux.Handle("/new", f)
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/new", http.StatusPermanentRedirect)
	})
	same := httptest.NewServer(mux)
	defer same.Close()
	tools, err := ListTools(context.Background(), nil, same.URL+"/old", authHdr)
	require.NoError(t, err)
	assert.Equal(t, []string{"list_items", "send_item"}, toolNames(tools))
}

// 错误信息不带任何请求头的值：服务器在 JSON-RPC 错误里回显凭据时也会被抹掉。
func TestListToolsErrorRedactsHeaders(t *testing.T) {
	hdr := map[string]string{"Authorization": "Bearer probe-secret-token", "X-Api-Key": "key-0123456789"}
	f := &fakeServer{session: "s", errMsg: "bad credentials Bearer probe-secret-token / probe-secret-token / key-0123456789"}
	srv := httptest.NewServer(f)
	defer srv.Close()
	_, err := ListTools(context.Background(), nil, srv.URL, hdr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tools/list: JSON-RPC error -32601")
	assert.NotContains(t, err.Error(), "probe-secret-token")
	assert.NotContains(t, err.Error(), "key-0123456789")
	log, _, _, _ := f.calls()
	assert.Equal(t, "DELETE", log[len(log)-1], "出错时仍结束会话")

	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope "+r.Header.Get("Authorization"), http.StatusUnauthorized)
	}))
	defer unauthorized.Close()
	_, err = ListTools(context.Background(), nil, unauthorized.URL, hdr)
	require.Error(t, err)
	assert.Equal(t, "initialize: HTTP 401", err.Error())
}

func TestListToolsResponseTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"pad":"`+strings.Repeat("a", bodyMax)+`"}}`)
	}))
	defer srv.Close()
	_, err := ListTools(context.Background(), nil, srv.URL, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds 2 MiB")
}
