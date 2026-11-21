package api_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/repos"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// bareGit 在裸仓库 dir 上执行宿主 git，返回去掉首尾空白的输出。
func bareGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--git-dir", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func hostedDir(e *h2Env, id string) string { return filepath.Join(e.dataDir, "repos", id+".git") }

func deref[T any](p *T) T {
	var zero T
	if p != nil {
		zero = *p
	}
	return zero
}

func postProject(t *testing.T, e *h2Env, cred h2Cred, body map[string]any) (h2Resp, apigen.Project) {
	t.Helper()
	r := e.do("POST", "/api/v1/projects", cred, body)
	var p apigen.Project
	if r.status == 201 {
		r.json(t, &p)
	}
	return r, p
}

// 新建托管项目：带初始提交、基线缺省 main、cloneUrl；作者回落到 Oryn <oryn@<public_url 主机>>，设了全局身份时用它。
func TestProjectHostedCreate(t *testing.T) {
	e := newRepoEnv(t)
	dev := h2DeviceToken(t, e)

	r, p := postProject(t, e, dev, map[string]any{"name": "h", "hosted": true})
	require.Equal(t, 201, r.status, string(r.body))
	assert.True(t, deref(p.Hosted))
	assert.Equal(t, "main", deref(p.BaseBranch))
	assert.Equal(t, "https://oryn.test/git/"+p.ID+".git", deref(p.CloneURL))
	assert.Nil(t, p.RepoURL)
	dir := hostedDir(e, p.ID)
	assert.Equal(t, "1", bareGit(t, dir, "rev-list", "--count", "main"))
	assert.Equal(t, "Oryn <oryn@oryn.test>|Initial commit", bareGit(t, dir, "log", "--format=%an <%ae>|%s"))

	// 列表里同样带 hosted 与 cloneUrl；非托管项目不带
	plain := createRepoProject(t, e, dev, "plain", "")
	var list apigen.ProjectList
	e.do("GET", "/api/v1/projects", dev, nil).json(t, &list)
	for _, x := range list.Projects {
		switch x.ID {
		case p.ID:
			assert.Equal(t, "https://oryn.test/git/"+p.ID+".git", deref(x.CloneURL))
		case plain:
			assert.Nil(t, x.Hosted)
			assert.Nil(t, x.CloneURL)
		}
	}

	// 全局 git 身份（要先有所有者）
	require.NoError(t, e.st.Q.CreateOwner(context.Background(), db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))
	r = e.do("PUT", "/api/v1/settings/git-identity", dev, map[string]any{"name": "Gal", "email": "gal@example.com"})
	require.Equal(t, 200, r.status, string(r.body))
	r, p2 := postProject(t, e, dev, map[string]any{"name": "h-id", "hosted": true})
	require.Equal(t, 201, r.status, string(r.body))
	assert.Equal(t, "Gal <gal@example.com>", bareGit(t, hostedDir(e, p2.ID), "log", "--format=%an <%ae>"))

	// emptyRepo：没有 ref，HEAD → 指定的基线
	r, p3 := postProject(t, e, dev, map[string]any{"name": "h-empty", "hosted": true, "emptyRepo": true, "baseBranch": "trunk"})
	require.Equal(t, 201, r.status, string(r.body))
	assert.Empty(t, bareGit(t, hostedDir(e, p3.ID), "for-each-ref"))
	assert.Equal(t, "refs/heads/trunk", bareGit(t, hostedDir(e, p3.ID), "symbolic-ref", "HEAD"))

	// 校验
	for name, body := range map[string]map[string]any{
		"hosted+repoUrl":     {"name": "x1", "hosted": true, "repoUrl": "https://x.example/a/b"},
		"emptyRepo 无 hosted": {"name": "x2", "emptyRepo": true},
		"基线不合法":              {"name": "x3", "hosted": true, "baseBranch": "-x"},
	} {
		r, _ := postProject(t, e, dev, body)
		assert.Equal(t, 400, r.status, "%s: %s", name, r.body)
		assert.Equal(t, "invalid_request", r.code(t), name)
	}
}

