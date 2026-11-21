package github

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// withNode 给事件里的 issue / pull_request 补上 node_id。
func withNode(ev map[string]any, key, nodeID string) map[string]any {
	ev[key].(map[string]any)["node_id"] = nodeID
	return ev
}

// orbWithThread 建一台 identity 身份的 orb，并在其上建一个工作线程。
func (e *testEnv) orbWithThread(projectID, identity string) (orbID, threadID string) {
	e.t.Helper()
	o := e.orb(projectID, identity, "")
	id := ids.New("thr")
	now := e.clock.Now().UnixMilli()
	require.NoError(e.t, e.st.Q.CreateThread(e.ctx, db.CreateThreadParams{
		ID: id, ProjectID: &projectID, OrbID: &o.ID, Title: "t", TitleSource: "auto", State: "idle", StateChangedAt: now,
		SessionDir: "d", CreatedAt: now, UpdatedAt: now, Kind: "work",
	}))
	return o.ID, id
}

// afterNow 把服务的 AfterFunc 换成立即执行（先跑 before，再触发），记录每次的时长。
func (e *testEnv) afterNow(before func()) *[]time.Duration {
	var mu sync.Mutex
	var got []time.Duration
	e.s.cfg.AfterFunc = func(d time.Duration, f func()) func() {
		mu.Lock()
		got = append(got, d)
		mu.Unlock()
		if before != nil {
			before()
		}
		f()
		return func() {}
	}
	return &got
}

func (e *testEnv) audits() []db.GithubAudit {
	e.t.Helper()
	rows, err := e.st.Q.ListGithubAudit(e.ctx, db.ListGithubAuditParams{Before: 1 << 62, Limit: 100})
	require.NoError(e.t, err)
	return rows
}

// M15 2.3：Agent 账号发出的事件一律是自身写入；其它登录名（含空）在没有预登记时不是。
func TestSelfWriteAgentLogin(t *testing.T) {
	e := newListenEnv(t)
	probe := func(login string) selfProbe { return selfProbe{sender: sender{Login: login}} }
	assert.False(t, e.s.isSelfEvent(e.ctx, probe("oryn-bot")))
	require.NoError(t, e.connect("agent", "c2", "oryn-bot"))
	assert.True(t, e.s.isSelfEvent(e.ctx, probe("Oryn-Bot")))
	assert.False(t, e.s.isSelfEvent(e.ctx, probe("owner")))
	assert.False(t, e.s.isSelfEvent(e.ctx, probe("")))
}

// M15 2.3：webhook 比上游响应先到（只有 Begin 的预登记）时仍判为自身写入：GraphQL 按 node id，REST 按规范化路径；
// 正文按规范化后的哈希比较。
func TestSelfWriteBeforeResponse(t *testing.T) {
	e := newListenEnv(t)
	orbID := e.orb(e.prj, "user", "").ID
	w := egress.GitHubWrite{Method: "POST", Category: egress.CatCommentCreate, Target: "PR_42_12", Op: "addComment", BodySHA256: egress.BodyHash("Rebased onto main.\r\n")}
	assert.Empty(t, e.s.GitHubWriteBegin(e.ctx, orbID, w))
	g := e.deliver("issue_comment", withNode(issueCommentEvent(12, true, "owner", "  Rebased onto main."), "issue", "PR_42_12"))
	assert.Equal(t, "ignored:self", e.delivery(g).Outcome)
	e.s.GitHubWriteEnd(e.ctx, orbID, w, http.StatusOK, nil)

	rest := egress.GitHubWrite{
		Method: "POST", Category: egress.CatCommentCreate, Target: "/repos/acme/widgets/issues/12/comments",
		Op: "POST /repos/{owner}/{repo}/issues/{number}/comments", Repo: "acme/widgets", BodySHA256: egress.BodyHash("CI is green"),
	}
	e.s.GitHubWriteBegin(e.ctx, orbID, rest)
	g = e.deliver("issue_comment", issueCommentEvent(12, true, "owner", "CI is green"))
	assert.Equal(t, "ignored:self", e.delivery(g).Outcome)
	e.s.GitHubWriteEnd(e.ctx, orbID, rest, http.StatusCreated, []string{"comment:4242"})
	assert.Zero(t, e.outboxCount())
}

