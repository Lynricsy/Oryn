package github

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 步骤 5.5：问题自动关闭。

// newAutocloseEnv 是任务线程 + agent（我的账号 owner）在 #7 上回答过的环境：owner 的答复是最近一条自己的评论。
func newAutocloseEnv(t *testing.T) (*taskEnv, *githubtest.Comment) {
	e := newToolEnv(t, apigen.GitHubTriggerInput{})
	e.fake.AddComment(githubtest.Comment{RepoID: 42, Number: 7, Author: "owner", Body: "first"})
	e.fake.AddComment(githubtest.Comment{RepoID: 42, Number: 7, Author: "alice", Body: "but why"})
	answer := e.fake.AddComment(githubtest.Comment{RepoID: 42, Number: 7, Author: "owner", Body: "because"})
	e.fake.AddComment(githubtest.Comment{RepoID: 42, Number: 7, Author: "bob", Body: "+1"})
	return e, answer
}

func (e *taskEnv) autoclose() db.GithubAutoclose {
	e.t.Helper()
	ac, err := e.st.Q.GetGithubAutoclose(e.ctx, db.GetGithubAutocloseParams{RepoID: 42, Number: 7})
	require.NoError(e.t, err)
	return ac
}

func (e *taskEnv) schedule() {
	e.t.Helper()
	_, err := e.call("github_autoclose", `{}`)
	require.NoError(e.t, err)
}

func (e *taskEnv) dueScan() {
	e.clock.Add(defaultAutocloseHours*time.Hour + time.Second)
	e.s.scan(e.ctx)
}

// github_autoclose 记下线程身份最近一条评论、issue 作者与到期时间（规则值或默认 4 小时，可用 hours 覆盖）。
func TestAutocloseSchedule(t *testing.T) {
	e := newToolEnv(t, apigen.GitHubTriggerInput{})
	_, err := e.call("github_autoclose", `{}`)
	assert.Equal(t, "no_comment", codeOf(err), "还没回答")
	_, err = e.call("github_autoclose", `{"hours":0}`)
	assert.Equal(t, codeInvalidParams, codeOf(err))

	e.fake.AddComment(githubtest.Comment{RepoID: 42, Number: 7, Author: "owner", Body: "older"})
	answer := e.fake.AddComment(githubtest.Comment{RepoID: 42, Number: 7, Author: "owner", Body: "answer"})
	res, err := e.call("github_autoclose", `{}`)
	require.NoError(t, err)
	assert.Contains(t, res.Text, "in 4 hours unless alice reacts")
	ac := e.autoclose()
	assert.Equal(t, answer.ID, ac.CommentID)
	assert.Equal(t, "alice", ac.Author)
	assert.Equal(t, "pending", ac.State)
	assert.Equal(t, e.clock.Now().Add(4*time.Hour).UnixMilli(), ac.DueAt)

	_, err = e.call("github_autoclose", `{"hours":2}`)
	require.NoError(t, err)
	assert.Equal(t, e.clock.Now().Add(2*time.Hour).UnixMilli(), e.autoclose().DueAt, "重新排期覆盖")

	// 规则配置的小时数
	_, err = e.s.UpdateTrigger(e.ctx, e.rule.ID, apigen.GitHubTriggerPatch{AutocloseHours: new(9)})
	require.NoError(t, err)
	_, err = e.call("github_autoclose", `{}`)
	require.NoError(t, err)
	assert.Equal(t, e.clock.Now().Add(9*time.Hour).UnixMilli(), e.autoclose().DueAt)
}

// 到期时 issue 作者 👎：取消，不关闭。
func TestAutocloseAuthorThumbsDown(t *testing.T) {
	e, answer := newAutocloseEnv(t)
	e.schedule()
	e.fake.AddReaction(answer.ID, "alice", "-1")
	e.dueScan()
	ac := e.autoclose()
	assert.Equal(t, "cancelled", ac.State)
	assert.Equal(t, autocloseThumbsDown, *ac.Outcome)
	assert.Empty(t, e.fake.CallsTo("PATCH", "/repos/acme/widgets/issues/7"))
	assert.Equal(t, "open", e.fake.IssueState(42, 7))
}

