package egress

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// researchTok 是测试里后台研究持有的 MCP 凭据。
const researchTok = "mcp-research-token-9876543210"

// M26 4.1：研究凭据只能到达开放给研究的路由；tools/call 只放行白名单内的工具，白名单外的与其它方法在代理处
// 直接回 JSON-RPC 错误，上游收不到；助手凭据不受影响。
func TestMCPResearchFilter(t *testing.T) {
	var mu sync.Mutex
	var hits int
	var lastBody []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits++
		lastBody = b
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer up.Close()
	reset := func() {
		mu.Lock()
		hits, lastBody = 0, nil
		mu.Unlock()
	}
	upHits := func() (int, []byte) {
		mu.Lock()
		defer mu.Unlock()
		return hits, lastBody
	}
	srv := httptest.NewServer(NewHandler("orb-x", fakeR{pol: Policy{
		NoGit: true, MCPToken: assistantTok, MCPResearchToken: researchTok,
		MCP: map[string]MCPRoute{
			"m0": {URL: up.URL + "/mcp", ResearchTools: []string{"list_items"}},
			"m1": {URL: up.URL + "/closed"},
			"m2": {URL: up.URL + "/empty", ResearchTools: []string{}},
		},
	}}))
	defer srv.Close()

	do := func(method, path, tok, body string, hdr map[string]string) (*http.Response, []byte) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, srv.URL+path, rd)
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		if tok != "" {
			req.Header.Set(MCPTokenHeader, tok)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}
	type rpcErr struct {
		ID    json.RawMessage `json:"id"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	parseErr := func(b []byte) rpcErr {
		t.Helper()
		var e rpcErr
		require.NoError(t, json.Unmarshal(b, &e), string(b))
		return e
	}

	t.Run("白名单内的工具原样到达上游", func(t *testing.T) {
		reset()
		body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"list_items","arguments":{"q":"x"}}}`
		resp, _ := do("POST", "/mcp/m0", researchTok, body, map[string]string{"Mcp-Method": "tools/call", "Mcp-Name": "list_items"})
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		n, got := upHits()
		assert.Equal(t, 1, n)
		assert.Equal(t, body, string(got), "请求体不变")
	})

	t.Run("白名单外的工具回 -32602 且上游收不到", func(t *testing.T) {
		reset()
		resp, b := do("POST", "/mcp/m0", researchTok,
			`{"jsonrpc":"2.0","id":"req-9","method":"tools/call","params":{"name":"send_item","arguments":{}}}`, nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
		e := parseErr(b)
		assert.JSONEq(t, `"req-9"`, string(e.ID), "带原 id")
		assert.Equal(t, -32602, e.Error.Code)
		assert.Contains(t, e.Error.Message, `"send_item"`)
		n, _ := upHits()
		assert.Zero(t, n)
	})

	t.Run("空白名单一个工具也不放行", func(t *testing.T) {
		reset()
		_, b := do("POST", "/mcp/m2", researchTok, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_items"}}`, nil)
		e := parseErr(b)
		assert.Equal(t, -32602, e.Error.Code)
		assert.JSONEq(t, `3`, string(e.ID))
		n, _ := upHits()
		assert.Zero(t, n)
	})

	t.Run("其它方法回 -32601", func(t *testing.T) {
		reset()
		for _, m := range []string{"resources/read", "prompts/get", "Tools/Call", "tools/call/"} {
			_, b := do("POST", "/mcp/m0", researchTok, `{"jsonrpc":"2.0","id":5,"method":"`+m+`","params":{"uri":"file:///x","name":"list_items"}}`, nil)
			e := parseErr(b)
			assert.Equal(t, -32601, e.Error.Code, m)
			assert.JSONEq(t, `5`, string(e.ID), m)
		}
		n, _ := upHits()
		assert.Zero(t, n)
	})

	t.Run("请求头与请求体不符 400", func(t *testing.T) {
		reset()
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_items"}}`
		for name, hdr := range map[string]map[string]string{
			"Mcp-Name 写另一个工具":   {"Mcp-Name": "send_item"},
			"Mcp-Method 写另一个方法": {"Mcp-Method": "tools/list"},
		} {
			resp, _ := do("POST", "/mcp/m0", researchTok, body, hdr)
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode, name)
		}
		// 握手请求不该带 Mcp-Name
		resp, _ := do("POST", "/mcp/m0", researchTok, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, map[string]string{"Mcp-Name": "send_item"})
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		n, _ := upHits()
		assert.Zero(t, n)
	})

	t.Run("握手、列工具、通知、应答与 GET、DELETE 放行", func(t *testing.T) {
		reset()
		for _, body := range []string{
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
			`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
			`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
			`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
			`{"jsonrpc":"2.0","id":"s1","result":{}}`,
		} {
			resp, _ := do("POST", "/mcp/m0", researchTok, body, nil)
			assert.Equal(t, http.StatusOK, resp.StatusCode, body)
		}
		resp, _ := do("GET", "/mcp/m0", researchTok, "", nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		resp, _ = do("DELETE", "/mcp/m0", researchTok, "", nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		n, _ := upHits()
		assert.Equal(t, 7, n)
	})

	t.Run("格式不对的请求被拒绝且上游收不到", func(t *testing.T) {
		reset()
		call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_items"}}`
		for name, tc := range map[string]struct {
			body string
			want int
		}{
			"批量数组":            {`[` + call + `]`, http.StatusBadRequest},
			"不是 JSON":         {`tools/call list_items`, http.StatusBadRequest},
			"对象后还有内容":         {call + call, http.StatusBadRequest},
			"method 不是字符串":    {`{"jsonrpc":"2.0","id":1,"method":1}`, http.StatusBadRequest},
			"大小写变体的 name":     {`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_items","Name":"send_item"}}`, http.StatusBadRequest},
			"只有大写的 Method":    {`{"jsonrpc":"2.0","id":1,"Method":"tools/call","params":{"name":"send_item"}}`, http.StatusBadRequest},
			"长 s 写的 params":   {`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_items"},"param\u017f":{"name":"send_item"}}`, http.StatusBadRequest},
			"重复的 method":      {`{"jsonrpc":"2.0","id":1,"method":"tools/list","method":"tools/call","params":{"name":"send_item"}}`, http.StatusBadRequest},
			"重复的 params.name": {`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_items","name":"send_item"}}`, http.StatusBadRequest},
		} {
			resp, _ := do("POST", "/mcp/m0", researchTok, tc.body, nil)
			assert.Equal(t, tc.want, resp.StatusCode, name)
		}
		resp, _ := do("PUT", "/mcp/m0", researchTok, call, nil)
		assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
		resp, _ = do("POST", "/mcp/m0", researchTok, call, map[string]string{"Content-Encoding": "gzip"})
		assert.Equal(t, http.StatusUnsupportedMediaType, resp.StatusCode)
		big := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_items","arguments":{"x":"` +
			strings.Repeat("a", mcpFilterBodyMax) + `"}}}`
		resp, _ = do("POST", "/mcp/m0", researchTok, big, nil)
		assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
		n, _ := upHits()
		assert.Zero(t, n)
	})

	t.Run("没有开放给研究的路由 403", func(t *testing.T) {
		reset()
		for _, m := range []string{"GET", "POST"} {
			resp, b := do(m, "/mcp/m1", researchTok, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
			assert.Equal(t, http.StatusForbidden, resp.StatusCode, m)
			assert.Contains(t, string(b), "没有开放给后台研究")
		}
		n, _ := upHits()
		assert.Zero(t, n)
	})

	t.Run("助手凭据调任何工具都到达上游", func(t *testing.T) {
		reset()
		for _, body := range []string{
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_item"}}`,
			`{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"file:///x"}}`,
			`[{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"send_item"}}]`,
		} {
			resp, _ := do("POST", "/mcp/m1", assistantTok, body, nil)
			assert.Equal(t, http.StatusOK, resp.StatusCode, body)
		}
		n, _ := upHits()
		assert.Equal(t, 3, n)
	})

	t.Run("错误凭据 403", func(t *testing.T) {
		reset()
		for _, tok := range []string{"", "wrong-token-0000000000", researchTok[:12], researchTok + "x"} {
			resp, _ := do("POST", "/mcp/m0", tok, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
			assert.Equal(t, http.StatusForbidden, resp.StatusCode, tok)
		}
		n, _ := upHits()
		assert.Zero(t, n)
	})

	t.Run("策略没有研究凭据时研究凭据无效", func(t *testing.T) {
		reset()
		srv2 := httptest.NewServer(NewHandler("orb-x", fakeR{pol: Policy{NoGit: true, MCPToken: assistantTok, MCP: map[string]MCPRoute{
			"m0": {URL: up.URL + "/mcp", ResearchTools: []string{"list_items"}},
		}}}))
		defer srv2.Close()
		req, _ := http.NewRequest("POST", srv2.URL+"/mcp/m0", bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)))
		req.Header.Set(MCPTokenHeader, researchTok)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		n, _ := upHits()
		assert.Zero(t, n)
	})
}
