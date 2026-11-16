package github

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// stubNotifier 记录发出的通知（"线程\nkind\n正文"）；无线程通知另记在 system（"kind\n标题\n正文\n链接"）。
type stubNotifier struct {
	mu     sync.Mutex
	posts  []string
	system []string
}

func (n *stubNotifier) PostThread(_ context.Context, threadID, kind, body string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.posts = append(n.posts, threadID+"\n"+kind+"\n"+body)
	return nil
}

func (n *stubNotifier) PostSystem(_ context.Context, kind, title, body, link string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.system = append(n.system, kind+"\n"+title+"\n"+body+"\n"+link)
	return nil
}

func (n *stubNotifier) systemPosts() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.system...)
}

// titles 返回各条通知正文的标题部分（第一个 ": " 之前）。
func (n *stubNotifier) titles() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, p := range n.posts {
		head, _, _ := strings.Cut(strings.SplitN(p, "\n", 3)[2], ": ")
		out = append(out, head)
	}
	return out
}

// kinds 返回各条通知的 kind。
func (n *stubNotifier) kinds() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, p := range n.posts {
		out = append(out, strings.Split(p, "\n")[1])
	}
	return out
}

// failingThreads 的 Deliver 返回 err（为 nil 时照常记录）。
type failingThreads struct {
	*stubThreads
	err error
}

func (t *failingThreads) Deliver(ctx context.Context, threadID string, m threads.AutoMessage, ack func(error)) (func() bool, error) {
	if t.err != nil {
		return nil, t.err
	}
	return t.stubThreads.Deliver(ctx, threadID, m, ack)
}

func (e *testEnv) delivered() []string {
	e.th.mu.Lock()
	defer e.th.mu.Unlock()
	return append([]string(nil), e.th.delivered...)
}

// listenEnv 是回流用例的环境：Acme/Widgets 绑到一个项目，alice / bob 有写权限、carol 只读，
// 线程 thr 关联 PR #12。
type listenEnv struct {
	*testEnv
	prj string
	thr string
	n   *stubNotifier
}

func newListenEnv(t *testing.T) *listenEnv {
	e := newEnv(t)
	n := &stubNotifier{}
	e.s.n = n
	prj := e.boundProject()
	for i, login := range []string{"alice", "bob", "carol"} {
		e.fake.AddUser(int64(30+i), login, "")
	}
	// 我的账号要有 push 权限，canDrive 才会调权限接口（M15：权限接口要求调用者有 push 权限）
	e.setPerm("owner", "admin")
	e.setPerm("alice", "write")
	e.setPerm("bob", "admin")
	e.setPerm("carol", "read")
	le := &listenEnv{testEnv: e, prj: prj, n: n}
	le.thr = le.workThread(prj)
	le.linkPull(le.thr, 12)
	return le
}

// workThread 在项目下建一个工作线程。
func (e *testEnv) workThread(projectID string) string {
	e.t.Helper()
	id := ids.New("thr")
	now := e.clock.Now().UnixMilli()
	require.NoError(e.t, e.st.Q.CreateThread(e.ctx, db.CreateThreadParams{
		ID: id, ProjectID: &projectID, Title: "t", TitleSource: "auto", State: "idle", StateChangedAt: now,
		SessionDir: "d", CreatedAt: now, UpdatedAt: now, Kind: "work",
	}))
	return id
}

