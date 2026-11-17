package coord

import (
	"context"
	"io/fs"
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
	}}`)}, nil, fakeEnv{"TOKEN": "t"})
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

	e.c.SetAgentConfig(fakeHome{"mcp.json": []byte(`{`)}, nil, nil)
	got, err = e.c.MCPServers(e.ctx)
	require.NoError(t, err)
	assert.Empty(t, got, "解析失败时为空")
}
