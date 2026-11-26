package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/orbdrt"
	"github.com/Lynricsy/Oryn/internal/orbsock"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// M15 步骤 9.2：GitHub 改用 OAuth App 之后的端到端验收。
//
// 真实 orynd（newStack：process 驱动的 orbd + fakeomp）对接进程内假 GitHub（githubtest；GitRoot 下的裸仓库经
// git http-backend 提供 smart-HTTP，API 与网页同一基址）。agent 在 orb 里的 GitHub 操作按附录 A 的请求形态直接发给
// orb 的出站代理（orbd 的 egress 监听 → link → 控制面 egress handler → 假 GitHub），git 用真实 git 客户端经 orb 的
// http.<github>.proxy 走同一条路；orb 内工具（github_prepare、github_autoclose）经 orbd.sock 调用，与扩展工具同一入口。
// GitHub 侧的事件由假 GitHub 以仓库 hook 身份签名投递到 orynd 的 webhook 端点；上游仓库没有 Oryn 的 hook，靠对象级轮询。
//
// 子用例按顺序共享一套环境，前一步失败时后续步骤跳过。整套不访问外网；唯一要等定时器的是对象级轮询与任务扫描
// （restPollInterval / scanInterval 都是 60 秒，两处等待重叠）。

const (
	m15Repo      = "acme/widgets" // 项目绑定的仓库：我的账号是 admin，Oryn 建 hook
	m15RepoID    = 42
	m15Upstream  = "oss/lib" // 无项目线程的上游：我的账号只读，没有 hook
	m15UpID      = 77
	m15Issue     = 7
	m15Version   = "1.2.3"
	m15BumpMsg   = "chore: bump version to " + m15Version
	m15FixMsg    = m15BumpMsg + ": fix flaky timeout in the CLI tests"
	m15Scopes    = "repo workflow read:org admin:repo_hook gist write:packages user:email"
	m15PollLimit = 150 * time.Second // 轮询 / 扫描的首个 tick 在 orynd 启动 60 秒后，留足余量
)

// gh 的 mutation 文档（附录 A，cli/cli v2.102.1 的 api/queries_pr.go、queries_comments.go）。
const (
	gqlCreatePR = `mutation PullRequestCreate($input: CreatePullRequestInput!) {
	createPullRequest(input: $input) {
		pullRequest {
			id
			url
		}
	}
}`
	gqlAddComment = `mutation CommentCreate($input: AddCommentInput!) {
	addComment(input: $input) {
		commentEdge {
			node {
				url
			}
		}
	}
}`
)

type m15Env struct {
	*stack
	fake *githubtest.Server
	ctx  context.Context
	prj  string
}

// m15Thread 是 GET /api/v1/threads/{id} 里本用例关心的字段。
type m15Thread struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	OrbID     string `json:"orbId"`
	Title     string `json:"title"`
	ShippedAt *int64 `json:"shippedAt"`
	Pulls     []struct {
		Repo   string `json:"repo"`
		Number int64  `json:"number"`
		State  string `json:"state"`
	} `json:"pulls"`
	GithubTask *struct {
		Kind    string `json:"kind"`
		Repo    string `json:"repo"`
		Number  int64  `json:"number"`
		Version string `json:"version"`
		State   string `json:"state"`
	} `json:"githubTask"`
}

type m15Audit struct {
	ThreadID   string `json:"threadId"`
	OrbID      string `json:"orbId"`
	Slot       string `json:"slot"`
	Method     string `json:"method"`
	Op         string `json:"op"`
	Repo       string `json:"repo"`
	Status     int    `json:"status"`
	Gate       string `json:"gate"`
	BodySha256 string `json:"bodySha256"`
}

// orbCtx 是一台 orb 的出站代理地址、HOME 与线程。
type orbCtx struct {
	thread, orb, home, egress string
}

func newM15Env(t *testing.T) *m15Env {
	t.Helper()
	fake := githubtest.New()
	t.Cleanup(fake.Close)
	fake.GitRoot = t.TempDir()
	fake.AddUser(1, "owner", "The Owner")
	fake.AddUser(7, "oryn-bot", "")
	fake.AddUser(303, "alice", "Alice")
	fake.AddUser(404, "maint", "Upstream Maintainer")
	fake.AddRepo(githubtest.Repo{ID: m15RepoID, FullName: m15Repo, OwnerID: 900, DefaultBranch: "main",
		Permissions: map[string]string{"owner": "admin", "oryn-bot": "write"}})
	fake.AddRepo(githubtest.Repo{ID: m15UpID, FullName: m15Upstream, OwnerID: 901, DefaultBranch: "main",
		Permissions: map[string]string{"maint": "admin"}})
	require.NoError(t, fake.InitRepo(m15Repo))
	require.NoError(t, fake.InitRepo(m15Upstream))

	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)},
		func(c *config.Orynd) { c.GitHub.APIURL, c.GitHub.WebURL = fake.URL(), fake.URL() })
	return &m15Env{stack: s, fake: fake, ctx: context.Background()}
}

func TestE2EM15(t *testing.T) {
	e := newM15Env(t)
	var upstream, triage, gate orbCtx
	steps := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{"配置OAuthApp", e.stepOAuthApp},
		{"连接两个身份", e.stepIdentities},
		{"项目绑定与规则", e.stepProject},
		{"无项目线程fork推送开上游PR", func(t *testing.T) { upstream = e.stepUpstreamPR(t) }},
		{"robomp分诊建线程", func(t *testing.T) { triage = e.stepTriage(t) }},
		{"PR门禁与自身写入不回流", func(t *testing.T) { gate = e.stepGate(t) }},
		{"发布哨兵到green", e.stepRelease},
		{"上游PR合并后已交付", func(t *testing.T) { e.stepUpstreamMerged(t, upstream) }},
		{"自动关闭", func(t *testing.T) { e.stepAutoclose(t, triage) }},
		{"审计完整", func(t *testing.T) { e.stepAudit(t, upstream, triage, gate) }},
	}
	for _, st := range steps {
		// stack 的辅助方法（rest 等）用 e.t 报错：每步换成该子用例的 t，失败时只终止本步
		ok := t.Run(st.name, func(sub *testing.T) {
			e.stack.t = sub
			defer func() { e.stack.t = t }()
			st.fn(sub)
		})
		if !ok {
			t.Fatalf("%s 失败，后续步骤依赖它，停止", st.name)
		}
	}
}

// ---------------------------------------------------------------- 步骤

