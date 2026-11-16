package orbdrt

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// M15 5.1：orbd.sock 的 github.call 原样转成 ctl github.call；无效线程 id 与缺工具名不转发；控制面的错误码原样回到 sock。
func TestGithubCallForward(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)

	calls := make(chan apigen.LinkCtlGithubCallParams, 4)
	var ctlErr atomic.Pointer[link.CtlError]
	cp.extra = func(method string, params json.RawMessage) (any, bool, error) {
		if method != string(apigen.MethodGithubCall) {
			return nil, false, nil
		}
		var p apigen.LinkCtlGithubCallParams
		_ = json.Unmarshal(params, &p)
		calls <- p
		if e := ctlErr.Load(); e != nil {
			return nil, true, e
		}
		return apigen.LinkCtlGithubCallResult{Text: "ok " + p.Tool, Details: map[string]any{"tool": p.Tool}}, true, nil
	}

	raw, err := sockCall(t, rt, orbsock.MethodGithubCall, apigen.LinkCtlGithubCallParams{
		ThreadID: libThread, Tool: "github_task_state", Params: map[string]any{"state": "fixing"},
	}, nil)
	require.NoError(t, err)
	var res apigen.LinkCtlGithubCallResult
	require.NoError(t, json.Unmarshal(raw, &res))
	assert.Equal(t, "ok github_task_state", res.Text)
	assert.Equal(t, "github_task_state", res.Details["tool"])
	p := <-calls
	assert.Equal(t, libThread, p.ThreadID)
	assert.Equal(t, "github_task_state", p.Tool)
	assert.Equal(t, map[string]any{"state": "fixing"}, p.Params)

	// 缺 params 时补成空对象（控制面按工具严格解码）
	_, err = sockCall(t, rt, orbsock.MethodGithubCall, map[string]any{"threadId": libThread, "tool": "github_abort_task"}, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{}, (<-calls).Params)

	for _, bad := range []map[string]any{
		{"threadId": "../x", "tool": "github_task_state"},
		{"threadId": libThread, "tool": " "},
	} {
		_, err := sockCall(t, rt, orbsock.MethodGithubCall, bad, nil)
		assert.Equal(t, orbsock.CodeInvalidParams, sockErrCode(err), "%v", bad)
	}
	assert.Empty(t, calls, "无效参数不转发")

	ctlErr.Store(&link.CtlError{Code: "not_implemented", Message: "github_autoclose 尚未实现"})
	_, err = sockCall(t, rt, orbsock.MethodGithubCall, apigen.LinkCtlGithubCallParams{ThreadID: libThread, Tool: "github_autoclose", Params: map[string]any{}}, nil)
	var se *orbsock.Error
	require.ErrorAs(t, err, &se)
	assert.Equal(t, "not_implemented", se.Code)
	assert.Equal(t, "github_autoclose 尚未实现", se.Message)
	<-calls
}

// M15 5.1：orbd.sock 的 github.prepare 在线程工作目录（线程未在本 orbd 运行时为 orb 工作目录）里按下发的门禁执行；
// 通过时经 ctl github.prepare.report 上报 (sha, level, skipped, reason) 并回结果；失败不上报；参数无效不执行。
func TestGithubPrepareSock(t *testing.T) {
	repo := newPrepRepo(t)
	head := repo.commit("a.txt", "feat: a")
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)
	setWorkdir(rt, repo.dir)
	setGates := func(g apigen.GitHubGates) {
		rt.envMu.Lock()
		rt.githubGates = &g
		rt.envMu.Unlock()
	}
	setGates(apigen.GitHubGates{Push: true, CheckCmd: new("echo checked")})

	reports := make(chan apigen.LinkCtlGithubPrepareReportParams, 4)
	cp.extra = func(method string, params json.RawMessage) (any, bool, error) {
		if method != string(apigen.MethodGithubPrepareReport) {
			return nil, false, nil
		}
		var p apigen.LinkCtlGithubPrepareReportParams
		_ = json.Unmarshal(params, &p)
		reports <- p
		return apigen.LinkCtlOkResult{Ok: true}, true, nil
	}
	call := func(p orbsock.GithubPrepareParams) (PrepareResult, error) {
		t.Helper()
		raw, err := sockCall(t, rt, orbsock.MethodGithubPrepare, p, nil)
		if err != nil {
			return PrepareResult{}, err
		}
		var res PrepareResult
		require.NoError(t, json.Unmarshal(raw, &res))
		return res, nil
	}

	res, err := call(orbsock.GithubPrepareParams{ThreadID: libThread, Level: "push"})
	require.NoError(t, err)
	require.True(t, res.OK, "%+v", res)
	assert.Equal(t, head, res.SHA)
	assert.False(t, res.Skipped)
	var checked bool
	for _, s := range res.Steps {
		if s.Name == "check" {
			checked = true
			assert.Contains(t, s.Output, "checked")
		}
	}
	assert.True(t, checked, "运行了下发的检查命令：%+v", res.Steps)
	rep := recvReport(t, reports)
	assert.Equal(t, libThread, rep.ThreadID)
	assert.Equal(t, head, rep.Sha)
	assert.EqualValues(t, "push", rep.Level)
	assert.False(t, rep.Skipped)
	assert.Nil(t, rep.Reason)

	// 跳过检查：带 reason 上报 skipped
	res, err = call(orbsock.GithubPrepareParams{ThreadID: libThread, Level: "pr", SkipChecks: true, Reason: "docs only"})
	require.NoError(t, err)
	require.True(t, res.OK, "%+v", res)
	assert.True(t, res.Skipped)
	rep = recvReport(t, reports)
	assert.EqualValues(t, "pr", rep.Level)
	assert.True(t, rep.Skipped)
	require.NotNil(t, rep.Reason)
	assert.Equal(t, "docs only", *rep.Reason)

	// 检查失败：回失败结果，不上报
	setGates(apigen.GitHubGates{Push: true, CheckCmd: new("echo boom; exit 3")})
	res, err = call(orbsock.GithubPrepareParams{ThreadID: libThread, Level: "push"})
	require.NoError(t, err)
	assert.False(t, res.OK)
	assert.Empty(t, res.SHA)
	assert.Contains(t, res.Failure, "check")

	// 参数无效：不执行、不上报
	for _, bad := range []orbsock.GithubPrepareParams{
		{ThreadID: libThread, Level: "merge"},
		{ThreadID: libThread, Level: "push", SkipChecks: true},
		{ThreadID: "", Level: "push"},
	} {
		_, err := call(bad)
		assert.Equal(t, orbsock.CodeInvalidParams, sockErrCode(err), "%+v", bad)
	}
	assert.Empty(t, reports, "失败与无效参数都不上报")
}

func recvReport(t *testing.T, ch <-chan apigen.LinkCtlGithubPrepareReportParams) apigen.LinkCtlGithubPrepareReportParams {
	t.Helper()
	select {
	case p := <-ch:
		return p
	case <-time.After(10 * time.Second):
		t.Fatal("没有收到 github.prepare.report")
		return apigen.LinkCtlGithubPrepareReportParams{}
	}
}
