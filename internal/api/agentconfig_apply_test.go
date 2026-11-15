package api_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M13 6.3：apply-status / restart-sessions 的响应形状（没有需重启的会话时为空列表，不是 null）。
func TestAgentConfigApplyStatusAPI(t *testing.T) {
	e := newThreadsEnv(t)
	code, body := e.do("GET", "/api/v1/agent-config/apply-status", nil)
	require.Equal(t, 200, code, string(body))
	assert.JSONEq(t, `{"threads":[],"pendingReloads":0}`, string(body))

	code, body = e.do("POST", "/api/v1/agent-config/restart-sessions", nil)
	require.Equal(t, 200, code, string(body))
	assert.JSONEq(t, `{"threadIds":[]}`, string(body))
}