// 1. 设置 API 保存 OAuth App；状态里给出回调地址与 webhook 基址，不回显 secret。
func (e *m15Env) stepOAuthApp(t *testing.T) {
	var st struct {
		OauthApp *struct {
			ClientID string `json:"clientId"`
		} `json:"oauthApp"`
		CallbackURL string `json:"callbackUrl"`
		WebhookBase string `json:"webhookBase"`
		WebURL      string `json:"webUrl"`
	}
	e.rest("PUT", "/api/v1/github/oauth-app", map[string]any{
		"clientId": e.fake.OAuthApp.ClientID, "clientSecret": e.fake.OAuthApp.ClientSecret, "webhookBase": e.http.URL,
	}, &st, 200)
	require.NotNil(t, st.OauthApp)
	assert.Equal(t, e.fake.OAuthApp.ClientID, st.OauthApp.ClientID)
	assert.Equal(t, e.cfg.PublicURL+"/api/v1/public/github/oauth/callback", st.CallbackURL)
	assert.Equal(t, e.http.URL, st.WebhookBase)
	assert.Equal(t, e.fake.URL(), st.WebURL)
	assert.NotContains(t, e.raw("GET", "/api/v1/github"), e.fake.OAuthApp.ClientSecret, "状态不回显 Client Secret")
}

// 2. 我的账号与 Agent 账号各走一遍 OAuth 网页流：授权地址带 Q3 的七个 scope，回调 302 回设置页。
func (e *m15Env) stepIdentities(t *testing.T) {
	for _, c := range []struct{ slot, code, login string }{{"user", "code-owner", "owner"}, {"agent", "code-bot", "oryn-bot"}} {
		e.fake.GrantCode(c.code, c.login)
		var red struct {
			URL string `json:"url"`
		}
		e.rest("POST", "/api/v1/github/identities/"+c.slot+"/authorize", nil, &red, 200)
		u, err := url.Parse(red.URL)
		require.NoError(t, err)
		assert.Equal(t, e.fake.URL()+"/login/oauth/authorize", u.Scheme+"://"+u.Host+u.Path)
		q := u.Query()
		assert.Equal(t, m15Scopes, q.Get("scope"))
		assert.Equal(t, e.fake.OAuthApp.ClientID, q.Get("client_id"))
		assert.Equal(t, e.cfg.PublicURL+"/api/v1/public/github/oauth/callback", q.Get("redirect_uri"))
		resp := noRedirect(t, e.http.URL+"/api/v1/public/github/oauth/callback?code="+c.code+"&state="+url.QueryEscape(q.Get("state")))
		assert.Equal(t, http.StatusFound, resp.StatusCode)
		assert.Equal(t, e.cfg.PublicURL+"/settings/git?connected="+c.slot, resp.Header.Get("Location"))
	}
	type ident struct {
		Login         string   `json:"login"`
		Status        string   `json:"status"`
		Scopes        []string `json:"scopes"`
		MissingScopes []string `json:"missingScopes"`
	}
	var st struct {
		Identities struct {
			User  *ident `json:"user"`
			Agent *ident `json:"agent"`
		} `json:"identities"`
		AutomationIdentity string `json:"automationIdentity"`
	}
	e.rest("GET", "/api/v1/github", nil, &st, 200)
	for slot, want := range map[string]*ident{"owner": st.Identities.User, "oryn-bot": st.Identities.Agent} {
		require.NotNil(t, want, slot)
		assert.Equal(t, slot, want.Login)
		assert.Equal(t, "ok", want.Status)
		assert.ElementsMatch(t, strings.Fields(m15Scopes), want.Scopes)
		assert.Empty(t, want.MissingScopes)
	}
	assert.Equal(t, "agent", st.AutomationIdentity, "自动化身份缺省为 agent（R6）")
}

// 3. 项目绑定 acme/widgets（我的账号是 admin → Oryn 建 hook，状态 active），配门禁与 robomp 分诊、发布哨兵两条预设规则。
func (e *m15Env) stepProject(t *testing.T) {
	var prj struct {
		ID     string `json:"id"`
		GitHub *struct {
			RepoID     int64  `json:"repoId"`
			FullName   string `json:"fullName"`
			Bound      bool   `json:"bound"`
			HookStatus string `json:"hookStatus"`
		} `json:"github"`
		GithubGates *struct {
			Push bool `json:"push"`
			PR   bool `json:"pr"`
		} `json:"githubGates"`
	}
	e.rest("POST", "/api/v1/projects", map[string]any{"name": "widgets", "repoUrl": "https://github.com/" + m15Repo}, &prj, 201)
	e.prj = prj.ID
	// 门禁（R19）：推送与 PR 都开；检查 / 测试命令用 true，正文须带 closing ref
	e.rest("PATCH", "/api/v1/projects/"+e.prj, map[string]any{"githubGates": map[string]any{
		"push": true, "pr": true, "checkCmd": "true", "testCmd": "true", "prHeadings": []string{"Summary"}, "requireClosingRef": true,
	}}, &prj, 200)
	require.NotNil(t, prj.GithubGates, "PATCH 返回保存后的门禁")
	assert.True(t, prj.GithubGates.Push && prj.GithubGates.PR)
	require.NotNil(t, prj.GitHub)
	assert.EqualValues(t, m15RepoID, prj.GitHub.RepoID)
	assert.True(t, prj.GitHub.Bound)
	assert.Equal(t, "active", prj.GitHub.HookStatus, "项目视图带 hook 状态（依赖 Step94 的修复）")

	var hooks struct {
		Hooks []struct {
			FullName string `json:"fullName"`
			Status   string `json:"status"`
		} `json:"hooks"`
	}
	e.rest("GET", "/api/v1/github/hooks", nil, &hooks, 200)
	require.Len(t, hooks.Hooks, 1)
	assert.Equal(t, "active", hooks.Hooks[0].Status)
	h, ok := e.fake.HookOf(m15Repo, e.http.URL+"/api/v1/public/github/webhook")
	require.True(t, ok, "假 GitHub 上有指向 orynd 的仓库 hook")
	assert.Equal(t, "json", h.ContentType)

	var presets struct {
		Presets []struct {
			ID      string         `json:"id"`
			Trigger map[string]any `json:"trigger"`
		} `json:"presets"`
	}
	e.rest("GET", "/api/v1/github/presets", nil, &presets, 200)
	for _, id := range []string{"robomp_triage", "robomp_release"} {
		i := slices.IndexFunc(presets.Presets, func(p struct {
			ID      string         `json:"id"`
			Trigger map[string]any `json:"trigger"`
		}) bool {
			return p.ID == id
		})
		require.GreaterOrEqual(t, i, 0, id)
		e.rest("POST", "/api/v1/projects/"+e.prj+"/github/triggers", presets.Presets[i].Trigger, nil, 201)
	}
}