// 托管项目的修改规则、仓库状态、同步与删除。
func TestProjectHostedPatchAndRepo(t *testing.T) {
	e := newRepoEnv(t)
	dev := h2DeviceToken(t, e)
	r, p := postProject(t, e, dev, map[string]any{"name": "h", "hosted": true})
	require.Equal(t, 201, r.status, string(r.body))
	base := "/api/v1/projects/" + p.ID
	dir := hostedDir(e, p.ID)

	r = e.do("PATCH", base, dev, map[string]any{"repoUrl": "https://x.example/a/b"})
	assert.Equal(t, 409, r.status, string(r.body))
	assert.Equal(t, "repo_hosted", r.code(t))
	r = e.do("PATCH", base, dev, map[string]any{"repoUrl": "", "description": "d"})
	assert.Equal(t, 200, r.status, string(r.body))
	r = e.do("PATCH", base, dev, map[string]any{"baseBranch": ""})
	assert.Equal(t, 400, r.status, string(r.body))
	r = e.do("PATCH", base, dev, map[string]any{"baseBranch": "a..b"})
	assert.Equal(t, 400, r.status, string(r.body))
	r = e.do("PATCH", base, dev, map[string]any{"baseBranch": "dev"})
	require.Equal(t, 200, r.status, string(r.body))
	assert.Equal(t, "refs/heads/dev", bareGit(t, dir, "symbolic-ref", "HEAD"))

	var st apigen.ProjectRepoStatus
	r = e.do("GET", base+"/repo", dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &st)
	assert.Equal(t, repos.KindHosted, st.Kind)
	assert.Equal(t, repos.StateReady, st.State)
	assert.Equal(t, "dev", deref(st.BaseBranch))
	assert.Equal(t, "dev", deref(st.DefaultBranch))
	assert.Equal(t, "https://oryn.test/git/"+p.ID+".git", deref(st.CloneURL))

	r = e.do("POST", base+"/repo/sync", dev, nil)
	assert.Equal(t, 409, r.status, string(r.body))
	assert.Equal(t, "repo_hosted", r.code(t))

	r = e.do("DELETE", base, dev, nil)
	require.Equal(t, 204, r.status, string(r.body))
	assert.NoDirExists(t, dir)
}

// 为没有仓库的项目创建托管仓库：重复、外部仓库、归档都回 409；带标记的现成正本被认领而不是重建。
func TestProjectHostedRepoEndpoint(t *testing.T) {
	e := newRepoEnv(t)
	dev := h2DeviceToken(t, e)

	id := createRepoProject(t, e, dev, "bare", "")
	r := e.do("POST", "/api/v1/projects/"+id+"/repo/hosted", dev, nil)
	require.Equal(t, 201, r.status, string(r.body))
	var p apigen.Project
	r.json(t, &p)
	assert.True(t, deref(p.Hosted))
	assert.Equal(t, "main", deref(p.BaseBranch))
	assert.Equal(t, "1", bareGit(t, hostedDir(e, id), "rev-list", "--count", "main"))
	r = e.do("POST", "/api/v1/projects/"+id+"/repo/hosted", dev, nil)
	assert.Equal(t, 409, r.status, string(r.body))
	assert.Equal(t, "repo_exists", r.code(t))

	ext := createRepoProject(t, e, dev, "ext", "https://x.example/a/b")
	r = e.do("POST", "/api/v1/projects/"+ext+"/repo/hosted", dev, nil)
	assert.Equal(t, 409, r.status, string(r.body))
	assert.Equal(t, "repo_exists", r.code(t))

	arch := createRepoProject(t, e, dev, "arch", "")
	require.Equal(t, 200, e.do("PATCH", "/api/v1/projects/"+arch, dev, map[string]any{"archived": true}).status)
	r = e.do("POST", "/api/v1/projects/"+arch+"/repo/hosted", dev, nil)
	assert.Equal(t, 409, r.status, string(r.body))
	assert.Equal(t, "project_archived", r.code(t))

	r = e.do("POST", "/api/v1/projects/prj_01JCZ00000000000000000NONE/repo/hosted", dev, nil)
	assert.Equal(t, 404, r.status, string(r.body))

	// 认领：目标位置已有带 oryn-hosted 标记、含 2 个提交的正本
	adopt := createRepoProject(t, e, dev, "adopt", "")
	dir := hostedDir(e, adopt)
	seedBare(t, dir, 2)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "oryn-hosted"), []byte(adopt+"\n"), 0o600))
	r = e.do("POST", "/api/v1/projects/"+adopt+"/repo/hosted", dev, nil)
	require.Equal(t, 201, r.status, string(r.body))
	assert.Equal(t, "2", bareGit(t, dir, "rev-list", "--count", "main"))
}

