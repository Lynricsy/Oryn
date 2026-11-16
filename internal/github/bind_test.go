package github

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// project 建一个仓库地址为 repoURL 的项目。
func (e *testEnv) project(repoURL string) string {
	e.t.Helper()
	id := ids.New("prj")
	var u *string
	if repoURL != "" {
		u = &repoURL
	}
	require.NoError(e.t, e.st.Q.CreateProject(e.ctx, db.CreateProjectParams{ID: id, Name: id, RepoUrl: u, CreatedAt: 1, UpdatedAt: 1}))
	return id
}

func (e *testEnv) getProject(id string) db.Project {
	e.t.Helper()
	p, err := e.st.Q.GetProject(e.ctx, id)
	require.NoError(e.t, err)
	return p
}

// widgets 在假服务上建 Acme/Widgets（id 42）。
func (e *testEnv) widgets() *githubtest.Repo {
	e.t.Helper()
	return e.fake.AddRepo(githubtest.Repo{ID: 42, FullName: "Acme/Widgets", OwnerID: 900})
}

// userEnv 配好 OAuth App 并连接我的账号 owner。
func (e *testEnv) userEnv() {
	e.t.Helper()
	e.oauthApp()
	e.fake.AddUser(1, "owner", "The Owner")
	require.NoError(e.t, e.connect(slotUser, "c-owner", "owner"))
}

func TestBindProject(t *testing.T) {
	e := newEnv(t)
	e.widgets()
	// 我的账号未连接：什么都不做
	p := e.project("https://github.com/acme/widgets.git")
	require.NoError(t, e.s.BindProject(e.ctx, p))
	assert.Nil(t, ProjectGitHubAPI(e.getProject(p)))

	e.userEnv()
	require.NoError(t, e.s.BindProject(e.ctx, p))
	row := e.getProject(p)
	require.NotNil(t, row.GithubRepoID)
	assert.EqualValues(t, 42, *row.GithubRepoID)
	assert.Nil(t, row.GithubBindStatus)
	api := ProjectGitHubAPI(row)
	require.NotNil(t, api)
	assert.Equal(t, "acme/widgets", api.FullName)
	assert.True(t, api.Bound)
	assert.Nil(t, api.BindStatus)
	assert.True(t, api.Listen.Reviews && api.Listen.Comments && api.Listen.Ci)
	repoID := int64(42)
	bound, err := e.st.Q.ListProjectsByGithubRepo(e.ctx, &repoID)
	require.NoError(t, err)
	assert.Len(t, bound, 1)

	// 非 github.com、路径不是两段：不绑定、也没有原因
	gitlab := e.project("https://gitlab.com/acme/widgets")
	deep := e.project("https://github.com/acme/widgets/extra")
	for _, id := range []string{gitlab, deep} {
		require.NoError(t, e.s.BindProject(e.ctx, id))
		assert.Nil(t, ProjectGitHubAPI(e.getProject(id)), id)
	}

	// 读不到的仓库：未绑定，原因 not_found
	other := e.project("https://github.com/acme/other")
	require.NoError(t, e.s.BindProject(e.ctx, other))
	api = ProjectGitHubAPI(e.getProject(other))
	require.NotNil(t, api)
	assert.False(t, api.Bound)
	require.NotNil(t, api.BindStatus)
	assert.EqualValues(t, "not_found", *api.BindStatus)

	// 组织限制 / SSO：清空仓库 id 并记原因（R8）；恢复后重新绑定并清空原因
	for _, kind := range []string{"org_restricted", "sso_required"} {
		e.fake.Lock()
		e.fake.Repos[0].Restricted = kind
		e.fake.Unlock()
		require.NoError(t, e.s.BindProject(e.ctx, p))
		row = e.getProject(p)
		assert.Nil(t, row.GithubRepoID, kind)
		require.NotNil(t, row.GithubBindStatus, kind)
		assert.Equal(t, kind, *row.GithubBindStatus)
		bound, err = e.st.Q.ListProjectsByGithubRepo(e.ctx, &repoID)
		require.NoError(t, err)
		assert.Empty(t, bound, kind)
	}
	e.fake.Lock()
	e.fake.Repos[0].Restricted = ""
	e.fake.Unlock()
	require.NoError(t, e.s.BindProject(e.ctx, p))
	row = e.getProject(p)
	require.NotNil(t, row.GithubRepoID)
	assert.Nil(t, row.GithubBindStatus)

	// 仓库地址改成非 GitHub：清空绑定
	require.NoError(t, e.st.Q.SetProjectRepoURL(e.ctx, db.SetProjectRepoURLParams{RepoUrl: new("https://gitlab.com/x/y"), GithubPrevUrls: "[]", UpdatedAt: 2, ID: p}))
	require.NoError(t, e.s.BindProject(e.ctx, p))
	assert.Nil(t, ProjectGitHubAPI(e.getProject(p)))
}

