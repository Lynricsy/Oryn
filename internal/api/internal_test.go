package api_test

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/coord"
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

var pathParamRE = regexp.MustCompile(`\{[^}]+\}`)