// 非作者的 👎 不算：以 completed 关闭（线程身份的令牌，预登记防回流）；关闭事件回来后任务置为 closed。
func TestAutocloseNonAuthorThumbsDown(t *testing.T) {
	e, answer := newAutocloseEnv(t)
	e.schedule()
	e.fake.AddReaction(answer.ID, "bob", "-1")
	e.fake.AddReaction(answer.ID, "alice", "+1")
	e.s.scan(e.ctx)
	assert.Equal(t, "pending", e.autoclose().State, "未到期")
	e.dueScan()
	ac := e.autoclose()
	assert.Equal(t, "done", ac.State)
	assert.Equal(t, autocloseClosed, *ac.Outcome)
	patches := e.fake.CallsTo("PATCH", "/repos/acme/widgets/issues/7")
	require.Len(t, patches, 1)
	assert.JSONEq(t, `{"state":"closed","state_reason":"completed"}`, string(patches[0].Body))
	assert.Equal(t, e.userAuth(), patches[0].Auth)
	assert.Equal(t, "closed", e.fake.IssueState(42, 7))
	rows, err := e.st.Q.ListGithubSelfWrites(e.ctx, db.ListGithubSelfWritesParams{Target: "/repos/acme/widgets/issues/7", Category: egress.CatIssueUpdate, Since: 0})
	require.NoError(t, err)
	assert.Len(t, rows, 1)

	g := e.deliver("issues", issuesEvent("closed", assocIssue(7, "alice", "MEMBER", "b"), "owner", ""))
	require.Equal(t, "handled", e.delivery(g).Outcome)
	assert.Equal(t, taskClosed, e.threadIssue(7).State)
	assert.Equal(t, "done", e.autoclose().State, "已执行的排期不受关闭事件影响")
	e.dueScan()
	assert.Len(t, e.fake.CallsTo("PATCH", "/repos/acme/widgets/issues/7"), 1, "只执行一次")
}

// 期间有人发新评论：同步取消。
func TestAutocloseNewCommentCancels(t *testing.T) {
	e, _ := newAutocloseEnv(t)
	e.schedule()
	g := e.deliver("issue_comment", issueCommentEvent(7, false, "carol", "this is still a problem"))
	require.Equal(t, "ignored:no_write_access", e.delivery(g).Outcome, "不能驱动的人发评论也取消")
	ac := e.autoclose()
	assert.Equal(t, "cancelled", ac.State)
	assert.Equal(t, autocloseCommented, *ac.Outcome)
	e.dueScan()
	assert.Empty(t, e.fake.CallsTo("PATCH", "/repos/acme/widgets/issues/7"))
}

// issue 被外部关闭：同步取消（任务置为 closed）。
func TestAutocloseExternalCloseCancels(t *testing.T) {
	e, _ := newAutocloseEnv(t)
	e.schedule()
	g := e.deliver("issues", issuesEvent("closed", assocIssue(7, "alice", "MEMBER", "b"), "alice", ""))
	require.Equal(t, "handled", e.delivery(g).Outcome)
	ac := e.autoclose()
	assert.Equal(t, "cancelled", ac.State)
	assert.Equal(t, autocloseExternalClose, *ac.Outcome)
	e.dueScan()
	assert.Empty(t, e.fake.CallsTo("PATCH", "/repos/acme/widgets/issues/7"))
}

// 并发的扫描只关闭一次；关闭回 404 时记为 already_closed。
func TestAutocloseConcurrentAndNotFound(t *testing.T) {
	e, _ := newAutocloseEnv(t)
	e.schedule()
	e.clock.Add(defaultAutocloseHours*time.Hour + time.Second)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.s.scanAutoclose(e.ctx)
		}()
	}
	wg.Wait()
	assert.Len(t, e.fake.CallsTo("PATCH", "/repos/acme/widgets/issues/7"), 1)
	assert.Equal(t, autocloseClosed, *e.autoclose().Outcome)

	// 重新排期（issue 重开后），关闭时 404
	e.fake.Lock()
	for _, is := range e.fake.Issues {
		if is.Number == 7 {
			is.State = "open"
		}
	}
	e.fake.Fail = map[string]int{"PATCH /repos/acme/widgets/issues/7": 404}
	e.fake.Unlock()
	e.schedule()
	e.dueScan()
	ac := e.autoclose()
	assert.Equal(t, "done", ac.State)
	assert.Equal(t, autocloseAlreadyClosed, *ac.Outcome)
}
