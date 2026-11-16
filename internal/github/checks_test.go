package github

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// fakeAfter 是假 AfterFunc：只登记，由测试决定何时触发。
type fakeAfter struct {
	mu     sync.Mutex
	fns    []func()
	delays []time.Duration
}

func (f *fakeAfter) AfterFunc(d time.Duration, fn func()) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fns, f.delays = append(f.fns, fn), append(f.delays, d)
	return func() {}
}

func (f *fakeAfter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fns)
}

func (f *fakeAfter) fire(i int) {
	f.mu.Lock()
	fn := f.fns[i]
	f.mu.Unlock()
	fn()
}

func checkSuiteEvent(sha string) map[string]any {
	return map[string]any{
		"action": "completed", "check_suite": map[string]any{"head_sha": sha},
		"repository": map[string]any{"id": 42, "full_name": "Acme/Widgets"},
	}
}

func (e *testEnv) pull(number int64) db.GithubPull {
	e.t.Helper()
	p, err := e.st.Q.GetGithubPull(e.ctx, db.GetGithubPullParams{RepoID: 42, Number: number})
	require.NoError(e.t, err)
	return p
}

// rollupEnv 建一个绑定项目、一个采到 shaA 的线程，以及假服务上头提交为 shaA 的 PR #12。
func rollupEnv(t *testing.T) (*testEnv, *fakeAfter, string) {
	e := newEnv(t)
	ft := &fakeAfter{}
	e.s.cfg.AfterFunc = ft.AfterFunc
	p := e.boundProject()
	th := e.thread(p, 0)
	e.threadCommit(th, shaA)
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 12, Title: "Fix login"}, HeadRef: "fix-login", HeadSHA: shaA})
	return e, ft, th
}

func TestRollupOnLinkAndCheckSuite(t *testing.T) {
	e, ft, th := rollupEnv(t)
	e.fake.SetRollup(shaA, "PENDING")

	// 关联建立时即使没有 CI 事件也汇总一次
	e.deliver("pull_request", prEvent("opened", 12, shaA, 0, nil))
	require.Equal(t, []int64{12}, e.links(th))
	assert.Len(t, e.fake.CallsTo("POST", "/graphql"), 1)
	assert.Equal(t, "pending", deref(e.pull(12).Checks))
	assert.Equal(t, shaA, deref(e.pull(12).ChecksSha))
	assert.Zero(t, ft.count(), "关联建立的汇总不防抖")

	// 防抖窗口内连投 3 个事件只排一次、只发一次 GraphQL
	e.fake.SetRollup(shaA, "FAILURE")
	for range 3 {
		assert.Equal(t, "handled", e.delivery(e.deliver("check_suite", checkSuiteEvent(shaA))).Outcome)
	}
	require.Equal(t, 1, ft.count())
	assert.Equal(t, rollupDebounce, ft.delays[0])
	assert.Equal(t, "pending", deref(e.pull(12).Checks), "到点前不汇总")
	ft.fire(0)
	e.drain()
	assert.Len(t, e.fake.CallsTo("POST", "/graphql"), 2)
	assert.Equal(t, "failure", deref(e.pull(12).Checks))
	assert.Equal(t, shaA, deref(e.pull(12).ChecksSha))

	// 新的头提交：UpsertGithubPull 清掉旧汇总
	e.deliver("pull_request", prEvent("synchronize", 12, shaB, 0, nil))
	assert.Nil(t, e.pull(12).Checks)
	assert.Nil(t, e.pull(12).ChecksSha)

	// 没有打开的 PR 以该提交为头
	assert.Equal(t, "ignored:no_pull", e.delivery(e.deliver("check_suite", checkSuiteEvent(shaA))).Outcome)
}