func TestBindProjectFollowsRename(t *testing.T) {
	e := newEnv(t)
	e.userEnv()
	e.widgets()
	p := e.project("https://github.com/acme/widgets.git")
	require.NoError(t, e.s.BindProject(e.ctx, p))
	require.NoError(t, e.st.Q.UpsertGithubPull(e.ctx, db.UpsertGithubPullParams{
		RepoID: 42, Number: 1, RepoFullName: "Acme/Widgets", Title: "t", State: "open", HeadRef: "h", HeadSha: "s",
		BaseRef: "main", AuthorLogin: "a", HtmlUrl: "u", UpdatedAt: 1,
	}))
	th := e.thread(p, 0)
	require.NoError(t, e.st.Q.UpsertGithubWatch(e.ctx, db.UpsertGithubWatchParams{
		Kind: "pull", RepoID: 42, FullName: "Acme/Widgets", Number: 1, ThreadID: th, Slot: "user",
	}))

	e.fake.Rename("Acme/Widgets", "acme/gadgets")
	require.NoError(t, e.s.BindProject(e.ctx, p))
	row := e.getProject(p)
	assert.Equal(t, "https://github.com/acme/gadgets", *row.RepoUrl)
	var prev []string
	require.NoError(t, json.Unmarshal([]byte(row.GithubPrevUrls), &prev))
	assert.Equal(t, []string{"https://github.com/acme/widgets.git"}, prev)
	assert.Equal(t, "acme/gadgets", ProjectGitHubAPI(row).FullName)
	pull, err := e.st.Q.GetGithubPull(e.ctx, db.GetGithubPullParams{RepoID: 42, Number: 1})
	require.NoError(t, err)
	assert.Equal(t, "acme/gadgets", pull.RepoFullName)

	// 对象级 watch 的键跟着改名（否则新旧名字下各轮询一份）
	w, err := e.st.Q.GetGithubWatch(e.ctx, db.GetGithubWatchParams{Kind: "pull", FullName: "acme/gadgets", Number: 1})
	require.NoError(t, err)
	assert.Equal(t, th, w.ThreadID)
	// 已是新名：不再写
	require.NoError(t, e.s.BindProject(e.ctx, p))
	assert.Equal(t, row.GithubPrevUrls, e.getProject(p).GithubPrevUrls)
}

func TestFollowRenameKeepsFivePrevURLs(t *testing.T) {
	e := newEnv(t)
	p := e.project("https://github.com/acme/n0")
	repoID := int64(42)
	require.NoError(t, e.st.Q.SetProjectGithub(e.ctx, db.SetProjectGithubParams{GithubRepoID: &repoID, ID: p}))
	for _, n := range []string{"n1", "n2", "n3", "n4", "n5", "n6", "n1"} {
		require.NoError(t, e.s.followRename(e.ctx, 42, "acme/"+n))
	}
	var prev []string
	require.NoError(t, json.Unmarshal([]byte(e.getProject(p).GithubPrevUrls), &prev))
	assert.Equal(t, []string{
		"https://github.com/acme/n2", "https://github.com/acme/n3", "https://github.com/acme/n4",
		"https://github.com/acme/n5", "https://github.com/acme/n6",
	}, prev)
	assert.Equal(t, "https://github.com/acme/n1", *e.getProject(p).RepoUrl)
}

// 连接我的账号后在后台重新绑定全部项目。
func TestBindProjectsOnConnect(t *testing.T) {
	e := newEnv(t)
	e.widgets()
	p := e.project("https://github.com/Acme/Widgets")
	e.userEnv()
	assert.Nil(t, e.getProject(p).GithubRepoID, "绑定在任务队列里")
	e.drain()
	row := e.getProject(p)
	require.NotNil(t, row.GithubRepoID)
	assert.EqualValues(t, 42, *row.GithubRepoID)
}
