package github

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// hookEnv 配好 OAuth App（webhook 基址指向把请求交给 HandleWebhook 的测试端点）、
// 连接我的账号 owner，并在假服务上建 Acme/Widgets（id 42，owner 是 admin）。返回 webhook 基址。
func (e *testEnv) hookEnv() string {
	e.t.Helper()
	base := e.hookURL()
	require.NoError(e.t, e.s.SetOAuthApp(e.ctx, e.fake.OAuthApp.ClientID, e.fake.OAuthApp.ClientSecret, base))
	e.widgets()
	e.setPerm("owner", "admin")
	e.fake.AddUser(1, "owner", "The Owner")
	require.NoError(e.t, e.connect(slotUser, "c-owner", "owner"))
	return base
}

func (e *testEnv) hookRow(repoID int64) db.GithubHook {
	e.t.Helper()
	row, err := e.st.Q.GetGithubHook(e.ctx, repoID)
	require.NoError(e.t, err)
	return row
}

func (e *testEnv) hookSecret(row db.GithubHook) string {
	e.t.Helper()
	b, err := e.s.cfg.Box.Open(aadHookSecret(row.RepoID), row.SecretEnc)
	require.NoError(e.t, err)
	return string(b)
}

func (e *testEnv) fakeHooks() []githubtest.RepoHook {
	e.fake.Lock()
	defer e.fake.Unlock()
	var out []githubtest.RepoHook
	for _, h := range e.fake.RepoHooks {
		out = append(out, *h)
	}
	return out
}

func (e *testEnv) bindWidgets() string {
	e.t.Helper()
	p := e.project("https://github.com/acme/widgets")
	require.NoError(e.t, e.s.BindProject(e.ctx, p))
	return p
}

func TestHookCreateAndIdempotent(t *testing.T) {
	e := newEnv(t)
	base := e.hookEnv()
	e.bindWidgets() // 绑定后自动建 hook

	hooks := e.fakeHooks()
	require.Len(t, hooks, 1)
	h := hooks[0]
	assert.Equal(t, base+"/api/v1/public/github/webhook", h.URL)
	assert.Equal(t, "web", h.Name)
	assert.True(t, h.Active)
	assert.Equal(t, hookEvents, h.Events)
	assert.Equal(t, "json", h.ContentType)
	assert.Equal(t, "0", h.InsecureSSL)
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), h.Secret)

	row := e.hookRow(42)
	assert.Equal(t, hookActive, row.Status)
	require.NotNil(t, row.HookID)
	assert.Equal(t, h.ID, *row.HookID)
	assert.Equal(t, "Acme/Widgets", row.FullName)
	assert.NotContains(t, string(row.SecretEnc), h.Secret)
	assert.Equal(t, h.Secret, e.hookSecret(row))
	_, err := e.s.cfg.Box.Open(aadHookSecret(43), row.SecretEnc)
	assert.Error(t, err, "secret 的 AAD 绑定仓库 id")
	assert.True(t, e.s.isOrynHook(e.ctx, "acme/widgets", h.ID))
	assert.False(t, e.s.isOrynHook(e.ctx, "acme/other", h.ID))
	assert.False(t, e.s.isOrynHook(e.ctx, "acme/widgets", h.ID+1))

	// 幂等：再同步只 PATCH 同一个 hook（连同 secret 重写 config），不新建
	e.fake.ResetCalls()
	require.NoError(t, e.s.ensureHook(e.ctx, 42, "acme/widgets"))
	assert.Empty(t, e.fake.CallsTo("POST", "/repos/Acme/Widgets/hooks"))
	patches := e.fake.CallsTo("PATCH", "/repos/Acme/Widgets/hooks/*")
	require.Len(t, patches, 1)
	assert.Contains(t, string(patches[0].Body), h.Secret)
	assert.Len(t, e.fakeHooks(), 1)
	assert.Equal(t, h.Secret, e.fakeHooks()[0].Secret)

	// GitHub 上的 hook 被人删了：重建，secret 沿用
	e.fake.Lock()
	e.fake.RepoHooks = nil
	e.fake.Unlock()
	require.NoError(t, e.s.ensureHook(e.ctx, 42, "acme/widgets"))
	hooks = e.fakeHooks()
	require.Len(t, hooks, 1)
	assert.NotEqual(t, h.ID, hooks[0].ID)
	assert.Equal(t, h.Secret, hooks[0].Secret)
	assert.Equal(t, hooks[0].ID, *e.hookRow(42).HookID)
}

