package github

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// Service 满足 orbs.GitHub（含 M15 的 Call / PrepareReport）。
var _ orbs.GitHub = (*Service)(nil)

// M15 5.1 / 5.2：github_release_status 在 6.1 之前回 not_implemented，未知工具回 invalid_params；没有 GitHub 任务的线程
// 调用任务工具回 no_github_task；错误能按原码转成 ctl 错误。
func TestToolCallNotImplemented(t *testing.T) {
	e := newEnv(t)
	_, err := e.s.Call(e.ctx, "orb-1", "T-1", apigen.LinkCtlGithubCallParams{ThreadID: "T-1", Tool: "github_release_status", Params: map[string]any{}})
	var ge *Error
	require.ErrorAs(t, err, &ge)
	assert.Equal(t, codeNotImplemented, ge.Code)
	ce := ge.CtlError()
	assert.Equal(t, codeNotImplemented, ce.Code)
	assert.Contains(t, ce.Message, "github_release_status")
	_, err = e.s.Call(e.ctx, "orb-1", "T-1", apigen.LinkCtlGithubCallParams{ThreadID: "T-1", Tool: "github_abort_task", Params: map[string]any{"reason": "x"}})
	assert.Equal(t, codeNoTask, codeOf(err))
	for _, tool := range []string{"", "github_prepare", "github_nope"} {
		_, err := e.s.Call(e.ctx, "orb-1", "T-1", apigen.LinkCtlGithubCallParams{ThreadID: "T-1", Tool: tool})
		var ge *Error
		require.ErrorAs(t, err, &ge, tool)
		assert.Equal(t, codeInvalidParams, ge.Code, tool)
	}
}

// M15 5.1：PrepareReport 写入 / 覆盖 github_prepare；PrepareOK 按 orb、sha 与级别判断，pr 级记录同时满足 push，
// 未跳过的记录优先；无效参数不写。
func TestPrepareReport(t *testing.T) {
	e := newEnv(t)
	sha := "0123456789abcdef0123456789abcdef01234567"
	sha2 := "89abcdef0123456789abcdef0123456789abcdef"
	report := func(orbID, s, level string, skipped bool, reason *string) error {
		return e.s.PrepareReport(e.ctx, orbID, apigen.LinkCtlGithubPrepareReportParams{
			ThreadID: "T-1", Sha: s, Level: apigen.LinkCtlGithubPrepareReportParamsLevel(level), Skipped: skipped, Reason: reason,
		})
	}

	ok, _ := e.s.PrepareOK(e.ctx, "orb-1", sha, "push")
	assert.False(t, ok, "没有记录")

	require.NoError(t, report("orb-1", sha, "push", false, nil))
	ok, skipped := e.s.PrepareOK(e.ctx, "orb-1", sha, "push")
	assert.True(t, ok)
	assert.False(t, skipped)
	ok, _ = e.s.PrepareOK(e.ctx, "orb-1", sha, "pr")
	assert.False(t, ok, "push 级记录不满足 pr")
	ok, _ = e.s.PrepareOK(e.ctx, "orb-2", sha, "push")
	assert.False(t, ok, "记录按 orb 区分")

	// pr 级（跳过检查）满足 push 与 pr；大写 sha 规范化为小写
	require.NoError(t, report("orb-1", "89ABCDEF0123456789ABCDEF0123456789ABCDEF", "pr", true, new("docs only")))
	for _, level := range []string{"push", "pr"} {
		ok, skipped = e.s.PrepareOK(e.ctx, "orb-1", sha2, level)
		assert.True(t, ok, level)
		assert.True(t, skipped, level)
	}
	rec, err := e.st.Q.FindGithubPrepare(e.ctx, db.FindGithubPrepareParams{Sha: sha2, OrbID: "orb-1", Levels: []string{"pr"}})
	require.NoError(t, err)
	require.NotNil(t, rec.Reason)
	assert.Equal(t, "docs only", *rec.Reason)
	assert.Equal(t, e.clock.Now().UnixMilli(), rec.At)

	// 同级再次上报覆盖（不再跳过）；push 有未跳过的 pr 记录时 skipped=false
	e.clock.Add(time.Minute)
	require.NoError(t, report("orb-1", sha2, "pr", false, nil))
	ok, skipped = e.s.PrepareOK(e.ctx, "orb-1", sha2, "push")
	assert.True(t, ok)
	assert.False(t, skipped)
	rec, err = e.st.Q.FindGithubPrepare(e.ctx, db.FindGithubPrepareParams{Sha: sha2, OrbID: "orb-1", Levels: []string{"pr"}})
	require.NoError(t, err)
	assert.Nil(t, rec.Reason)
	assert.Equal(t, e.clock.Now().UnixMilli(), rec.At)

	// 无效参数
	for name, err := range map[string]error{
		"level":   report("orb-1", sha, "merge", false, nil),
		"short":   report("orb-1", "abc123", "push", false, nil),
		"nonhex":  report("orb-1", "zz23456789abcdef0123456789abcdef01234567", "push", false, nil),
		"reason":  report("orb-1", sha, "push", true, nil),
		"blankrs": report("orb-1", sha, "push", true, new("  ")),
	} {
		var ge *Error
		require.ErrorAs(t, err, &ge, name)
		assert.Equal(t, codeInvalidParams, ge.Code, name)
	}
	ok, _ = e.s.PrepareOK(e.ctx, "orb-1", sha, "bogus")
	assert.False(t, ok, "未知级别不放行")
}
