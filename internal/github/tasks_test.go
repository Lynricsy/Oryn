package github

import (
	"context"
	"strings"
	"sync"
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

// 步骤 4.3：状态机、终态应答、实现授权、任务时限与完成提醒。

// freezeOrbs 记下被冻结的 orb。
type freezeOrbs struct {
	mu     sync.Mutex
	frozen []string
}

func (o *freezeOrbs) Freeze(_ context.Context, orbID string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.frozen = append(o.frozen, orbID)
	return nil
}

func (o *freezeOrbs) list() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.frozen...)
}

type taskEnv struct {
	*triggerEnv
	orbs *freezeOrbs
	rule apigen.GitHubTrigger
}

// newTaskEnv 建一条 issue_opened 规则并让 alice 开 issue #7，得到一个任务线程。
func newTaskEnv(t *testing.T, in apigen.GitHubTriggerInput) *taskEnv {
	e := newTriggerEnv(t)
	o := &freezeOrbs{}
	e.s.SetOrbs(o)
	if in.Name == "" {
		in.Name, in.Prompt = "triage", "p"
	}
	in.Event, in.Announce = apigen.IssueOpened, new(false)
	te := &taskEnv{triggerEnv: e, orbs: o, rule: e.trigger(in)}
	g := e.deliver("issues", issuesEvent("opened", assocIssue(7, "alice", "MEMBER", "b"), "alice", ""))
	require.Equal(t, "handled", e.delivery(g).Outcome)
	return te
}

func (e *taskEnv) thread() string { return e.threadIssue(7).ThreadID }

func (e *taskEnv) orbOf(threadID string) string {
	e.t.Helper()
	th, err := e.st.Q.GetThread(e.ctx, threadID)
	require.NoError(e.t, err)
	require.NotNil(e.t, th.OrbID)
	return *th.OrbID
}

// R15：状态机——工具改状态、issue 关闭置 closed 并冻结 orb、终态不被覆盖、未知状态与没有任务的线程报错。
func TestTaskStateMachine(t *testing.T) {
	e := newTaskEnv(t, apigen.GitHubTriggerInput{})
	thr := e.thread()
	assert.Equal(t, taskNew, e.threadIssue(7).State)
	assert.True(t, strings.HasPrefix(e.ct.created()[0].Tag, turnTagPrefix), "首条提示词带 GitHub 标签")

	require.NoError(t, e.s.SetTaskState(e.ctx, thr, taskFixing, "repro ok"))
	assert.Equal(t, taskFixing, e.threadIssue(7).State)
	assert.Empty(t, e.orbs.list())
	assert.Equal(t, "invalid_request", codeOf(e.s.SetTaskState(e.ctx, thr, "done", "")))
	assert.Equal(t, "not_found", codeOf(e.s.SetTaskState(e.ctx, e.workThread(e.prj), taskFixing, "")))

	g := e.deliver("issues", issuesEvent("closed", assocIssue(7, "alice", "MEMBER", "b"), "oryn-bot", ""))
	assert.Equal(t, "handled", e.delivery(g).Outcome, "agent 自己关闭也同步状态")
	assert.Equal(t, taskClosed, e.threadIssue(7).State)
	assert.Equal(t, []string{e.orbOf(thr)}, e.orbs.list())
	g = e.deliver("issues", issuesEvent("closed", assocIssue(8, "alice", "MEMBER", "b"), "alice", ""))
	assert.Equal(t, "ignored:no_thread", e.delivery(g).Outcome)

	// 先合并、后因 Fixes #N 关闭：保持 merged
	require.NoError(t, e.s.SetTaskState(e.ctx, thr, taskMerged, ""))
	g = e.deliver("issues", issuesEvent("closed", assocIssue(7, "alice", "MEMBER", "b"), "alice", ""))
	require.Equal(t, "handled", e.delivery(g).Outcome)
	assert.Equal(t, taskMerged, e.threadIssue(7).State)

	// CleanupTask：置 abandoned 并冻结；未知的 issue 报 not_found
	require.NoError(t, e.s.CleanupTask(e.ctx, "Acme/Widgets", 7))
	assert.Equal(t, taskAbandoned, e.threadIssue(7).State)
	assert.Equal(t, "not_found", codeOf(e.s.CleanupTask(e.ctx, "acme/widgets", 99)))
}