// linkPull 记一个 acme/widgets 的 PR 并把它关联到线程。
func (e *testEnv) linkPull(threadID string, number int64) {
	e.t.Helper()
	require.NoError(e.t, e.st.Q.UpsertGithubPull(e.ctx, db.UpsertGithubPullParams{
		RepoID: 42, Number: number, RepoFullName: "acme/widgets", Title: "Fix login redirect loop", State: "open",
		SameRepo: true, HeadRef: "fix", HeadSha: "abc", BaseRef: "main", AuthorLogin: "owner",
		HtmlUrl: "https://github.com/acme/widgets/pull/" + itoa(number), UpdatedAt: 1,
	}))
	n, err := e.st.Q.LinkThreadPull(e.ctx, db.LinkThreadPullParams{ThreadID: threadID, RepoID: 42, Number: number, LinkedAt: 1})
	require.NoError(e.t, err)
	require.EqualValues(e.t, 1, n)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func repoJSON() map[string]any { return map[string]any{"id": 42, "full_name": "acme/widgets"} }

func who(login string) map[string]any { return map[string]any{"login": login, "type": "User"} }

func pullJSON(number int64) map[string]any {
	return map[string]any{"number": number, "title": "Fix login redirect loop", "html_url": "https://github.com/acme/widgets/pull/" + itoa(number)}
}

func reviewEvent(number int64, login, state, body string) map[string]any {
	return map[string]any{
		"action": "submitted", "repository": repoJSON(), "sender": who(login), "pull_request": pullJSON(number),
		"review": map[string]any{"state": state, "body": body, "user": who(login)},
	}
}

func reviewCommentEvent(number int64, login, path string, line any, body string) map[string]any {
	return map[string]any{
		"action": "created", "repository": repoJSON(), "sender": who(login), "pull_request": pullJSON(number),
		"comment": map[string]any{"id": 1, "path": path, "line": line, "original_line": 7, "body": body, "user": who(login)},
	}
}

func issueCommentEvent(number int64, pull bool, login, body string) map[string]any {
	issue := map[string]any{"number": number, "title": "Login loops", "html_url": "https://github.com/acme/widgets/issues/" + itoa(number), "user": who("dave")}
	if pull {
		issue["title"] = "Fix login redirect loop"
		issue["html_url"] = "https://github.com/acme/widgets/pull/" + itoa(number)
		issue["pull_request"] = map[string]any{"url": "x"}
	}
	return map[string]any{
		"action": "created", "repository": repoJSON(), "sender": who(login), "issue": issue,
		"comment": map[string]any{"id": 77, "body": body, "user": who(login)},
	}
}

func (e *testEnv) outboxCount() int {
	e.t.Helper()
	var n int
	require.NoError(e.t, e.st.DB.QueryRowContext(e.ctx, `SELECT COUNT(*) FROM github_outbox`).Scan(&n))
	return n
}

const goldenPull = "[Oryn GitHub event on acme/widgets#12 — delivered automatically, not typed by the owner]\n" +
	"Pull request: Fix login redirect loop (https://github.com/acme/widgets/pull/12)\n" +
	"\n" +
	"@alice reviewed (changes requested):\n" +
	"Please fix the redirect.\n" +
	"\n" +
	"@alice commented on internal/auth/login.go:42:\n" +
	"This loops forever.\n" +
	"\n" +
	"@bob commented on internal/auth/session.go:7:\n" +
	"Same here.\n" +
	"\n" +
	"The comments above were written by GitHub users with write access to the repository. Address them on the pull request branch and push; reply with `gh pr comment` where a reply helps."

func TestListenReviewsGolden(t *testing.T) {
	e := newListenEnv(t)
	g := e.deliver("pull_request_review", reviewEvent(12, "alice", "changes_requested", "Please fix the redirect."))
	assert.Equal(t, "handled", e.delivery(g).Outcome)
	e.clock.Add(time.Second)
	e.deliver("pull_request_review_comment", reviewCommentEvent(12, "alice", "internal/auth/login.go", 42, "This loops forever."))
	e.clock.Add(time.Second)
	e.deliver("pull_request_review_comment", reviewCommentEvent(12, "bob", "internal/auth/session.go", nil, "Same here."))

	e.s.flush(e.ctx) // 第一条还没到期
	assert.Empty(t, e.delivered())
	e.clock.Add(14 * time.Second)
	e.s.flush(e.ctx)
	got := e.delivered()
	require.Len(t, got, 1)
	assert.Equal(t, e.thr+"\n"+goldenPull, got[0])
	e.clock.Add(time.Minute)
	e.s.flush(e.ctx)
	assert.Len(t, e.delivered(), 1)

	// 空的 commented 评审不回流
	g = e.deliver("pull_request_review", reviewEvent(12, "alice", "commented", ""))
	assert.Equal(t, "ignored:empty", e.delivery(g).Outcome)
	// 没有目标线程、仓库未绑定
	g = e.deliver("pull_request_review", reviewEvent(99, "alice", "approved", ""))
	assert.Equal(t, "ignored:no_thread", e.delivery(g).Outcome)
	other := reviewEvent(12, "alice", "approved", "")
	other["repository"] = map[string]any{"id": 4242, "full_name": "x/y"}
	g = e.deliver("pull_request_review", other)
	assert.Equal(t, "ignored:unbound_repo", e.delivery(g).Outcome)
}

func TestComposeEventVariants(t *testing.T) {
	row := func(kind string, it string, isPull bool, at int64) db.GithubOutbox {
		return db.GithubOutbox{Kind: kind, Item: it, IsPull: isPull, Title: "Login loops", Url: "https://github.com/acme/widgets/issues/7", CreatedAt: at}
	}
	got := composeEvent("acme/widgets", 7, []db.GithubOutbox{
		row("comment", `{"actor":"bob","body":"Still broken"}`, false, 1),
	})
	assert.Equal(t, "[Oryn GitHub event on acme/widgets#7 — delivered automatically, not typed by the owner]\n"+
		"Issue: Login loops (https://github.com/acme/widgets/issues/7)\n\n"+
		"@bob commented:\nStill broken\n\n"+
		"The comments above were written by GitHub users with write access to the repository. Reply with `gh issue comment` where a reply helps.", got)
	got = composeEvent("acme/widgets", 12, []db.GithubOutbox{
		row("review", `{"actor":"alice","state":"approved"}`, true, 1),
	})
	assert.Contains(t, got, "\n\n@alice reviewed (approved)\n\nThe comments above")
}

func TestListenAdmission(t *testing.T) {
	e := newListenEnv(t)
	// 所有者本人（user 槽位）照常回流
	g := e.deliver("issue_comment", issueCommentEvent(12, true, "owner", "please rebase"))
	assert.Equal(t, "handled", e.delivery(g).Outcome)
	// Agent 账号 → self
	require.NoError(t, e.connect("agent", "c2", "oryn-bot"))
	g = e.deliver("issue_comment", issueCommentEvent(12, true, "oryn-bot", "hi"))
	assert.Equal(t, "ignored:self", e.delivery(g).Outcome)
	// 只读权限
	g = e.deliver("issue_comment", issueCommentEvent(12, true, "carol", "hi"))
	assert.Equal(t, "ignored:no_write_access", e.delivery(g).Outcome)
	bot := issueCommentEvent(12, true, "ci[bot]", "hi")
	bot["sender"] = map[string]any{"login": "ci[bot]", "type": "Bot"}
	g = e.deliver("issue_comment", bot)
	assert.Equal(t, "ignored:bot", e.delivery(g).Outcome)
	assert.Equal(t, 1, e.outboxCount())

	// 关联暂停 → muted
	_, err := e.st.Q.SetThreadPullListening(e.ctx, db.SetThreadPullListeningParams{Listening: false, ThreadID: e.thr, RepoID: 42, Number: 12})
	require.NoError(t, err)
	g = e.deliver("issue_comment", issueCommentEvent(12, true, "alice", "hi"))
	assert.Equal(t, "ignored:muted", e.delivery(g).Outcome)
}

func TestListenProjectSwitches(t *testing.T) {
	e := newListenEnv(t)
	// 第二个项目绑同一仓库，关联 PR #13；第一个项目关掉评审回流
	prj2 := e.project("https://github.com/acme/widgets")
	require.NoError(t, e.s.BindProject(e.ctx, prj2))
	thr2 := e.workThread(prj2)
	e.linkPull(thr2, 13)
	require.NoError(t, e.st.Q.SetProjectGithubListen(e.ctx, db.SetProjectGithubListenParams{
		GithubListenReviews: false, GithubListenComments: true, GithubListenCi: true, UpdatedAt: 2, ID: e.prj,
	}))
	g := e.deliver("pull_request_review", reviewEvent(12, "alice", "approved", "LGTM"))
	assert.Equal(t, "ignored:muted", e.delivery(g).Outcome)
	g = e.deliver("pull_request_review_comment", reviewCommentEvent(12, "alice", "a.go", 1, "x"))
	assert.Equal(t, "ignored:muted", e.delivery(g).Outcome)
	g = e.deliver("issue_comment", issueCommentEvent(12, true, "alice", "hi"))
	assert.Equal(t, "handled", e.delivery(g).Outcome)
	g = e.deliver("pull_request_review", reviewEvent(13, "alice", "approved", "LGTM"))
	assert.Equal(t, "handled", e.delivery(g).Outcome)
}

func TestListenRateLimit(t *testing.T) {
	e := newListenEnv(t)
	for i := range maxPerLinkPerHour {
		g := e.deliver("issue_comment", issueCommentEvent(12, true, "owner", "c"+itoa(int64(i))))
		require.Equal(t, "handled", e.delivery(g).Outcome)
	}
	g := e.deliver("issue_comment", issueCommentEvent(12, true, "owner", "one more"))
	assert.Equal(t, "ignored:rate_limited", e.delivery(g).Outcome)
	assert.Equal(t, []string{"GitHub events paused"}, e.n.titles())
	rows, err := e.st.Q.ListThreadPulls(e.ctx, e.thr)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.False(t, rows[0].Listening)
	assert.Contains(t, e.th.published, e.thr)

	// issue 关联超限：只丢弃，不通知
	thr2 := e.workThread(e.prj)
	require.NoError(t, e.st.Q.UpsertThreadIssue(e.ctx, db.UpsertThreadIssueParams{RepoID: 42, Number: 7, ThreadID: thr2, CreatedAt: 1}))
	for range maxPerLinkPerHour {
		g = e.deliver("issue_comment", issueCommentEvent(7, false, "owner", "x"))
		require.Equal(t, "handled", e.delivery(g).Outcome)
	}
	g = e.deliver("issue_comment", issueCommentEvent(7, false, "owner", "x"))
	assert.Equal(t, "ignored:rate_limited", e.delivery(g).Outcome)
	assert.Len(t, e.n.titles(), 1)

	// 一小时后恢复额度（PR 关联仍是暂停的）
	e.clock.Add(time.Hour + time.Second)
	g = e.deliver("issue_comment", issueCommentEvent(7, false, "owner", "x"))
	assert.Equal(t, "handled", e.delivery(g).Outcome)
}

func TestFlushDeliverErrors(t *testing.T) {
	e := newListenEnv(t)
	ft := &failingThreads{stubThreads: e.th, err: &threads.Error{Code: "queue_full", Message: "full"}}
	e.s.th = ft
	e.deliver("issue_comment", issueCommentEvent(12, true, "owner", "hi"))
	e.clock.Add(16 * time.Second)
	e.s.flush(e.ctx)
	assert.Empty(t, e.delivered())
	ft.err = nil
	e.s.flush(e.ctx)
	assert.Len(t, e.delivered(), 1)

	ft.err = &threads.Error{Code: "archived", Message: "archived"}
	e.deliver("issue_comment", issueCommentEvent(12, true, "owner", "again"))
	e.clock.Add(16 * time.Second)
	e.s.flush(e.ctx)
	ft.err = nil
	e.s.flush(e.ctx)
	assert.Len(t, e.delivered(), 1) // 已丢弃，不重试

	// 超过最长保留时间的失败行被丢弃
	ft.err = &threads.Error{Code: "queue_full", Message: "full"}
	e.deliver("issue_comment", issueCommentEvent(12, true, "owner", "late"))
	e.clock.Add(outboxMaxAge + time.Minute)
	e.s.flush(e.ctx)
	ft.err = nil
	e.s.flush(e.ctx)
	assert.Len(t, e.delivered(), 1)
}

func TestIssueCommentOnIssueLink(t *testing.T) {
	e := newListenEnv(t)
	thr2 := e.workThread(e.prj)
	require.NoError(t, e.st.Q.UpsertThreadIssue(e.ctx, db.UpsertThreadIssueParams{RepoID: 42, Number: 7, ThreadID: thr2, CreatedAt: 1}))
	g := e.deliver("issue_comment", issueCommentEvent(7, false, "bob", "Still broken"))
	assert.Equal(t, "handled", e.delivery(g).Outcome)
	// fork PR：没有 PR 关联，走 issue 关联
	require.NoError(t, e.st.Q.UpsertThreadIssue(e.ctx, db.UpsertThreadIssueParams{RepoID: 42, Number: 30, ThreadID: thr2, CreatedAt: 1}))
	g = e.deliver("issue_comment", issueCommentEvent(30, true, "bob", "fork pr"))
	assert.Equal(t, "handled", e.delivery(g).Outcome)
	g = e.deliver("issue_comment", issueCommentEvent(8, false, "bob", "nobody"))
	assert.Equal(t, "ignored:no_thread", e.delivery(g).Outcome)

	e.clock.Add(16 * time.Second)
	e.s.flush(e.ctx)
	got := e.delivered()
	require.Len(t, got, 2)
	assert.Contains(t, strings.Join(got, "\n---\n"), thr2+"\n[Oryn GitHub event on acme/widgets#7 — delivered automatically, not typed by the owner]\nIssue: Login loops")
	assert.Contains(t, strings.Join(got, "\n---\n"), "[Oryn GitHub event on acme/widgets#30 — delivered automatically, not typed by the owner]\nPull request: Fix login redirect loop")
}