// seedBare 在 dir 建一个裸仓库，main 上有 n 个提交。
func seedBare(t *testing.T, dir string, n int) {
	t.Helper()
	work := t.TempDir()
	git := func(cwd string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = cwd
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+work,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	git(work, "init", "-q", "--bare", "--initial-branch=main", dir)
	git(work, "init", "-q", "--initial-branch=main", "w")
	w := filepath.Join(work, "w")
	for i := range n {
		require.NoError(t, os.WriteFile(filepath.Join(w, "f.txt"), []byte(strings.Repeat("x", i+1)), 0o644))
		git(w, "add", "-A")
		git(w, "commit", "-q", "-m", "c")
	}
	git(w, "push", "-q", dir, "main")
}

// 建仓失败时撤掉项目行与目录：不出现「有行没仓库」的托管项目。
func TestProjectHostedCreateCompensates(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "git")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\n[ \"$1\" = version ] && { echo 'git version 2.45.0'; exit 0; }\nexit 1\n"), 0o755))
	e, _ := newRepoEnvFake(t, func(c *repos.Config) { c.Git = fake })
	dev := h2DeviceToken(t, e)

	r, _ := postProject(t, e, dev, map[string]any{"name": "h2", "hosted": true})
	assert.GreaterOrEqual(t, r.status, 500, string(r.body))
	var list apigen.ProjectList
	e.do("GET", "/api/v1/projects", dev, nil).json(t, &list)
	for _, p := range list.Projects {
		assert.NotEqual(t, "h2", p.Name)
	}
	ents, _ := filepath.Glob(filepath.Join(e.dataDir, "repos", "prj_*"))
	assert.Empty(t, ents)

	// 无仓库项目上创建失败：托管标记撤回
	id := createRepoProject(t, e, dev, "bare", "")
	r = e.do("POST", "/api/v1/projects/"+id+"/repo/hosted", dev, nil)
	assert.GreaterOrEqual(t, r.status, 500, string(r.body))
	var p apigen.Project
	e.do("GET", "/api/v1/projects/"+id, dev, nil).json(t, &p)
	assert.Nil(t, p.Hosted)
}

// 改基线分支不在事务里碰 git：与并发的读请求都在 2 s 内返回（唯一的库连接不会被卡住）。
func TestProjectHostedBaseChangeDoesNotBlock(t *testing.T) {
	e := newRepoEnv(t)
	dev := h2DeviceToken(t, e)
	r, p := postProject(t, e, dev, map[string]any{"name": "h", "hosted": true})
	require.Equal(t, 201, r.status, string(r.body))

	done := make(chan int, 2)
	var wg sync.WaitGroup
	wg.Go(func() {
		done <- e.do("PATCH", "/api/v1/projects/"+p.ID, dev, map[string]any{"baseBranch": "dev"}).status
	})
	wg.Go(func() { done <- e.do("GET", "/api/v1/projects", dev, nil).status })
	ok := make(chan struct{})
	go func() { wg.Wait(); close(ok) }()
	select {
	case <-ok:
	case <-time.After(2 * time.Second):
		t.Fatal("改基线分支与并发读请求没有在 2 s 内返回")
	}
	close(done)
	for s := range done {
		assert.Equal(t, 200, s)
	}
	assert.Equal(t, "refs/heads/dev", bareGit(t, hostedDir(e, p.ID), "symbolic-ref", "HEAD"))
}