// R15：PR 合并时关联线程的任务置 merged；PR 评审线程（fork PR，没有关联）随 PR 关闭置 closed。
func TestTaskPullMergedAndClosed(t *testing.T) {
	e := newTriggerEnv(t)
	o := &freezeOrbs{}
	e.s.SetOrbs(o)
	e.trigger(apigen.GitHubTriggerInput{Name: "m", Event: apigen.Mention, Prompt: "p", Announce: new(false)})
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 40, Title: "Same", Author: "alice"}, HeadRef: "f", HeadSHA: "s40"})
	g := e.deliver("issue_comment", issueCommentEvent(40, true, "alice", "@oryn-bot fix"))
	require.Equal(t, "handled", e.delivery(g).Outcome)
	merged := map[string]any{
		"action": "closed", "repository": repoJSON(), "sender": who("alice"),
		"pull_request": map[string]any{
			"number": 40, "title": "Same", "state": "closed", "merged": true, "merged_at": "2026-11-16T00:00:00Z",
			"html_url": "https://github.com/acme/widgets/pull/40", "user": who("alice"),
			"head": map[string]any{"ref": "f", "sha": "s40", "repo": map[string]any{"id": 42}}, "base": map[string]any{"ref": "main", "repo": map[string]any{"id": 42}},
		},
	}
	g = e.deliver("pull_request", merged)
	require.Equal(t, "handled", e.delivery(g).Outcome)
	assert.Equal(t, taskMerged, e.threadIssue(40).State)
	assert.Len(t, o.list(), 1)

	// PR 评审规则建在 fork PR 上的线程：PR 未合并关闭 → closed
	e.trigger(apigen.GitHubTriggerInput{Name: "review", Event: apigen.PullRequestOpened, Prompt: "p", Drivers: new(apigen.GitHubDriversAnyone), Announce: new(false)})
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 50, Title: "x", Author: "carol"}, HeadRef: "f", HeadSHA: "s50", HeadRepoID: 999})
	g = e.deliver("pull_request", pullOpenedEvent("opened", 50, "carol", "NONE", "carol", false))
	require.Equal(t, "handled", e.delivery(g).Outcome)
	closed := pullOpenedEvent("closed", 50, "carol", "NONE", "carol", false)
	closed["pull_request"].(map[string]any)["state"] = "closed"
	g = e.deliver("pull_request", closed)
	require.Equal(t, "handled", e.delivery(g).Outcome)
	assert.Equal(t, taskClosed, e.threadIssue(50).State)
}

// R15 + 3.1：issue 关闭后任务为 closed，退场的 issue watch 不再被补登记。
func TestTaskClosedStopsBackfill(t *testing.T) {
	e := newTaskEnv(t, apigen.GitHubTriggerInput{})
	_, ok := e.watchRow("issue", "acme/widgets", 7)
	require.True(t, ok, "规则建的 issue 线程登记对象级 watch")
	issueWatch := func() bool {
		ws, err := e.st.Q.ListGithubWatchesByRepo(e.ctx, 42)
		require.NoError(t, err)
		for _, w := range ws {
			if w.Kind == "issue" && w.Number == 7 {
				return true
			}
		}
		return false
	}
	deleteWatch := func() {
		ws, err := e.st.Q.ListGithubWatchesByRepo(e.ctx, 42)
		require.NoError(t, err)
		for _, w := range ws {
			_, err := e.st.Q.DeleteGithubWatch(e.ctx, db.DeleteGithubWatchParams{Kind: w.Kind, FullName: w.FullName, Number: w.Number})
			require.NoError(t, err)
		}
	}
	// 对照：任务未在终态时，补登记会恢复 watch
	deleteWatch()
	require.NoError(t, e.s.backfillWatches(e.ctx))
	require.True(t, issueWatch())

	g := e.deliver("issues", issuesEvent("closed", assocIssue(7, "alice", "MEMBER", "b"), "alice", ""))
	require.Equal(t, "handled", e.delivery(g).Outcome)
	assert.Equal(t, taskClosed, e.threadIssue(7).State)
	deleteWatch() // watch 在对象终态满 24 小时后退场
	require.NoError(t, e.s.backfillWatches(e.ctx))
	assert.False(t, issueWatch(), "closed 的任务不再补登记")
}

