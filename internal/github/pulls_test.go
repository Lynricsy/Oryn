package github

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

var (
	shaA = strings.Repeat("a1", 20)
	shaB = strings.Repeat("b2", 20)
)

// stubSampler 记录被要求立即采样的线程。
type stubSampler struct {
	mu      sync.Mutex
	threads []string
}

func (s *stubSampler) SampleCommits(_ context.Context, threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threads = append(s.threads, threadID)
	return nil
}

// thread 在项目下建一个工作线程（带一台 orb）；active 越大越近期活跃。
func (e *testEnv) thread(projectID string, active int64) string {
	e.t.Helper()
	o := e.orb(projectID, "", "")
	id := ids.New("thr")
	now := e.clock.Now().UnixMilli()
	require.NoError(e.t, e.st.Q.CreateThread(e.ctx, db.CreateThreadParams{
		ID: id, ProjectID: &projectID, OrbID: &o.ID, Title: "t", TitleSource: "auto", State: "idle", StateChangedAt: now,
		SessionDir: "d", CreatedAt: now, UpdatedAt: now, Kind: "work",
	}))
	_, err := e.st.DB.ExecContext(e.ctx, `UPDATE threads SET last_message_at = ? WHERE id = ?`, now+active, id)
	require.NoError(e.t, err)
	return id
}

func (e *testEnv) threadCommit(threadID, sha string) {
	e.t.Helper()
	require.NoError(e.t, e.st.Q.InsertThreadCommit(e.ctx, db.InsertThreadCommitParams{ThreadID: threadID, Sha: sha, Ts: 1}))
}

// links 返回线程的未取消 PR 关联编号。
func (e *testEnv) links(threadID string) []int64 {
	e.t.Helper()
	rows, err := e.st.Q.ListThreadPulls(e.ctx, threadID)
	require.NoError(e.t, err)
	var out []int64
	for _, r := range rows {
		out = append(out, r.Number)
	}
	return out
}

// prEvent 是 acme/widgets（id 42）上 PR 的 pull_request 事件载荷；headRepo 为 0 表示同仓库。
func prEvent(action string, number int64, sha string, headRepo int64, mod func(pr map[string]any)) map[string]any {
	if headRepo == 0 {
		headRepo = 42
	}
	pr := map[string]any{
		"number": number, "title": "Fix login", "state": "open", "draft": false, "merged": false, "merged_at": nil,
		"html_url": "https://github.com/Acme/Widgets/pull/12", "user": map[string]any{"login": "alice"},
		"head": map[string]any{"ref": "fix-login", "sha": sha, "repo": map[string]any{"id": headRepo, "full_name": "x/Widgets"}},
		"base": map[string]any{"ref": "main", "repo": map[string]any{"id": 42, "full_name": "Acme/Widgets"}},
	}
	if mod != nil {
		mod(pr)
	}
	return map[string]any{
		"action": action, "pull_request": pr, "repository": map[string]any{"id": 42, "full_name": "Acme/Widgets"},
		"sender": map[string]any{"login": "alice", "type": "User"},
	}
}

func TestPullOpenedLinksThread(t *testing.T) {
	e := newEnv(t)
	p := e.boundProject()
	th := e.thread(p, 0)
	e.threadCommit(th, shaA)

	guid := e.deliver("pull_request", prEvent("opened", 12, shaA, 0, nil))
	assert.Equal(t, "handled", e.delivery(guid).Outcome)
	assert.Equal(t, []int64{12}, e.links(th))
	assert.Contains(t, e.th.published, th)
	pull, err := e.st.Q.GetGithubPull(e.ctx, db.GetGithubPullParams{RepoID: 42, Number: 12})
	require.NoError(t, err)
	assert.Equal(t, "open", pull.State)
	assert.True(t, pull.SameRepo)
	assert.Equal(t, "Acme/Widgets", pull.RepoFullName)
	assert.Equal(t, shaA, pull.HeadSha)

	// 用户取消关联后，后续事件不会重连
	n, err := e.st.Q.UnlinkThreadPull(e.ctx, db.UnlinkThreadPullParams{ThreadID: th, RepoID: 42, Number: 12})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	e.deliver("pull_request", prEvent("synchronize", 12, shaA, 0, nil))
	assert.Empty(t, e.links(th))

	// 未绑定的仓库
	ev := prEvent("opened", 1, shaA, 0, nil)
	ev["repository"] = map[string]any{"id": 77, "full_name": "x/y"}
	assert.Equal(t, "ignored:unbound_repo", e.delivery(e.deliver("pull_request", ev)).Outcome)
	assert.Equal(t, "ignored:action", e.delivery(e.deliver("pull_request", prEvent("labeled", 12, shaA, 0, nil))).Outcome)
}

