package orbs

// M24 阶段 ④：项目级环境变量与 Git 令牌按「项目 → 全局」解析（setup 计划、出站代理 git 令牌）。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// projectSecretsEnv 是真实 store + vault，带两个项目 A、B。
func projectSecretsEnv(t *testing.T) (st *store.Store, v *secrets.Vault, a, b string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.New(make([]byte, 32))
	require.NoError(t, err)
	v = secrets.NewVault(st, box)
	a, b = ids.New("prj"), ids.New("prj")
	for _, p := range []string{a, b} {
		require.NoError(t, st.Q.CreateProject(ctx, db.CreateProjectParams{ID: p, Name: p, CreatedAt: 1, UpdatedAt: 1}))
	}
	return st, v, a, b
}

// 全局 env 密钥 < 项目条目；项目 git 主机只并给本项目；runner 与协调宿主都拿不到项目条目。
func TestPlanProjectEnvPrecedence(t *testing.T) {
	ctx := context.Background()
	st, v, a, b := projectSecretsEnv(t)
	_, err := v.Put(ctx, "FOO", "env", "baz", nil)
	require.NoError(t, err)
	_, err = v.PutProject(ctx, a, "FOO", "env", "bar", false)
	require.NoError(t, err)
	_, err = v.PutProject(ctx, a, "git/gitlab.example.com/token", "proxy", "t", true)
	require.NoError(t, err)
	d := &DefaultSetup{Store: st, Vault: v}

	pa, err := d.Plan(ctx, db.Orb{ID: "orb-a", ProjectID: &a})
	require.NoError(t, err)
	assert.Equal(t, "bar", pa.Params.Env["FOO"], "项目条目覆盖同名全局 env 密钥")
	assert.Contains(t, pa.Params.GitHosts, "gitlab.example.com")

	pb, err := d.Plan(ctx, db.Orb{ID: "orb-b", ProjectID: &b})
	require.NoError(t, err)
	assert.Equal(t, "baz", pb.Params.Env["FOO"], "别的项目用全局值")
	assert.NotContains(t, pb.Params.GitHosts, "gitlab.example.com")

	pr, err := d.Plan(ctx, db.Orb{ID: "orb-r", ProjectID: &a, Kind: KindRunner})
	require.NoError(t, err)
	assert.Empty(t, pr.Params.Env, "runner 不下发环境变量")
	assert.Empty(t, pr.Params.GitHosts)

	pc, err := d.Plan(ctx, db.Orb{ID: "orb-c", ProjectID: &a, Coordinator: true})
	require.NoError(t, err)
	assert.NotContains(t, pc.Params.Env, "FOO", "协调宿主不下发项目环境变量")
}

func TestUnionHosts(t *testing.T) {
	assert.Equal(t, []string{"gitlab.com", "a.example"}, unionHosts([]string{"gitlab.com"}, []string{"GitLab.com", "a.example"}))
	assert.Equal(t, []string{"x"}, unionHosts(nil, []string{"x"}))
	assert.Empty(t, unionHosts(nil, nil))
}

// 出站代理的 git 令牌：项目 → 全局；读项目令牌失败时拒绝而不回落；GitHub 集成管着的 github.com 仍走 OAuth。
func TestEgressResolverProjectGitToken(t *testing.T) {
	ctx := context.Background()
	st, v, a, b := projectSecretsEnv(t)
	_, err := v.Put(ctx, "git/gitlab.com/token", "proxy", "gtok", nil)
	require.NoError(t, err)
	_, err = v.Put(ctx, "git/github.com/token", "proxy", "gh-global", nil)
	require.NoError(t, err)
	_, err = v.PutProject(ctx, a, "git/gitlab.com/token", "proxy", "ptok", true)
	require.NoError(t, err)
	_, err = v.PutProject(ctx, a, "git/github.com/token", "proxy", "gh-project", true)
	require.NoError(t, err)

	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	mkOrb := func(project *string) string {
		id := "orb-" + strings.ToLower(ids.New("x")[2:])
		require.NoError(t, st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: id, ProviderID: "prov_1", Type: "small", Image: "img",
			State: StateReady, TokenHash: ids.Token("h"), CreatedAt: 1, ProjectID: project}))
		return id
	}
	orbA, orbB, orbNone := mkOrb(&a), mkOrb(&b), mkOrb(nil)
	res := func(sec EgressSecrets, g GitHub, orbID string) egressResolver {
		s := &Service{st: st, secrets: sec}
		if g != nil {
			s.github = g
		}
		return egressResolver{s: s, orbID: orbID}
	}
	get := func(r egressResolver, host string) string {
		t.Helper()
		tok, ok := r.GitToken(host)
		if !ok {
			return "<denied>"
		}
		return tok
	}

	assert.Equal(t, "ptok", get(res(v, nil, orbA), "gitlab.com"), "项目令牌优先")
	assert.Equal(t, "gtok", get(res(v, nil, orbB), "gitlab.com"), "别的项目用全局令牌")
	assert.Equal(t, "gtok", get(res(v, nil, orbNone), "gitlab.com"), "无项目用全局令牌")
	assert.Equal(t, "gtok", get(res(v, nil, "orb-gone"), "gitlab.com"), "orb 不存在 = 无项目")

	bad := gitVault{projectErr: errors.New("boom")}
	assert.Equal(t, "<denied>", get(res(bad, nil, orbA), "gitlab.com"), "读项目令牌失败时拒绝，不回落到全局")
	assert.Equal(t, "tok", get(res(bad, nil, orbNone), "gitlab.com"), "无项目时不读项目令牌")

	assert.Equal(t, "oauth", get(res(v, stubGitHub{tok: "oauth", state: GitHubTokenOK}, orbA), "github.com"),
		"GitHub 集成管着时 github.com 走 OAuth，项目令牌不生效")
	assert.Equal(t, "gh-project", get(res(v, stubGitHub{state: GitHubTokenUnmanaged}, orbA), "github.com"),
		"不归 GitHub 集成管时用项目的 github.com 令牌")
	assert.Equal(t, "gh-global", get(res(v, stubGitHub{state: GitHubTokenUnmanaged}, orbB), "github.com"))

	require.NoError(t, st.Close())
	assert.Equal(t, "<denied>", get(res(v, nil, orbA), "gitlab.com"), "读 orb 行失败时拒绝")
}
