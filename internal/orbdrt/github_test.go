package orbdrt

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
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

// fakeGates 是控制面对 github_gates 的假答复：项目当前的门禁与哈希，可在调用之间改（模拟项目门禁的 PATCH）。
type fakeGates struct {
	mu      sync.Mutex
	gates   *apigen.GitHubGates
	hash    string
	err     *link.CtlError // 非 nil：github_gates 回这个错误
	reply   any            // 非 nil：原样作为答复（畸形答复）
	missing bool           // 控制面不认识 github_gates（不处理该请求）
	after   func()         // 答复之后调用一次（模拟 prepare 与上报之间的 PATCH）
	queries []apigen.LinkCtlGithubCallParams
}

var (
	hashA = strings.Repeat("a", 64)
	hashB = strings.Repeat("b", 64)
	hashC = strings.Repeat("c", 64)
)

func (f *fakeGates) set(g apigen.GitHubGates, hash string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gates, f.hash = &g, hash
}

// handle 处理 ctl github.call；不是 github_gates 时 handled 为假。after 在算好答复之后、答复发出之前同步调用。
func (f *fakeGates) handle(params json.RawMessage) (res any, handled bool, err error) {
	var p apigen.LinkCtlGithubCallParams
	_ = json.Unmarshal(params, &p)
	if p.Tool != "github_gates" {
		return nil, false, nil
	}
	f.mu.Lock()
	f.queries = append(f.queries, p)
	after := f.after
	f.after = nil
	switch {
	case f.missing:
		res, handled = nil, false
	case f.err != nil:
		res, handled, err = nil, true, f.err
	case f.reply != nil:
		res, handled = f.reply, true
	default:
		res, handled = apigen.LinkCtlGithubCallResult{Text: "gates", Details: map[string]any{"gates": f.gates, "gatesHash": f.hash}}, true
	}
	f.mu.Unlock()
	if after != nil {
		after()
	}
	return res, handled, err
}

func (f *fakeGates) queryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queries)
}

func (f *fakeGates) lastQuery() apigen.LinkCtlGithubCallParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queries[len(f.queries)-1]
}