// R15：终态任务的非指令评论只收到一条固定回帖（预登记，回流的回帖认作自身写入）；维护者 @Agent 的指令重开任务。
func TestTaskFinalizedReply(t *testing.T) {
	e := newTaskEnv(t, apigen.GitHubTriggerInput{})
	thr := e.thread()
	require.NoError(t, e.s.SetTaskState(e.ctx, thr, taskClosed, ""))

	g := e.deliver("issue_comment", issueCommentEvent(7, false, "bob", "still broken for me"))
	assert.Equal(t, "ignored:finalized", e.delivery(g).Outcome)
	posts := e.fake.CallsTo("POST", "/repos/acme/widgets/issues/7/comments")
	require.Len(t, posts, 1)
	assert.Contains(t, string(posts[0].Body), finalizedReply())
	assert.Zero(t, e.outboxCount(), "不回流")

	// 回帖经轮询或 webhook 回来（我的账号发的）：预登记命中，不再回帖
	echo := issueCommentEvent(7, false, "owner", finalizedReply())
	echo["comment"].(map[string]any)["id"] = 991
	g = e.deliver("issue_comment", echo)
	assert.Equal(t, "ignored:self", e.delivery(g).Outcome)
	assert.Len(t, e.fake.CallsTo("POST", "/repos/acme/widgets/issues/7/comments"), 1)

	// 写权限者但不是维护者（association 为 NONE）的 @ 不算指令
	g = e.deliver("issue_comment", issueCommentEvent(7, false, "alice", "@oryn-bot reopen"))
	assert.Equal(t, "ignored:finalized", e.delivery(g).Outcome)
	// 维护者 @Agent：重开，评论照常回流
	directive := issueCommentEvent(7, false, "bob", "@oryn-bot please look again")
	directive["comment"].(map[string]any)["author_association"] = "MEMBER"
	g = e.deliver("issue_comment", directive)
	assert.Equal(t, "handled", e.delivery(g).Outcome)
	assert.Equal(t, taskNew, e.threadIssue(7).State)
	assert.Equal(t, 1, e.outboxCount())
}

// R16：accepted 标签（仓库 owner / 我的账号 / 规则维护者）或 go ahead 评论（OWNER / 规则维护者）授权实现。
func TestTaskImplAuth(t *testing.T) {
	e := newTaskEnv(t, apigen.GitHubTriggerInput{Maintainers: []string{"dave"}})
	implAuth := func() bool { return e.threadIssue(7).ImplAuthorized }

	g := e.deliver("issues", issuesEvent("labeled", assocIssue(7, "alice", "MEMBER", "b"), "carol", "accepted"))
	assert.Equal(t, "ignored:no_trigger", e.delivery(g).Outcome)
	assert.False(t, implAuth(), "只读的 carol 不能授权")
	g = e.deliver("issues", issuesEvent("labeled", assocIssue(7, "alice", "MEMBER", "b"), "acme", "Accepted"))
	assert.Equal(t, "handled", e.delivery(g).Outcome)
	assert.True(t, implAuth(), "仓库 owner 授权")

	// 重新开始：go ahead 评论
	_, err := e.st.Q.SetThreadIssueImplAuthorized(e.ctx, db.SetThreadIssueImplAuthorizedParams{RepoID: 42, Number: 7})
	require.NoError(t, err)
	member := issueCommentEvent(7, false, "bob", "Go ahead!")
	member["comment"].(map[string]any)["author_association"] = "MEMBER"
	g = e.deliver("issue_comment", member)
	require.Equal(t, "handled", e.delivery(g).Outcome)
	assert.False(t, implAuth(), "MEMBER 不是 OWNER，也不在维护者名单")
	// 规则维护者（只读的 dave）：能驱动，也能授权
	g = e.deliver("issue_comment", issueCommentEvent(7, false, "dave", "ok, GO AHEAD"))
	require.Equal(t, "handled", e.delivery(g).Outcome)
	assert.True(t, implAuth(), "规则维护者授权")
}

