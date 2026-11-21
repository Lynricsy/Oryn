package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// pushEnv 准备 --push 的本机仓库（main 上一个提交）、假 API 与假 git 主机上的空裸仓库。
func pushEnv(t *testing.T) (dir string, f *fakeAPI, credURL, cloneURL, bare string) {
	t.Helper()
	requireGit(t)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir = gitRepo(t, map[string]string{"a.txt": "hello\n"})
	fake, cloneURL := fakeGitHost(t, "demo", false)
	f = newFakeAPI(t)
	return dir, f, fake.URL(), cloneURL, fake.BarePath("acme/demo")
}

func runProjectPush(t *testing.T, f *fakeAPI, credURL, dir, remote string, remoteGiven bool) (apigen.Project, string, error) {
	t.Helper()
	var stderr bytes.Buffer
	p, err := projectPush(context.Background(), f.client(), credURL, dir, apigen.ProjectCreate{Name: "demo", ShallowClone: new(false)},
		remote, remoteGiven, &bytes.Buffer{}, &stderr)
	return p, stderr.String(), err
}

func postCount(f *fakeAPI) int {
	n := 0
	for _, r := range f.requests() {
		if r.Method == http.MethodPost {
			n++
		}
	}
	return n
}

func remoteURL(dir, name string) (string, bool) {
	out, err := exec.Command("git", "-C", dir, "config", "--get", "remote."+name+".url").Output()
	return string(bytes.TrimSpace(out)), err == nil
}

func TestProjectPushSuccess(t *testing.T) {
	dir, f, credURL, cloneURL, bare := pushEnv(t)
	f.json("POST /api/v1/projects", 201, hostedProject("demo", cloneURL))
	gitT(t, dir, "remote", "add", "origin", "https://github.com/acme/elsewhere")

	// 已有 origin、没给 --remote：报错并提示 --remote，不建项目
	_, _, err := runProjectPush(t, f, credURL, dir, "origin", false)
	require.ErrorContains(t, err, "--remote")
	assert.Zero(t, postCount(f))

	// 显式给了已存在的远端名
	_, _, err = runProjectPush(t, f, credURL, dir, "origin", true)
	require.ErrorContains(t, err, "远端 origin 已存在")
	assert.Zero(t, postCount(f))

	p, _, err := runProjectPush(t, f, credURL, dir, "oryn", true)
	require.NoError(t, err)
	assert.Equal(t, "demo", p.Name)
	assert.Equal(t, gitT(t, dir, "rev-parse", "HEAD"), gitT(t, bare, "rev-parse", "refs/heads/main"))

	reqs := f.requests()
	require.Len(t, reqs, 1)
	var body map[string]any
	require.NoError(t, json.Unmarshal(reqs[0].Body, &body))
	assert.Equal(t, true, body["hosted"])
	assert.Equal(t, true, body["emptyRepo"])
	assert.Equal(t, "main", body["baseBranch"])
	assert.NotContains(t, body, "repoUrl")

	u, ok := remoteURL(dir, "oryn")
	require.True(t, ok)
	assert.Equal(t, cloneURL, u)
	origin, _ := credentialOrigin(cloneURL)
	out, err := exec.Command("git", "-C", dir, "config", "--local", "--get-all", "credential."+origin+".helper").Output()
	require.NoError(t, err)
	assert.Equal(t, "\n"+orynHelper()+"\n", string(out))
	assert.Equal(t, "oryn/main", gitT(t, dir, "rev-parse", "--abbrev-ref", "main@{upstream}"))
}