func TestRollupStaleHeadIgnored(t *testing.T) {
	e, ft, _ := rollupEnv(t)
	e.fake.SetRollup(shaA, "SUCCESS")
	e.deliver("pull_request", prEvent("opened", 12, shaA, 0, nil))
	assert.Equal(t, "success", deref(e.pull(12).Checks))

	// 假服务上 PR 已推进到 shaB，而库里还是 shaA：放弃
	e.fake.Lock()
	e.fake.Pulls[0].HeadSHA = shaB
	e.fake.Unlock()
	e.fake.SetRollup(shaB, "FAILURE")
	e.deliver("status", map[string]any{"sha": shaA, "state": "failure", "repository": map[string]any{"id": 42}})
	require.Equal(t, 1, ft.count())
	ft.fire(0)
	e.drain()
	assert.Equal(t, "success", deref(e.pull(12).Checks))
}

func TestRollupStateMapping(t *testing.T) {
	for in, want := range map[string]string{
		"SUCCESS": "success", "FAILURE": "failure", "ERROR": "failure", "PENDING": "pending", "EXPECTED": "pending", "": "",
	} {
		assert.Equal(t, want, rollupState(in), in)
	}
}

// ciRound 把 PR #12 推进到新的头提交 sha，令其 CI 失败（两条失败检查），汇总一次并投递到期的回流。
func (e *testEnv) ciRound(sha, rollup string) {
	e.t.Helper()
	e.fake.Lock()
	e.fake.Pulls[0].HeadSHA = sha
	e.fake.Unlock()
	e.fake.SetRollup(sha, rollup)
	e.fake.SetChecks(sha, []githubtest.CheckRun{
		{Name: "build", Conclusion: "failure", URL: "https://ci.test/1"},
		{Name: "lint", Conclusion: "timed_out", URL: "https://ci.test/2"},
		{Name: "test", Conclusion: "success", URL: "https://ci.test/3"},
	}, nil)
	e.deliver("pull_request", prEvent("synchronize", 12, sha, 0, nil))
	e.s.rollup(e.ctx, 42, 12, false)
	e.s.flush(e.ctx)
}

func (e *testEnv) ciRounds(threadID string) int64 {
	e.t.Helper()
	var n int64
	require.NoError(e.t, e.st.DB.QueryRowContext(e.ctx, `SELECT ci_rounds FROM thread_pulls WHERE thread_id = ? AND number = 12`, threadID).Scan(&n))
	return n
}

const goldenCI = "[Oryn GitHub event on Acme/Widgets#12 — delivered automatically, not typed by the owner]\n" +
	"Pull request: Fix login (https://github.com/Acme/Widgets/pull/12)\n" +
	"\n" +
	"CI failed on a1a1a1a:\n" +
	"- build (failure) https://ci.test/1\n" +
	"- lint (timed_out) https://ci.test/2\n" +
	"\n" +
	"Fix the failures on the pull request branch and push. Use `gh run view --log-failed` to read the logs."

func TestCIFailureDelivered(t *testing.T) {
	e, _, th := rollupEnv(t)
	e.fake.SetRollup(shaA, "PENDING")
	e.deliver("pull_request", prEvent("opened", 12, shaA, 0, nil))
	e.s.flush(e.ctx)
	assert.Empty(t, e.delivered(), "pending 不回流")

	// pending → failure：一条含两条失败检查的消息
	e.fake.SetRollup(shaA, "FAILURE")
	e.fake.SetChecks(shaA, []githubtest.CheckRun{
		{Name: "build", Conclusion: "failure", URL: "https://ci.test/1"},
		{Name: "lint", Conclusion: "timed_out", URL: "https://ci.test/2"},
		{Name: "test", Conclusion: "success", URL: "https://ci.test/3"},
	}, nil)
	e.s.rollup(e.ctx, 42, 12, false)
	e.s.flush(e.ctx)
	assert.Equal(t, []string{th + "\n" + goldenCI}, e.delivered())
	assert.EqualValues(t, 1, e.ciRounds(th))

	// 同一 sha 再来一次汇总：不重复投递
	e.s.rollup(e.ctx, 42, 12, false)
	e.s.flush(e.ctx)
	assert.Len(t, e.delivered(), 1)
	assert.Equal(t, 1, e.outboxCount())
}

