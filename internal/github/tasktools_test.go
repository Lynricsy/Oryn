package github

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// 步骤 5.2：issue 任务工具的控制面处理器。

// newToolEnv 是带任务线程（acme/widgets#7，假 GitHub 上也有这个 issue）的环境。
func newToolEnv(t *testing.T, in apigen.GitHubTriggerInput) *taskEnv {
	e := newTaskEnv(t, in)
	e.fake.AddIssue(githubtest.Issue{RepoID: 42, Number: 7, Title: "t", Author: "alice", State: "open"})
	return e
}

// call 以 JSON 往返后的参数调用工具（与 orbd 经 ctl 转来的形态一致）。
func (e *taskEnv) call(tool, params string) (apigen.LinkCtlGithubCallResult, error) {
	e.t.Helper()
	var p map[string]any
	require.NoError(e.t, json.Unmarshal([]byte(params), &p))
	return e.s.Call(e.ctx, e.orbOf(e.thread()), e.thread(), apigen.LinkCtlGithubCallParams{ThreadID: e.thread(), Tool: tool, Params: p})
}

func TestToolClassify(t *testing.T) {
	e := newToolEnv(t, apigen.GitHubTriggerInput{})
	for _, bad := range []string{
		`{"primary":"nope","rationale":"r"}`,
		`{"primary":"bug","rationale":"r"}`,
		`{"primary":"question","rationale":""}`,
		`{"primary":"question","rationale":"r","extra":1}`,
		`{"primary":"bug","priority":"p1","rationale":"r"}`,
		`{"primary":"bug","priority":"prio:p1","functional":["kernel"],"rationale":"r"}`,
		`{"primary":"bug","priority":"prio:p1","provider":"openai","rationale":"r"}`,
		`{"primary":"bug","priority":"prio:p1","branch_slug":"Fix_X","rationale":"r"}`,
	} {
		_, err := e.call("github_classify_issue", bad)
		assert.Equal(t, codeInvalidParams, codeOf(err), bad)
	}
	assert.Nil(t, e.threadIssue(7).Classification, "无效参数不写入")

	res, err := e.call("github_classify_issue", `{"primary":"bug","priority":"prio:p1","functional":["cli","tui"],"provider":"provider:openai",
		"platform":"platform:linux","rationale":"crashes on start","branch_slug":"fix-start-crash"}`)
	require.NoError(t, err)
	assert.Equal(t, "bug", *e.threadIssue(7).Classification)
	labels := []string{"bug", "prio:p1", "provider:openai", "platform:linux", "cli", "tui"}
	assert.Equal(t, labels, e.fake.IssueLabels(42, 7))
	assert.Equal(t, true, res.Details["implAuthorized"])
	branch, _ := res.Details["branch"].(string)
	assert.True(t, strings.HasPrefix(branch, "oryn/") && strings.HasSuffix(branch, "/fix-start-crash"), branch)
	assert.Contains(t, res.Text, "implAuthorized: true")
	// 打标签预登记，回流的 issues.labeled 认作自身写入
	rows, err := e.st.Q.ListGithubSelfWrites(e.ctx, db.ListGithubSelfWritesParams{Target: "/repos/acme/widgets/issues/7/labels", Category: egress.CatIssueUpdate, Since: 0})
	require.NoError(t, err)
	assert.Len(t, rows, 1)

	// 打标签失败：分类照记，结果里说明
	e.fake.Lock()
	e.fake.Fail = map[string]int{"POST /repos/acme/widgets/issues/7/labels": 500}
	e.fake.Unlock()
	res, err = e.call("github_classify_issue", `{"primary":"question","rationale":"asks how"}`)
	require.NoError(t, err)
	assert.Equal(t, "question", *e.threadIssue(7).Classification)
	assert.Contains(t, res.Text, "applying labels failed")
}

// 规则开了「实现需授权」：question 未授权；bug / documentation 或 impl_authorized 置位后授权。
func TestToolClassifyImplAuth(t *testing.T) {
	e := newToolEnv(t, apigen.GitHubTriggerInput{ImplAuth: new(true)})
	res, err := e.call("github_classify_issue", `{"primary":"enhancement","rationale":"wants a flag"}`)
	require.NoError(t, err)
	assert.Equal(t, false, res.Details["implAuthorized"])
	res, err = e.call("github_classify_issue", `{"primary":"documentation","rationale":"typo"}`)
	require.NoError(t, err)
	assert.Equal(t, true, res.Details["implAuthorized"])
	require.NoError(t, e.s.authorizeImpl(e.ctx, 42, 7, "owner"))
	res, err = e.call("github_classify_issue", `{"primary":"enhancement","rationale":"wants a flag"}`)
	require.NoError(t, err)
	assert.Equal(t, true, res.Details["implAuthorized"])
}