// 推送后 Webhook 的设置接口：只对托管项目、地址校验、密钥只报告有没有（省略 = 保留，空串 = 清除）、移除幂等。
func TestProjectWebhookAPI(t *testing.T) {
	e := newRepoEnv(t)
	dev := h2DeviceToken(t, e)
	_, p := postProject(t, e, dev, map[string]any{"name": "h", "hosted": true})
	plain := createRepoProject(t, e, dev, "plain", "")
	hookPath := "/api/v1/projects/" + p.ID + "/webhook"

	r := e.do("PUT", "/api/v1/projects/"+plain+"/webhook", dev, map[string]any{"url": "https://ci.example/h"})
	assert.Equal(t, 409, r.status, string(r.body))
	assert.Equal(t, "repo_not_hosted", r.code(t))
	r = e.do("PUT", "/api/v1/projects/prj_nope/webhook", dev, map[string]any{"url": "https://ci.example/h"})
	assert.Equal(t, 404, r.status)

	for _, bad := range []string{"ftp://x", "https://u:p@x", "https://x/" + strings.Repeat("a", 2049-len("https://x/")), "", "http:x"} {
		r := e.do("PUT", hookPath, dev, map[string]any{"url": bad})
		assert.Equal(t, 400, r.status, "%q: %s", bad, r.body)
		assert.Equal(t, "invalid_request", r.code(t), bad)
	}
	r = e.do("PUT", hookPath, dev, map[string]any{"url": "https://ci.example/h", "secret": strings.Repeat("s", 257)})
	assert.Equal(t, 400, r.status)

	webhookOf := func(r h2Resp) *apigen.ProjectWebhook {
		t.Helper()
		require.Equal(t, 200, r.status, string(r.body))
		var out apigen.Project
		r.json(t, &out)
		return out.Webhook
	}
	r = e.do("PUT", hookPath, dev, map[string]any{"url": "https://ci.example/h", "secret": "s3cret-value"})
	assert.NotContains(t, string(r.body), "s3cret-value")
	wh := webhookOf(r)
	require.NotNil(t, wh)
	assert.Equal(t, apigen.ProjectWebhook{URL: "https://ci.example/h", HasSecret: true}, *wh)
	row, err := e.st.Q.GetProjectWebhook(context.Background(), p.ID)
	require.NoError(t, err)
	assert.NotContains(t, string(row.SecretEnc), "s3cret-value")

	// 省略 secret 保留原密钥；单个项目与列表里都带 webhook
	assert.True(t, webhookOf(e.do("PUT", hookPath, dev, map[string]any{"url": "http://10.0.0.5:8080/h"})).HasSecret)
	got := webhookOf(e.do("GET", "/api/v1/projects/"+p.ID, dev, nil))
	assert.Equal(t, "http://10.0.0.5:8080/h", got.URL)
	var list apigen.ProjectList
	e.do("GET", "/api/v1/projects", dev, nil).json(t, &list)
	for _, x := range list.Projects {
		if x.ID == p.ID {
			require.NotNil(t, x.Webhook)
			assert.True(t, x.Webhook.HasSecret)
		} else {
			assert.Nil(t, x.Webhook)
		}
	}
	assert.NotContains(t, string(e.do("GET", "/api/v1/projects", dev, nil).body), "s3cret-value")

	// 最近投递的字段原样带出
	_, err = e.st.DB.Exec(`UPDATE project_webhooks SET last_delivery_at = 5, last_status = 500, last_error = 'HTTP 500' WHERE project_id = ?`, p.ID)
	require.NoError(t, err)
	got = webhookOf(e.do("GET", "/api/v1/projects/"+p.ID, dev, nil))
	assert.Equal(t, int64(5), deref(got.LastDeliveryAt))
	assert.Equal(t, 500, deref(got.LastStatus))
	assert.Equal(t, "HTTP 500", deref(got.LastError))

	// 空串清除密钥
	assert.False(t, webhookOf(e.do("PUT", hookPath, dev, map[string]any{"url": "https://ci.example/h", "secret": ""})).HasSecret)

	r = e.do("DELETE", hookPath, dev, nil)
	assert.Equal(t, 204, r.status, string(r.body))
	assert.Nil(t, webhookOf(e.do("GET", "/api/v1/projects/"+p.ID, dev, nil)))
	assert.Equal(t, 204, e.do("DELETE", hookPath, dev, nil).status)
	assert.Equal(t, 404, e.do("DELETE", "/api/v1/projects/prj_nope/webhook", dev, nil).status)
}