// 4. 无项目线程（我的账号）：fork 上游、经出站代理 clone / push 到 fork、对上游开 PR（createPullRequest），PR 关联到线程。
func (e *m15Env) stepUpstreamPR(t *testing.T) orbCtx {
	o := e.newThread(t, map[string]any{"prompt": "fix the typo in oss/lib"})
	assert.Equal(t, "owner", e.ghLogin(t, o), "无项目 orb 也有凭据，身份是我的账号（R5 / R6）")
	// 出站代理的响应里没有真实令牌（R5）
	e.fake.Lock()
	var calls []githubtest.Call
	calls = append(calls, e.fake.Calls...)
	e.fake.Unlock()
	tok := strings.TrimPrefix(calls[len(calls)-1].Auth, "Bearer ")
	require.NotEmpty(t, tok)
	_, me := e.gh(t, o, "GET", "/user", nil)
	assert.NotContains(t, me, tok)
	env, err := os.ReadFile(filepath.Join(o.home, ".gitconfig"))
	require.NoError(t, err)
	assert.NotContains(t, string(env), tok, "orb 的 git 配置里没有真实令牌")

	code, body := e.gh(t, o, "POST", "/repos/"+m15Upstream+"/forks", map[string]any{})
	require.Equal(t, http.StatusAccepted, code, body)
	var fork struct {
		FullName string `json:"full_name"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &fork))
	require.Equal(t, "owner/lib", fork.FullName)

	dir := filepath.Join(o.home, "workspace", "lib")
	e.orbGit(t, o, filepath.Join(o.home, "workspace"), "clone", "-q", "https://github.com/owner/lib", dir)
	e.orbGit(t, o, dir, "checkout", "-q", "-b", "fix-typo")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# oss/lib\n\nFixed typo.\n"), 0o644))
	e.orbGit(t, o, dir, "commit", "-q", "-am", "docs: fix typo")
	head := e.orbGit(t, o, dir, "rev-parse", "HEAD")
	e.orbGit(t, o, dir, "push", "-q", "origin", "fix-typo")
	assert.Equal(t, head, e.bareRev(t, "owner/lib", "refs/heads/fix-typo"), "推送到了 fork（无项目 orb 对 github.com 读写不限仓库，R5）")

	code, body = e.graphql(t, o, gqlCreatePR, "PullRequestCreate", map[string]any{"input": map[string]any{
		"repositoryId": githubtest.RepoNodeID(m15UpID), "baseRefName": "main", "headRefName": "owner:fix-typo",
		"title": "docs: fix typo", "body": "The README had a typo.", "draft": false, "maintainerCanModify": true,
	}})
	require.Equal(t, http.StatusOK, code, body)
	assert.Contains(t, body, `/oss/lib/pull/1"`)
	e.fake.Lock()
	pr := *e.fake.Pulls[len(e.fake.Pulls)-1]
	e.fake.Unlock()
	assert.EqualValues(t, m15UpID, pr.RepoID)
	assert.Equal(t, head, pr.HeadSHA)
	assert.NotZero(t, pr.HeadRepoID, "head 在 fork")

	th := e.waitM15Thread(t, o.thread, 30*time.Second, func(v m15Thread) bool { return len(v.Pulls) == 1 })
	assert.Equal(t, m15Upstream, th.Pulls[0].Repo)
	assert.EqualValues(t, 1, th.Pulls[0].Number)
	assert.Equal(t, "open", th.Pulls[0].State)
	var via bool
	require.NoError(t, e.srv.Store.DB.QueryRowContext(e.ctx,
		`SELECT created_via_egress FROM thread_pulls WHERE thread_id = ? AND number = 1`, o.thread).Scan(&via))
	assert.True(t, via, "跨仓库 PR 由出站代理观察到创建才关联（R11）")
	// watch 在关联、通知线程之后登记：等它出现
	assert.Eventually(t, func() bool {
		var n int
		_ = e.srv.Store.DB.QueryRowContext(e.ctx,
			`SELECT COUNT(*) FROM github_watches WHERE kind = 'pull' AND full_name = ? AND number = 1 AND done_at IS NULL`, m15Upstream).Scan(&n)
		return n == 1
	}, 10*time.Second, 50*time.Millisecond, "上游没有 Oryn 的 hook：登记对象级轮询（R10）")
	return o
}

// 5. robomp 分诊预设：alice 开 issue → hook 投递 issues.opened → 以 Agent 账号建任务线程；agent 的回答经出站代理发出，
// 对应 webhook 不回流（Agent 账号发出的事件一律不回流，R7）；随后排期自动关闭。
func (e *m15Env) stepTriage(t *testing.T) orbCtx {
	e.fake.AddIssue(githubtest.Issue{RepoID: m15RepoID, Number: m15Issue, Title: "How do I configure the cache?", Body: "Where is the setting?", Author: "alice"})
	g := e.deliver(t, "issues", map[string]any{"action": "opened", "issue": e.issueP(m15Issue, false), "repository": repoP(), "sender": userP("alice")})
	require.Equal(t, "handled", e.outcome(t, g))

	ti, err := e.srv.Store.Q.GetThreadIssue(e.ctx, db.GetThreadIssueParams{RepoID: m15RepoID, Number: m15Issue})
	require.NoError(t, err)
	assert.Equal(t, "new", ti.State)
	assert.NotNil(t, ti.RuleID)
	th := e.waitM15Thread(t, ti.ThreadID, 90*time.Second, settled)
	require.NotNil(t, th.GithubTask)
	assert.Equal(t, m15Repo, th.GithubTask.Repo)
	assert.EqualValues(t, m15Issue, th.GithubTask.Number)
	entries := e.raw("GET", "/api/v1/threads/"+th.ID+"/entries?limit=500")
	assert.Contains(t, entries, `[Oryn GitHub trigger \"robomp 分诊\" on acme/widgets#7`)
	assert.Contains(t, entries, "github_classify_issue", "首条提示词是分诊预设")
	orb, err := e.srv.Store.Q.GetOrb(e.ctx, th.OrbID)
	require.NoError(t, err)
	assert.Equal(t, "agent", orb.GithubIdentity, "全自动线程用 Agent 账号（R6）")
	o := e.orbOf(t, th)
	assert.Equal(t, "oryn-bot", e.ghLogin(t, o))

	answer := "The cache lives under `cache:` in config.yaml; see docs/cache.md."
	code, body := e.gh(t, o, "POST", fmt.Sprintf("/repos/%s/issues/%d/comments", m15Repo, m15Issue), map[string]any{"body": answer})
	require.Equal(t, http.StatusCreated, code, body)
	var c struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &c))
	g = e.deliver(t, "issue_comment", map[string]any{
		"action": "created", "issue": e.issueP(m15Issue, false), "repository": repoP(), "sender": userP("oryn-bot"),
		"comment": commentP(c.ID, "oryn-bot", answer, "COLLABORATOR"),
	})
	assert.Equal(t, "ignored:self", e.outcome(t, g), "Agent 账号的评论不回流")

	var res apigen.LinkCtlGithubCallResult
	e.sockCall(t, o, orbsock.MethodGithubCall, apigen.LinkCtlGithubCallParams{ThreadID: apigen.ThreadID(o.thread), Tool: "github_autoclose", Params: map[string]any{}}, &res)
	assert.EqualValues(t, c.ID, res.Details["commentId"], "记下线程身份在该 issue 上的最近一条评论")
	assert.Equal(t, "alice", res.Details["author"])
	due, _ := res.Details["dueAt"].(float64)
	assert.InDelta(t, float64(time.Now().Add(4*time.Hour).UnixMilli()), due, float64(time.Minute.Milliseconds()), "预设的 autoclose_hours = 4")
	// 模拟 4 小时过去：把到期时间拨到现在（其余照常由 orynd 的 scanInterval 扫描执行）
	_, err = e.srv.Store.DB.ExecContext(e.ctx, `UPDATE github_autoclose SET due_at = ? WHERE repo_id = ? AND number = ?`,
		time.Now().Add(-time.Second).UnixMilli(), m15RepoID, m15Issue)
	require.NoError(t, err)
	return o
}