// M15 2.3：主人本人手写的、同一目标但正文不同的评论照常回流；正文相同但发送者不是该槽位的也回流。
// 写操作在途时按服务时钟延后复查一次。
func TestSelfWriteOwnerHandwritten(t *testing.T) {
	e := newListenEnv(t)
	orbID := e.orb(e.prj, "user", "").ID
	waits := e.afterNow(nil)
	w := egress.GitHubWrite{Method: "POST", Category: egress.CatCommentCreate, Target: "PR_42_12", Op: "addComment", BodySHA256: egress.BodyHash("agent reply")}
	e.s.GitHubWriteBegin(e.ctx, orbID, w)
	g := e.deliver("issue_comment", withNode(issueCommentEvent(12, true, "owner", "please also fix the docs"), "issue", "PR_42_12"))
	assert.Equal(t, "handled", e.delivery(g).Outcome)
	assert.Equal(t, []time.Duration{selfRecheckDelay}, *waits, "槽位有写操作在途：复查一次")
	e.s.GitHubWriteEnd(e.ctx, orbID, w, http.StatusOK, nil)

	g = e.deliver("issue_comment", withNode(issueCommentEvent(12, true, "alice", "agent reply"), "issue", "PR_42_12"))
	assert.Equal(t, "handled", e.delivery(g).Outcome)
	g = e.deliver("issue_comment", withNode(issueCommentEvent(12, true, "owner", "another one"), "issue", "PR_42_12"))
	assert.Equal(t, "handled", e.delivery(g).Outcome)
	assert.Len(t, *waits, 1, "没有在途写操作时不等待")
	assert.Equal(t, 3, e.outboxCount())
}

// M15 2.3：随评审一次提交的行内评论在响应回来之前到达：复查时评审的补充键已落库，判为自身写入。
func TestSelfWriteRecheckReviewComment(t *testing.T) {
	e := newListenEnv(t)
	orbID := e.orb(e.prj, "user", "").ID
	w := egress.GitHubWrite{
		Method: "POST", Category: egress.CatReview, Target: "/repos/acme/widgets/pulls/12/reviews",
		Op: "POST /repos/{owner}/{repo}/pulls/{number}/reviews", Repo: "acme/widgets", BodySHA256: egress.BodyHash("Two nits."),
	}
	e.s.GitHubWriteBegin(e.ctx, orbID, w)
	waits := e.afterNow(func() {
		e.clock.Add(selfRecheckDelay)
		e.s.GitHubWriteEnd(e.ctx, orbID, w, http.StatusOK, []string{"review:900", "node:PRR_900"})
	})
	ev := reviewCommentEvent(12, "owner", "a.go", 3, "nit: rename")
	ev["comment"].(map[string]any)["pull_request_review_id"] = 900
	g := e.deliver("pull_request_review_comment", ev)
	assert.Equal(t, "ignored:self", e.delivery(g).Outcome)
	assert.Equal(t, []time.Duration{selfRecheckDelay}, *waits)

	ev = reviewCommentEvent(12, "owner", "a.go", 4, "and this")
	ev["comment"].(map[string]any)["pull_request_review_id"] = 901
	g = e.deliver("pull_request_review_comment", ev)
	assert.Equal(t, "handled", e.delivery(g).Outcome)
	assert.Len(t, *waits, 1)
}

// M15 2.3：无正文的动作（打标签）在 selfWriteWindow 内匹配预登记，过了时间窗就照常触发。
func TestSelfWriteBodylessWindow(t *testing.T) {
	e := newTriggerEnv(t)
	e.trigger(apigen.GitHubTriggerInput{Name: "Triage", Event: "issue_labeled", Label: new("oryn"), Prompt: "Triage {{url}}"})
	orbID := e.orb(e.prj, "user", "").ID
	for _, n := range []string{"7", "8"} {
		w := egress.GitHubWrite{Method: "POST", Category: egress.CatIssueUpdate, Target: "I_42_" + n, Op: "addLabelsToLabelable"}
		e.s.GitHubWriteBegin(e.ctx, orbID, w)
		e.s.GitHubWriteEnd(e.ctx, orbID, w, http.StatusOK, nil)
	}
	g := e.deliver("issues", withNode(issuesEvent("labeled", issueJSON(7, "x", "", "carol"), "owner", "oryn"), "issue", "I_42_7"))
	assert.Equal(t, "ignored:self", e.delivery(g).Outcome)

	e.clock.Add(selfWriteWindow + time.Second)
	g = e.deliver("issues", withNode(issuesEvent("labeled", issueJSON(8, "x", "", "carol"), "owner", "oryn"), "issue", "I_42_8"))
	assert.Equal(t, "handled", e.delivery(g).Outcome)
	assert.Len(t, e.ct.created(), 1)
}

