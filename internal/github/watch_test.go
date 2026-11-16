package github

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// upstreamEnv 是向上游开 PR 的环境：我的账号 owner 已连接；项目绑到自己的 fork owner/widgets（id 43），
// 上游 Acme/Widgets（id 42）没有项目、没有 hook；alice 对上游有写权限。
type upstreamEnv struct {
	*testEnv
	prj string
}

func newUpstreamEnv(t *testing.T) *upstreamEnv {
	e := newEnv(t)
	e.s.cfg.AfterFunc = (&fakeAfter{}).AfterFunc
	e.oauthApp()
	e.fake.AddUser(1, "owner", "The Owner")
	e.fake.AddUser(7, "oryn-bot", "")
	e.fake.AddUser(30, "alice", "")
	e.fake.AddUser(31, "mallory", "")
	e.fake.AddRepo(githubtest.Repo{ID: 42, FullName: "Acme/Widgets", OwnerID: 900, Permissions: map[string]string{"alice": "write"}})
	e.fake.AddRepo(githubtest.Repo{ID: 43, FullName: "owner/widgets", OwnerID: 1})
	require.NoError(t, e.connect(slotUser, "c1", "owner"))
	prj := e.project("https://github.com/owner/widgets")
	require.NoError(t, e.s.BindProject(e.ctx, prj))
	require.NotNil(t, e.getProject(prj).GithubRepoID)
	return &upstreamEnv{testEnv: e, prj: prj}
}

// bareThread 在 orb 上建一个没有项目的工作线程。
func (e *testEnv) bareThread(orbID string) string {
	e.t.Helper()
	id := ids.New("thr")
	now := e.clock.Now().UnixMilli()
	require.NoError(e.t, e.st.Q.CreateThread(e.ctx, db.CreateThreadParams{
		ID: id, OrbID: &orbID, Title: "t", TitleSource: "auto", State: "idle", StateChangedAt: now,
		SessionDir: "d", CreatedAt: now, UpdatedAt: now, Kind: "work",
	}))
	return id
}

// upstreamPull 在上游登记一个 owner 从 fork 开出的 PR，并模拟 orb 经出站代理用 gh pr create（GraphQL createPullRequest）创建了它。
func (e *testEnv) upstreamPull(orbID string, number int64) {
	e.t.Helper()
	e.fake.AddPull(githubtest.Pull{
		Issue:   githubtest.Issue{RepoID: 42, Number: number, Title: "Fix login", Body: "Fixes #3", Author: "owner"},
		HeadRef: "fix-login", HeadSHA: shaA, HeadRepoID: 43,
	})
	w := egress.GitHubWrite{
		Method: "POST", Category: egress.CatPRCreate, Op: "createPullRequest", Target: githubtest.RepoNodeID(42),
		BodySHA256: egress.BodyHash("Fixes #3"),
	}
	e.s.GitHubWriteBegin(e.ctx, orbID, w)
	e.s.GitHubWriteEnd(e.ctx, orbID, w, 200, nil)
	e.drain()
}

// tick 跑一轮 REST 轮询（各槽位都视为到期）并处理合成的事件。时钟不动：假服务的时间取墙上时钟，游标与它比较。
func (e *testEnv) tick() {
	e.t.Helper()
	e.s.watchMu.Lock()
	e.s.watchNext = nil
	e.s.watchMu.Unlock()
	e.pollAt(0)
}

// pollAt 把时钟拨过 d 后跑一轮 REST 轮询（按槽位的到期时刻）并处理合成的事件。
func (e *testEnv) pollAt(d time.Duration) {
	e.t.Helper()
	e.clock.Add(d)
	e.s.pollWatches(e.ctx)
	e.drain()
}

// restOutcomes 返回 REST 合成的、guid 以 prefix 开头的投递结果（按 guid 排序）。
func (e *testEnv) restOutcomes(prefix string) []string {
	e.t.Helper()
	rows, err := e.st.DB.QueryContext(e.ctx, `SELECT outcome FROM github_deliveries WHERE source = 'rest' AND guid LIKE ? ORDER BY guid`, prefix+"%")
	require.NoError(e.t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		require.NoError(e.t, rows.Scan(&v))
		out = append(out, v)
	}
	require.NoError(e.t, rows.Err())
	return out
}