func TestCIRoundsLimitAndReset(t *testing.T) {
	e, _, th := rollupEnv(t)
	n := &stubNotifier{}
	e.s.n = n
	e.fake.SetRollup(shaA, "PENDING")
	e.deliver("pull_request", prEvent("opened", 12, shaA, 0, nil))
	sha := func(i int) string { return fmt.Sprintf("%040x", i+1) }

	for i := range 5 {
		e.ciRound(sha(i), "FAILURE")
	}
	assert.Len(t, e.delivered(), 5)
	assert.Empty(t, n.titles())

	// 第 6 次失败不再投递，通知一次；之后也不再通知
	e.ciRound(sha(5), "FAILURE")
	e.ciRound(sha(6), "FAILURE")
	assert.Len(t, e.delivered(), 5)
	assert.Equal(t, []string{"CI still failing"}, n.titles())
	assert.Contains(t, n.posts[0], "Acme/Widgets#12 is still failing after 5 automatic rounds; automatic CI delivery stopped for this pull request.")

	// 中途变成功：计数清零，之后的失败重新回流
	e.ciRound(sha(7), "SUCCESS")
	assert.Zero(t, e.ciRounds(th))
	e.ciRound(sha(8), "FAILURE")
	assert.Len(t, e.delivered(), 6)
	assert.EqualValues(t, 1, e.ciRounds(th))
}

func TestCIListenOffAndFailingAtLink(t *testing.T) {
	e, _, th := rollupEnv(t)
	row, err := e.st.Q.GetThread(e.ctx, th)
	require.NoError(t, err)
	require.NoError(t, e.st.Q.SetProjectGithubListen(e.ctx, db.SetProjectGithubListenParams{
		GithubListenReviews: true, GithubListenComments: true, GithubListenCi: false, UpdatedAt: 2, ID: *row.ProjectID,
	}))
	e.fake.SetRollup(shaA, "FAILURE")
	e.deliver("pull_request", prEvent("opened", 12, shaA, 0, nil))
	e.s.flush(e.ctx)
	assert.Equal(t, "failure", deref(e.pull(12).Checks))
	assert.Empty(t, e.delivered(), "项目关掉 CI 回流")

	// 关联建立时 CI 已经失败：建立后立即投递一次
	e2, _, th2 := rollupEnv(t)
	e2.fake.SetRollup(shaA, "FAILURE")
	e2.fake.SetChecks(shaA, []githubtest.CheckRun{{Name: "build", Conclusion: "failure", URL: "https://ci.test/1"}},
		[]githubtest.CommitStatus{{Context: "ci/legacy", State: "error", URL: "https://ci.test/9"}})
	e2.deliver("pull_request", prEvent("opened", 12, shaA, 0, nil))
	e2.s.flush(e2.ctx)
	got := e2.delivered()
	require.Len(t, got, 1)
	assert.Contains(t, got[0], th2+"\n")
	assert.Contains(t, got[0], "- build (failure) https://ci.test/1\n- ci/legacy (error) https://ci.test/9\n")
}

func TestComposeEventMixedCI(t *testing.T) {
	ci, _ := json.Marshal(outboxItem{SHA: "1a2b3c4", Checks: []failedCheck{{Name: "build", Conclusion: "failure", URL: "u"}}})
	rv, _ := json.Marshal(outboxItem{Actor: "alice", State: "commented", Body: "hi"})
	rows := []db.GithubOutbox{
		{Kind: "review", Item: string(rv), IsPull: true, Title: "T", Url: "https://github.com/acme/widgets/pull/12", CreatedAt: 1},
		{Kind: "ci", Item: string(ci), IsPull: true, Title: "T", Url: "https://github.com/acme/widgets/pull/12", CreatedAt: 2},
	}
	got := composeEvent("acme/widgets", 12, rows)
	assert.Contains(t, got, "@alice reviewed (commented):\nhi\n\nCI failed on 1a2b3c4:\n- build (failure) u\n\n")
	assert.True(t, strings.HasSuffix(got, closingPull), "与评审段混合时用评审的结尾句")
}