// 6. 项目线程（我的账号，项目开了推送与 PR 门禁）：没有 github_prepare 时推送回 ng、开 PR 回 403；prepare 通过后放行。
// 随后对自己的 PR 经出站代理发两条评论（GraphQL addComment 与 REST），对应 webhook 判为自身写入；我本人手写的评论正常回流。
func (e *m15Env) stepGate(t *testing.T) orbCtx {
	o := e.newThread(t, map[string]any{"prompt": "fix the login loop", "projectId": e.prj})
	dir := filepath.Join(o.home, "workspace", "repo")
	e.orbGit(t, o, dir, "checkout", "-q", "-b", "fix-login")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "login.txt"), []byte("no more loops\n"), 0o644))
	e.orbGit(t, o, dir, "add", "login.txt")
	e.orbGit(t, o, dir, "commit", "-q", "-m", "fix: stop the login redirect loop")
	head := e.orbGit(t, o, dir, "rev-parse", "HEAD")

	out, err := e.orbGitErr(o, dir, "push", "origin", "fix-login")
	require.Error(t, err, out)
	assert.Contains(t, out, "[remote rejected]")
	assert.Contains(t, out, "oryn: 推送门禁：fix-login 的 "+head[:12]+" 没有 github_prepare 通过记录")
	assert.Empty(t, e.bareRevOpt("acme/widgets", "refs/heads/fix-login"), "被拒绝的推送没有到达 GitHub")

	var prep orbdrt.PrepareResult
	e.sockCall(t, o, orbsock.MethodGithubPrepare, orbsock.GithubPrepareParams{ThreadID: o.thread, Level: "push"}, &prep)
	require.True(t, prep.OK, "%+v", prep)
	assert.Equal(t, head, prep.SHA)
	e.orbGit(t, o, dir, "push", "-q", "origin", "fix-login")
	assert.Equal(t, head, e.bareRev(t, m15Repo, "refs/heads/fix-login"))

	input := map[string]any{
		"repositoryId": githubtest.RepoNodeID(m15RepoID), "baseRefName": "main", "headRefName": "fix-login",
		"title": "fix: stop the login redirect loop", "body": "## Summary\n\nStop the loop.\n\nFixes #" + fmt.Sprint(m15Issue), "draft": false,
	}
	e.fake.Lock()
	before := len(e.fake.Pulls)
	e.fake.Unlock()
	code, body := e.graphql(t, o, gqlCreatePR, "PullRequestCreate", map[string]any{"input": input})
	assert.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, body, "没有 pr 级 github_prepare 通过记录")
	e.fake.Lock()
	assert.Len(t, e.fake.Pulls, before, "被门禁拒绝的 createPullRequest 没有到达 GitHub")
	e.fake.Unlock()

	e.sockCall(t, o, orbsock.MethodGithubPrepare, orbsock.GithubPrepareParams{ThreadID: o.thread, Level: "pr"}, &prep)
	require.True(t, prep.OK, "%+v", prep)
	code, body = e.graphql(t, o, gqlCreatePR, "PullRequestCreate", map[string]any{"input": input})
	require.Equal(t, http.StatusOK, code, body)
	e.fake.Lock()
	pr := *e.fake.Pulls[len(e.fake.Pulls)-1]
	e.fake.Unlock()
	assert.Equal(t, head, pr.HeadSHA)
	th := e.waitM15Thread(t, o.thread, 30*time.Second, func(v m15Thread) bool { return len(v.Pulls) == 1 })
	assert.Equal(t, m15Repo, th.Pulls[0].Repo)
	assert.Equal(t, pr.Number, th.Pulls[0].Number)

	// 自身写入：GraphQL addComment（gh pr comment）与 REST 评论，webhook 里的 sender 是我的账号
	viaGQL := "Rebased onto main.\r\nCI should be green now.  "
	code, body = e.graphql(t, o, gqlAddComment, "CommentCreate", map[string]any{"input": map[string]any{
		"subjectId": githubtest.PullNodeID(m15RepoID, pr.Number), "body": viaGQL,
	}})
	require.Equal(t, http.StatusOK, code, body)
	gqlID := e.lastCommentID(t, pr.Number)
	viaREST := "Also covered the logout path."
	code, body = e.gh(t, o, "POST", fmt.Sprintf("/repos/%s/issues/%d/comments", m15Repo, pr.Number), map[string]any{"body": viaREST})
	require.Equal(t, http.StatusCreated, code, body)
	restID := e.lastCommentID(t, pr.Number)
	for id, b := range map[int64]string{gqlID: viaGQL, restID: viaREST} {
		g := e.deliver(t, "issue_comment", map[string]any{
			"action": "created", "issue": e.issueP(pr.Number, true), "repository": repoP(), "sender": userP("owner"),
			"comment": commentP(id, "owner", b, "COLLABORATOR"),
		})
		assert.Equal(t, "ignored:self", e.outcome(t, g), "agent 以我的账号发出的评论 %d 不回流", id)
	}
	// 我本人在 GitHub 上手写的评论（同一 PR、不同正文）照常回流到线程
	human := "Please also add a regression test."
	hc := e.fake.AddComment(githubtest.Comment{RepoID: m15RepoID, Number: pr.Number, Author: "owner", Body: human})
	g := e.deliver(t, "issue_comment", map[string]any{
		"action": "created", "issue": e.issueP(pr.Number, true), "repository": repoP(), "sender": userP("owner"),
		"comment": commentP(hc.ID, "owner", human, "COLLABORATOR"),
	})
	require.Equal(t, "handled", e.outcome(t, g))
	deadline := time.Now().Add(60 * time.Second)
	for !strings.Contains(e.raw("GET", "/api/v1/threads/"+o.thread+"/entries?limit=500"), human) {
		require.True(t, time.Now().Before(deadline), "我本人的评论没有回流到线程")
		time.Sleep(200 * time.Millisecond)
	}
	entries := e.raw("GET", "/api/v1/threads/"+o.thread+"/entries?limit=500")
	assert.Contains(t, entries, "[Oryn GitHub event on acme/widgets#"+fmt.Sprint(pr.Number))
	assert.NotContains(t, entries, "Rebased onto main.")
	assert.NotContains(t, entries, viaREST)
	return o
}

