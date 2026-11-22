package coord

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// fakeHome 是只读的全局 omp 配置。
type fakeHome map[string][]byte

func (h fakeHome) Get(p string) ([]byte, agentcfg.FileInfo, error) {
	b, ok := h[p]
	if !ok {
		return nil, agentcfg.FileInfo{}, fs.ErrNotExist
	}
	return b, agentcfg.FileInfo{}, nil
}

type fakeEnv map[string]string

func (e fakeEnv) EnvSecrets() (map[string]string, error) { return e, nil }

// M19 1.2：设置的缺省值、校验与保存；保存后立即刷新协调宿主的出站策略，并让静止的助手对话停掉 agent（下一条消息用新配置）。
func TestAssistantSettings(t *testing.T) {
	e := newEnv(t, false)
	var mu sync.Mutex
	var refreshed []string
	e.c.refreshPolicy = func(_ context.Context, orbID string) error {
		mu.Lock()
		defer mu.Unlock()
		refreshed = append(refreshed, orbID)
		return nil
	}

	got, err := e.c.Settings(e.ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.AssistantSettings{WebAccess: true, DisabledMcpServers: []string{}}, got, "没有设置行时联网开、无禁用项")

	long := make([]rune, 201)
	for i := range long {
		long[i] = 'x'
	}
	for _, bad := range []apigen.AssistantSettings{
		{DisabledMcpServers: []string{""}},
		{DisabledMcpServers: []string{string(long)}},
		{DisabledMcpServers: make([]string, 201)},
	} {
		_, err := e.c.PutSettings(e.ctx, bad)
		assert.Equal(t, CodeInvalid, errCode(err))
	}

	// 一个已经静止、agent 在跑的助手对话
	conv, err := e.c.CreateConversation(e.ctx, threads.CoordinatorParams{Prompt: "hi"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		th, err := e.th.Get(e.ctx, conv.ID)
		return err == nil && th.State == apigen.ThreadStateIdle && slices.Contains(e.orbs.Calls(), "agent.start")
	}, 10*time.Second, 20*time.Millisecond)
	stops := func() int {
		n := 0
		for _, c := range e.orbs.Calls() {
			if c == "agent.stop" {
				n++
			}
		}
		return n
	}
	before := stops()

	prov := "docker"
	got, err = e.c.PutSettings(e.ctx, apigen.AssistantSettings{
		WebAccess: false, DisabledMcpServers: []string{"a", "b", "a"}, Computer: apigen.AssistantComputerSettings{Provider: &prov},
	})
	require.NoError(t, err)
	assert.Equal(t, apigen.AssistantSettings{WebAccess: false, DisabledMcpServers: []string{"a", "b"},
		Computer: apigen.AssistantComputerSettings{Provider: &prov}}, got, "去重")
	again, err := e.c.Settings(e.ctx)
	require.NoError(t, err)
	assert.Equal(t, got, again)
	assert.False(t, e.c.webAccess(e.ctx))
	mu.Lock()
	assert.Equal(t, []string{e.coordOrb}, refreshed, "出站策略立即刷新")
	mu.Unlock()
	require.Eventually(t, func() bool { return stops() > before }, 10*time.Second, 20*time.Millisecond, "静止的助手对话停掉 agent")
}

// M19 1.2：MCP 列表带可用性、原因、主机与是否开启；没有 mcp.json 时为空。
func TestAssistantMCPServers(t *testing.T) {
	e := newEnv(t, true)
	got, err := e.c.MCPServers(e.ctx)
	require.NoError(t, err)
	assert.Empty(t, got, "没有配置时为空")

	e.c.SetAgentConfig(fakeHome{"mcp.json": []byte(`{"mcpServers": {
		"web": {"type": "http", "url": "https://mcp.example.com:8443/x", "headers": {"Authorization": "Bearer ${TOKEN}"}},
		"local": {"command": "npx"},
		"bad": {"type": "http", "url": "https://b.example.com", "headers": {"X": "${NOPE}"}}
	}}`)}, fakeEnv{"TOKEN": "t"})
	_, err = e.c.PutSettings(e.ctx, apigen.AssistantSettings{WebAccess: true, DisabledMcpServers: []string{"web"}})
	require.NoError(t, err)
	got, err = e.c.MCPServers(e.ctx)
	require.NoError(t, err)
	host := func(s string) *string { return &s }
	reason := func(r apigen.AssistantMcpServerReason) *apigen.AssistantMcpServerReason { return &r }
	assert.Equal(t, []apigen.AssistantMcpServer{
		{Name: "bad", Transport: "http", Host: host("b.example.com"), Usable: false, Reason: reason("unresolved_env"), Enabled: true},
		{Name: "local", Transport: "stdio", Usable: false, Reason: reason("stdio"), Enabled: true},
		{Name: "web", Transport: "http", Host: host("mcp.example.com"), Usable: true, Enabled: false},
	}, got)

	e.c.SetAgentConfig(fakeHome{"mcp.json": []byte(`{`)}, nil)
	got, err = e.c.MCPServers(e.ctx)
	require.NoError(t, err)
	assert.Empty(t, got, "解析失败时为空")
}

