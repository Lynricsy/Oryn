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

// widgets 在假服务上建 Acme/Widgets（id 42）并安装 App（安装 10）。
func (e *testEnv) widgets() *githubtest.Repo {
	e.t.Helper()
	e.fake.AddInstallation(10, "Acme")
	return e.fake.AddRepo(githubtest.Repo{ID: 42, FullName: "Acme/Widgets", OwnerID: 900, InstallationID: 10})
}

func TestBindProject(t *testing.T) {
	e := newEnv(t)
	// 没有 App：什么都不做
	p := e.project("https://github.com/acme/widgets.git")
	require.NoError(t, e.s.BindProject(e.ctx, p))
	assert.Nil(t, ProjectGitHubAPI(e.getProject(p)))

	e.installApp()
	e.widgets()
	require.NoError(t, e.s.BindProject(e.ctx, p))
	row := e.getProject(p)
	require.NotNil(t, row.GithubRepoID)
	assert.EqualValues(t, 42, *row.GithubRepoID)
	assert.EqualValues(t, 900, *row.GithubOwnerID)
	assert.EqualValues(t, 10, *row.GithubInstallationID)
	api := ProjectGitHubAPI(row)
	require.NotNil(t, api)
	assert.Equal(t, "acme/widgets", api.FullName)
	assert.True(t, api.Bound)
	assert.True(t, api.Listen.Reviews && api.Listen.Comments && api.Listen.Ci)
	repoID := int64(42)
	bound, err := e.st.Q.ListProjectsByGithubRepo(e.ctx, &repoID)
	require.NoError(t, err)
	assert.Len(t, bound, 1)

	// App 也没装的仓库、非 github.com、路径不是两段
	other := e.project("https://github.com/acme/other")
	gitlab := e.project("https://gitlab.com/acme/widgets")
	deep := e.project("https://github.com/acme/widgets/extra")
	for _, id := range []string{other, gitlab, deep} {
		require.NoError(t, e.s.BindProject(e.ctx, id))
		assert.Nil(t, ProjectGitHubAPI(e.getProject(id)), id)
	}

	// 卸载 App 后：仓库 id 仍在，但不再算绑定
	e.fake.RemoveInstallation(10)
	require.NoError(t, e.s.BindProject(e.ctx, p))
	api = ProjectGitHubAPI(e.getProject(p))
	require.NotNil(t, api)
	assert.False(t, api.Bound)
	bound, err = e.st.Q.ListProjectsByGithubRepo(e.ctx, &repoID)
	require.NoError(t, err)
	assert.Empty(t, bound)
}

func TestBindProjectFollowsRename(t *testing.T) {
	e := newEnv(t)
	e.installApp()
	e.widgets()
	p := e.project("https://github.com/acme/widgets.git")
	require.NoError(t, e.s.BindProject(e.ctx, p))
	require.NoError(t, e.st.Q.UpsertGithubPull(e.ctx, db.UpsertGithubPullParams{
		RepoID: 42, Number: 1, RepoFullName: "Acme/Widgets", Title: "t", State: "open", HeadRef: "h", HeadSha: "s",
		BaseRef: "main", AuthorLogin: "a", HtmlUrl: "u", UpdatedAt: 1,
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

	// 已是新名：不再写
	require.NoError(t, e.s.BindProject(e.ctx, p))
	assert.Equal(t, row.GithubPrevUrls, e.getProject(p).GithubPrevUrls)
}

func TestFollowRenameKeepsFivePrevURLs(t *testing.T) {
	e := newEnv(t)
	p := e.project("https://github.com/acme/n0")
	repoID, owner, inst := int64(42), int64(1), int64(10)
	require.NoError(t, e.st.Q.SetProjectGithub(e.ctx, db.SetProjectGithubParams{GithubRepoID: &repoID, GithubOwnerID: &owner, GithubInstallationID: &inst, ID: p}))
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

func TestBindProjectsOnSync(t *testing.T) {
	e := newEnv(t)
	e.installApp()
	e.widgets()
	p := e.project("https://github.com/Acme/Widgets")
	require.NoError(t, e.s.SyncInstallations(e.ctx))
	row := e.getProject(p)
	require.NotNil(t, row.GithubInstallationID)
	assert.EqualValues(t, 10, *row.GithubInstallationID)
}