// M15 2.3：触发回执的回帖（rememberSelfKeys 记下的评论 id）回流时判为自身写入；主人的其它评论照常回流。
func TestSelfWriteAnnounceReply(t *testing.T) {
	e := newTriggerEnv(t)
	e.trigger(apigen.GitHubTriggerInput{Name: "Triage", Event: "issue_labeled", Label: new("oryn"), Prompt: "Triage {{url}}"})
	g := e.deliver("issues", issuesEvent("labeled", issueJSON(7, "x", "", "carol"), "alice", "oryn"))
	require.Equal(t, "handled", e.delivery(g).Outcome)
	e.fake.Lock()
	c := *e.fake.Comments[len(e.fake.Comments)-1]
	e.fake.Unlock()
	require.Equal(t, "owner", c.Author)

	ev := issueCommentEvent(7, false, "owner", c.Body)
	ev["comment"].(map[string]any)["id"] = c.ID
	g = e.deliver("issue_comment", ev)
	assert.Equal(t, "ignored:self", e.delivery(g).Outcome)

	ev = issueCommentEvent(7, false, "owner", "thanks")
	ev["comment"].(map[string]any)["id"] = c.ID + 1
	g = e.deliver("issue_comment", ev)
	assert.Equal(t, "handled", e.delivery(g).Outcome)
}

// M15 2.3：审计每个写操作一行（推送按 ref），带线程、槽位、门禁结论与正文哈希；仓库取不到时 op 带上 node id。
// 补充键只记成功的写操作；Unparsed 与 other 类别不预登记。
func TestSelfWriteAudit(t *testing.T) {
	e := newListenEnv(t)
	orbID, thr := e.orbWithThread(e.prj, "agent")
	sha := egress.BodyHash("hello")
	writes := []egress.GitHubWrite{
		{Method: "POST", Category: egress.CatCommentCreate, Target: "/repos/acme/widgets/issues/3/comments", Op: "POST /repos/{owner}/{repo}/issues/{number}/comments", Repo: "acme/widgets", BodySHA256: sha},
		{Method: "POST", Category: egress.CatCommentCreate, Target: "PR_42_12", Op: "addComment", BodySHA256: sha, Gate: "denied"},
		{Method: egress.MethodPush, Op: "git-receive-pack", Repo: "acme/widgets", Refs: []egress.RefUpdate{{Ref: "refs/heads/a"}, {Ref: "refs/tags/v1"}}},
		{Method: "POST", Op: "POST /graphql", Unparsed: true},
		{Method: "POST", Category: egress.CatOther, Target: "/repos/acme/widgets/forks", Op: "POST /repos/{owner}/{repo}/forks", Repo: "acme/widgets"},
	}
	for i, w := range writes {
		e.s.GitHubWriteBegin(e.ctx, orbID, w)
		status := http.StatusCreated
		if i == 1 {
			status = http.StatusForbidden
		}
		e.s.GitHubWriteEnd(e.ctx, orbID, w, status, []string{"comment:" + strconv.Itoa(100+i)})
	}
	rows := e.audits()
	require.Len(t, rows, 6)
	type row struct {
		Method, Op, Repo, Gate string
		Status                 int64
		HasSHA                 bool
	}
	got := make([]row, len(rows))
	for i, r := range rows {
		assert.Equal(t, orbID, r.OrbID)
		assert.Equal(t, "agent", r.Slot)
		require.NotNil(t, r.ThreadID)
		assert.Equal(t, thr, *r.ThreadID)
		got[i] = row{r.Method, r.Op, r.Repo, r.Gate, r.Status, r.BodySha256 != nil}
	}
	assert.ElementsMatch(t, []row{
		{"POST", "POST /repos/{owner}/{repo}/issues/{number}/comments", "acme/widgets", "", 201, true},
		{"POST", "addComment PR_42_12", "", "denied", 403, true},
		{"PUSH", "refs/heads/a", "acme/widgets", "", 201, false},
		{"PUSH", "refs/tags/v1", "acme/widgets", "", 201, false},
		{"POST", "POST /graphql", "", "", 201, false},
		{"POST", "POST /repos/{owner}/{repo}/forks", "acme/widgets", "", 201, false},
	}, got)

	hit, err := e.st.Q.GithubSelfKeyExists(e.ctx, []string{"comment:100"})
	require.NoError(t, err)
	assert.True(t, hit)
	hit, err = e.st.Q.GithubSelfKeyExists(e.ctx, []string{"comment:101"})
	require.NoError(t, err)
	assert.False(t, hit, "失败的写操作不记补充键")
	var n int
	require.NoError(t, e.st.DB.QueryRowContext(e.ctx, `SELECT COUNT(*) FROM github_self_writes`).Scan(&n))
	assert.Equal(t, 2, n, "只预登记可回流类别的已解析写操作")
	e.s.mu.Lock()
	assert.Empty(t, e.s.selfInflight, "Begin / End 配对后没有在途写操作")
	e.s.mu.Unlock()

	// 没有线程或不止一个线程的 orb：线程留空
	two := e.orb(e.prj, "", "")
	for range 2 {
		now := e.clock.Now().UnixMilli()
		require.NoError(t, e.st.Q.CreateThread(e.ctx, db.CreateThreadParams{
			ID: ids.New("thr"), ProjectID: &e.prj, OrbID: &two.ID, Title: "t", TitleSource: "auto", State: "idle", StateChangedAt: now,
			SessionDir: "d", CreatedAt: now, UpdatedAt: now, Kind: "work",
		}))
	}
	e.s.GitHubWriteEnd(e.ctx, two.ID, writes[0], http.StatusCreated, nil)
	rows = e.audits()
	require.Len(t, rows, 7)
	assert.Nil(t, rows[0].ThreadID)
	assert.Equal(t, "user", rows[0].Slot)
}