// 7. 发布哨兵：发布提交的 CI 失败 → 以 Agent 账号开发布线程（fixing）；线程在默认分支上提交修复，prepare 通过后按 R22 的
// 原子推送同时移动分支与 tag；新提交的 CI 通过且 Release 已发布 → green。
func (e *m15Env) stepRelease(t *testing.T) {
	e.fake.Lock()
	comments := len(e.fake.Comments)
	e.fake.Unlock()
	sha1 := e.hostCommit(t, m15Repo, m15BumpMsg, "VERSION", m15Version+"\n", "v"+m15Version)
	run1 := e.fake.AddRun(githubtest.WorkflowRun{RepoID: m15RepoID, Name: "CI", Event: "push", Status: "completed", Conclusion: "failure",
		HeadSHA: sha1, HeadBranch: "main", Message: m15BumpMsg, Actor: "owner"})
	e.fake.AddJob(githubtest.WorkflowJob{RunID: run1.ID, RepoID: m15RepoID, Name: "test", Conclusion: "failure", Log: "timeout after 30s\n",
		Steps: []githubtest.JobStep{{Number: 1, Name: "Run tests", Conclusion: "failure"}}})
	require.Equal(t, "handled", e.outcome(t, e.deliver(t, "workflow_run", runP(run1))))

	rel, err := e.srv.Store.Q.GetGithubRelease(e.ctx, db.GetGithubReleaseParams{RepoID: m15RepoID, Version: m15Version})
	require.NoError(t, err)
	assert.Equal(t, "fixing", rel.State)
	require.NotNil(t, rel.ThreadID)
	th := e.waitM15Thread(t, *rel.ThreadID, 90*time.Second, settled)
	assert.Equal(t, "Release v"+m15Version, th.Title)
	// 页头的发布状态（依赖发布线程 githubTask 的修复，已报告给 Main）
	if assert.NotNil(t, th.GithubTask, "发布线程的页头带发布状态") {
		assert.Equal(t, "release", th.GithubTask.Kind)
		assert.Equal(t, m15Version, th.GithubTask.Version)
	}
	assert.Contains(t, e.raw("GET", "/api/v1/threads/"+th.ID+"/entries?limit=500"),
		"[Oryn release sentinel on acme/widgets v"+m15Version+" — started automatically, not typed by the owner]")
	o := e.orbOf(t, th)
	assert.Equal(t, "oryn-bot", e.ghLogin(t, o))

	dir := filepath.Join(o.home, "workspace", "repo")
	require.Equal(t, sha1, e.orbGit(t, o, dir, "rev-parse", "HEAD"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "timeout.txt"), []byte("60s\n"), 0o644))
	e.orbGit(t, o, dir, "add", "timeout.txt")
	e.orbGit(t, o, dir, "commit", "-q", "-m", m15FixMsg)
	e.orbGit(t, o, dir, "tag", "-f", "v"+m15Version, "HEAD")
	sha2 := e.orbGit(t, o, dir, "rev-parse", "HEAD")
	push := []string{"push", "--atomic", "--force-with-lease=refs/tags/v" + m15Version + ":" + *rel.TagOid, "origin", "HEAD:refs/heads/main", "refs/tags/v" + m15Version}

	out, err := e.orbGitErr(o, dir, push...)
	require.Error(t, err, "推送门禁对发布线程同样要求 prepare")
	assert.Contains(t, out, "没有 github_prepare 通过记录")
	var prep orbdrt.PrepareResult
	e.sockCall(t, o, orbsock.MethodGithubPrepare, orbsock.GithubPrepareParams{ThreadID: o.thread, Level: "push"}, &prep)
	require.True(t, prep.OK, "发布线程可以在默认分支上 prepare：%+v", prep)
	e.orbGit(t, o, dir, push...)
	assert.Equal(t, sha2, e.bareRev(t, m15Repo, "refs/heads/main"), "发布线程可推默认分支")
	assert.Equal(t, sha2, e.bareRev(t, m15Repo, "refs/tags/v"+m15Version), "tag 随分支原子移动")

	run2 := e.fake.AddRun(githubtest.WorkflowRun{RepoID: m15RepoID, Name: "CI", Event: "push", Status: "completed", Conclusion: "success",
		HeadSHA: sha2, HeadBranch: "main", Message: m15FixMsg, Actor: "oryn-bot"})
	e.fake.AddRelease(githubtest.Release{RepoID: m15RepoID, TagName: "v" + m15Version, Name: "v" + m15Version})
	require.Equal(t, "handled", e.outcome(t, e.deliver(t, "workflow_run", runP(run2))))
	rel, err = e.srv.Store.Q.GetGithubRelease(e.ctx, db.GetGithubReleaseParams{RepoID: m15RepoID, Version: m15Version})
	require.NoError(t, err)
	assert.Equal(t, "green", rel.State)
	assert.Equal(t, sha2, rel.Sha)
	var tasks struct {
		Tasks []struct {
			Kind, Version, State, ThreadID string
		} `json:"tasks"`
	}
	e.rest("GET", "/api/v1/github/tasks", nil, &tasks, 200)
	i := slices.IndexFunc(tasks.Tasks, func(x struct{ Kind, Version, State, ThreadID string }) bool { return x.Kind == "release" })
	require.GreaterOrEqual(t, i, 0)
	assert.Equal(t, "green", tasks.Tasks[i].State)
	assert.Equal(t, th.ID, tasks.Tasks[i].ThreadID)
	e.fake.Lock()
	defer e.fake.Unlock()
	assert.Len(t, e.fake.Comments, comments, "发布哨兵的终态不在 GitHub 上发言")
}