func TestHookAdoptExisting(t *testing.T) {
	e := newEnv(t)
	base := e.hookEnv()
	// 同 URL 的 hook 已存在（例如上次的库丢了）：接管它，PATCH 写入我们的 secret
	id := e.fake.AddRepoHook("Acme/Widgets", githubtest.RepoHook{
		Active: false, Events: []string{"push"}, URL: base + webhookPath, ContentType: "form", Secret: "stale",
	})
	e.fake.AddRepoHook("Acme/Widgets", githubtest.RepoHook{Active: true, Events: []string{"push"}, URL: "https://ci.example.com/hook"})
	e.bindWidgets()
	assert.Empty(t, e.fake.CallsTo("POST", "/repos/Acme/Widgets/hooks"))
	row := e.hookRow(42)
	require.NotNil(t, row.HookID)
	assert.Equal(t, id, *row.HookID)
	h, ok := e.fake.HookOf("Acme/Widgets", base+webhookPath)
	require.True(t, ok)
	assert.Equal(t, e.hookSecret(row), h.Secret)
	assert.True(t, h.Active)
	assert.Equal(t, "json", h.ContentType)
	assert.Equal(t, hookEvents, h.Events)
	assert.Len(t, e.fakeHooks(), 2, "别人的 hook 不动")
}

func TestHookNoAdmin(t *testing.T) {
	e := newEnv(t)
	e.hookEnv()
	e.setPerm("owner", "write")
	e.bindWidgets()
	row := e.hookRow(42)
	assert.Equal(t, hookNoAdmin, row.Status)
	assert.Nil(t, row.HookID)
	assert.Empty(t, e.fakeHooks())

	// 升为 admin 后下一轮同步建成
	e.setPerm("owner", "admin")
	require.NoError(t, e.s.ResyncHooks(e.ctx))
	assert.Equal(t, hookActive, e.hookRow(42).Status)
	assert.Len(t, e.fakeHooks(), 1)
}

func TestHookOrgRestricted(t *testing.T) {
	e := newEnv(t)
	e.hookEnv()
	// 先以非 admin 绑定（只记 no_admin，不建 hook），再模拟组织限制：绑定本身遇到限制时项目会解绑（见 TestBindProject）
	e.setPerm("owner", "read")
	e.bindWidgets()
	e.setPerm("owner", "admin")
	e.fake.Lock()
	e.fake.Repos[0].Restricted = "org_restricted"
	e.fake.Unlock()
	require.NoError(t, e.s.ensureHook(e.ctx, 42, "acme/widgets"))
	row := e.hookRow(42)
	assert.Equal(t, kindOrgRestricted, row.Status)
	require.NotNil(t, row.Error)
	assert.Contains(t, *row.Error, "OAuth App access restrictions")
	assert.Empty(t, e.fakeHooks())

	e.fake.Lock()
	e.fake.Repos[0].Restricted = "sso_required"
	e.fake.Unlock()
	require.NoError(t, e.s.ensureHook(e.ctx, 42, "acme/widgets"))
	row = e.hookRow(42)
	assert.Equal(t, kindSSORequired, row.Status)
	require.NotNil(t, row.Error)
	assert.Contains(t, *row.Error, "/orgs/Acme/sso", "记下 X-GitHub-SSO 里的授权链接")

	// 其余上游错误记 error
	e.fake.Lock()
	e.fake.Repos[0].Restricted = ""
	e.fake.Fail = map[string]int{"POST /repos/Acme/Widgets/hooks": http.StatusUnprocessableEntity}
	e.fake.Unlock()
	require.NoError(t, e.s.ensureHook(e.ctx, 42, "acme/widgets"))
	row = e.hookRow(42)
	assert.Equal(t, hookError, row.Status)
	require.NotNil(t, row.Error)
	assert.Contains(t, *row.Error, "HTTP 422")
}

func TestHookSharedRepoUnbind(t *testing.T) {
	e := newEnv(t)
	e.hookEnv()
	p1 := e.bindWidgets()
	p2 := e.bindWidgets()
	require.Len(t, e.fakeHooks(), 1)

	// p1 改到非 GitHub 地址：解绑，但 p2 仍绑定该仓库，hook 保留
	other := "https://gitlab.com/acme/widgets"
	require.NoError(t, e.st.Q.SetProjectRepoURL(e.ctx, db.SetProjectRepoURLParams{RepoUrl: &other, GithubPrevUrls: "[]", UpdatedAt: 2, ID: p1}))
	require.NoError(t, e.s.BindProject(e.ctx, p1))
	assert.Nil(t, e.getProject(p1).GithubRepoID)
	assert.Len(t, e.fakeHooks(), 1)
	assert.Equal(t, hookActive, e.hookRow(42).Status)

	// 归档的项目不算绑定：p2 归档后释放即删除
	prj := e.getProject(p2)
	_, err := e.st.Q.UpdateProject(e.ctx, db.UpdateProjectParams{
		Name: prj.Name, RepoUrl: prj.RepoUrl, ArchivedAt: new(int64(3)), UpdatedAt: 3, ID: p2,
	})
	require.NoError(t, err)
	e.fake.ResetCalls()
	require.NoError(t, e.s.ReleaseHook(e.ctx, 42))
	assert.Len(t, e.fake.CallsTo("DELETE", "/repos/Acme/Widgets/hooks/*"), 1)
	assert.Empty(t, e.fakeHooks())
	_, err = e.st.Q.GetGithubHook(e.ctx, 42)
	assert.True(t, isNoRows(err))
	require.NoError(t, e.s.ReleaseHook(e.ctx, 42), "没有 hook 时无事可做")
}