// R25：任务时限到点时中止当前回合、发通知，且只处理一次。
func TestTaskDeadline(t *testing.T) {
	e := newTaskEnv(t, apigen.GitHubTriggerInput{MaxDurationSec: new(600)})
	thr := e.thread()
	row := e.threadIssue(7)
	require.NotNil(t, row.DeadlineAt)
	e.s.scanDeadlines(e.ctx)
	assert.Empty(t, e.th.aborted, "未到点")

	e.clock.Add(10*time.Minute + time.Second)
	e.s.scan(e.ctx)
	assert.Equal(t, []string{thr}, e.th.aborted)
	require.Len(t, e.n.posts, 1)
	assert.Contains(t, e.n.posts[0], "GitHub task time limit reached")
	assert.Nil(t, e.threadIssue(7).DeadlineAt)
	e.s.scan(e.ctx)
	assert.Len(t, e.th.aborted, 1, "只处理一次")
}

// R25：完成提醒——规则开启、状态未完成、分类为 bug / documentation 或未分类时最多补发 2 次；经 TurnObserver 驱动，
// 只认 GitHub 投递正常结束的回合。
func TestTaskReminder(t *testing.T) {
	e := newTaskEnv(t, apigen.GitHubTriggerInput{CompletionReminders: new(true)})
	thr := e.thread()
	obs := e.s.TurnObserver()
	turn := func(tag, status string) {
		obs.TurnEnded(threads.TurnEvent{ThreadID: thr, Tag: tag, Status: status})
		e.drain()
	}
	turn("schedule-run", threads.TurnCompleted)
	turn(newTurnTag(), threads.TurnAborted)
	assert.Empty(t, e.delivered(), "别人的标签、非正常结束不提醒")

	turn(newTurnTag(), threads.TurnCompleted)
	d := e.delivered()
	require.Len(t, d, 1)
	assert.True(t, strings.HasPrefix(d[0], thr+"\n[Oryn completion reminder 1/2 for acme/widgets#7"), d[0])
	turn(newTurnTag(), threads.TurnCompleted)
	turn(newTurnTag(), threads.TurnCompleted)
	assert.Len(t, e.delivered(), maxCompletionReminders)
	assert.EqualValues(t, maxCompletionReminders, e.threadIssue(7).Reminders)

	// 分类为 question、或状态已推进，都不提醒
	for _, c := range []struct{ state, class string }{{taskNew, "question"}, {taskReviewing, ""}} {
		_, err := e.st.Q.UpdateThreadMeta(e.ctx, db.UpdateThreadMetaParams{Title: "t", TitleSource: "auto", UpdatedAt: 1, ID: thr})
		require.NoError(t, err)
		require.NoError(t, e.st.Q.UpsertThreadIssue(e.ctx, db.UpsertThreadIssueParams{RepoID: 42, Number: 7, ThreadID: thr, TriggerID: &e.rule.ID, CreatedAt: 2}))
		require.NoError(t, e.st.Q.SetThreadIssueTask(e.ctx, db.SetThreadIssueTaskParams{RuleID: &e.rule.ID, RepoID: 42, Number: 7}))
		_, err = e.st.Q.SetThreadIssueState(e.ctx, db.SetThreadIssueStateParams{State: c.state, ThreadID: thr})
		require.NoError(t, err)
		if c.class != "" {
			_, err = e.st.Q.SetThreadIssueClassification(e.ctx, db.SetThreadIssueClassificationParams{Classification: new(c.class), ThreadID: thr})
			require.NoError(t, err)
		}
		before := len(e.delivered())
		e.s.remind(e.ctx, thr)
		assert.Len(t, e.delivered(), before, c)
	}

	// 规则没开完成提醒：不提醒
	off := newTaskEnv(t, apigen.GitHubTriggerInput{})
	off.s.remind(off.ctx, off.thread())
	assert.Empty(t, off.delivered())
}

// 控制面自己的写入先预登记：响应还没回来时到达的事件也认得出。
func TestTaskSelfWritePreRegistered(t *testing.T) {
	e := newTaskEnv(t, apigen.GitHubTriggerInput{})
	path := issuePath("Acme/Widgets", 7, "/comments")
	assert.Equal(t, "/repos/acme/widgets/issues/7/comments", path)
	_, err := e.s.selfWrite(e.ctx, slotUser, "POST", path, egress.CatCommentCreate, []string{path}, map[string]any{"body": "x"}, "x")
	require.NoError(t, err)
	rows, err := e.st.Q.ListGithubSelfWrites(e.ctx, db.ListGithubSelfWritesParams{Target: path, Category: egress.CatCommentCreate, Since: 0})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, slotUser, rows[0].Slot)
}