// M15 5.1：orbd.sock 的 github.prepare 每次先经 ctl github.call（github_gates）现查项目当前的门禁，在线程工作目录
// （线程未在本 orbd 运行时为 orb 工作目录）里按它执行，不用 setup 时下发的那份；项目门禁改了，运行中的 orb 下一次
// prepare 就跑新命令。通过时经 ctl github.prepare.report 上报 (sha, level, skipped, reason, gatesHash) 并回结果；失败不上报；
// 参数无效不执行。prepare 与上报之间门禁又被改时，上报的仍是这次所用配置的哈希（出站门禁据此拒绝，见 github 包）。
func TestGithubPrepareSock(t *testing.T) {
	repo := newPrepRepo(t)
	head := repo.commit("a.txt", "feat: a")
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)
	setWorkdir(rt, repo.dir)
	fg := &fakeGates{}
	fg.set(apigen.GitHubGates{Push: true, CheckCmd: new("echo checked")}, hashA)

	reports := make(chan apigen.LinkCtlGithubPrepareReportParams, 4)
	cp.extra = func(method string, params json.RawMessage) (any, bool, error) {
		switch method {
		case string(apigen.MethodGithubCall):
			return fg.handle(params)
		case string(apigen.MethodGithubPrepareReport):
			var p apigen.LinkCtlGithubPrepareReportParams
			_ = json.Unmarshal(params, &p)
			reports <- p
			return apigen.LinkCtlOkResult{Ok: true}, true, nil
		}
		return nil, false, nil
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
	checkOutput := func(res PrepareResult) string {
		t.Helper()
		for _, s := range res.Steps {
			if s.Name == "check" {
				return s.Output
			}
		}
		t.Fatalf("没有运行检查命令：%+v", res.Steps)
		return ""
	}

	res, err := call(orbsock.GithubPrepareParams{ThreadID: libThread, Level: "push"})
	require.NoError(t, err)
	require.True(t, res.OK, "%+v", res)
	assert.Equal(t, head, res.SHA)
	assert.False(t, res.Skipped)
	assert.Contains(t, checkOutput(res), "checked", "运行控制面现查到的检查命令")
	require.Equal(t, 1, fg.queryCount())
	q := fg.lastQuery()
	assert.Equal(t, libThread, q.ThreadID)
	assert.Equal(t, map[string]any{}, q.Params, "不带 agent 的任何参数")
	rep := recvReport(t, reports)
	assert.Equal(t, libThread, rep.ThreadID)
	assert.Equal(t, head, rep.Sha)
	assert.EqualValues(t, "push", rep.Level)
	assert.False(t, rep.Skipped)
	assert.Nil(t, rep.Reason)
	assert.Equal(t, hashA, rep.GatesHash)

	// 项目门禁被 PATCH：运行中的 orb 下一次 prepare 就跑新命令、带新哈希上报（不用等 setup / resume）
	fg.set(apigen.GitHubGates{Push: true, CheckCmd: new("echo patched")}, hashB)
	res, err = call(orbsock.GithubPrepareParams{ThreadID: libThread, Level: "push"})
	require.NoError(t, err)
	require.True(t, res.OK, "%+v", res)
	assert.Contains(t, checkOutput(res), "patched")
	assert.Equal(t, hashB, recvReport(t, reports).GatesHash)

	// 跳过检查：带 reason 与门禁哈希上报 skipped
	res, err = call(orbsock.GithubPrepareParams{ThreadID: libThread, Level: "pr", SkipChecks: true, Reason: "docs only"})
	require.NoError(t, err)
	require.True(t, res.OK, "%+v", res)
	assert.True(t, res.Skipped)
	rep = recvReport(t, reports)
	assert.EqualValues(t, "pr", rep.Level)
	assert.True(t, rep.Skipped)
	require.NotNil(t, rep.Reason)
	assert.Equal(t, "docs only", *rep.Reason)
	assert.Equal(t, hashB, rep.GatesHash)

	// prepare 与上报之间门禁又被改：上报的是这次实际所用配置（B）的哈希，不是改后的 C
	fg.mu.Lock()
	fg.after = func() { fg.set(apigen.GitHubGates{Push: true, CheckCmd: new("echo third")}, hashC) }
	fg.mu.Unlock()
	res, err = call(orbsock.GithubPrepareParams{ThreadID: libThread, Level: "push"})
	require.NoError(t, err)
	require.True(t, res.OK, "%+v", res)
	assert.Contains(t, checkOutput(res), "patched")
	assert.Equal(t, hashB, recvReport(t, reports).GatesHash)

	// 检查失败：回失败结果，不上报
	fg.set(apigen.GitHubGates{Push: true, CheckCmd: new("echo boom; exit 3")}, hashC)
	res, err = call(orbsock.GithubPrepareParams{ThreadID: libThread, Level: "push"})
	require.NoError(t, err)
	assert.False(t, res.OK)
	assert.Empty(t, res.SHA)
	assert.Contains(t, res.Failure, "check")

	// 下发的提交身份与区间内提交的作者不符：在作者检查处失败，不上报
	rt.envMu.Lock()
	rt.gitIdentity = &apigen.GitIdentity{Name: "Someone Else", Email: "else@example.com"}
	rt.envMu.Unlock()
	fg.set(apigen.GitHubGates{Push: true}, hashC)
	res, err = call(orbsock.GithubPrepareParams{ThreadID: libThread, Level: "push"})
	require.NoError(t, err)
	assert.False(t, res.OK)
	assert.Contains(t, res.Failure, "author")

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

// M15：现查项目门禁失败（控制面回错、不认识 github_gates、答复畸形）时 github.prepare 直接失败，错误说明原因；不回落到
// setup 时下发的门禁，不执行任何命令（包括 skipChecks），不上报。
func TestGithubPrepareGatesQueryFails(t *testing.T) {
	repo := newPrepRepo(t)
	repo.commit("a.txt", "feat: a")
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)
	setWorkdir(rt, repo.dir)
	marker := filepath.Join(t.TempDir(), "ran")
	touch := "touch " + marker
	fg := &fakeGates{}
	fg.set(apigen.GitHubGates{Push: true, FixCmd: &touch, CheckCmd: &touch}, hashA)
	var reports atomic.Int32
	cp.extra = func(method string, params json.RawMessage) (any, bool, error) {
		switch method {
		case string(apigen.MethodGithubCall):
			return fg.handle(params)
		case string(apigen.MethodGithubPrepareReport):
			reports.Add(1)
			return apigen.LinkCtlOkResult{Ok: true}, true, nil
		}
		return nil, false, nil
	}

	cases := []struct {
		name, code, msg string
		apply           func()
	}{
		{"ctl 错误", "internal", "database is locked", func() { fg.err = &link.CtlError{Code: "internal", Message: "database is locked"} }},
		{"线程不属于 orb", "forbidden", "not belong", func() { fg.err = &link.CtlError{Code: "forbidden", Message: "thread does not belong to this orb"} }},
		{"控制面不认识", orbsock.CodeMethodNotFound, "", func() { fg.missing = true }},
		{"缺 gates", orbsock.CodeInternal, "malformed", func() {
			fg.reply = apigen.LinkCtlGithubCallResult{Text: "x", Details: map[string]any{"gatesHash": hashA}}
		}},
		{"缺哈希", orbsock.CodeInternal, "malformed", func() {
			fg.reply = apigen.LinkCtlGithubCallResult{Text: "x", Details: map[string]any{"gates": nil}}
		}},
		{"哈希形状不对", orbsock.CodeInternal, "malformed", func() {
			fg.reply = apigen.LinkCtlGithubCallResult{Text: "x", Details: map[string]any{"gates": nil, "gatesHash": "abc"}}
		}},
		{"gates 形状不对", orbsock.CodeInternal, "malformed gates", func() {
			fg.reply = apigen.LinkCtlGithubCallResult{Text: "x", Details: map[string]any{"gates": []int{1}, "gatesHash": hashA}}
		}},
	}
	for _, c := range cases {
		for _, p := range []orbsock.GithubPrepareParams{
			{ThreadID: libThread, Level: "push"},
			{ThreadID: libThread, Level: "pr", SkipChecks: true, Reason: "docs only"},
		} {
			fg.mu.Lock()
			fg.err, fg.missing, fg.reply = nil, false, nil
			c.apply()
			fg.mu.Unlock()
			before := fg.queryCount()
			_, err := sockCall(t, rt, orbsock.MethodGithubPrepare, p, nil)
			var se *orbsock.Error
			require.ErrorAs(t, err, &se, c.name)
			if c.code != orbsock.CodeMethodNotFound {
				assert.Equal(t, c.code, se.Code, c.name)
			}
			assert.Contains(t, se.Message, "current gate configuration", c.name)
			assert.Contains(t, se.Message, "nothing was run", c.name)
			assert.Contains(t, se.Message, c.msg, c.name)
			assert.Equal(t, before+1, fg.queryCount(), c.name)
		}
	}
	assert.NoFileExists(t, marker, "没有执行任何门禁命令")
	assert.Zero(t, reports.Load(), "不上报")

	// 控制面恢复后照常执行
	fg.mu.Lock()
	fg.err, fg.missing, fg.reply = nil, false, nil
	fg.mu.Unlock()
	raw, err := sockCall(t, rt, orbsock.MethodGithubPrepare, orbsock.GithubPrepareParams{ThreadID: libThread, Level: "push"}, nil)
	require.NoError(t, err)
	var res PrepareResult
	require.NoError(t, json.Unmarshal(raw, &res))
	assert.True(t, res.OK, "%+v", res)
	assert.FileExists(t, marker)
	assert.EqualValues(t, 1, reports.Load())
}