func (e *testEnv) deliveryCount() int {
	e.t.Helper()
	var n int
	require.NoError(e.t, e.st.DB.QueryRowContext(e.ctx, `SELECT COUNT(*) FROM github_deliveries`).Scan(&n))
	return n
}

func (e *testEnv) watchRow(kind, full string, number int64) (db.GithubWatch, bool) {
	e.t.Helper()
	w, err := e.st.Q.GetGithubWatch(e.ctx, db.GetGithubWatchParams{Kind: kind, FullName: full, Number: number})
	if isNoRows(err) {
		return w, false
	}
	require.NoError(e.t, err)
	return w, true
}

// 304 不产生事件：第二轮全部命中 ETag，没有新投递。
func TestWatchNotModified(t *testing.T) {
	e := newUpstreamEnv(t)
	orbID, _ := e.orbWithThread(e.prj, slotUser)
	e.upstreamPull(orbID, 12)
	w, ok := e.watchRow("pull", "Acme/Widgets", 12)
	require.True(t, ok)
	require.NotNil(t, w.LastSeenAt)

	e.tick()
	w, _ = e.watchRow("pull", "Acme/Widgets", 12)
	require.NotNil(t, w.EtagIssue)
	require.NotNil(t, w.EtagComments)
	require.NotNil(t, w.EtagReviews)
	require.NotNil(t, w.EtagReviewComments)
	require.NotNil(t, w.EtagChecks)
	before := e.deliveryCount()

	e.fake.ResetCalls()
	e.tick()
	assert.Equal(t, before, e.deliveryCount())
	assert.NotEmpty(t, e.fake.CallsTo("GET", "/repos/Acme/Widgets/pulls/12"), "仍在轮询")
}

// 新评论回流一次：仓库级与对象级 watch 都看到同一条评论，guid 相同，只处理一次；之后的轮次不再重复。
func TestWatchNewCommentOnce(t *testing.T) {
	e := newTriggerEnv(t)
	e.s.cfg.AfterFunc = (&fakeAfter{}).AfterFunc
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 12, Title: "Fix login redirect loop", Author: "owner"}, HeadRef: "fix", HeadSHA: "abc"})
	e.trigger(apigen.GitHubTriggerInput{Name: "triage", Event: apigen.IssueOpened, Prompt: "look"})
	_, err := e.st.Q.GetGithubRepoWatch(e.ctx, 42)
	require.NoError(t, err, "有启用规则、hook 不是 active：登记仓库级 watch")
	// 升级前建立的关联由补登记补上对象级 watch
	require.NoError(t, e.s.backfillWatches(e.ctx))
	_, ok := e.watchRow("pull", "acme/widgets", 12)
	require.True(t, ok)

	e.tick() // 仓库级 watch 建立基线
	c := e.fake.AddComment(githubtest.Comment{RepoID: 42, Number: 12, Author: "alice", Body: "please rebase"})
	e.tick()
	assert.Equal(t, []string{"handled"}, e.restOutcomes("rest:issue_comment.created:acme/widgets:"+itoa(c.ID)+":"))
	assert.Equal(t, 1, e.outboxCount())
	e.tick()
	assert.Equal(t, 1, e.outboxCount())
}

// 上游 PR 合并后设置 shippedAt；终态满 24 小时后 watch 退场。
func TestWatchUpstreamMergedShipped(t *testing.T) {
	e := newUpstreamEnv(t)
	orbID, thr := e.orbWithThread(e.prj, slotUser)
	e.upstreamPull(orbID, 12)
	e.tick()
	_, shipped := e.shippedAt(thr)
	require.False(t, shipped)

	e.fake.Lock()
	for _, p := range e.fake.Pulls {
		if p.Number == 12 {
			p.MergedAt, p.MergedBy, p.UpdatedAt = time.Now().UTC().Truncate(time.Second), "alice", time.Now().UTC().Add(time.Second).Truncate(time.Second)
		}
	}
	e.fake.Unlock()
	e.tick()
	_, shipped = e.shippedAt(thr)
	assert.True(t, shipped)
	assert.Equal(t, "merged", e.pull(12).State)
	w, ok := e.watchRow("pull", "Acme/Widgets", 12)
	require.True(t, ok)
	require.NotNil(t, w.DoneAt)

	e.clock.Add(watchRetire + time.Minute)
	e.tick()
	_, ok = e.watchRow("pull", "Acme/Widgets", 12)
	assert.False(t, ok, "终态满 24 小时退场")
}