// 8. 上游维护者合并 PR：Oryn 在上游没有 hook，靠对象级 REST 轮询发现合并，线程标为已交付。
func (e *m15Env) stepUpstreamMerged(t *testing.T, o orbCtx) {
	e.fake.Lock()
	for _, pr := range e.fake.Pulls {
		if pr.RepoID == m15UpID && pr.Number == 1 {
			now := time.Now().UTC().Truncate(time.Second)
			pr.State, pr.MergedAt, pr.MergedBy, pr.UpdatedAt = "closed", now, "maint", now
		}
	}
	e.fake.Unlock()
	th := e.waitM15Thread(t, o.thread, m15PollLimit, func(v m15Thread) bool { return v.ShippedAt != nil })
	require.Len(t, th.Pulls, 1)
	assert.Equal(t, "merged", th.Pulls[0].State)
	assert.NotEmpty(t, e.fake.CallsTo("GET", "/repos/"+m15Upstream+"/pulls/1"), "经对象级轮询读到合并")
}

// 9. 自动关闭：到期后 orynd 的扫描只看 issue 作者在那条评论上的 👎（没有），以 completed 关闭 issue，令牌用线程身份；
// 关闭事件的 webhook 不回流，任务进入终态。
func (e *m15Env) stepAutoclose(t *testing.T, o orbCtx) {
	deadline := time.Now().Add(m15PollLimit)
	for e.fake.IssueState(m15RepoID, m15Issue) != "closed" {
		require.True(t, time.Now().Before(deadline), "自动关闭没有执行")
		time.Sleep(200 * time.Millisecond)
	}
	patches := e.fake.CallsTo("PATCH", fmt.Sprintf("/repos/%s/issues/%d", m15Repo, m15Issue))
	require.Len(t, patches, 1, "每条排期只执行一次")
	assert.JSONEq(t, `{"state":"closed","state_reason":"completed"}`, string(patches[0].Body))
	posts := e.fake.CallsTo("POST", fmt.Sprintf("/repos/%s/issues/%d/comments", m15Repo, m15Issue))
	require.NotEmpty(t, posts)
	assert.Equal(t, posts[0].Auth, patches[0].Auth, "关闭用线程身份（Agent 账号）的令牌")
	assert.NotEmpty(t, e.fake.CallsTo("GET", "/repos/"+m15Repo+"/issues/comments/*"), "检查过评论上的反应")
	ac, err := e.srv.Store.Q.GetGithubAutoclose(e.ctx, db.GetGithubAutocloseParams{RepoID: m15RepoID, Number: m15Issue})
	require.NoError(t, err)
	assert.Equal(t, "done", ac.State)
	require.NotNil(t, ac.Outcome)
	assert.Equal(t, "closed", *ac.Outcome)

	issue := e.issueP(m15Issue, false)
	issue["state"], issue["state_reason"] = "closed", "completed"
	require.Equal(t, "handled", e.outcome(t, e.deliver(t, "issues", map[string]any{"action": "closed", "issue": issue, "repository": repoP(), "sender": userP("oryn-bot")})))
	th := e.waitM15Thread(t, o.thread, 30*time.Second, func(v m15Thread) bool { return v.GithubTask != nil && v.GithubTask.State == "closed" })
	assert.NotContains(t, e.raw("GET", "/api/v1/threads/"+th.ID+"/entries?limit=500"), "[Oryn GitHub event on acme/widgets#7", "关闭事件不作为事件回流")
}

// 10. 审计：每台 orb 的 GitHub 写操作各一行（REST 非 GET、GraphQL mutation、推送按 ref），带线程、身份、方法、操作、仓库、
// 状态码、门禁结论与正文哈希；被门禁拒绝的也记。按线程与按仓库都能查。
func (e *m15Env) stepAudit(t *testing.T, up, triage, gate orbCtx) {
	byThread := func(o orbCtx) []m15Audit {
		var out struct {
			Entries []m15Audit `json:"entries"`
		}
		e.rest("GET", "/api/v1/github/audit?limit=200&thread="+o.thread, nil, &out, 200)
		for _, a := range out.Entries {
			assert.Equal(t, o.thread, a.ThreadID)
			assert.Equal(t, o.orb, a.OrbID)
		}
		return out.Entries
	}
	type row struct {
		slot, method, op, repo string
		status                 int
		gate                   string
		body                   bool
	}
	pick := func(rows []m15Audit) []row {
		var out []row
		for _, a := range rows {
			out = append(out, row{a.Slot, a.Method, a.Op, a.Repo, a.Status, a.Gate, a.BodySha256 != ""})
		}
		slices.Reverse(out) // 接口按时间倒序
		return out
	}
	t.Logf("审计（上游）：%+v", pick(byThread(up)))
	t.Logf("审计（分诊）：%+v", pick(byThread(triage)))
	t.Logf("审计（门禁）：%+v", pick(byThread(gate)))

	assert.Equal(t, []row{
		{"user", "POST", "POST /repos/{owner}/{repo}/forks", m15Upstream, 202, "", false}, // 正文哈希只对有 body 字段的写操作
		{"user", "PUSH", "refs/heads/fix-typo", "owner/lib", 200, "", false},
		{"user", "POST", "createPullRequest " + githubtest.RepoNodeID(m15UpID), "", 200, "", true},
	}, pick(byThread(up)))
	assert.Equal(t, []row{
		{"agent", "POST", "POST /repos/{owner}/{repo}/issues/{number}/comments", m15Repo, 201, "", true},
	}, pick(byThread(triage)))
	g := pick(byThread(gate))
	require.Len(t, g, 6)
	assert.Equal(t, row{"user", "PUSH", "refs/heads/fix-login", m15Repo, 200, g[0].gate, false}, g[0])
	assert.Contains(t, g[0].gate, "没有 github_prepare 通过记录", "被拒绝的推送记下拒绝原因")
	assert.Equal(t, row{"user", "PUSH", "refs/heads/fix-login", m15Repo, 200, "pass", false}, g[1])
	assert.Equal(t, row{"user", "POST", "createPullRequest " + githubtest.RepoNodeID(m15RepoID), "", 403, g[2].gate, true}, g[2])
	assert.Contains(t, g[2].gate, "没有 pr 级 github_prepare 通过记录")
	assert.Equal(t, row{"user", "POST", "createPullRequest " + githubtest.RepoNodeID(m15RepoID), "", 200, "pass", true}, g[3])
	assert.Equal(t, row{"user", "POST", "addComment " + githubtest.PullNodeID(m15RepoID, 8), "", 200, "", true}, g[4])
	assert.Equal(t, row{"user", "POST", "POST /repos/{owner}/{repo}/issues/{number}/comments", m15Repo, 201, "", true}, g[5])

	var byRepo struct {
		Entries []m15Audit `json:"entries"`
	}
	e.rest("GET", "/api/v1/github/audit?limit=200&repo="+m15Repo, nil, &byRepo, 200)
	threads := map[string]bool{}
	for _, a := range byRepo.Entries {
		assert.Equal(t, m15Repo, a.Repo)
		threads[a.ThreadID] = true
	}
	assert.True(t, threads[triage.thread] && threads[gate.thread], "按仓库查询含两条线程的写操作：%v", threads)
}