// M15 2.3：每小时的清理按 selfWriteRetention / auditRetention 删除旧行。
func TestSelfWriteCleanup(t *testing.T) {
	e := newListenEnv(t)
	orbID := e.orb(e.prj, "", "").ID
	w := egress.GitHubWrite{Method: "POST", Category: egress.CatCommentCreate, Target: "PR_42_12", Op: "addComment", BodySHA256: egress.BodyHash("x")}
	e.s.GitHubWriteBegin(e.ctx, orbID, w)
	e.s.GitHubWriteEnd(e.ctx, orbID, w, http.StatusOK, []string{"comment:1"})
	count := func(table string) int {
		var n int
		require.NoError(t, e.st.DB.QueryRowContext(e.ctx, `SELECT COUNT(*) FROM `+table).Scan(&n))
		return n
	}
	e.s.cleanup(e.ctx)
	assert.Equal(t, []int{1, 1, 1}, []int{count("github_self_writes"), count("github_self_keys"), count("github_audit")})

	e.clock.Add(selfWriteRetention + time.Hour)
	e.s.cleanup(e.ctx)
	assert.Equal(t, []int{0, 0, 1}, []int{count("github_self_writes"), count("github_self_keys"), count("github_audit")})
	e.clock.Add(auditRetention)
	e.s.cleanup(e.ctx)
	assert.Zero(t, count("github_audit"))
}

// svcResolver 把出站代理接到 GitHub 服务上（端到端：orb → egress → 假 GitHub，回调进 GitHubWriteBegin / End 与门禁现查）。
// ga 是策略里的门禁部分（origin 由 Policy 填）。
type svcResolver struct {
	egress.NoMemory
	egress.NoHostedGit
	e     *testEnv
	orbID string
	tok   string
	ga    egress.GitHubAccess
}

func (r svcResolver) Policy() egress.Policy {
	ga := r.ga
	ga.APIOrigin, ga.GitOrigin = r.e.fake.URL(), r.e.fake.URL()
	return egress.Policy{GitHub: &ga}
}