// D10：第一层（web）与 MCP 凭据只进全局助手的 AgentProfile；Space 协调者两者都没有。
func TestAgentProfileWebAndMCPTokenOnlyForAssistant(t *testing.T) {
	e := newEnv(t, true)
	e.c.cfg.MCPToken = "mcp-secret-token"
	asst, err := e.c.AgentProfile(e.ctx, e.assistant().ID)
	require.NoError(t, err)
	assert.True(t, asst.Web)
	assert.Equal(t, "mcp-secret-token", asst.MCPToken)
	assert.NotContains(t, asst.SystemPrompt, "mcp-secret-token", "凭据不进系统提示词")

	chat := e.spaceChat(e.space("Alpha", ""))
	sp, err := e.c.AgentProfile(e.ctx, chat.ID)
	require.NoError(t, err)
	assert.False(t, sp.Web)
	assert.Empty(t, sp.MCPToken)
}

// M26 4.2：列出 MCP 服务器的工具：带 mcp.json 里的鉴权头走 Streamable HTTP；SSE 不支持；找不到、不可用、上游失败各有错误码。
func TestMCPServerTools(t *testing.T) {
	var mu sync.Mutex
	var auth []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fake"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{
				{"name": "list_items", "description": "List items", "annotations": map[string]any{"readOnlyHint": true}},
				{"name": "send_item", "annotations": map[string]any{"readOnlyHint": false}},
				{"name": "plain"},
			}}
		default:
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer up.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer broken.Close()

	e := newEnv(t, true)
	e.c.SetAgentConfig(fakeHome{"mcp.json": []byte(`{"mcpServers": {
		"mail": {"type": "http", "url": "` + up.URL + `/mcp", "headers": {"Authorization": "Bearer ${TOKEN}"}},
		"old": {"type": "sse", "url": "https://sse.example.com/sse"},
		"local": {"command": "npx"},
		"down": {"type": "http", "url": "` + broken.URL + `/mcp", "headers": {"Authorization": "Bearer ${TOKEN}"}}
	}}`)}, fakeEnv{"TOKEN": "secret-token"})

	got, err := e.c.MCPServerTools(e.ctx, "mail")
	require.NoError(t, err)
	desc := "List items"
	yes, no := true, false
	assert.Equal(t, []apigen.AssistantMcpTool{
		{Name: "list_items", Description: &desc, ReadOnly: &yes},
		{Name: "send_item", ReadOnly: &no},
		{Name: "plain"},
	}, got)
	mu.Lock()
	require.NotEmpty(t, auth)
	for _, a := range auth {
		assert.Equal(t, "Bearer secret-token", a, "带上 mcp.json 里展开后的鉴权头")
	}
	mu.Unlock()

	_, err = e.c.MCPServerTools(e.ctx, "old")
	assert.Equal(t, CodeUnsupportedTransport, errCode(err))
	_, err = e.c.MCPServerTools(e.ctx, "nope")
	assert.Equal(t, CodeNotFound, errCode(err))
	_, err = e.c.MCPServerTools(e.ctx, "local")
	assert.Equal(t, CodeInvalid, errCode(err))
	_, err = e.c.MCPServerTools(e.ctx, "down")
	assert.Equal(t, CodeUpstreamFailed, errCode(err))
	assert.NotContains(t, err.Error(), "secret-token", "错误里不带请求头的值")
}
