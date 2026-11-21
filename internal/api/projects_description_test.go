package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 项目描述（M24）：trim、≤200 字符、PATCH 只在给出时改、空串清除。
func TestProjectDescriptionAPI(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)
	var p struct {
		ID          string
		Description *string
	}

	r := e.do("POST", "/api/v1/projects", c, map[string]any{"name": "long", "description": strings.Repeat("字", 201)})
	assert.Equal(t, 400, r.status, string(r.body))
	assert.Contains(t, string(r.body), "invalid_request")

	r = e.do("POST", "/api/v1/projects", c, map[string]any{"name": "p", "description": "  一行说明  "})
	require.Equal(t, 201, r.status, string(r.body))
	assert.Contains(t, string(r.body), `"description":"一行说明"`)
	r.json(t, &p)

	url := "/api/v1/projects/" + p.ID
	// 未涉及 description 的更新不动它
	r = e.do("PATCH", url, c, map[string]any{"color": "red"})
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &p)
	require.NotNil(t, p.Description)
	assert.Equal(t, "一行说明", *p.Description)

	// 恰好 200 个字符可以保存，201 个不行
	exact := strings.Repeat("描", 200)
	r = e.do("PATCH", url, c, map[string]any{"description": exact})
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &p)
	assert.Equal(t, exact, *p.Description)
	r = e.do("PATCH", url, c, map[string]any{"description": exact + "x"})
	assert.Equal(t, 400, r.status, string(r.body))
	assert.Contains(t, string(r.body), "invalid_request")
	row, err := e.st.Q.GetProject(context.Background(), p.ID)
	require.NoError(t, err)
	require.NotNil(t, row.Description)
	assert.Equal(t, exact, *row.Description)

	// 空串清除
	r = e.do("PATCH", url, c, map[string]any{"description": ""})
	require.Equal(t, 200, r.status, string(r.body))
	assert.NotContains(t, string(r.body), `"description"`)
	row, err = e.st.Q.GetProject(context.Background(), p.ID)
	require.NoError(t, err)
	assert.Nil(t, row.Description)
}
