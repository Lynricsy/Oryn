package server

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M24 步骤 2.9：项目仓库镜像的端到端验收。
//
// 复用 M15 的环境（真实 orynd + 进程内假 GitHub，acme/widgets 已绑定且仓库 hook 为 active）：镜像首次读取时克隆，
// 读接口（commits / tree / blob）走真实 git；假 GitHub 上新增提交后投递 push 事件触发拉取；拉取请求带用户身份的
// OAuth 令牌（Basic x-access-token）；删除项目连镜像目录一起删。

// requireGitE2E 在找不到 git 时跳过；ORYN_REQUIRE_GIT=1（CI）时改为失败。
func requireGitE2E(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		if os.Getenv("ORYN_REQUIRE_GIT") == "1" {
			t.Fatal("ORYN_REQUIRE_GIT=1，但找不到 git")
		}
		t.Skip("找不到 git")
	}
}

type repoCommitsResp struct {
	Commits []struct {
		Sha     string `json:"sha"`
		Subject string `json:"subject"`
	} `json:"commits"`
}

func TestE2ERepo(t *testing.T) {
	requireGitE2E(t)
	e := newM15Env(t)
	steps := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{"配置OAuthApp", e.stepOAuthApp},
		{"连接两个身份", e.stepIdentities},
		{"项目绑定", e.stepProject},
		{"镜像就绪与读接口", e.stepRepoReady},
		{"push事件触发拉取", e.stepRepoPush},
		{"删除项目连镜像", e.stepRepoDelete},
	}
	for _, st := range steps {
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

// 镜像在首次读状态时后台克隆，30 秒内就绪；提交列表、目录与文件内容来自镜像，ETag 相同时回 304。
func (e *m15Env) stepRepoReady(t *testing.T) {
	var st struct {
		Kind          string `json:"kind"`
		State         string `json:"state"`
		Available     bool   `json:"available"`
		DefaultBranch string `json:"defaultBranch"`
		LastError     *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"lastError"`
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		e.rest("GET", "/api/v1/projects/"+e.prj+"/repo", nil, &st, 200)
		if st.State == "ready" {
			break
		}
		require.NotEqual(t, "error", st.State, "克隆失败：%+v", st.LastError)
		require.True(t, time.Now().Before(deadline), "镜像 30 秒内没有就绪：%+v", st)
		time.Sleep(200 * time.Millisecond)
	}
	assert.Equal(t, "mirror", st.Kind)
	assert.True(t, st.Available)
	assert.Equal(t, "main", st.DefaultBranch)

	var cs repoCommitsResp
	e.rest("GET", "/api/v1/projects/"+e.prj+"/repo/commits", nil, &cs, 200)
	require.NotEmpty(t, cs.Commits)
	assert.Equal(t, "initial commit", cs.Commits[0].Subject)

	var tree struct {
		Entries []struct {
			Name       string `json:"name"`
			Type       string `json:"type"`
			LastCommit *struct {
				Subject string `json:"subject"`
			} `json:"lastCommit"`
		} `json:"entries"`
	}
	e.rest("GET", "/api/v1/projects/"+e.prj+"/repo/tree", nil, &tree, 200)
	require.Len(t, tree.Entries, 1)
	assert.Equal(t, "README.md", tree.Entries[0].Name)
	assert.Equal(t, "file", tree.Entries[0].Type)
	require.NotNil(t, tree.Entries[0].LastCommit)
	assert.Equal(t, "initial commit", tree.Entries[0].LastCommit.Subject)

	code, body, etag := e.repoBlob(t, "README.md", "")
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, "# "+m15Repo+"\n", body)
	require.NotEmpty(t, etag)
	code, _, _ = e.repoBlob(t, "README.md", etag)
	assert.Equal(t, http.StatusNotModified, code, "If-None-Match 相同时 304")
}

// 假 GitHub 上（不经 Oryn）新增提交并投递 push 事件 → 镜像拉取，提交列表出现新提交；拉取都带用户身份的令牌。
func (e *m15Env) stepRepoPush(t *testing.T) {
	sha := e.repoCommitTo(t, "second", "NOTES.md", "second\n")
	guid := e.deliver(t, "push", map[string]any{"ref": "refs/heads/main", "after": sha, "repository": map[string]any{"id": m15RepoID}})
	assert.Equal(t, "handled", e.outcome(t, guid))

	deadline := time.Now().Add(30 * time.Second)
	for {
		var cs repoCommitsResp
		e.rest("GET", "/api/v1/projects/"+e.prj+"/repo/commits", nil, &cs, 200)
		if len(cs.Commits) > 0 && cs.Commits[0].Subject == "second" {
			assert.Equal(t, sha, cs.Commits[0].Sha)
			break
		}
		require.True(t, time.Now().Before(deadline), "push 之后 30 秒内镜像没有拉到新提交：%+v", cs)
		time.Sleep(200 * time.Millisecond)
	}

	calls := e.fake.CallsTo("GET", "/"+m15Repo+".git/info/refs")
	require.GreaterOrEqual(t, len(calls), 2, "克隆与拉取各一次")
	for _, c := range calls {
		enc, ok := strings.CutPrefix(c.Auth, "Basic ")
		require.True(t, ok, "拉取请求带 Basic 凭据：%q", c.Auth)
		raw, err := base64.StdEncoding.DecodeString(enc)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(string(raw), "x-access-token:"), "用户名是 x-access-token")
		assert.Greater(t, len(raw), len("x-access-token:"), "带令牌")
	}
}

// 删除项目后镜像目录不在。
func (e *m15Env) stepRepoDelete(t *testing.T) {
	dir := filepath.Join(e.cfg.DataDir, "repos", e.prj+".git")
	_, err := os.Stat(dir)
	require.NoError(t, err, "删除前镜像目录存在")
	e.rest("DELETE", "/api/v1/projects/"+e.prj, nil, nil, 204)
	_, err = os.Stat(dir)
	assert.True(t, os.IsNotExist(err), "删除项目后镜像目录仍在：%v", err)
}

// repoBlob 读镜像里的文件；etag 非空时带 If-None-Match。返回状态码、正文与 ETag。
func (e *m15Env) repoBlob(t *testing.T, path, etag string) (int, string, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/projects/%s/repo/blob?path=%s", e.http.URL, e.prj, path), nil)
	req.Header.Set("Authorization", "Bearer "+e.token)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(b), resp.Header.Get("ETag")
}

// repoCommitTo 在宿主上给假 GitHub 裸仓库的 main 提交一个文件（不经 Oryn），返回新提交的 sha。
func (e *m15Env) repoCommitTo(t *testing.T, msg, file, content string) string {
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
	run("clone", "-q", e.fake.BarePath(m15Repo), ".")
	require.NoError(t, os.WriteFile(filepath.Join(work, file), []byte(content), 0o644))
	run("add", file)
	run("commit", "-q", "-m", msg)
	run("push", "-q", "origin", "HEAD:refs/heads/main")
	return run("rev-parse", "HEAD")
}
