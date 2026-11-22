package api_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/research"
)

// M26 步骤 1.5：跟进话题的 REST。
func TestResearchTopicsREST(t *testing.T) {
	e, _ := newCoordEnv(t)
	e.apiSrv.Research = &api.ResearchDeps{Service: research.New(research.Config{Store: e.st, Host: e.coordSvc})}

	code, body := e.do("POST", "/api/v1/assistant/research/topics", map[string]any{"kind": "question", "title": "Node 24 LTS", "question": "When?"})
	require.Equal(t, 201, code, string(body))
	var tp apigen.ResearchTopic
	require.NoError(t, json.Unmarshal(body, &tp))
	assert.Equal(t, apigen.ResearchOriginManual, tp.Origin)

	code, body = e.do("GET", "/api/v1/assistant/research/topics", nil)
	require.Equal(t, 200, code)
	var list apigen.ResearchTopicList
	require.NoError(t, json.Unmarshal(body, &list))
	require.Len(t, list.Topics, 1)
	assert.Equal(t, tp.ID, list.Topics[0].ID)

	code, body = e.do("PATCH", "/api/v1/assistant/research/topics/"+tp.ID, map[string]any{"state": "paused"})
	require.Equal(t, 200, code, string(body))
	require.NoError(t, json.Unmarshal(body, &tp))
	assert.Equal(t, apigen.ResearchTopicPaused, tp.State)

	code, body = e.do("POST", "/api/v1/assistant/research/topics/"+tp.ID+"/run", nil)
	assert.Equal(t, 400, code, "暂停的话题不能立即检查")
	assert.Equal(t, "invalid_request", errCode(body))

	code, _ = e.do("DELETE", "/api/v1/assistant/research/topics/"+tp.ID, nil)
	require.Equal(t, 204, code)
	code, body = e.do("GET", "/api/v1/assistant/research/topics", nil)
	require.Equal(t, 200, code)
	require.NoError(t, json.Unmarshal(body, &list))
	assert.Empty(t, list.Topics, "缺省不含已删除的")
	code, body = e.do("GET", "/api/v1/assistant/research/topics?state=all", nil)
	require.Equal(t, 200, code)
	require.NoError(t, json.Unmarshal(body, &list))
	require.Len(t, list.Topics, 1)
	assert.Equal(t, apigen.ResearchTopicDismissed, list.Topics[0].State)

	code, body = e.do("POST", "/api/v1/assistant/research/topics", map[string]any{"kind": "question", "title": "node 24 lts", "question": "q"})
	assert.Equal(t, 400, code, "重名")
	assert.Equal(t, "invalid_request", errCode(body))
	code, _ = e.do("GET", "/api/v1/assistant/research/topics/rtp_missing", nil)
	assert.Equal(t, 404, code)

	code, body = e.do("GET", "/api/v1/assistant/research/settings", nil)
	require.Equal(t, 200, code)
	var env apigen.ResearchSettingsEnvelope
	require.NoError(t, json.Unmarshal(body, &env))
	assert.False(t, env.Settings.Enabled)
	code, body = e.do("PUT", "/api/v1/assistant/research/settings", map[string]any{"enabled": true, "web": true, "mine": false,
		"internal": false, "mcp": false, "dailyUsd": 5, "preferences": "", "mcpTools": map[string]any{}})
	require.Equal(t, 200, code, string(body))
	require.NoError(t, json.Unmarshal(body, &env))
	assert.True(t, env.Settings.Enabled)

	code, body = e.do("GET", "/api/v1/assistant/research/runs?limit=10", nil)
	require.Equal(t, 200, code)
	var runs apigen.ResearchRunList
	require.NoError(t, json.Unmarshal(body, &runs))
	assert.Empty(t, runs.Runs)
}
