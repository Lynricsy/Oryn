package github

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// Service 满足 orbs.GitHub（含 M15 的 Call / PrepareReport）。
var _ orbs.GitHub = (*Service)(nil)

// M15 5.1 / 5.2 / 6.1：不是发布线程时 github_release_status 回 not_release_thread，未知工具回 invalid_params；没有 GitHub
// 任务的线程调用任务工具回 no_github_task；错误能按原码转成 ctl 错误。
func TestToolCallErrors(t *testing.T) {
	e := newEnv(t)
	_, err := e.s.Call(e.ctx, "orb-1", "T-1", apigen.LinkCtlGithubCallParams{ThreadID: "T-1", Tool: "github_release_status", Params: map[string]any{}})
	var ge *Error
	require.ErrorAs(t, err, &ge)
	assert.Equal(t, codeNotRelease, ge.Code)
	ce := ge.CtlError()
	assert.Equal(t, codeNotRelease, ce.Code)
	assert.Contains(t, ce.Message, "release")
	_, err = e.s.Call(e.ctx, "orb-1", "T-1", apigen.LinkCtlGithubCallParams{ThreadID: "T-1", Tool: "github_abort_task", Params: map[string]any{"reason": "x"}})
	assert.Equal(t, codeNoTask, codeOf(err))
	for _, tool := range []string{"", "github_prepare", "github_nope"} {
		_, err := e.s.Call(e.ctx, "orb-1", "T-1", apigen.LinkCtlGithubCallParams{ThreadID: "T-1", Tool: tool})
		var ge *Error
		require.ErrorAs(t, err, &ge, tool)
		assert.Equal(t, codeInvalidParams, ge.Code, tool)
	}
}

// M15 5.1：PrepareReport 写入 / 覆盖 github_prepare（含门禁哈希）；Prepared 按 orb、sha 与级别判断，pr 级记录同时满足
// push，未跳过的记录优先；无效参数（含缺门禁哈希）不写。
func TestPrepareReport(t *testing.T) {
	e := newEnv(t)
	prj := e.project("")
	orb1, orb2 := e.orb(prj, "", "").ID, e.orb(prj, "", "").ID
	hash := orbs.GitHubGatesHash(nil)
	sha := "0123456789abcdef0123456789abcdef01234567"
	sha2 := "89abcdef0123456789abcdef0123456789abcdef"
	reportH := func(orbID, s, level string, skipped bool, reason *string, h string) error {
		return e.s.PrepareReport(e.ctx, orbID, apigen.LinkCtlGithubPrepareReportParams{
			ThreadID: "T-1", Sha: s, Level: apigen.LinkCtlGithubPrepareReportParamsLevel(level), Skipped: skipped, Reason: reason, GatesHash: h,
		})
	}
	report := func(orbID, s, level string, skipped bool, reason *string) error {
		return reportH(orbID, s, level, skipped, reason, hash)
	}
	pr := func(orbID, s, level string) egress.PrepareState { return e.s.Prepared(e.ctx, orbID, s, level) }
	rec := func(s string) db.GithubPrepare {
		t.Helper()
		recs, err := e.st.Q.ListGithubPrepares(e.ctx, db.ListGithubPreparesParams{Sha: s, OrbID: orb1, Levels: []string{"pr"}})
		require.NoError(t, err)
		require.Len(t, recs, 1)
		return recs[0]
	}

	assert.Equal(t, egress.PrepareNone, pr(orb1, sha, "push"), "没有记录")

	require.NoError(t, report(orb1, sha, "push", false, nil))
	assert.Equal(t, egress.PreparePassed, pr(orb1, sha, "push"))
	assert.Equal(t, egress.PrepareNone, pr(orb1, sha, "pr"), "push 级记录不满足 pr")
	assert.Equal(t, egress.PrepareNone, pr(orb2, sha, "push"), "记录按 orb 区分")

	// pr 级（跳过检查）满足 push 与 pr；大写 sha 规范化为小写
	require.NoError(t, report(orb1, "89ABCDEF0123456789ABCDEF0123456789ABCDEF", "pr", true, new("docs only")))
	for _, level := range []string{"push", "pr"} {
		assert.Equal(t, egress.PrepareSkipped, pr(orb1, sha2, level), level)
	}
	r := rec(sha2)
	require.NotNil(t, r.Reason)
	assert.Equal(t, "docs only", *r.Reason)
	assert.Equal(t, hash, r.GatesHash)
	assert.Equal(t, e.clock.Now().UnixMilli(), r.At)

	// 同级再次上报覆盖（不再跳过）；push 有未跳过的 pr 记录时不算跳过
	e.clock.Add(time.Minute)
	require.NoError(t, report(orb1, sha2, "pr", false, nil))
	assert.Equal(t, egress.PreparePassed, pr(orb1, sha2, "push"))
	r = rec(sha2)
	assert.Nil(t, r.Reason)
	assert.Equal(t, e.clock.Now().UnixMilli(), r.At)

	// 项目门禁改动：旧哈希的记录一律过期（跳过检查的也一样），以新哈希重新上报后放行
	raw, err := orbs.EncodeGitHubGates([]byte(`{"push":true,"pr":true,"checkCmd":"make check"}`))
	require.NoError(t, err)
	require.NoError(t, e.st.Q.SetProjectGithubGates(e.ctx, db.SetProjectGithubGatesParams{GithubGates: raw, UpdatedAt: 2, ID: prj}))
	assert.Equal(t, egress.PrepareStale, pr(orb1, sha, "push"))
	assert.Equal(t, egress.PrepareStale, pr(orb1, sha2, "pr"))
	require.NoError(t, report(orb1, sha2, "push", true, new("docs only")))
	assert.Equal(t, egress.PrepareStale, pr(orb1, sha2, "push"), "仍是旧哈希")
	newHash := orbs.GitHubGatesHash(raw)
	require.NoError(t, reportH(orb1, sha2, "push", true, new("docs only"), newHash))
	assert.Equal(t, egress.PrepareSkipped, pr(orb1, sha2, "push"), "新哈希的跳过记录优先于旧哈希的 pr 记录")
	assert.Equal(t, egress.PrepareStale, pr(orb1, sha2, "pr"))
	require.NoError(t, reportH(orb1, sha2, "pr", false, nil, newHash))
	assert.Equal(t, egress.PreparePassed, pr(orb1, sha2, "push"))

	// 无效参数
	for name, err := range map[string]error{
		"level":     report(orb1, sha, "merge", false, nil),
		"short":     report(orb1, "abc123", "push", false, nil),
		"nonhex":    report(orb1, "zz23456789abcdef0123456789abcdef01234567", "push", false, nil),
		"reason":    report(orb1, sha, "push", true, nil),
		"blankrs":   report(orb1, sha, "push", true, new("  ")),
		"nohash":    reportH(orb1, sha, "push", false, nil, ""),
		"shorthash": reportH(orb1, sha, "push", false, nil, hash[:40]),
		"upperhash": reportH(orb1, sha, "push", false, nil, strings.ToUpper(hash)),
	} {
		var ge *Error
		require.ErrorAs(t, err, &ge, name)
		assert.Equal(t, codeInvalidParams, ge.Code, name)
	}
	assert.Equal(t, egress.PrepareNone, pr(orb1, sha, "bogus"), "未知级别不放行")
}
