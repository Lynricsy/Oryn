package github

import (
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
