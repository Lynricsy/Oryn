package github

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	gateSHA   = "1234567890abcdef1234567890abcdef12345678"
	gateOther = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
)

// setRefs 给假 GitHub 上的仓库设 ref。
func (e *testEnv) setRefs(repoID int64, refs map[string]string) {
	e.t.Helper()
	e.fake.Lock()
	defer e.fake.Unlock()
	for _, rp := range e.fake.Repos {
		if rp.ID == repoID {
			rp.Refs = refs
			return
		}
	}
	e.t.Fatalf("仓库 %d 不存在", repoID)
}

// 门禁现查：实现授权随线程任务变化；发布任务只认进行中的状态；hook 按全局 id 判定。
func TestGateLookups(t *testing.T) {
	e := newEnv(t)
	prj := e.boundProject()
	orbID, thr := e.orbWithThread(prj, "user")
	bare := e.orb(prj, "user", "").ID // 没有线程的 orb

	assert.False(t, e.s.ImplAuthorized(e.ctx, orbID), "没有任务")
	require.NoError(t, e.st.Q.UpsertThreadIssue(e.ctx, db.UpsertThreadIssueParams{RepoID: 42, Number: 7, ThreadID: thr, CreatedAt: 1}))
	assert.False(t, e.s.ImplAuthorized(e.ctx, orbID), "未分类、未授权")
	question := "question"
	_, err := e.st.Q.SetThreadIssueClassification(e.ctx, db.SetThreadIssueClassificationParams{Classification: &question, ThreadID: thr})
	require.NoError(t, err)
	assert.False(t, e.s.ImplAuthorized(e.ctx, orbID))
	bug := "bug"
	_, err = e.st.Q.SetThreadIssueClassification(e.ctx, db.SetThreadIssueClassificationParams{Classification: &bug, ThreadID: thr})
	require.NoError(t, err)
	assert.True(t, e.s.ImplAuthorized(e.ctx, orbID), "分类为 bug 即授权")
	_, err = e.st.Q.SetThreadIssueClassification(e.ctx, db.SetThreadIssueClassificationParams{Classification: &question, ThreadID: thr})
	require.NoError(t, err)
	_, err = e.st.Q.SetThreadIssueImplAuthorized(e.ctx, db.SetThreadIssueImplAuthorizedParams{ImplAuthorized: true, RepoID: 42, Number: 7})
	require.NoError(t, err)
	assert.True(t, e.s.ImplAuthorized(e.ctx, orbID), "accepted / go ahead")
	assert.False(t, e.s.ImplAuthorized(e.ctx, bare))

	assert.False(t, e.s.ReleaseThread(e.ctx, orbID))
	require.NoError(t, e.st.Q.UpsertGithubRelease(e.ctx, db.UpsertGithubReleaseParams{RepoID: 42, Version: "1.2.0", Sha: gateSHA, State: "fixing", UpdatedAt: 1}))
	require.NoError(t, e.st.Q.SetGithubReleaseThread(e.ctx, db.SetGithubReleaseThreadParams{ThreadID: &thr, UpdatedAt: 2, RepoID: 42, Version: "1.2.0"}))
	assert.True(t, e.s.ReleaseThread(e.ctx, orbID), "进行中的发布任务")
	require.NoError(t, e.st.Q.SetGithubReleaseState(e.ctx, db.SetGithubReleaseStateParams{State: "green", Rounds: 1, UpdatedAt: 3, RepoID: 42, Version: "1.2.0"}))
	assert.False(t, e.s.ReleaseThread(e.ctx, orbID), "已结束的发布任务不再例外")
	assert.False(t, e.s.ReleaseThread(e.ctx, bare))

	hid := int64(9001)
	require.NoError(t, e.st.Q.UpsertGithubHook(e.ctx, db.UpsertGithubHookParams{RepoID: 42, FullName: "Acme/Widgets", HookID: &hid, Status: "active", UpdatedAt: 1}))
	assert.True(t, e.s.IsOrynHook(e.ctx, "acme/widgets", hid))
	assert.True(t, e.s.IsOrynHook(e.ctx, "acme/renamed", hid), "改名后的仓库名同样命中")
	assert.True(t, e.s.IsOrynHook(e.ctx, "", hid), "/repositories/{id} 形态")
	assert.False(t, e.s.IsOrynHook(e.ctx, "acme/widgets", hid+1))
}

