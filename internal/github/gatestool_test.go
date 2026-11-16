package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// M15：github_gates 现查调用方 orb 所属项目当前的门禁配置与哈希（PATCH 之后立即可见）；没有项目时为未配置；不接受参数；
// 它是 orbd 内部工具，不出现在未知工具的可用列表里。
func TestToolGates(t *testing.T) {
	e := newEnv(t)
	prj := e.project("")
	orbID := e.orb(prj, "", "").ID
	call := func(orbID string, params map[string]any) (apigen.LinkCtlGithubCallResult, error) {
		return e.s.Call(e.ctx, orbID, "T-1", apigen.LinkCtlGithubCallParams{ThreadID: "T-1", Tool: "github_gates", Params: params})
	}

	res, err := call(orbID, map[string]any{})
	require.NoError(t, err)
	assert.Nil(t, res.Details["gates"], "未配置")
	assert.Equal(t, orbs.GitHubGatesHash(nil), res.Details["gatesHash"])
	assert.Contains(t, res.Text, "no GitHub gates")

	raw, err := orbs.EncodeGitHubGates([]byte(`{"push":true,"pr":false,"checkCmd":"make check"}`))
	require.NoError(t, err)
	require.NoError(t, e.st.Q.SetProjectGithubGates(e.ctx, db.SetProjectGithubGatesParams{GithubGates: raw, UpdatedAt: 2, ID: prj}))
	res, err = call(orbID, nil)
	require.NoError(t, err)
	g, ok := res.Details["gates"].(*apigen.GitHubGates)
	require.True(t, ok, "%T", res.Details["gates"])
	require.NotNil(t, g)
	assert.True(t, g.Push)
	require.NotNil(t, g.CheckCmd)
	assert.Equal(t, "make check", *g.CheckCmd)
	assert.Equal(t, orbs.GitHubGatesHash(raw), res.Details["gatesHash"])
	assert.NotEqual(t, orbs.GitHubGatesHash(nil), res.Details["gatesHash"])

	res, err = call(e.orb("", "", "").ID, nil)
	require.NoError(t, err)
	assert.Nil(t, res.Details["gates"], "没有项目 = 未配置")

	_, err = call(orbID, map[string]any{"checkCmd": "rm -rf /"})
	assert.Equal(t, codeInvalidParams, codeOf(err), "不接受参数")

	_, err = e.s.Call(e.ctx, orbID, "T-1", apigen.LinkCtlGithubCallParams{ThreadID: "T-1", Tool: "github_nope"})
	var ge *Error
	require.ErrorAs(t, err, &ge)
	assert.NotContains(t, ge.Message, "github_gates", "内部工具不列为可用工具")
}
