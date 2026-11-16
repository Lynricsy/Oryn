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
	s.setPolicy("orb-other", other, s.policyGeneration())

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

// 安全边界（代际）：policyFor 重建读到旧门禁之后、写缓存之前项目门禁被改并作废，旧策略不写回缓存；重建重来，
// 返回并缓存新门禁。
func TestPolicyRebuildRacingInvalidate(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	prj, orb, _ := gateStore(t, st)
	gh := stubGitHub{ok: true, gs: GitHubSetup{Login: "oryn-bot", APIOrigin: "https://api.github.com", GitOrigin: "https://github.com"}}
	s := &Service{st: st, setup: &DefaultSetup{Store: st, GitHub: gh}, policies: map[string]egress.Policy{}}

	raw, err := EncodeGitHubGates([]byte(`{"push":true,"pr":false}`))
	require.NoError(t, err)
	rebuilds := 0
	s.hookPolicyRebuilt = func(string) {
		rebuilds++
		if rebuilds != 1 {
			return
		}
		// 第一次重建已读完（无门禁）配置、尚未写缓存：此刻 PATCH 提交并作废
		require.NoError(t, st.Q.SetProjectGithubGates(ctx, db.SetProjectGithubGatesParams{GithubGates: raw, UpdatedAt: 2, ID: prj}))
		s.InvalidateProjectPolicies(ctx, prj)
	}
	pol := s.policyFor(orb.ID)
	assert.Equal(t, 2, rebuilds, "撞上作废后重建一次")
	require.NotNil(t, pol.GitHub)
	require.NotNil(t, pol.GitHub.Gates, "返回新门禁")
	assert.True(t, pol.GitHub.Gates.Push)
	s.hookPolicyRebuilt = nil
	s.mu.Lock()
	cached, ok := s.policies[orb.ID]
	s.mu.Unlock()
	require.True(t, ok, "新策略已缓存")
	require.NotNil(t, cached.GitHub.Gates)
	assert.True(t, cached.GitHub.Gates.Push)

	// setup 路径同理：读配置之前取的代际在作废后失效，写回被拒并清掉缓存，下一次 policyFor 按当前配置重建
	gen := s.policyGeneration()
	plan, err := s.setup.Plan(ctx, orb)
	require.NoError(t, err)
	require.NoError(t, st.Q.SetProjectGithubGates(ctx, db.SetProjectGithubGatesParams{GithubGates: nil, UpdatedAt: 3, ID: prj}))
	s.InvalidateProjectPolicies(ctx, prj)
	assert.False(t, s.setPolicy(orb.ID, plan.Egress, gen), "旧代际的策略不写缓存")
	s.mu.Lock()
	_, ok = s.policies[orb.ID]
	s.mu.Unlock()
	assert.False(t, ok)
	assert.Nil(t, s.policyFor(orb.ID).GitHub.Gates, "重建得到关闭后的门禁")

	// 一直撞上作废：重建次数有上限，用尽后本次请求拿拒绝策略（不放行 GitHub 与 git、不写缓存），而不是最后一次读到的配置
	s.mu.Lock()
	clear(s.policies)
	s.mu.Unlock()
	require.NoError(t, st.Q.SetProjectGithubGates(ctx, db.SetProjectGithubGatesParams{GithubGates: raw, UpdatedAt: 4, ID: prj}))
	rebuilds = 0
	s.hookPolicyRebuilt = func(string) {
		rebuilds++
		s.InvalidateProjectPolicies(ctx, prj)
	}
	denied := s.policyFor(orb.ID)
	assert.Equal(t, policyRebuildRetries, rebuilds)
	assert.Nil(t, denied.GitHub, "用尽后不放行 GitHub")
	assert.True(t, denied.NoGit, "用尽后关闭 git 路由")
	assert.Empty(t, denied.Routes)
	s.mu.Lock()
	_, ok = s.policies[orb.ID]
	s.mu.Unlock()
	assert.False(t, ok, "没有一次重建未撞上作废，不缓存")
}

// 门禁哈希只随规范化后的配置变化：等价写法同哈希，命令改动换哈希，未配置与空串同哈希。
func TestGitHubGatesHash(t *testing.T) {
	enc := func(body string) *string {
		t.Helper()
		raw, err := EncodeGitHubGates([]byte(body))
		require.NoError(t, err)
		return raw
	}
	a := GitHubGatesHash(enc(`{"push":true,"pr":false,"checkCmd":"make check"}`))
	assert.Regexp(t, `^[0-9a-f]{64}$`, a)
	assert.Equal(t, a, GitHubGatesHash(enc(`{"pr":false,"checkCmd":"  make check ","push":true,"fixCmd":""}`)))
	assert.NotEqual(t, a, GitHubGatesHash(enc(`{"push":true,"pr":false,"checkCmd":"make check2"}`)))
	empty := ""
	assert.Equal(t, GitHubGatesHash(nil), GitHubGatesHash(&empty))
	assert.NotEqual(t, a, GitHubGatesHash(nil))
}