// ThreadGates：这些端到端用例的 orb 没有线程限制（线程限制的现查在 orbs 里实现与测试）。
func (svcResolver) ThreadGates() (egress.ThreadGates, error) { return egress.ThreadGates{}, nil }
func (r svcResolver) Prepared(sha, level string) egress.PrepareState {
	return r.e.s.Prepared(r.e.ctx, r.orbID, sha, level)
}
func (r svcResolver) ImplAuthorized() bool { return r.e.s.ImplAuthorized(r.e.ctx, r.orbID) }
func (r svcResolver) HeadSHA(repo, ref string) (string, error) {
	return r.e.s.HeadSHA(r.e.ctx, r.orbID, repo, ref)
}
func (r svcResolver) DefaultBranch(repo string) (string, error) {
	return r.e.s.DefaultBranch(r.e.ctx, r.orbID, repo)
}
func (r svcResolver) IsOrynHook(repo string, id int64) bool {
	return r.e.s.IsOrynHook(r.e.ctx, repo, id)
}
func (r svcResolver) ReleaseThread() bool                      { return r.e.s.ReleaseThread(r.e.ctx, r.orbID) }
func (svcResolver) SecretByID(string) (string, []string, bool) { return "", nil, false }
func (svcResolver) GitToken(string) (string, bool)             { return "", false }

// GitHubToken：tok 为空时每次经 OrbToken 现取（与 orbs 的 egressResolver 一致），否则固定用 tok。
func (r svcResolver) GitHubToken() (string, bool) {
	if r.tok != "" {
		return r.tok, true
	}
	tok, st := r.e.s.OrbToken(r.e.ctx, r.orbID)
	return tok, st == orbs.GitHubTokenOK
}
func (r svcResolver) GitHubTokenRejected(tok string) {
	r.e.s.GitHubTokenRejected(r.e.ctx, r.orbID, tok)
}
func (r svcResolver) GitHubWriteBegin(w egress.GitHubWrite) string {
	return r.e.s.GitHubWriteBegin(r.e.ctx, r.orbID, w)
}
func (r svcResolver) GitHubWriteEnd(w egress.GitHubWrite, status int, keys []string) {
	r.e.s.GitHubWriteEnd(r.e.ctx, r.orbID, w, status, keys)
}

// M15 2.3 端到端：agent 经出站代理用 GraphQL addComment 在 PR 上发言，假 GitHub 随后投递的 webhook 不回流；
// REST fork 记审计（仓库取自路径）。
func TestSelfWriteThroughEgress(t *testing.T) {
	e := newListenEnv(t)
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 12, Title: "Fix login redirect loop", Author: "owner"}, HeadRef: "fix", HeadSHA: "abc"})
	orbID, _ := e.orbWithThread(e.prj, "user")
	tok, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)
	px := httptest.NewServer(egress.NewHandler(orbID, svcResolver{e: e.testEnv, orbID: orbID, tok: tok}))
	defer px.Close()

	body := `{"query":"mutation CommentCreate($input: AddCommentInput!) { addComment(input: $input) { commentEdge { node { url } } } }",` +
		`"operationName":"CommentCreate","variables":{"input":{"subjectId":"` + githubtest.PullNodeID(42, 12) + `","body":"Pushed the fix.\n"}}}`
	resp, err := http.Post(px.URL+"/gh/api.github.com/graphql", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	e.fake.Lock()
	c := *e.fake.Comments[len(e.fake.Comments)-1]
	e.fake.Unlock()
	require.Equal(t, "Pushed the fix.\n", c.Body)

	ev := withNode(issueCommentEvent(12, true, "owner", c.Body), "issue", githubtest.PullNodeID(42, 12))
	ev["comment"].(map[string]any)["id"] = c.ID
	g := e.deliver("issue_comment", ev)
	assert.Equal(t, "ignored:self", e.delivery(g).Outcome)

	resp, err = http.Post(px.URL+"/gh/api.github.com/repos/acme/widgets/forks", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	rows := e.audits()
	require.Len(t, rows, 2)
	assert.Equal(t, "POST /repos/{owner}/{repo}/forks", rows[0].Op)
	assert.Equal(t, "acme/widgets", rows[0].Repo)
	assert.EqualValues(t, http.StatusAccepted, rows[0].Status)
	assert.Equal(t, "addComment "+githubtest.PullNodeID(42, 12), rows[1].Op)
	e.fake.Lock()
	fork := e.fake.Repos[len(e.fake.Repos)-1]
	e.fake.Unlock()
	require.Equal(t, "owner/Widgets", fork.FullName)
	hit, err := e.st.Q.GithubSelfKeyExists(e.ctx, []string{"node:" + githubtest.RepoNodeID(fork.ID)})
	require.NoError(t, err)
	assert.True(t, hit, "REST 响应的 node_id 记为补充键")
}
