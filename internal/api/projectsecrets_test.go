package api_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 项目级密钥与环境变量（M24 阶段 ④）：校验、保密值不回显、与全局隔离、归档仍可写、删除。
func TestProjectSecretsAPI(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)

	assert.Equal(t, 404, e.do("GET", "/api/v1/projects/prj_nope/secrets", c, nil).status)
	r := e.do("PUT", "/api/v1/projects/prj_nope/secrets/FOO", c, map[string]any{"value": "v", "mode": "env"})
	assert.Equal(t, 404, r.status, string(r.body))
	assert.Equal(t, "not_found", r.code(t))

	r = e.do("POST", "/api/v1/projects", c, map[string]any{"name": "demo"})
	require.Equal(t, 201, r.status, string(r.body))
	var p struct{ ID string }
	r.json(t, &p)
	base := "/api/v1/projects/" + p.ID + "/secrets"

	r = e.do("PUT", base+"/lower", c, map[string]any{"value": "v", "mode": "env"})
	assert.Equal(t, 400, r.status, string(r.body))
	assert.Equal(t, "invalid_request", r.code(t))
	r = e.do("PUT", base+"/omp%2Fmodels%2Fx%2FapiKey", c, map[string]any{"value": "v", "mode": "proxy"})
	assert.Equal(t, 400, r.status, string(r.body))
	assert.Contains(t, string(r.body), "只能在全局")
	r = e.do("PUT", base+"/git%2Fgitlab.com%2Ftoken", c, map[string]any{"value": "t", "mode": "proxy", "secret": false})
	assert.Equal(t, 400, r.status, string(r.body))
	r = e.do("PUT", base+"/FOO", c, map[string]any{"value": "", "mode": "env"})
	assert.Equal(t, 400, r.status, string(r.body))
	r = e.do("PUT", base+"/FOO", c, map[string]any{"value": "v", "mode": "bogus"})
	assert.Equal(t, 400, r.status, string(r.body))

	r = e.do("PUT", base+"/FOO", c, map[string]any{"value": "bar", "mode": "env", "secret": false})
	require.Equal(t, 200, r.status, string(r.body))
	assert.Contains(t, string(r.body), `"value":"bar"`)
	assert.Contains(t, string(r.body), `"secret":false`)

	r = e.do("PUT", base+"/TOK", c, map[string]any{"value": "sk-x", "mode": "env"})
	require.Equal(t, 200, r.status, string(r.body))
	assert.NotContains(t, string(r.body), "sk-x")
	assert.Contains(t, string(r.body), `"secret":true`)

	r = e.do("PUT", base+"/git%2Fgitlab.com%2Ftoken", c, map[string]any{"value": "glpat-1", "mode": "proxy"})
	require.Equal(t, 200, r.status, string(r.body))
	assert.NotContains(t, string(r.body), "glpat-1")

	r = e.do("GET", base, c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	assert.NotContains(t, string(r.body), "sk-x")
	assert.NotContains(t, string(r.body), "glpat-1")
	var list struct {
		Secrets []struct {
			Name   string
			Mode   string
			Secret bool
			Value  *string
		}
	}
	r.json(t, &list)
	require.Len(t, list.Secrets, 3)
	assert.Equal(t, "FOO", list.Secrets[0].Name)
	require.NotNil(t, list.Secrets[0].Value)
	assert.Equal(t, "bar", *list.Secrets[0].Value)
	assert.Equal(t, "TOK", list.Secrets[1].Name)
	assert.Nil(t, list.Secrets[1].Value)
	assert.Equal(t, "git/gitlab.com/token", list.Secrets[2].Name)
	assert.Equal(t, "proxy", list.Secrets[2].Mode)

	// 全局列表看不到项目条目
	r = e.do("GET", "/api/v1/secrets", c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	assert.NotContains(t, string(r.body), "FOO")

	// 已归档的项目照常可写
	r = e.do("PATCH", "/api/v1/projects/"+p.ID, c, map[string]any{"archived": true})
	require.Equal(t, 200, r.status, string(r.body))
	r = e.do("PUT", base+"/FOO", c, map[string]any{"value": "baz", "mode": "env", "secret": false})
	require.Equal(t, 200, r.status, string(r.body))
	assert.Contains(t, string(r.body), `"value":"baz"`)

	assert.Equal(t, 204, e.do("DELETE", base+"/FOO", c, nil).status)
	r = e.do("DELETE", base+"/FOO", c, nil)
	assert.Equal(t, 404, r.status)
	assert.Equal(t, "not_found", r.code(t))
	assert.Equal(t, 404, e.do("DELETE", "/api/v1/projects/prj_nope/secrets/FOO", c, nil).status)
}