// hook 为 active 时不轮询：不登记仓库级 watch，已有的对象级 watch 注销，也不发请求。
func TestWatchSkipsActiveHook(t *testing.T) {
	e := newListenEnv(t)
	hookID := int64(555)
	require.NoError(t, e.st.Q.UpsertGithubHook(e.ctx, db.UpsertGithubHookParams{
		RepoID: 42, FullName: "Acme/Widgets", HookID: &hookID, SecretEnc: []byte("x"), Status: "active", UpdatedAt: 1,
	}))
	_, err := e.s.CreateTrigger(e.ctx, e.prj, apigen.GitHubTriggerInput{Name: "triage", Event: apigen.IssueOpened, Prompt: "look"})
	require.NoError(t, err)
	_, err = e.st.Q.GetGithubRepoWatch(e.ctx, 42)
	assert.True(t, isNoRows(err))
	e.s.registerWatch(e.ctx, "pull", 42, "acme/widgets", 12, e.thr)
	_, ok := e.watchRow("pull", "acme/widgets", 12)
	assert.False(t, ok)

	require.NoError(t, e.st.Q.UpsertGithubWatch(e.ctx, db.UpsertGithubWatchParams{
		Kind: "pull", RepoID: 42, FullName: "acme/widgets", Number: 12, ThreadID: e.thr, Slot: slotUser,
	}))
	e.drain() // 环境搭建时排队的任务（连接后的重新绑定）先跑完
	e.fake.ResetCalls()
	e.tick()
	assert.Empty(t, e.fake.CallsTo("GET", "/repos/*"))
	_, ok = e.watchRow("pull", "acme/widgets", 12)
	assert.False(t, ok)
}

// 无项目线程的上游 PR 能回流：令牌取线程 orb 的槽位，回流开关取默认开启。
func TestWatchNoProjectUpstream(t *testing.T) {
	e := newUpstreamEnv(t)
	o := e.orb("", slotUser, "")
	thr := e.bareThread(o.ID)
	e.upstreamPull(o.ID, 12)
	assert.Equal(t, []int64{12}, e.links(thr))
	w, ok := e.watchRow("pull", "Acme/Widgets", 12)
	require.True(t, ok)
	assert.Equal(t, slotUser, w.Slot)

	e.tick()
	e.fake.AddComment(githubtest.Comment{RepoID: 42, Number: 12, Author: "alice", Body: "please add a test"})
	e.fake.AddReview(githubtest.Review{RepoID: 42, Number: 12, Author: "alice", Body: "Needs work.", State: "CHANGES_REQUESTED"})
	e.tick()
	assert.Equal(t, []string{"handled"}, e.restOutcomes("rest:issue_comment.created:"))
	assert.Equal(t, []string{"handled"}, e.restOutcomes("rest:pull_request_review.submitted:"))
	var n int
	require.NoError(t, e.st.DB.QueryRowContext(e.ctx, `SELECT COUNT(*) FROM github_outbox WHERE thread_id = ? AND repo_id = 42 AND number = 12`, thr).Scan(&n))
	assert.Equal(t, 2, n)
}

// agent 经出站代理的写入与 Agent 账号的发言，经 REST 轮询也不回流。
func TestWatchSelfWriteNotEchoed(t *testing.T) {
	e := newUpstreamEnv(t)
	require.NoError(t, e.connect(slotAgent, "c2", "oryn-bot"))
	orbID, _ := e.orbWithThread(e.prj, slotUser)
	e.upstreamPull(orbID, 12)
	e.tick()

	// gh pr comment（GraphQL addComment，subjectId 是 PR 的 node id）：webhook 不可达，只能靠 REST 合成的事件认出
	w := egress.GitHubWrite{
		Method: "POST", Category: egress.CatCommentCreate, Op: "addComment", Target: githubtest.PullNodeID(42, 12),
		BodySHA256: egress.BodyHash("Rebased onto main."),
	}
	e.s.GitHubWriteBegin(e.ctx, orbID, w)
	e.s.GitHubWriteEnd(e.ctx, orbID, w, 200, nil)
	e.fake.AddComment(githubtest.Comment{RepoID: 42, Number: 12, Author: "owner", Body: "Rebased onto main."})
	e.fake.AddComment(githubtest.Comment{RepoID: 42, Number: 12, Author: "oryn-bot", Body: "hi"})
	// 主人本人手写的评论照常回流
	e.fake.AddComment(githubtest.Comment{RepoID: 42, Number: 12, Author: "owner", Body: "Please also update the docs."})
	e.tick()
	assert.ElementsMatch(t, []string{"ignored:self", "ignored:self", "handled"}, e.restOutcomes("rest:issue_comment.created:"))
	assert.Equal(t, 1, e.outboxCount())
}