func TestProjectPushLocalChecks(t *testing.T) {
	dir, f, credURL, cloneURL, _ := pushEnv(t)
	f.json("POST /api/v1/projects", 201, hostedProject("demo", cloneURL))

	_, _, err := runProjectPush(t, f, credURL, t.TempDir(), "origin", false)
	require.ErrorContains(t, err, "不是 git 工作区")

	empty := t.TempDir()
	gitT(t, empty, "init", "-q", "-b", "main")
	_, _, err = runProjectPush(t, f, credURL, empty, "origin", false)
	require.ErrorContains(t, err, "还没有提交")

	gitT(t, dir, "checkout", "-q", "--detach")
	_, _, err = runProjectPush(t, f, credURL, dir, "origin", false)
	require.ErrorContains(t, err, "分离状态")

	_, _, err = runProjectPush(t, f, credURL, dir, "-x", true)
	require.ErrorContains(t, err, "远端名")

	assert.Zero(t, postCount(f), "本机检查没过就不建项目")
}

func TestProjectPushServerSide(t *testing.T) {
	t.Run("同名项目 409", func(t *testing.T) {
		dir, f, credURL, _, _ := pushEnv(t)
		f.json("POST /api/v1/projects", 409, map[string]string{"code": "conflict", "message": "已有同名项目"})
		_, _, err := runProjectPush(t, f, credURL, dir, "origin", false)
		require.ErrorContains(t, err, "已有同名项目 (409 conflict)")
		_, ok := remoteURL(dir, "origin")
		assert.False(t, ok, "本机没有新远端")
	})
	t.Run("克隆地址不是 http(s)", func(t *testing.T) {
		dir, f, credURL, _, _ := pushEnv(t)
		f.json("POST /api/v1/projects", 201, hostedProject("demo", "ext::sh -c id"))
		_, stderr, err := runProjectPush(t, f, credURL, dir, "origin", false)
		require.ErrorContains(t, err, "不是 http(s)")
		assert.Contains(t, stderr, "已建好")
		_, ok := remoteURL(dir, "origin")
		assert.False(t, ok, "不安全的地址绝不交给 git")
	})
	t.Run("克隆地址与登录服务器不同", func(t *testing.T) {
		dir, f, _, cloneURL, _ := pushEnv(t)
		f.json("POST /api/v1/projects", 201, hostedProject("demo", cloneURL))
		_, _, err := runProjectPush(t, f, "https://oryn.example.com", dir, "origin", false)
		require.ErrorContains(t, err, "与登录的服务器")
		_, ok := remoteURL(dir, "origin")
		assert.False(t, ok)
	})
	t.Run("推送失败保留项目与远端", func(t *testing.T) {
		dir, f, credURL, cloneURL, _ := pushEnv(t)
		missing := cloneURL[:len(cloneURL)-len("demo.git")] + "nope.git"
		f.json("POST /api/v1/projects", 201, hostedProject("demo", missing))
		p, _, err := runProjectPush(t, f, credURL, dir, "origin", false)
		require.ErrorContains(t, err, "已建好")
		require.ErrorContains(t, err, "git push -u origin main")
		var ec *exitCodeError
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, 1, ec.code)
		assert.Equal(t, "demo", p.Name)
		u, ok := remoteURL(dir, "origin")
		require.True(t, ok)
		assert.Equal(t, missing, u)
	})
}

// requireGitLFS：找不到 git-lfs 时，ORYN_REQUIRE_GIT_LFS=1 则失败，否则跳过。
func requireGitLFS(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git-lfs"); err != nil {
		if os.Getenv("ORYN_REQUIRE_GIT_LFS") == "1" {
			t.Fatalf("git-lfs 不可用：%v", err)
		}
		t.Skip("git-lfs 不可用")
	}
}