func TestHookSyncDropsUnbound(t *testing.T) {
	e := newEnv(t)
	e.hookEnv()
	p := e.bindWidgets()
	require.Len(t, e.fakeHooks(), 1)
	require.NoError(t, e.st.Q.ClearProjectGithub(e.ctx, p))
	require.NoError(t, e.s.ResyncHooks(e.ctx))
	assert.Empty(t, e.fakeHooks())
	_, err := e.st.Q.GetGithubHook(e.ctx, 42)
	assert.True(t, isNoRows(err))
}

func TestDeleteOAuthAppDropsHooks(t *testing.T) {
	e := newEnv(t)
	e.hookEnv()
	e.fake.AddRepo(githubtest.Repo{ID: 43, FullName: "Acme/Gadgets", OwnerID: 900, Permissions: map[string]string{"owner": "admin"}})
	e.bindWidgets()
	g := e.project("https://github.com/acme/gadgets")
	require.NoError(t, e.s.BindProject(e.ctx, g))
	require.Len(t, e.fakeHooks(), 2)
	// 别人的 hook 不动
	e.fake.AddRepoHook("Acme/Widgets", githubtest.RepoHook{Active: true, URL: "https://ci.example.com/hook"})

	e.fake.ResetCalls()
	require.NoError(t, e.s.DeleteOAuthApp(e.ctx))
	assert.Len(t, e.fake.CallsTo("DELETE", "/repos/*"), 2)
	hooks := e.fakeHooks()
	require.Len(t, hooks, 1)
	assert.Equal(t, "https://ci.example.com/hook", hooks[0].URL)
	rows, err := e.st.Q.ListGithubHooks(e.ctx)
	require.NoError(t, err)
	assert.Empty(t, rows)
	_, err = e.s.tokenFor(e.ctx, slotUser)
	assert.Equal(t, "github_identity_required", codeOf(err))
}

func TestDisconnectUserDropsHooks(t *testing.T) {
	e := newEnv(t)
	e.hookEnv()
	e.bindWidgets()
	require.Len(t, e.fakeHooks(), 1)
	require.NoError(t, e.s.Disconnect(e.ctx, slotUser))
	assert.Empty(t, e.fakeHooks())
	rows, err := e.st.Q.ListGithubHooks(e.ctx)
	require.NoError(t, err)
	assert.Empty(t, rows)
	assert.Equal(t, "not_found", codeOf(e.s.Disconnect(e.ctx, slotUser)))

	// 我的账号不可用时同步什么都不做
	require.NoError(t, e.s.ResyncHooks(e.ctx))
	assert.Empty(t, e.fakeHooks())
}

func TestHookWebhookBaseChange(t *testing.T) {
	e := newEnv(t)
	e.hookEnv()
	e.bindWidgets()
	before := e.fakeHooks()[0]

	e.fake.ResetCalls()
	require.NoError(t, e.s.SetOAuthApp(e.ctx, e.fake.OAuthApp.ClientID, "", "https://hooks.example.com/"))
	after := e.fakeHooks()
	require.Len(t, after, 1)
	assert.Equal(t, before.ID, after[0].ID)
	assert.Equal(t, "https://hooks.example.com"+webhookPath, after[0].URL)
	assert.Equal(t, before.Secret, after[0].Secret)
	assert.Len(t, e.fake.CallsTo("PATCH", "/repos/Acme/Widgets/hooks/*"), 1)

	// 基址不变（只改 client id）时不重同步
	e.fake.ResetCalls()
	require.NoError(t, e.s.SetOAuthApp(e.ctx, e.fake.OAuthApp.ClientID, "", "https://hooks.example.com"))
	assert.Empty(t, e.fake.CallsTo("PATCH", "/repos/*"))

	// 清空基址 = 回到 public_url
	require.NoError(t, e.s.SetOAuthApp(e.ctx, e.fake.OAuthApp.ClientID, "", ""))
	assert.Equal(t, testPublicURL+webhookPath, e.fakeHooks()[0].URL)
}

func TestHookNotManagedWithoutOAuthApp(t *testing.T) {
	e := newEnv(t)
	e.oauthApp()
	e.widgets()
	e.setPerm("owner", "admin")
	e.fake.AddUser(1, "owner", "")
	require.NoError(t, e.connect(slotUser, "c1", "owner"))
	require.NoError(t, e.st.Q.DeleteGithubOAuthApp(e.ctx))
	e.bindWidgets()
	assert.NotNil(t, e.getProject(e.bindWidgets()).GithubRepoID, "绑定不依赖 OAuth 配置行")
	assert.Empty(t, e.fakeHooks())
	require.NoError(t, e.s.ResyncHooks(e.ctx))
	assert.Empty(t, e.fakeHooks())
}
