package github

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/egress"
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

func (e *testEnv) shippedAt(threadID string) (int64, bool) {
	e.th.mu.Lock()
	defer e.th.mu.Unlock()
	at, ok := e.th.shipped[threadID]
	return at, ok
}

func TestPullMergedMarksShipped(t *testing.T) {
	e := newEnv(t)
	n := &stubNotifier{}
	e.s.n = n
	p := e.boundProject()
	th := e.thread(p, 0)
	e.threadCommit(th, shaA)
	e.deliver("pull_request", prEvent("opened", 12, shaA, 0, nil))
	require.Equal(t, []int64{12}, e.links(th))
	_, shipped := e.shippedAt(th)
	assert.False(t, shipped)

	merged := "2026-11-16T09:30:00Z"
	guid := e.deliver("pull_request", prEvent("closed", 12, shaA, 0, func(pr map[string]any) {
		pr["state"], pr["merged"], pr["merged_at"] = "closed", true, merged
	}))
	assert.Equal(t, "handled", e.delivery(guid).Outcome)
	at, shipped := e.shippedAt(th)
	require.True(t, shipped)
	assert.Equal(t, githubTimeMs(merged), at)
	assert.Equal(t, []string{"PR #12 merged"}, n.titles())
	assert.Equal(t, []string{"done"}, n.kinds())
	assert.Contains(t, n.posts[0], "PR #12 merged: ")
	assert.Equal(t, "merged", e.pull(12).State)

	// 未合并的关闭不标记
	other := e.thread(p, 0)
	e.threadCommit(other, shaB)
	e.deliver("pull_request", prEvent("opened", 13, shaB, 0, nil))
	require.Equal(t, []int64{13}, e.links(other))
	e.deliver("pull_request", prEvent("closed", 13, shaB, 0, func(pr map[string]any) { pr["state"] = "closed" }))
	_, shipped = e.shippedAt(other)
	assert.False(t, shipped)
}

func TestLinkMergedPullMarksShipped(t *testing.T) {
	e := newEnv(t)
	n := &stubNotifier{}
	e.s.n = n
	p := e.boundProject()
	th := e.thread(p, 0)
	merged := time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC)
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 5, Title: "Done", State: "closed"}, HeadSHA: shaB, MergedAt: merged})
	e.s.ThreadCommitsSampled(th, shaB)
	e.drain()
	assert.Equal(t, []int64{5}, e.links(th))
	at, shipped := e.shippedAt(th)
	require.True(t, shipped)
	assert.Equal(t, merged.UnixMilli(), at)
	assert.Equal(t, []string{"PR #5 merged"}, n.titles())
}

func TestSetPullListeningAndUnlink(t *testing.T) {
	e := newEnv(t)
	p := e.boundProject()
	th := e.workThread(p)
	e.linkPull(th, 12)
	_, err := e.st.DB.ExecContext(e.ctx, `UPDATE thread_pulls SET ci_rounds = 3 WHERE thread_id = ?`, th)
	require.NoError(t, err)

	require.NoError(t, e.s.SetPullListening(e.ctx, th, 12, false))
	rows, err := e.st.Q.ListThreadPulls(e.ctx, th)
	require.NoError(t, err)
	assert.False(t, rows[0].Listening)
	require.NoError(t, e.s.SetPullListening(e.ctx, th, 12, true))
	target, err := e.st.Q.PullTargetThread(e.ctx, db.PullTargetThreadParams{RepoID: 42, Number: 12})
	require.NoError(t, err)
	assert.True(t, target.Listening)
	assert.Zero(t, target.CiRounds, "恢复时 CI 轮数清零")
	assert.Contains(t, e.th.published, th)

	assert.Equal(t, "not_found", e.s.SetPullListening(e.ctx, th, 99, true).(*Error).Code)
	require.NoError(t, e.s.UnlinkPull(e.ctx, th, 12))
	assert.Empty(t, e.links(th))
	assert.Equal(t, "not_found", e.s.UnlinkPull(e.ctx, th, 12).(*Error).Code)
	assert.Equal(t, "not_found", e.s.UnlinkPull(e.ctx, "thr_missing", 12).(*Error).Code)
	plain := e.workThread(e.project(""))
	assert.Equal(t, "not_found", e.s.UnlinkPull(e.ctx, plain, 12).(*Error).Code)
}

