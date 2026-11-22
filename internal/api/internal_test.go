package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/coord"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// 进程内入口不经鉴权中间件，以助手主体直接调用 REST 处理器。
func TestInternalDo(t *testing.T) {
	e, _ := newCoordEnv(t)
	in := api.NewInternal(e.apiSrv)
	ctx := context.Background()

	status, _, out, total := in.Do(ctx, "T-x", "POST", "/api/v1/spaces", "application/json", []byte(`{"name":"S1"}`), 1<<20)
	require.Equal(t, http.StatusCreated, status, string(out))
	assert.Contains(t, string(out), `"name":"S1"`)
	assert.Equal(t, len(out), total)

	status, _, out, _ = in.Do(ctx, "T-x", "GET", "/api/v1/spaces", "", nil, 1<<20)
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, string(out), "S1")

	// ListDevices 需要主体：助手主体可用
	status, _, out, _ = in.Do(ctx, "T-x", "GET", "/api/v1/devices", "", nil, 1<<20)
	assert.Equal(t, http.StatusOK, status, string(out))

	// 响应只保留前 max 字节，total 是全长
	status, _, out, total = in.Do(ctx, "T-x", "GET", "/api/v1/spaces", "", nil, 5)
	require.Equal(t, http.StatusOK, status)
	assert.Len(t, out, 5)
	assert.Greater(t, total, 5)

	status, _, out, _ = in.Do(ctx, "T-x", "GET", "/api/v1/nope", "", nil, 1<<20)
	assert.Equal(t, http.StatusNotFound, status)
	assert.True(t, strings.Contains(string(out), "not_found"))
}

// 助手可用的每个操作都必须接受 KindAssistant 主体：回 401 说明处理器依赖网页会话或设备主体，应改为 deny。
func TestInternalNoUnauthorized(t *testing.T) {
	e, _ := newCoordEnv(t)
	in := api.NewInternal(e.apiSrv)
	ops := coord.AllowedAPIOps()
	require.NotEmpty(t, ops)
	for _, op := range ops {
		method, path, ok := coord.APIEndpoint(op)
		require.True(t, ok, op)
		target := pathParamRE.ReplaceAllString(path, "x")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		status, _, out, _ := in.Do(ctx, "T-x", method, target, "", nil, 4096)
		cancel()
		assert.NotEqual(t, http.StatusUnauthorized, status, "%s %s %s: %s", op, method, target, out)
	}
}

// 端到端：助手对话经 oryn 工具在进程内调用真实处理器，结果经 HTTP 可见，并写审计。
func TestInternalOrynEndToEnd(t *testing.T) {
	e, _ := newCoordEnv(t)
	e.coordSvc.SetAPI(api.NewInternal(e.apiSrv))
	code, body := e.do("POST", "/api/v1/assistant/conversations", nil)
	require.Equal(t, http.StatusCreated, code, string(body))
	var conv apigen.Thread
	require.NoError(t, json.Unmarshal(body, &conv))

	res := e.coordSvc.HostToolCall(context.Background(), threads.HostToolCall{ThreadID: conv.ID, Kind: threads.KindAssistant, Name: coord.ToolOryn,
		Args: json.RawMessage(`{"op":"createSpace","args":{"body":{"name":"S2"}}}`), OwnerTurn: true})
	require.False(t, res.IsError, res.Text)
	code, body = e.do("GET", "/api/v1/spaces", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, string(body), `"S2"`)
	code, body = e.do("GET", "/api/v1/automation/actions", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, string(body), `"api_call"`)
}

// oryn 读全局 Agent 配置文件时，mcp.json 的任何等价路径都不能读出 MCP 凭据（经真实路由与 Home.Get）。
func TestInternalOrynMCPConfigHidden(t *testing.T) {
	const cred = "Bearer PLACEHOLDER-not-a-real-token"
	e, _ := newCoordEnv(t)
	home, err := agentcfg.NewHome(t.TempDir())
	require.NoError(t, err)
	ctx := context.Background()
	_, err = home.Put(ctx, "mcp.json", []byte(`{"mcpServers":{"web":{"type":"http","url":"https://mcp.example.com/x","headers":{"Authorization":"`+cred+`"}}}}`), 0o600, nil)
	require.NoError(t, err)
	_, err = home.Put(ctx, "config.yml", []byte("theme: dark\n"), 0o644, nil)
	require.NoError(t, err)
	e.apiSrv.Auth = &api.AuthDeps{Home: home}
	in := api.NewInternal(e.apiSrv)
	e.coordSvc.SetAPI(in)
	code, body := e.do("POST", "/api/v1/assistant/conversations", nil)
	require.Equal(t, http.StatusCreated, code, string(body))
	var conv apigen.Thread
	require.NoError(t, json.Unmarshal(body, &conv))
	call := func(path string) threads.HostToolResult {
		args, _ := json.Marshal(map[string]any{"op": "getAgentConfigFile", "args": map[string]any{"path": path}})
		return e.coordSvc.HostToolCall(ctx, threads.HostToolCall{ThreadID: conv.ID, Kind: threads.KindAssistant, Name: coord.ToolOryn, Args: args, OwnerTurn: true})
	}

	// 对照：路由与文件可读，且未加守卫时 ./mcp.json 确实能读出凭据
	res := call("config.yml")
	require.False(t, res.IsError, res.Text)
	assert.Contains(t, res.Text, "theme: dark")
	status, _, out, _ := in.Do(ctx, conv.ID, "GET", "/api/v1/agent-config/files/.%2Fmcp.json", "", nil, 1<<20)
	require.Equal(t, http.StatusOK, status, string(out))
	require.Contains(t, string(out), cred)

	for _, p := range []string{"mcp.json", "./mcp.json", "rules/../mcp.json", "a/./b/../../mcp.json"} {
		res := call(p)
		assert.True(t, strings.HasPrefix(res.Text, coord.CodeNotAvailable+":"), "%s: %s", p, res.Text)
		assert.NotContains(t, res.Text, cred, p)
	}
}

var pathParamRE = regexp.MustCompile(`\{[^}]+\}`)