// ---------------------------------------------------------------- 辅助

// raw 发一个 REST 请求并返回响应正文（要求 200）。
func (e *m15Env) raw(method, path string) string {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.http.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(e.t, http.StatusOK, resp.StatusCode, "%s %s: %s", method, path, b)
	return string(b)
}

func noRedirect(t *testing.T, u string) *http.Response {
	t.Helper()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(u)
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp
}

// newThread 建一个新 orb 上的线程，等它空闲后返回 orb 的出站代理地址与 HOME。
func (e *m15Env) newThread(t *testing.T, body map[string]any) orbCtx {
	t.Helper()
	body["executor"] = map[string]any{"kind": "new_orb", "provider": "local", "type": "small"}
	var created struct {
		Thread threadView `json:"thread"`
	}
	e.rest("POST", "/api/v1/threads", body, &created, 201)
	th := e.waitM15Thread(t, created.Thread.ID, 90*time.Second, settled)
	return e.orbOf(t, th)
}

// settled 报告线程的 orb 已就绪、首个回合已结束：fakeomp 看到消息里含 ask（预设提示词里有 task）时停在 awaiting_input。
func settled(v m15Thread) bool {
	return (v.State == "idle" || v.State == "awaiting_input") && v.OrbID != ""
}

func (e *m15Env) waitM15Thread(t *testing.T, id string, timeout time.Duration, cond func(m15Thread) bool) m15Thread {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var v m15Thread
		e.rest("GET", "/api/v1/threads/"+id, nil, &v, 200)
		if cond(v) {
			return v
		}
		require.True(t, time.Now().Before(deadline), "线程 %s 未达到期望状态，当前 %+v", id, v)
		time.Sleep(100 * time.Millisecond)
	}
}

// orbOf 从 orb 的全局 git 配置里读出出站代理地址：setup 写的 `http.http://github.com/.proxy`；
// 读不到时回落到老 orbd 写的 `url.http://127.0.0.1:<port>/git/github.com/.insteadOf`。
func (e *m15Env) orbOf(t *testing.T, th m15Thread) orbCtx {
	t.Helper()
	o := orbCtx{thread: th.ID, orb: th.OrbID, home: e.drv.HomeDir(th.OrbID)}
	if out, err := orbGitCmd(o, o.home, "config", "--global", "--get", "http.http://github.com/.proxy").Output(); err == nil {
		o.egress = strings.TrimSpace(string(out))
	}
	if o.egress == "" {
		out, _ := orbGitCmd(o, o.home, "config", "--global", "--get-regexp", `^url\.`).Output()
		for line := range strings.SplitSeq(string(out), "\n") {
			key, val, _ := strings.Cut(line, " ")
			if val == "https://github.com/" && strings.Contains(key, "/git/") {
				o.egress, _, _ = strings.Cut(strings.TrimPrefix(key, "url."), "/git/")
			}
		}
	}
	require.NotEmpty(t, o.egress, "orb 的 git 配置里既没有 http.http://github.com/.proxy，也没有老的 /git/ insteadOf")
	return o
}

func orbGitCmd(o orbCtx, dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + o.home, "PATH=" + os.Getenv("PATH"), "LC_ALL=C", "GIT_TERMINAL_PROMPT=0"}
	return cmd
}

// orbGit 以 orb 的 HOME（其 git 配置、提交身份与 insteadOf）运行 git，返回去掉首尾空白的输出。
func (e *m15Env) orbGit(t *testing.T, o orbCtx, dir string, args ...string) string {
	t.Helper()
	out, err := orbGitCmd(o, dir, args...).CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

func (e *m15Env) orbGitErr(o orbCtx, dir string, args ...string) (string, error) {
	out, err := orbGitCmd(o, dir, args...).CombinedOutput()
	return string(out), err
}

// gh 以 orb 内 gh 的形态经出站代理调用 GitHub REST（占位令牌由出站代理换成槽位的 OAuth 令牌）。
func (e *m15Env) gh(t *testing.T, o orbCtx, method, path string, body any) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, o.egress+"/gh/api.github.com"+path, rd)
	require.NoError(t, err)
	req.Header.Set("Authorization", "token oryn-proxied")
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// graphql 以 gh 的形态（query + operationName + variables）经出站代理调用 GraphQL。
func (e *m15Env) graphql(t *testing.T, o orbCtx, query, op string, vars map[string]any) (int, string) {
	t.Helper()
	return e.gh(t, o, "POST", "/graphql", map[string]any{"query": query, "operationName": op, "variables": vars})
}