// 槽位剩余额度低于 rateLimitFloor 时轮询间隔加倍。
func TestWatchRateLimitDoubles(t *testing.T) {
	e := newUpstreamEnv(t)
	orbID, _ := e.orbWithThread(e.prj, slotUser)
	e.upstreamPull(orbID, 12)
	e.fake.Lock()
	e.fake.CoreRemaining = rateLimitFloor - 1
	e.fake.Unlock()

	e.pollAt(0)
	require.True(t, e.s.watchLow[slotUser])
	e.fake.ResetCalls()
	e.pollAt(restPollInterval)
	assert.Empty(t, e.fake.CallsTo("GET", "/repos/Acme/Widgets/*"), "加倍后这一轮跳过")
	e.pollAt(restPollInterval)
	assert.NotEmpty(t, e.fake.CallsTo("GET", "/repos/Acme/Widgets/*"))

	// 额度恢复后回到每轮都轮询
	e.fake.Lock()
	e.fake.CoreRemaining = 4000
	e.fake.Unlock()
	e.pollAt(2 * restPollInterval)
	require.False(t, e.s.watchLow[slotUser])
	e.fake.ResetCalls()
	e.pollAt(restPollInterval)
	assert.NotEmpty(t, e.fake.CallsTo("GET", "/repos/Acme/Widgets/*"))
}

// 仓库级 watch：新 issue 触发规则；控制面自己的回帖不回流；时间线上的打标签合成 issues.labeled；规则全部停用后注销。
func TestWatchRepoLevel(t *testing.T) {
	e := newTriggerEnv(t)
	e.s.cfg.AfterFunc = (&fakeAfter{}).AfterFunc
	tr := e.trigger(apigen.GitHubTriggerInput{Name: "triage", Event: apigen.IssueOpened, Prompt: "look"})
	e.tick() // 基线
	w, err := e.st.Q.GetGithubRepoWatch(e.ctx, 42)
	require.NoError(t, err)
	require.NotNil(t, w.SinceAt)

	is := e.fake.AddIssue(githubtest.Issue{RepoID: 42, Number: 3, Title: "Crash on start", Author: "alice"})
	e.tick()
	require.Len(t, e.ct.created(), 1)
	assert.Equal(t, []string{"handled"}, e.restOutcomes("rest:issues.opened:acme/widgets:"+itoa(githubtest.IssueID(42, 3))+":"))
	_, ok := e.watchRow("issue", "Acme/Widgets", 3)
	assert.True(t, ok, "触发的线程登记对象级 watch")

	// 回执回帖（控制面直连 GitHub，补充键已记下）经仓库级与对象级轮询都认作自身写入
	e.tick()
	assert.Equal(t, []string{"ignored:self"}, e.restOutcomes("rest:issue_comment.created:"))

	e.fake.Lock()
	is.UpdatedAt = time.Now().UTC().Add(2 * time.Second).Truncate(time.Second)
	e.fake.Unlock()
	ev := e.fake.AddIssueEvent(githubtest.IssueEvent{RepoID: 42, Number: 3, Event: "labeled", Actor: "alice", Label: "bug"})
	e.tick()
	assert.Len(t, e.restOutcomes("rest:issues.labeled:acme/widgets:"+itoa(ev.ID)+":"), 1)
	assert.Len(t, e.ct.created(), 1)

	off := false
	_, err = e.s.UpdateTrigger(e.ctx, tr.ID, apigen.GitHubTriggerPatch{Enabled: &off})
	require.NoError(t, err)
	_, err = e.st.Q.GetGithubRepoWatch(e.ctx, 42)
	assert.True(t, isNoRows(err), "规则停用后注销")
}