func TestPullForkNotLinked(t *testing.T) {
	e := newEnv(t)
	p := e.boundProject()
	th := e.thread(p, 0)
	e.threadCommit(th, shaA)
	e.deliver("pull_request", prEvent("opened", 12, shaA, 99, nil))
	assert.Empty(t, e.links(th))
	pull, err := e.st.Q.GetGithubPull(e.ctx, db.GetGithubPullParams{RepoID: 42, Number: 12})
	require.NoError(t, err)
	assert.False(t, pull.SameRepo)
}

func TestPullLinksMostRecentThread(t *testing.T) {
	e := newEnv(t)
	p := e.boundProject()
	older, newer := e.thread(p, 0), e.thread(p, 1000)
	e.threadCommit(older, shaA)
	e.threadCommit(newer, shaA)
	e.deliver("pull_request", prEvent("opened", 12, shaA, 0, nil))
	assert.Empty(t, e.links(older))
	assert.Equal(t, []int64{12}, e.links(newer))
}

func TestPullSamplesCandidatesThenLinks(t *testing.T) {
	e := newEnv(t)
	sm := &stubSampler{}
	e.s.SetSampler(sm)
	p := e.boundProject()
	t1, t2 := e.thread(p, 0), e.thread(p, 10)

	e.deliver("pull_request", prEvent("opened", 12, shaA, 0, nil))
	assert.ElementsMatch(t, []string{t1, t2}, sm.threads, "没有线程采到头提交：候选线程各采样一次")
	assert.Empty(t, e.links(t1))

	// 采样落库后的回调建立关联
	e.threadCommit(t1, shaA)
	e.s.ThreadCommitsSampled(t1, shaA)
	e.drain()
	assert.Equal(t, []int64{12}, e.links(t1))
	assert.Empty(t, e.links(t2))
}

func TestThreadCommitsSampledAsksGitHub(t *testing.T) {
	e := newEnv(t)
	p := e.boundProject()
	th := e.thread(p, 0)
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 5, Title: "Old PR", Author: "owner"}, HeadRef: "feat", HeadSHA: shaB})
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 6, Title: "Fork PR"}, HeadSHA: shaB, HeadRepoID: 99})

	e.s.ThreadCommitsSampled(th, shaB)
	e.drain()
	assert.Equal(t, []int64{5}, e.links(th))
	pull, err := e.st.Q.GetGithubPull(e.ctx, db.GetGithubPullParams{RepoID: 42, Number: 5})
	require.NoError(t, err)
	assert.Equal(t, "Old PR", pull.Title)
	assert.Equal(t, "Acme/Widgets", pull.RepoFullName)
	path := "/repos/acme/widgets/commits/" + shaB + "/pulls"
	assert.Len(t, e.fake.CallsTo("GET", path), 1)

	// 同一 head 不再反查（库里没有该线程提交对应的 PR，但 head 已查过）
	other := e.thread(p, 0)
	e.s.ThreadCommitsSampled(th, shaB)
	e.drain()
	assert.Len(t, e.fake.CallsTo("GET", path), 1)
	// 空 head 不反查
	e.s.ThreadCommitsSampled(other, "")
	e.drain()
	assert.Len(t, e.fake.CallsTo("GET", "/repos/acme/widgets/commits/*"), 1)
}
