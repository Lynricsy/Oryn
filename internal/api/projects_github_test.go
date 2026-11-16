package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
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

// M15 8.2：PATCH 的 githubGates 校验、规整后存入 projects.github_gates；详情、列表、PATCH 响应都带出。省略或 null 不改；
// 什么都没配置时存 NULL、视图省略。未绑定 GitHub 的项目同样可以保存（绑定后生效）。
func TestProjectGitHubGates(t *testing.T) {
	e := newH2Env(t)
	dev := h2DeviceToken(t, e)
	ctx := context.Background()

	type project struct {
		ID          string
		GithubGates *apigen.GitHubGates
	}
	r := e.do("POST", "/api/v1/projects", dev, map[string]any{"name": "gated"})
	require.Equal(t, 201, r.status, string(r.body))
	var p project
	r.json(t, &p)
	assert.Nil(t, p.GithubGates, "新项目没有门禁")

	stored := func() *string {
		t.Helper()
		row, err := e.st.Q.GetProject(ctx, p.ID)
		require.NoError(t, err)
		return row.GithubGates
	}
	get := func() *apigen.GitHubGates {
		t.Helper()
		r := e.do("GET", "/api/v1/projects/"+p.ID, dev, nil)
		require.Equal(t, 200, r.status, string(r.body))
		var v project
		r.json(t, &v)
		return v.GithubGates
	}
	list := func() *apigen.GitHubGates {
		t.Helper()
		r := e.do("GET", "/api/v1/projects", dev, nil)
		require.Equal(t, 200, r.status, string(r.body))
		var l struct{ Projects []project }
		r.json(t, &l)
		for _, v := range l.Projects {
			if v.ID == p.ID {
				return v.GithubGates
			}
		}
		require.Failf(t, "项目不在列表里", "%s", p.ID)
		return nil
	}
	patch := func(body map[string]any) h2Resp {
		t.Helper()
		return e.do("PATCH", "/api/v1/projects/"+p.ID, dev, body)
	}

	r = patch(map[string]any{"githubGates": map[string]any{
		"push": true, "pr": true, "fixCmd": "  make fix  ", "checkCmd": " ", "testCmd": "make test",
		"prHeadings": []string{" Summary ", "Verification"}, "requireClosingRef": true,
	}})
	require.Equal(t, 200, r.status, string(r.body))
	fix, test, yes := "make fix", "make test", true
	want := &apigen.GitHubGates{Push: true, Pr: true, FixCmd: &fix, TestCmd: &test,
		PrHeadings: []string{"Summary", "Verification"}, RequireClosingRef: &yes}
	var v project
	r.json(t, &v)
	assert.Equal(t, want, v.GithubGates, "PATCH 响应")
	require.NotNil(t, stored())
	assert.JSONEq(t, `{"push":true,"pr":true,"fixCmd":"make fix","testCmd":"make test","prHeadings":["Summary","Verification"],"requireClosingRef":true}`, *stored())
	assert.Equal(t, want, get(), "详情")
	assert.Equal(t, want, list(), "列表")

	// 省略与 null 都不改
	r = patch(map[string]any{"name": "gated2"})
	require.Equal(t, 200, r.status, string(r.body))
	assert.Equal(t, want, get())
	r = patch(map[string]any{"githubGates": nil})
	require.Equal(t, 200, r.status, string(r.body))
	assert.Equal(t, want, get())

	// 非法输入 400 invalid_request，已存的门禁不变
	long := strings.Repeat("x", 2001)
	many := make([]string, 21)
	for i := range many {
		many[i] = "h"
	}
	for name, g := range map[string]any{
		"缺 pr":      map[string]any{"push": true},
		"缺 push":    map[string]any{"pr": true},
		"类型错":       map[string]any{"push": "yes", "pr": true},
		"不是对象":      "on",
		"命令过长":      map[string]any{"push": true, "pr": true, "testCmd": long},
		"标题过多":      map[string]any{"push": true, "pr": true, "prHeadings": many},
		"空标题":       map[string]any{"push": true, "pr": true, "prHeadings": []string{"  "}},
		"标题过长":      map[string]any{"push": true, "pr": true, "prHeadings": []string{strings.Repeat("标", 101)}},
		"多行标题":      map[string]any{"push": true, "pr": true, "prHeadings": []string{"a\nb"}},
		"标题项类型错":    map[string]any{"push": true, "pr": true, "prHeadings": []int{1}},
		"closing 错": map[string]any{"push": true, "pr": true, "requireClosingRef": "y"},
	} {
		r = patch(map[string]any{"githubGates": g, "name": "renamed"})
		require.Equal(t, 400, r.status, "%s: %s", name, r.body)
		assert.Equal(t, "invalid_request", r.code(t), name)
	}
	assert.Equal(t, want, get(), "非法输入不落库")
	row, err := e.st.Q.GetProject(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, "gated2", row.Name, "非法门禁整条 PATCH 都不生效")

	// 只关门禁、保留命令：照存
	r = patch(map[string]any{"githubGates": map[string]any{"push": false, "pr": false, "checkCmd": "make check"}})
	require.Equal(t, 200, r.status, string(r.body))
	check := "make check"
	assert.Equal(t, &apigen.GitHubGates{CheckCmd: &check}, get())

	// 什么都没配置：存 NULL，视图省略
	r = patch(map[string]any{"githubGates": map[string]any{"push": false, "pr": false, "prHeadings": []string{}, "requireClosingRef": false}})
	require.Equal(t, 200, r.status, string(r.body))
	v = project{}
	r.json(t, &v)
	assert.Nil(t, v.GithubGates)
	assert.Nil(t, stored())
	assert.Nil(t, get())
	assert.Nil(t, list())
}