func TestToolTaskState(t *testing.T) {
	e := newToolEnv(t, apigen.GitHubTriggerInput{})
	for _, bad := range []string{`{"state":"closed"}`, `{"state":"new"}`, `{"state":"fixing","repro":{"title":"t"}}`} {
		_, err := e.call("github_task_state", bad)
		assert.Equal(t, codeInvalidParams, codeOf(err), bad)
	}
	res, err := e.call("github_task_state", `{"state":"reproducing","note":"trying","repro":{"title":"start crash","command":"app --start",
		"output":"panic: boom","exit_code":2,"reproduced":true}}`)
	require.NoError(t, err)
	assert.Equal(t, taskReproducing, e.threadIssue(7).State)
	assert.Contains(t, res.Text, "### Repro: start crash (reproduced, exit 2)\n```\n$ app --start\npanic: boom\n```")
	assert.NotNil(t, res.Details["repro"])

	res, err = e.call("github_task_state", `{"state":"needs_info","note":"which version?"}`)
	require.NoError(t, err)
	assert.Equal(t, taskNeedsInfo, e.threadIssue(7).State)
	assert.Equal(t, []string{needsInfoLabel}, e.fake.IssueLabels(42, 7))
	assert.Contains(t, res.Text, "needs-info")

	require.NoError(t, e.s.SetTaskState(e.ctx, e.thread(), taskClosed, ""))
	_, err = e.call("github_task_state", `{"state":"fixing"}`)
	assert.Equal(t, "task_finished", codeOf(err))
}

// github_abort_task：置 abandoned、写审计、取消待执行的自动关闭、线程离开 working 之后才冻结 orb，不在 GitHub 上发言。
func TestToolAbortTask(t *testing.T) {
	e := newToolEnv(t, apigen.GitHubTriggerInput{})
	require.NoError(t, e.st.Q.UpsertGithubAutoclose(e.ctx, db.UpsertGithubAutocloseParams{
		RepoID: 42, Number: 7, ThreadID: e.thread(), CommentID: 1, Author: "alice", DueAt: e.clock.Now().UnixMilli() + 1000,
	}))
	_, err := e.call("github_abort_task", `{"reason":""}`)
	assert.Equal(t, codeInvalidParams, codeOf(err))
	e.fake.ResetCalls()
	res, err := e.call("github_abort_task", `{"reason":"the CI runner has no network"}`)
	require.NoError(t, err)
	assert.Contains(t, res.Text, "abandoned")
	assert.Equal(t, taskAbandoned, e.threadIssue(7).State)
	assert.Empty(t, e.orbs.list(), "冻结在应答之后")
	e.drain()
	assert.Empty(t, e.orbs.list(), "回合结束之前不冻结")
	e.s.TurnObserver().StateChanged(threads.StateEvent{ThreadID: e.thread(), Prev: threads.StateWorking, State: threads.StateIdle})
	e.drain()
	assert.Equal(t, []string{e.orbOf(e.thread())}, e.orbs.list())
	var n int
	require.NoError(t, e.st.DB.QueryRowContext(e.ctx, `SELECT COUNT(*) FROM github_audit WHERE method = 'TASK' AND op LIKE 'github_abort_task: the CI runner%' AND repo = 'acme/widgets'`).Scan(&n))
	assert.Equal(t, 1, n)
	ac, err := e.st.Q.GetGithubAutoclose(e.ctx, db.GetGithubAutocloseParams{RepoID: 42, Number: 7})
	require.NoError(t, err)
	assert.Equal(t, "cancelled", ac.State)
	e.fake.Lock()
	defer e.fake.Unlock()
	for _, c := range e.fake.Calls {
		assert.Equal(t, "GET", c.Method, "不在 GitHub 上发言：%s %s", c.Method, c.Path)
	}
}

// 真实任务队列在跑（Run 的工作协程随时消费）：github_abort_task 返回后队列里也没有冻结——排一个哨兵任务，它执行时
// 之前排进队列的任务都已执行完，orb 仍未冻结；别的线程离开 working、本线程 working→working 都不触发；本线程离开
// working（回合结束）后才冻结，且只冻结一次。
func TestToolAbortFreezeWaitsForTurnEndWithLiveQueue(t *testing.T) {
	e := newToolEnv(t, apigen.GitHubTriggerInput{})
	e.drain()
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { e.s.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	// settle 等到队列里此前排进的任务都已被 Run 的工作协程执行完
	settle := func() {
		t.Helper()
		ran := make(chan struct{})
		require.True(t, e.s.enqueue(func(context.Context) { close(ran) }))
		select {
		case <-ran:
		case <-time.After(5 * time.Second):
			t.Fatal("任务队列没有在跑")
		}
	}

	thr := e.thread()
	_, err := e.call("github_abort_task", `{"reason":"cannot reproduce on this runner"}`)
	require.NoError(t, err)
	settle()
	assert.Empty(t, e.orbs.list(), "工具应答之后、回合结束之前，队列里没有冻结")

	obs := e.s.TurnObserver()
	obs.StateChanged(threads.StateEvent{ThreadID: "thr_other", Prev: threads.StateWorking, State: threads.StateIdle})
	obs.StateChanged(threads.StateEvent{ThreadID: thr, Prev: threads.StateWorking, State: threads.StateWorking})
	obs.StateChanged(threads.StateEvent{ThreadID: thr, Prev: threads.StateIdle, State: threads.StateWorking})
	settle()
	assert.Empty(t, e.orbs.list(), "只有本线程离开 working 才冻结")

	obs.StateChanged(threads.StateEvent{ThreadID: thr, Prev: threads.StateWorking, State: threads.StateIdle})
	settle()
	assert.Equal(t, []string{e.orbOf(thr)}, e.orbs.list())
	obs.StateChanged(threads.StateEvent{ThreadID: thr, Prev: threads.StateWorking, State: threads.StateIdle})
	settle()
	assert.Len(t, e.orbs.list(), 1, "登记只消费一次")
}
