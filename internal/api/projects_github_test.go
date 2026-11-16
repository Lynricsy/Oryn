package api_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 项目详情与列表都带所绑仓库 hook 的状态（github_hooks），未绑定 / 没有 hook 行时省略。
func TestProjectGitHubHookStatus(t *testing.T) {
	e, fake, gh := newGitHubEnv(t)
	dev := h2DeviceToken(t, e)
	ctx := context.Background()
	connectGitHubUser(t, e, fake, gh, "owner")
	fake.AddRepo(githubtest.Repo{ID: 42, FullName: "Acme/Widgets", OwnerID: 900})

	type ghView struct {
		RepoID     int64
		Bound      bool
		HookStatus *string
	}
	type project struct {
		ID     string
		Github *ghView
	}
	get := func(id string) *ghView {
		t.Helper()
		r := e.do("GET", "/api/v1/projects/"+id, dev, nil)
		require.Equal(t, 200, r.status, string(r.body))
		var p project
		r.json(t, &p)
		return p.Github
	}
	list := func(id string) *ghView {
		t.Helper()
		r := e.do("GET", "/api/v1/projects", dev, nil)
		require.Equal(t, 200, r.status, string(r.body))
		var l struct{ Projects []project }
		r.json(t, &l)
		for _, p := range l.Projects {
			if p.ID == id {
				return p.Github
			}
		}
		require.Failf(t, "项目不在列表里", "%s", id)
		return nil
	}
	setHook := func(status string) {
		t.Helper()
		require.NoError(t, e.st.Q.UpsertGithubHook(ctx, db.UpsertGithubHookParams{
			RepoID: 42, FullName: "Acme/Widgets", Status: status, UpdatedAt: 1,
		}))
	}

	r := e.do("POST", "/api/v1/projects", dev, map[string]any{"name": "w", "repoUrl": "https://github.com/acme/widgets.git"})
	require.Equal(t, 201, r.status, string(r.body))
	var p project
	r.json(t, &p)
	require.NotNil(t, p.Github)
	require.True(t, p.Github.Bound)
	// 创建时绑定会建 hook：响应里的状态与 github_hooks 行一致
	row, err := e.st.Q.GetGithubHook(ctx, 42)
	require.NoError(t, err)
	require.NotNil(t, p.Github.HookStatus)
	assert.Equal(t, row.Status, *p.Github.HookStatus)

	// 未绑定的项目（非 GitHub 仓库）没有 github 字段，自然也没有 hookStatus
	r = e.do("POST", "/api/v1/projects", dev, map[string]any{"name": "plain"})
	require.Equal(t, 201, r.status, string(r.body))
	var plain project
	r.json(t, &plain)
	assert.Nil(t, get(plain.ID))
	assert.Nil(t, list(plain.ID))

	for _, st := range []string{"active", "no_admin"} {
		setHook(st)
		for name, v := range map[string]*ghView{"get": get(p.ID), "list": list(p.ID)} {
			require.NotNil(t, v, name)
			require.NotNil(t, v.HookStatus, "%s %s", name, st)
			assert.Equal(t, st, *v.HookStatus, name)
		}
	}

	// 改 listen（PATCH）的响应也带 hookStatus
	r = e.do("PATCH", "/api/v1/projects/"+p.ID, dev, map[string]any{"githubListen": map[string]bool{"reviews": false, "comments": true, "ci": true}})
	require.Equal(t, 200, r.status, string(r.body))
	p = project{}
	r.json(t, &p)
	require.NotNil(t, p.Github)
	require.NotNil(t, p.Github.HookStatus)
	assert.Equal(t, "no_admin", *p.Github.HookStatus)

	// 已绑定但没有 hook 行：省略 hookStatus
	_, err = e.st.Q.DeleteGithubHook(ctx, 42)
	require.NoError(t, err)
	for name, v := range map[string]*ghView{"get": get(p.ID), "list": list(p.ID)} {
		require.NotNil(t, v, name)
		assert.True(t, v.Bound, name)
		assert.Nil(t, v.HookStatus, name)
	}

	// 未绑定（读不到的仓库，只有 bindStatus）：没有 hookStatus
	setHook("active")
	r = e.do("PATCH", "/api/v1/projects/"+p.ID, dev, map[string]any{"repoUrl": "https://github.com/acme/other"})
	require.Equal(t, 200, r.status, string(r.body))
	p = project{}
	r.json(t, &p)
	require.NotNil(t, p.Github)
	assert.False(t, p.Github.Bound)
	assert.Nil(t, p.Github.HookStatus)
	v := list(p.ID)
	require.NotNil(t, v)
	assert.Nil(t, v.HookStatus)
}