func TestProjectPushLFS(t *testing.T) {
	t.Run("上传 LFS 对象先于推送", func(t *testing.T) {
		requireGitLFS(t)
		dir, f, _, _, _ := pushEnv(t)
		fake, cloneURL := fakeGitHost(t, "lfs", false)
		f.json("POST /api/v1/projects", 201, hostedProject("demo", cloneURL))
		gitT(t, dir, "lfs", "install", "--local")
		gitT(t, dir, "lfs", "track", "*.bin")
		// 钩子目录指向空目录（core.hooksPath 的仓库）：没有 pre-push，对象只能靠显式的 git lfs push 上传
		gitT(t, dir, "config", "core.hooksPath", t.TempDir())
		content := "lfs payload\n"
		writeFiles(t, dir, map[string]string{"a.bin": content})
		gitT(t, dir, "add", "-A")
		gitT(t, dir, "commit", "-q", "-m", "lfs")
		sum := sha256.Sum256([]byte(content))
		oid := hex.EncodeToString(sum[:])

		_, _, err := runProjectPush(t, f, fake.URL(), dir, "origin", false)
		require.NoError(t, err)
		got, ok := fake.LFSObject(oid)
		require.True(t, ok, "假上游存下了 LFS 对象")
		assert.Equal(t, content, string(got))

		upload, receive := -1, -1
		fake.Lock()
		for i, c := range fake.Calls {
			switch {
			case c.Method == http.MethodPut && c.Path == "/_lfs/objects/"+oid && upload < 0:
				upload = i
			case c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/git-receive-pack") && receive < 0:
				receive = i
			}
		}
		fake.Unlock()
		require.GreaterOrEqual(t, upload, 0)
		require.GreaterOrEqual(t, receive, 0)
		assert.Less(t, upload, receive, "LFS 对象先于 git-receive-pack 上传")
	})
	t.Run("本机没有 git-lfs", func(t *testing.T) {
		dir, f, credURL, cloneURL, _ := pushEnv(t)
		f.json("POST /api/v1/projects", 201, hostedProject("demo", cloneURL))
		// 不装 filter：直接提交 .gitattributes，attr pathspec 只看属性
		writeFiles(t, dir, map[string]string{".gitattributes": "*.bin filter=lfs diff=lfs merge=lfs -text\n", "a.bin": "x\n"})
		gitT(t, dir, "add", "-A")
		gitT(t, dir, "commit", "-q", "-m", "lfs")
		git, err := exec.LookPath("git")
		require.NoError(t, err)
		bin := t.TempDir()
		require.NoError(t, os.Symlink(git, filepath.Join(bin, "git")))
		t.Setenv("PATH", bin)

		_, _, err = runProjectPush(t, f, credURL, dir, "origin", false)
		require.ErrorContains(t, err, "本机没有 git-lfs")
		assert.Zero(t, postCount(f))
	})
	t.Run("没有 LFS 文件不调用 git lfs", func(t *testing.T) {
		dir, f, credURL, cloneURL, _ := pushEnv(t)
		f.json("POST /api/v1/projects", 201, hostedProject("demo", cloneURL))
		uses, err := repoUsesLFS(context.Background(), dir)
		require.NoError(t, err)
		assert.False(t, uses)
		// PATH 里只有 git：调用 git lfs 必然失败，推送成功即说明没有调用
		git, err := exec.LookPath("git")
		require.NoError(t, err)
		bin := t.TempDir()
		require.NoError(t, os.Symlink(git, filepath.Join(bin, "git")))
		t.Setenv("PATH", bin)
		_, _, err = runProjectPush(t, f, credURL, dir, "origin", false)
		require.NoError(t, err)
	})
}

func TestProjectCreateHostedFlags(t *testing.T) {
	saved := projectCreateOpts
	t.Cleanup(func() { projectCreateOpts = saved })
	for _, tc := range []struct {
		hosted, push         bool
		repo, branch, remote string
		want                 string
	}{
		{hosted: true, repo: "https://x/a/b", want: "--hosted / --push 与 --repo 只能二选一"},
		{push: true, repo: "https://x/a/b", want: "--hosted / --push 与 --repo 只能二选一"},
		{push: true, branch: "dev", want: "--push 以当前分支作基线"},
		{remote: "oryn", want: "--remote 只能与 --push 同用"},
	} {
		projectCreateOpts = saved
		projectCreateOpts.hosted, projectCreateOpts.push = tc.hosted, tc.push
		projectCreateOpts.repo, projectCreateOpts.branch, projectCreateOpts.remote = tc.repo, tc.branch, tc.remote
		err := projectCreateCmd.RunE(projectCreateCmd, []string{"x"})
		require.ErrorContains(t, err, tc.want)
		var ec *exitCodeError
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, 2, ec.code)
	}
}
