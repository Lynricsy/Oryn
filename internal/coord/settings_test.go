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

// 全局助手默认模型与思考档位：校验、保存、主线跟随切换（在线与离线恢复默认）、
// 新对话继承默认且显式参数优先、已有普通助手对话不切换也不被打断，
// 且有历史会话的主线在恢复默认并重启后，经真实 get_state 仍保持解析出的默认值（不回弹到历史旧模型/旧档位）。
func TestAssistantModelAndThinkingSettings(t *testing.T) {
	e := newEnv(t, false)
	e.c.SetAgentConfig(fakeHome{"config.yml": []byte("defaultThinkingLevel: medium\n")}, nil)

	// 校验非法 model 与 thinkingLevel
	badThinking := "turbo"
	for _, bad := range []apigen.AssistantSettings{
		{WebAccess: true, Model: &apigen.ModelRef{Provider: "", ID: "claude"}},
		{WebAccess: true, Model: &apigen.ModelRef{Provider: "anthropic", ID: ""}},
		{WebAccess: true, Model: &apigen.ModelRef{Provider: "a/b", ID: "claude"}},
		{WebAccess: true, ThinkingLevel: &badThinking},
	} {
		_, err := e.c.PutSettings(e.ctx, bad)
		assert.Equal(t, CodeInvalid, errCode(err))
	}

	// 辅助：向线程发一条 prompt 并等待完成（生成真实会话历史 .jsonl）
	runPrompt := func(threadID, msg string) {
		t.Helper()
		done := make(chan json.RawMessage, 1)
		f, _ := json.Marshal(map[string]string{"type": "prompt", "message": msg})
		require.NoError(t, e.th.Command(e.ctx, threadID, f, func(r json.RawMessage) { done <- r }))
		select {
		case raw := <-done:
			var resp struct {
				Success bool `json:"success"`
			}
			require.NoError(t, json.Unmarshal(raw, &resp))
			require.True(t, resp.Success, "prompt failed: %s", string(raw))
		case <-time.After(10 * time.Second):
			t.Fatal("timeout waiting for prompt response")
		}
		require.Eventually(t, func() bool {
			th, err := e.th.Get(e.ctx, threadID)
			return err == nil && th.State == apigen.ThreadStateIdle
		}, 10*time.Second, 20*time.Millisecond)
	}
	// 辅助：直接向运行中的 agent 发 get_state 并解析模型与思考档位
	getAgentState := func(threadID string) (provider, modelID, thinking string) {
		t.Helper()
		done := make(chan json.RawMessage, 1)
		require.NoError(t, e.th.Command(e.ctx, threadID, json.RawMessage(`{"type":"get_state"}`), func(r json.RawMessage) { done <- r }))
		select {
		case raw := <-done:
			var resp struct {
				Success bool `json:"success"`
				Data    struct {
					Model struct {
						Provider string `json:"provider"`
						ID       string `json:"id"`
					} `json:"model"`
					ThinkingLevel string `json:"thinkingLevel"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(raw, &resp))
			require.True(t, resp.Success, "get_state failed: %s", string(raw))
			return resp.Data.Model.Provider, resp.Data.Model.ID, resp.Data.ThinkingLevel
		case <-time.After(10 * time.Second):
			t.Fatal("timeout waiting for get_state response")
			return "", "", ""
		}
	}
	stops := func() int {
		n := 0
		for _, c := range e.orbs.Calls() {
			if c == "agent.stop" {
				n++
			}
		}
		return n
	}
	stopMain := func(threadID string) {
		t.Helper()
		before := stops()
		e.th.RefreshAgent(e.ctx, threadID)
		require.Eventually(t, func() bool { return stops() > before }, 10*time.Second, 20*time.Millisecond, "等待主线 agent 停止")
	}

	// 已有普通助手对话与主线对话均已就绪
	existing, err := e.c.CreateConversation(e.ctx, threads.CoordinatorParams{
		Model: "openai/gpt-old", ThinkingLevel: "low", Prompt: "hi",
	})
	require.NoError(t, err)
	mainTh, err := e.c.EnsureMain(e.ctx)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		a, err1 := e.th.Get(e.ctx, existing.ID)
		b, err2 := e.th.Get(e.ctx, mainTh.ID)
		return err1 == nil && err2 == nil &&
			a.State == apigen.ThreadStateIdle &&
			b.State == apigen.ThreadStateIdle
	}, 10*time.Second, 20*time.Millisecond)

	stopsBefore := stops()

	// 1. 仅改默认模型与思考档位：主线在线同步切换；已有普通对话不变且不触发 agent.stop
	xhigh := "xhigh"
	got, err := e.c.PutSettings(e.ctx, apigen.AssistantSettings{
		WebAccess:          true,
		DisabledMcpServers: []string{},
		Model:              &apigen.ModelRef{Provider: "anthropic", ID: "claude-opus-4-6"},
		ThinkingLevel:      &xhigh,
	})
	require.NoError(t, err)
	require.NotNil(t, got.Model)
	assert.Equal(t, apigen.ModelRef{Provider: "anthropic", ID: "claude-opus-4-6"}, *got.Model)
	require.NotNil(t, got.ThinkingLevel)
	assert.Equal(t, "xhigh", *got.ThinkingLevel)

	require.Eventually(t, func() bool {
		m, err := e.th.Get(e.ctx, mainTh.ID)
		return err == nil &&
			m.Model != nil && m.Model.Provider == "anthropic" && m.Model.ID == "claude-opus-4-6" &&
			m.ThinkingLevel != nil && *m.ThinkingLevel == "xhigh"
	}, 10*time.Second, 20*time.Millisecond, "主线应跟随切换到新的模型与思考档位")

	// 给主线跑一轮对话，让历史会话文件 (.jsonl) 明确记录旧模型与旧思考档位
	runPrompt(mainTh.ID, "first main turn with custom model")

	exAfter, err := e.th.Get(e.ctx, existing.ID)
	require.NoError(t, err)
	require.NotNil(t, exAfter.Model)
	assert.Equal(t, apigen.ModelRef{Provider: "openai", ID: "gpt-old"}, *exAfter.Model, "已有普通对话模型不切换")
	require.NotNil(t, exAfter.ThinkingLevel)
	assert.Equal(t, "low", *exAfter.ThinkingLevel, "已有普通对话思考档位不切换")
	assert.Equal(t, stopsBefore, stops(), "只改模型/档位时不应打断运行中的对话")

	// 新建全局助手对话（不显式传模型/档位）：继承全局助手设置
	created, err := e.c.CreateConversation(e.ctx, threads.CoordinatorParams{Prompt: "hello"})
	require.NoError(t, err)
	require.NotNil(t, created.Model)
	assert.Equal(t, apigen.ModelRef{Provider: "anthropic", ID: "claude-opus-4-6"}, *created.Model)
	require.NotNil(t, created.ThinkingLevel)
	assert.Equal(t, "xhigh", *created.ThinkingLevel)

	// 新建全局助手对话（显式传模型/档位）：显式参数优先
	override, err := e.c.CreateConversation(e.ctx, threads.CoordinatorParams{
		Model: "openai/gpt-5", ThinkingLevel: "minimal", Prompt: "override",
	})
	require.NoError(t, err)
	require.NotNil(t, override.Model)
	assert.Equal(t, apigen.ModelRef{Provider: "openai", ID: "gpt-5"}, *override.Model)
	require.NotNil(t, override.ThinkingLevel)
	assert.Equal(t, "minimal", *override.ThinkingLevel)

	// 2. 在线恢复默认（清空 model 与 thinkingLevel）：显式切回 default role 与 config.yml 的 defaultThinkingLevel(medium)
	got, err = e.c.PutSettings(e.ctx, apigen.AssistantSettings{
		WebAccess:          true,
		DisabledMcpServers: []string{},
	})
	require.NoError(t, err)
	assert.Nil(t, got.Model)
	assert.Nil(t, got.ThinkingLevel)

	require.Eventually(t, func() bool {
		m, err := e.th.Get(e.ctx, mainTh.ID)
		return err == nil &&
			m.Model != nil && m.Model.Provider == "fake" && m.Model.ID == "fake-model" &&
			m.ThinkingLevel != nil && *m.ThinkingLevel == "medium"
	}, 10*time.Second, 20*time.Millisecond, "在线恢复默认后主线应显式切回默认模型与 defaultThinkingLevel")

	// 停掉有历史的主线并重新拉起，直接校验新进程的 get_state 不会回弹到历史旧模型/旧档位
	stopMain(mainTh.ID)
	runPrompt(mainTh.ID, "after online reset restart")
	p, mid, th := getAgentState(mainTh.ID)
	assert.Equal(t, "fake", p, "重启后应保持默认 provider，而非历史里的 anthropic")
	assert.Equal(t, "fake-model", mid, "重启后应保持默认模型，而非历史里的 claude-opus-4-6")
	assert.Equal(t, "medium", th, "重启后应保持 config.yml 的 defaultThinkingLevel，而非历史里的 xhigh")

	// 3. 离线恢复默认：先切到另一组自定义模型/档位并跑一轮写进历史 → 停掉主线 → 离线恢复默认 → 重新唤醒并校验 get_state
	maxLevel := "max"
	_, err = e.c.PutSettings(e.ctx, apigen.AssistantSettings{
		WebAccess:          true,
		DisabledMcpServers: []string{},
		Model:              &apigen.ModelRef{Provider: "openai", ID: "gpt-6"},
		ThinkingLevel:      &maxLevel,
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		m, err := e.th.Get(e.ctx, mainTh.ID)
		return err == nil &&
			m.Model != nil && m.Model.Provider == "openai" && m.Model.ID == "gpt-6" &&
			m.ThinkingLevel != nil && *m.ThinkingLevel == "max"
	}, 10*time.Second, 20*time.Millisecond)
	runPrompt(mainTh.ID, "turn before offline reset")
	stopMain(mainTh.ID)

	// 离线状态下恢复默认
	_, err = e.c.PutSettings(e.ctx, apigen.AssistantSettings{
		WebAccess:          true,
		DisabledMcpServers: []string{},
	})
	require.NoError(t, err)

	// 重新唤醒有历史的主线并校验重启后的 get_state
	runPrompt(mainTh.ID, "wake after offline reset")
	p, mid, th = getAgentState(mainTh.ID)
	assert.Equal(t, "fake", p, "离线恢复默认后重启应切回默认 provider，而非历史里的 openai")
	assert.Equal(t, "fake-model", mid, "离线恢复默认后重启应切回默认模型，而非历史里的 gpt-6")
	assert.Equal(t, "medium", th, "离线恢复默认后重启应切回 defaultThinkingLevel，而非历史里的 max")

	// 已有普通助手对话全程不变
	exFinal, err := e.th.Get(e.ctx, existing.ID)
	require.NoError(t, err)
	require.NotNil(t, exFinal.Model)
	assert.Equal(t, apigen.ModelRef{Provider: "openai", ID: "gpt-old"}, *exFinal.Model)
	require.NotNil(t, exFinal.ThinkingLevel)
	assert.Equal(t, "low", *exFinal.ThinkingLevel)
}

// coordinator.model 非空时，全局助手主线恢复默认应显式回落到 coordinator.model，且重启后 get_state 保持不变。
func TestAssistantMainResetFallsBackToCoordinatorModel(t *testing.T) {
	e := newEnv(t, false, func(cfg *Config) { cfg.Model = "google/gemini-3" })
	mainTh, err := e.c.EnsureMain(e.ctx)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		m, err := e.th.Get(e.ctx, mainTh.ID)
		return err == nil && m.State == apigen.ThreadStateIdle
	}, 10*time.Second, 20*time.Millisecond)

	low := "low"
	_, err = e.c.PutSettings(e.ctx, apigen.AssistantSettings{
		WebAccess:          true,
		DisabledMcpServers: []string{},
		Model:              &apigen.ModelRef{Provider: "anthropic", ID: "claude-opus-4-6"},
		ThinkingLevel:      &low,
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		m, err := e.th.Get(e.ctx, mainTh.ID)
		return err == nil && m.Model != nil && m.Model.ID == "claude-opus-4-6" && m.ThinkingLevel != nil && *m.ThinkingLevel == "low"
	}, 10*time.Second, 20*time.Millisecond)

	// 跑一轮写进会话历史
	done := make(chan json.RawMessage, 1)
	require.NoError(t, e.th.Command(e.ctx, mainTh.ID, json.RawMessage(`{"type":"prompt","message":"record history"}`), func(r json.RawMessage) { done <- r }))
	<-done
	require.Eventually(t, func() bool {
		m, err := e.th.Get(e.ctx, mainTh.ID)
		return err == nil && m.State == apigen.ThreadStateIdle
	}, 10*time.Second, 20*time.Millisecond)

	// 恢复默认：应显式回落到 coordinator.model (google/gemini-3) 与缺省 defaultThinkingLevel (high)
	_, err = e.c.PutSettings(e.ctx, apigen.AssistantSettings{WebAccess: true, DisabledMcpServers: []string{}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		m, err := e.th.Get(e.ctx, mainTh.ID)
		return err == nil &&
			m.Model != nil && m.Model.Provider == "google" && m.Model.ID == "gemini-3" &&
			m.ThinkingLevel != nil && *m.ThinkingLevel == "high"
	}, 10*time.Second, 20*time.Millisecond)

	// 重启主线并通过真实 get_state 验证
	before := 0
	for _, c := range e.orbs.Calls() {
		if c == "agent.stop" {
			before++
		}
	}
	e.th.RefreshAgent(e.ctx, mainTh.ID)
	require.Eventually(t, func() bool {
		n := 0
		for _, c := range e.orbs.Calls() {
			if c == "agent.stop" {
				n++
			}
		}
		return n > before
	}, 10*time.Second, 20*time.Millisecond)

	done2 := make(chan json.RawMessage, 1)
	require.NoError(t, e.th.Command(e.ctx, mainTh.ID, json.RawMessage(`{"type":"prompt","message":"after restart"}`), func(r json.RawMessage) { done2 <- r }))
	<-done2
	require.Eventually(t, func() bool {
		m, err := e.th.Get(e.ctx, mainTh.ID)
		return err == nil && m.State == apigen.ThreadStateIdle
	}, 10*time.Second, 20*time.Millisecond)

	stDone := make(chan json.RawMessage, 1)
	require.NoError(t, e.th.Command(e.ctx, mainTh.ID, json.RawMessage(`{"type":"get_state"}`), func(r json.RawMessage) { stDone <- r }))
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			Model struct {
				Provider string `json:"provider"`
				ID       string `json:"id"`
			} `json:"model"`
			ThinkingLevel string `json:"thinkingLevel"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(<-stDone, &resp))
	require.True(t, resp.Success)
	assert.Equal(t, "google", resp.Data.Model.Provider)
	assert.Equal(t, "gemini-3", resp.Data.Model.ID)
	assert.Equal(t, "high", resp.Data.ThinkingLevel)
}