// M15 3.2：出站代理创建的跨仓库 PR 被关联（GraphQL createPullRequest 与 REST POST …/pulls 两种形态），并登记对象级 watch。
func TestPullCreatedViaEgressCrossRepo(t *testing.T) {
	e := newUpstreamEnv(t)
	orbID, thr := e.orbWithThread(e.prj, slotUser)
	e.upstreamPull(orbID, 12)
	assert.Equal(t, []int64{12}, e.links(thr))
	var via bool
	require.NoError(t, e.st.DB.QueryRowContext(e.ctx,
		`SELECT created_via_egress FROM thread_pulls WHERE thread_id = ? AND repo_id = 42 AND number = 12`, thr).Scan(&via))
	assert.True(t, via)
	pull := e.pull(12)
	assert.Equal(t, "Acme/Widgets", pull.RepoFullName)
	assert.False(t, pull.SameRepo)
	assert.Equal(t, shaA, pull.HeadSha)
	_, ok := e.watchRow("pull", "Acme/Widgets", 12)
	assert.True(t, ok)

	// REST：响应里的 node_id 指明是哪一个 PR
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 13, Title: "Docs", Author: "owner"}, HeadRef: "docs", HeadSHA: shaB, HeadRepoID: 43})
	w := egress.GitHubWrite{Method: "POST", Category: egress.CatPRCreate, Op: "POST /repos/{owner}/{repo}/pulls", Repo: "acme/widgets", Target: "/repos/acme/widgets/pulls"}
	e.s.GitHubWriteBegin(e.ctx, orbID, w)
	e.s.GitHubWriteEnd(e.ctx, orbID, w, 201, []string{"node:" + githubtest.PullNodeID(42, 13)})
	e.drain()
	assert.Equal(t, []int64{12, 13}, e.links(thr))

	// 上游拒绝的创建不关联
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 14, Title: "Other", Author: "owner"}, HeadRef: "o", HeadSHA: shaB, HeadRepoID: 43})
	e.s.GitHubWriteBegin(e.ctx, orbID, w)
	e.s.GitHubWriteEnd(e.ctx, orbID, w, 422, []string{"node:" + githubtest.PullNodeID(42, 14)})
	e.drain()
	assert.Equal(t, []int64{12, 13}, e.links(thr))
}

// M15 3.2：按 HEAD 反查到的跨仓库 PR 只在作者是已连接身份之一时关联；陌生人发起、head 相同的 PR 不关联。
func TestPullSampledCrossRepo(t *testing.T) {
	e := newUpstreamEnv(t)
	th := e.thread(e.prj, 0)
	e.fake.AddRepo(githubtest.Repo{ID: 44, FullName: "mallory/widgets", OwnerID: 31})
	// 陌生人从自己的 fork 把同一 head 发到项目仓库
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 43, Number: 7, Title: "Copy", Author: "mallory"}, HeadRef: "x", HeadSHA: shaA, HeadRepoID: 44})
	e.s.ThreadCommitsSampled(th, shaA)
	e.drain()
	assert.Empty(t, e.links(th))

	// 我的账号从项目 fork 向上游开的 PR 关联；陌生人拿同一个 fork 分支向上游开的 PR 不关联
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 12, Title: "Fix", Author: "owner"}, HeadRef: "fix", HeadSHA: shaB, HeadRepoID: 43})
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 13, Title: "Fix (copy)", Author: "mallory"}, HeadRef: "fix", HeadSHA: shaB, HeadRepoID: 43})
	e.s.ThreadCommitsSampled(th, shaB)
	e.drain()
	assert.Equal(t, []int64{12}, e.links(th))
	assert.Equal(t, "Acme/Widgets", e.pull(12).RepoFullName)
	_, ok := e.watchRow("pull", "Acme/Widgets", 12)
	assert.True(t, ok, "跨仓库关联登记对象级 watch")
}

// 提交 → PR → 线程（M24 仓库浏览补 squash 合并）：先找已合并的 PR，再找打开的；没有 PR 或未绑定时 ok=false。
func TestCommitThread(t *testing.T) {
	e := newEnv(t)
	p := e.boundProject()
	th := e.thread(p, 0)
	e.threadCommit(th, shaA)
	e.deliver("pull_request", prEvent("opened", 12, shaA, 0, nil))
	require.Equal(t, []int64{12}, e.links(th))
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 12, Title: "Fix"}, HeadRef: "fix", HeadSHA: shaA,
		MergedAt: time.Now().UTC().Truncate(time.Second)})

	tid, ok, err := e.s.CommitThread(e.ctx, p, shaA)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, th, tid)
	assert.Len(t, e.fake.CallsTo("GET", "/repos/acme/widgets/commits/"+shaA+"/pulls"), 1)

	// 没有 PR 的提交：确实没有线程
	_, ok, err = e.s.CommitThread(e.ctx, p, shaB)
	require.NoError(t, err)
	assert.False(t, ok)
	// 未绑定 GitHub 的项目不发请求
	_, ok, err = e.s.CommitThread(e.ctx, e.project("https://example.com/a/b"), shaA)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Len(t, e.fake.CallsTo("GET", "/repos/acme/widgets/commits/*"), 2)
}
