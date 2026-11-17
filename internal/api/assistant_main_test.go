package api_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// M19 步骤 3.1：GET assistant/main 没有则创建、再取得同一条；主线不能归档、标记已交付或删除（409 main_conversation）。
func TestAssistantMainREST(t *testing.T) {
	e, _ := newCoordEnv(t)

	code, body := e.do("GET", "/api/v1/assistant/main", nil)
	require.Equal(t, 200, code, string(body))
	var env apigen.ThreadEnvelope
	require.NoError(t, json.Unmarshal(body, &env))
	main := env.Thread
	require.NotNil(t, main.Main)
	assert.True(t, *main.Main)
	require.NotNil(t, main.Kind)
	assert.Equal(t, apigen.ThreadKind("assistant"), *main.Kind)

	code, body = e.do("GET", "/api/v1/assistant/main", nil)
	require.Equal(t, 200, code, string(body))
	require.NoError(t, json.Unmarshal(body, &env))
	assert.Equal(t, main.ID, env.Thread.ID)

	for _, patch := range []map[string]any{{"archived": true}, {"shipped": true}} {
		code, body = e.do("PATCH", "/api/v1/threads/"+main.ID, patch)
		assert.Equal(t, 409, code, string(body))
		assert.Equal(t, threads.CodeMainConversation, errCode(body))
	}
	code, body = e.do("DELETE", "/api/v1/threads/"+main.ID, nil)
	assert.Equal(t, 409, code, string(body))
	assert.Equal(t, threads.CodeMainConversation, errCode(body))
}