// M15 v4：github.prepare 的发布类别只取控制面。orbd 先以同一线程 id 发 ctl github.call（github_release_status，brief）：
// 进行中的发布线程在默认分支上通过并上报；不是发布线程、查询出错、控制面没有该方法、发布已结束时在默认分支上被拒、
// 不上报；agent 在 sock 参数里自称发布线程不起作用。
func TestGithubPrepareReleaseCategory(t *testing.T) {
	repo := newPrepRepo(t)
	repo.git("checkout", "-q", "main")
	head := repo.commit("a.txt", "chore: bump version to 1.2.3 fix flaky test")
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)
	setWorkdir(rt, repo.dir)
	fg := &fakeGates{}
	fg.set(apigen.GitHubGates{Push: true}, hashA)

	var mode atomic.Value // fixing | awaiting_ci | green | not_release | error | missing
	queries := make(chan apigen.LinkCtlGithubCallParams, 16)
	reports := make(chan apigen.LinkCtlGithubPrepareReportParams, 16)
	cp.extra = func(method string, params json.RawMessage) (any, bool, error) {
		switch method {
		case string(apigen.MethodGithubCall):
			if res, ok, err := fg.handle(params); ok {
				return res, true, err
			}
			var p apigen.LinkCtlGithubCallParams
			_ = json.Unmarshal(params, &p)
			queries <- p
			switch m := mode.Load().(string); m {
			case "not_release":
				return nil, true, &link.CtlError{Code: "not_release_thread", Message: "this thread is not a release sentinel thread"}
			case "error":
				return nil, true, &link.CtlError{Code: "internal", Message: "database is locked"}
			case "missing":
				return nil, false, nil
			default:
				return apigen.LinkCtlGithubCallResult{Text: "Release v1.2.3 is " + m, Details: map[string]any{"state": m, "version": "1.2.3"}}, true, nil
			}
		case string(apigen.MethodGithubPrepareReport):
			var p apigen.LinkCtlGithubPrepareReportParams
			_ = json.Unmarshal(params, &p)
			reports <- p
			return apigen.LinkCtlOkResult{Ok: true}, true, nil
		}
		return nil, false, nil
	}
	call := func(p any) PrepareResult {
		t.Helper()
		raw, err := sockCall(t, rt, orbsock.MethodGithubPrepare, p, nil)
		require.NoError(t, err)
		var res PrepareResult
		require.NoError(t, json.Unmarshal(raw, &res))
		return res
	}

	for _, m := range []string{"fixing", "awaiting_ci"} {
		mode.Store(m)
		res := call(orbsock.GithubPrepareParams{ThreadID: libThread, Level: "push"})
		require.True(t, res.OK, "%s: %+v", m, res)
		assert.Equal(t, head, res.SHA)
		q := <-queries
		assert.Equal(t, libThread, q.ThreadID)
		assert.Equal(t, "github_release_status", q.Tool)
		assert.Equal(t, map[string]any{"brief": true}, q.Params)
		rep := recvReport(t, reports)
		assert.Equal(t, head, rep.Sha)
		assert.Equal(t, libThread, rep.ThreadID)
	}

	for _, m := range []string{"not_release", "error", "missing", "green", "failed", "superseded"} {
		mode.Store(m)
		res := call(map[string]any{"threadId": libThread, "level": "push", "release": true})
		assert.False(t, res.OK, m)
		assert.Empty(t, res.SHA, m)
		assert.Contains(t, res.Failure, "default branch", m)
		q := <-queries
		assert.Equal(t, "github_release_status", q.Tool, m)
	}
	assert.Empty(t, reports, "非发布线程在默认分支上不上报")
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