// HeadSHA / DefaultBranch 用 orb 自己的槽位查询；仓库可写成 owner/repo、数值 id 或 node id，head 可带 fork 的 owner。
func TestGateHeadSHA(t *testing.T) {
	e := newEnv(t)
	prj := e.boundProject()
	orbID, _ := e.orbWithThread(prj, "user")
	e.setRefs(42, map[string]string{"heads/feat/x": gateSHA, "heads/main": gateOther})
	fork := e.fake.AddRepo(githubtest.Repo{ID: 77, FullName: "owner/Widgets", OwnerID: 1, DefaultBranch: "trunk"})
	e.setRefs(fork.ID, map[string]string{"heads/fix": gateOther})

	for _, repo := range []string{"acme/widgets", "42", githubtest.RepoNodeID(42)} {
		sha, err := e.s.HeadSHA(e.ctx, orbID, repo, "feat/x")
		require.NoError(t, err, repo)
		assert.Equal(t, gateSHA, sha, repo)
		def, err := e.s.DefaultBranch(e.ctx, orbID, repo)
		require.NoError(t, err, repo)
		assert.Equal(t, "main", def, repo)
	}
	sha, err := e.s.HeadSHA(e.ctx, orbID, githubtest.RepoNodeID(42), "owner:fix")
	require.NoError(t, err)
	assert.Equal(t, gateOther, sha, "owner: 前缀指向同名 fork")
	def, err := e.s.DefaultBranch(e.ctx, orbID, "owner/widgets")
	require.NoError(t, err)
	assert.Equal(t, "trunk", def)

	_, err = e.s.HeadSHA(e.ctx, orbID, "acme/widgets", "missing")
	assert.Error(t, err)
	_, err = e.s.DefaultBranch(e.ctx, orbID, "")
	assert.Error(t, err)
	_, err = e.s.HeadSHA(e.ctx, "orb-missing", "acme/widgets", "feat/x")
	assert.Error(t, err, "读不到 orb 时不放行")
	agentOrb, _ := e.orbWithThread(prj, "agent")
	_, err = e.s.DefaultBranch(e.ctx, agentOrb, "acme/widgets")
	assert.Error(t, err, "槽位不可用时不回落到我的账号")

	// 令牌来自 orb 的槽位
	e.fake.Lock()
	var auths []string
	for _, c := range e.fake.Calls {
		if strings.Contains(c.Path, "/git/ref/") {
			auths = append(auths, c.Auth)
		}
	}
	e.fake.Unlock()
	tok, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)
	require.NotEmpty(t, auths)
	for _, a := range auths {
		assert.Equal(t, "Bearer "+tok, a)
	}
}

// 端到端：PR 门禁经出站代理现查控制面的 prepare 记录与 head；拒绝时不触达 GitHub，审计记 Rejected 的门禁原因；
// prepare 上报后同一请求放行，审计记 pass。
func TestGateThroughEgress(t *testing.T) {
	e := newEnv(t)
	prj := e.boundProject()
	orbID, _ := e.orbWithThread(prj, "user")
	e.setRefs(42, map[string]string{"heads/feat": gateSHA})
	tok, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)
	px := httptest.NewServer(egress.NewHandler(orbID, svcResolver{e: e, orbID: orbID, tok: tok,
		ga: egress.GitHubAccess{Gates: &egress.GateConfig{PR: true, RequireClosingRef: true}}}))
	defer px.Close()
	body := `{"query":"mutation PullRequestCreate($input: CreatePullRequestInput!) { createPullRequest(input: $input) { pullRequest { id url } } }",` +
		`"operationName":"PullRequestCreate","variables":{"input":{"repositoryId":"` + githubtest.RepoNodeID(42) +
		`","baseRefName":"main","headRefName":"feat","title":"Fix","body":"Fixes #3"}}}`
	gqlURL := px.URL + "/gh/api.github.com/graphql"

	resp, err := http.Post(gqlURL, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	rows := e.audits()
	require.Len(t, rows, 1)
	assert.Contains(t, rows[0].Gate, "没有 pr 级 github_prepare 通过记录")
	assert.EqualValues(t, http.StatusForbidden, rows[0].Status)
	pre, err := e.st.Q.ListGithubSelfWrites(e.ctx, db.ListGithubSelfWritesParams{Target: githubtest.RepoNodeID(42), Category: egress.CatPRCreate})
	require.NoError(t, err)
	assert.Empty(t, pre, "被拒绝的写操作不预登记")
	assert.False(t, e.s.selfWriteInflight(slotUser), "被拒绝的写操作不计在途")

	require.NoError(t, e.s.PrepareReport(e.ctx, orbID, apigen.LinkCtlGithubPrepareReportParams{
		Sha: gateSHA, Level: apigen.GitHubPrepareLevelPr, ThreadID: ids.New("thr"),
	}))
	resp, err = http.Post(gqlURL, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	rows = e.audits()
	require.Len(t, rows, 2)
	assert.Contains(t, []string{rows[0].Gate, rows[1].Gate}, egress.GatePass)
	pre, err = e.st.Q.ListGithubSelfWrites(e.ctx, db.ListGithubSelfWritesParams{Target: githubtest.RepoNodeID(42), Category: egress.CatPRCreate})
	require.NoError(t, err)
	assert.Len(t, pre, 1, "放行后预登记")
}
