package orbs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 写入（EncodeGitHubGates）与读出（ParseGitHubGates）同形：规整后的 JSON 读回来就是规整后的值。
func TestEncodeGitHubGatesRoundTrip(t *testing.T) {
	raw, err := EncodeGitHubGates([]byte(`{"push":true,"pr":false,"fixCmd":" make fix ","checkCmd":"","prHeadings":[" Repro "],"requireClosingRef":false}`))
	require.NoError(t, err)
	require.NotNil(t, raw)
	fix := "make fix"
	assert.Equal(t, &apigen.GitHubGates{Push: true, FixCmd: &fix, PrHeadings: []string{"Repro"}}, ParseGitHubGates(raw))

	raw, err = EncodeGitHubGates([]byte(`{"push":false,"pr":false,"testCmd":"  "}`))
	require.NoError(t, err)
	assert.Nil(t, raw, "什么都没配置 = NULL")

	for _, bad := range []string{`{"push":true}`, `{"pr":true}`, `null`, `[]`, `{"push":true,"pr":true,"prHeadings":[""]}`} {
		_, err := EncodeGitHubGates([]byte(bad))
		assert.Error(t, err, bad)
	}
}

// 安全边界：项目门禁改动后，已 setup 的 orb 缓存的出站策略作废，下一次 policyFor 带上新门禁；其它项目的 orb 不受影响。
func TestInvalidateProjectPoliciesAppliesGates(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	prj, orb, _ := gateStore(t, st)
	gh := stubGitHub{ok: true, gs: GitHubSetup{Login: "oryn-bot", APIOrigin: "https://api.github.com", GitOrigin: "https://github.com"}}
	s := &Service{st: st, setup: &DefaultSetup{Store: st, GitHub: gh}, policies: map[string]egress.Policy{}}

	pol := s.policyFor(orb.ID)
	require.NotNil(t, pol.GitHub)
	assert.Nil(t, pol.GitHub.Gates, "尚无门禁")
	other := egress.Policy{Routes: map[string]egress.Route{"keep": {Origin: "https://keep.example.com"}}}
	s.setPolicy("orb-other", other)

	raw, err := EncodeGitHubGates([]byte(`{"push":true,"pr":true,"prHeadings":["Summary"]}`))
	require.NoError(t, err)
	require.NoError(t, st.Q.SetProjectGithubGates(ctx, db.SetProjectGithubGatesParams{GithubGates: raw, UpdatedAt: 2, ID: prj}))
	assert.Nil(t, s.policyFor(orb.ID).GitHub.Gates, "未作废前仍是缓存的旧策略")

	s.InvalidateProjectPolicies(ctx, prj)
	pol = s.policyFor(orb.ID)
	require.NotNil(t, pol.GitHub)
	require.NotNil(t, pol.GitHub.Gates)
	assert.True(t, pol.GitHub.Gates.Push)
	assert.True(t, pol.GitHub.Gates.PR)
	assert.Equal(t, []string{"Summary"}, pol.GitHub.Gates.PRHeadings)
	assert.Equal(t, other, s.policyFor("orb-other"), "其它项目的 orb 保留缓存")

	// 关掉门禁同样立即生效
	require.NoError(t, st.Q.SetProjectGithubGates(ctx, db.SetProjectGithubGatesParams{GithubGates: nil, UpdatedAt: 3, ID: prj}))
	s.InvalidateProjectPolicies(ctx, prj)
	assert.Nil(t, s.policyFor(orb.ID).GitHub.Gates)
}