func (e *m15Env) ghLogin(t *testing.T, o orbCtx) string {
	t.Helper()
	code, body := e.gh(t, o, "GET", "/user", nil)
	require.Equal(t, http.StatusOK, code, body)
	var u struct {
		Login string `json:"login"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &u))
	return u.Login
}

// sockCall 以线程的身份调用 orb 的 orbd.sock（omp 扩展工具的同一入口）。
func (e *m15Env) sockCall(t *testing.T, o orbCtx, method string, params, out any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(e.ctx, 2*time.Minute)
	defer cancel()
	raw, err := orbsock.Call(ctx, orbsock.Path(o.home), method, params, nil)
	require.NoError(t, err, method)
	require.NoError(t, json.Unmarshal(raw, out), "%s", raw)
}

// deliver 经假 GitHub 以 acme/widgets 的仓库 hook 身份签名投递一次事件，返回 guid。
func (e *m15Env) deliver(t *testing.T, event string, payload any) string {
	t.Helper()
	status, guid := e.fake.DeliverHook(m15Repo, event, payload)
	require.NotEmpty(t, guid, "仓库上没有订阅 %s 的 hook", event)
	require.Equal(t, http.StatusAccepted, status, "webhook 验签通过")
	return guid
}

// outcome 等待一次投递处理完，返回结果。
func (e *m15Env) outcome(t *testing.T, guid string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		d, err := e.srv.Store.Q.GetGithubDelivery(e.ctx, guid)
		if err == nil && d.Outcome != "" && d.Outcome != "pending" {
			return d.Outcome
		}
		require.True(t, time.Now().Before(deadline), "投递 %s 没有处理完（%v %+v）", guid, err, d)
		time.Sleep(50 * time.Millisecond)
	}
}

func (e *m15Env) lastCommentID(t *testing.T, number int64) int64 {
	t.Helper()
	e.fake.Lock()
	defer e.fake.Unlock()
	for i := len(e.fake.Comments) - 1; i >= 0; i-- {
		if c := e.fake.Comments[i]; c.RepoID == m15RepoID && c.Number == number {
			return c.ID
		}
	}
	t.Fatalf("#%d 没有评论", number)
	return 0
}

// bareRevOpt 读假 GitHub 裸仓库里的 ref；没有时为空串。
func (e *m15Env) bareRevOpt(full, ref string) string {
	out, err := exec.Command("git", "--git-dir", e.fake.BarePath(full), "rev-parse", "--verify", "-q", ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (e *m15Env) bareRev(t *testing.T, full, ref string) string {
	t.Helper()
	sha := e.bareRevOpt(full, ref)
	require.NotEmpty(t, sha, "%s 没有 %s", full, ref)
	return sha
}

// hostCommit 在宿主上（维护者的机器，不经 Oryn）给仓库的 main 提交一个文件并打轻量 tag，直接推到假 GitHub 的裸仓库。
func (e *m15Env) hostCommit(t *testing.T, full, msg, file, content, tag string) string {
	t.Helper()
	work := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = []string{"HOME=" + work, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=maint", "GIT_AUTHOR_EMAIL=maint@example.com", "GIT_COMMITTER_NAME=maint", "GIT_COMMITTER_EMAIL=maint@example.com"}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
		return strings.TrimSpace(string(out))
	}
	run("clone", "-q", e.fake.BarePath(full), ".")
	require.NoError(t, os.WriteFile(filepath.Join(work, file), []byte(content), 0o644))
	run("add", file)
	run("commit", "-q", "-m", msg)
	run("tag", tag)
	run("push", "-q", "origin", "HEAD:refs/heads/main", "refs/tags/"+tag)
	return run("rev-parse", "HEAD")
}

// ---------------------------------------------------------------- webhook 载荷（字段照 GitHub 文档，只给处理器会读的部分）

func repoP() map[string]any {
	return map[string]any{
		"id": m15RepoID, "node_id": githubtest.RepoNodeID(m15RepoID), "full_name": m15Repo, "name": "widgets",
		"owner": map[string]any{"login": "acme", "type": "Organization"}, "default_branch": "main", "private": false,
		"html_url": "https://github.com/" + m15Repo,
	}
}

func userP(login string) map[string]any { return map[string]any{"login": login, "type": "User"} }

// issueP 是 acme/widgets 上 issue（或 PR 的 issue 视图）number 的载荷，取自假 GitHub 的现状。
func (e *m15Env) issueP(number int64, pull bool) map[string]any {
	e.fake.Lock()
	defer e.fake.Unlock()
	title, body, author, kind := "", "", "", "issues"
	for _, is := range e.fake.Issues {
		if is.RepoID == m15RepoID && is.Number == number {
			title, body, author = is.Title, is.Body, is.Author
		}
	}
	m := map[string]any{
		"id": githubtest.IssueID(m15RepoID, number), "node_id": githubtest.IssueNodeID(m15RepoID, number), "number": number,
		"state": "open", "labels": []any{}, "author_association": "NONE",
	}
	if pull {
		kind = "pull"
		for _, pr := range e.fake.Pulls {
			if pr.RepoID == m15RepoID && pr.Number == number {
				title, body, author = pr.Title, pr.Body, pr.Author
			}
		}
		m["id"], m["node_id"] = githubtest.PullID(m15RepoID, number), githubtest.PullNodeID(m15RepoID, number)
		m["pull_request"] = map[string]any{"url": fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", m15Repo, number)}
		m["author_association"] = "COLLABORATOR"
	}
	m["title"], m["body"], m["user"] = title, body, userP(author)
	m["html_url"] = fmt.Sprintf("https://github.com/%s/%s/%d", m15Repo, kind, number)
	return m
}

func commentP(id int64, login, body, assoc string) map[string]any {
	return map[string]any{
		"id": id, "node_id": githubtest.CommentNodeID(id), "body": body, "user": userP(login), "author_association": assoc,
		"html_url": fmt.Sprintf("https://github.com/%s/issues/0#issuecomment-%d", m15Repo, id),
	}
}

func runP(run *githubtest.WorkflowRun) map[string]any {
	return map[string]any{
		"action": "completed", "repository": repoP(), "sender": userP(run.Actor),
		"workflow_run": map[string]any{
			"id": run.ID, "name": run.Name, "event": run.Event, "status": run.Status, "conclusion": run.Conclusion,
			"head_sha": run.HeadSHA, "head_branch": run.HeadBranch,
			"head_commit": map[string]any{"id": run.HeadSHA, "message": run.Message},
			"html_url":    fmt.Sprintf("https://github.com/%s/actions/runs/%d", m15Repo, run.ID),
		},
	}
}
