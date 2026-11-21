package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/config"
)

// M24 步骤 3.9：Oryn 托管仓库的端到端验收。
//
// 完整 orynd（newStack）+ 真实 git：建托管项目 → 设推送后 Webhook（带签名密钥）→ 用设备令牌经 /git/ 克隆
// → 本机提交并推送 → 提交列表出现新提交 → 本地接收端恰好收到一次带签名的 POST、最近投递记为 200
// → 归档后推送被拒、克隆照常 → 删除项目连仓库目录一起删。

// hostedHook 是接收端收到的一次 Webhook 请求。
type hostedHook struct {
	header http.Header
	body   []byte
}

func TestE2EHosted(t *testing.T) {
	requireGitE2E(t)
	s := newStack(t, config.Idle{})

	hooks := make(chan hostedHook, 8)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hooks <- hostedHook{header: r.Header.Clone(), body: b}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(recv.Close)

	// 1. 建托管项目
	var p struct {
		ID         string `json:"id"`
		Hosted     bool   `json:"hosted"`
		CloneURL   string `json:"cloneUrl"`
		BaseBranch string `json:"baseBranch"`
		Webhook    *struct {
			URL        string `json:"url"`
			HasSecret  bool   `json:"hasSecret"`
			LastStatus *int   `json:"lastStatus"`
		} `json:"webhook"`
	}
	s.rest("POST", "/api/v1/projects", map[string]any{"name": "demo", "hosted": true}, &p, 201)
	require.True(t, p.Hosted)
	assert.Equal(t, "http://localhost/git/"+p.ID+".git", p.CloneURL)
	assert.Equal(t, "main", p.BaseBranch)

	// 2. 推送后 Webhook（带签名密钥）
	s.rest("PUT", "/api/v1/projects/"+p.ID+"/webhook", map[string]any{"url": recv.URL, "secret": "s3cret"}, &p, 200)
	require.NotNil(t, p.Webhook)
	assert.True(t, p.Webhook.HasSecret)

	// 3. 用设备令牌克隆（等价于 oryn clone 写下的凭据助手）
	home := t.TempDir()
	remote := s.http.URL + "/git/" + p.ID + ".git"
	git := func(dir string, args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-c", "credential.helper=",
			"-c", "credential.helper=!f(){ echo username=x; echo password=" + s.token + "; }; f"}, args...)...)
		cmd.Dir = dir
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C",
			"GIT_AUTHOR_NAME=owner", "GIT_AUTHOR_EMAIL=owner@example.com", "GIT_COMMITTER_NAME=owner", "GIT_COMMITTER_EMAIL=owner@example.com"}
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	mustGit := func(dir string, args ...string) string {
		t.Helper()
		out, err := git(dir, args...)
		require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
		return out
	}
	tmp := t.TempDir()
	w := filepath.Join(tmp, "w")
	mustGit(tmp, "clone", "-q", remote, w)
	assert.Equal(t, "Initial commit", mustGit(w, "log", "--format=%s"))
	initial := mustGit(w, "rev-parse", "HEAD")

	// 4. 本机提交并推送
	require.NoError(t, os.WriteFile(filepath.Join(w, "a.txt"), []byte("hello\n"), 0o644))
	mustGit(w, "add", "a.txt")
	mustGit(w, "commit", "-q", "-m", "hello hosted")
	head := mustGit(w, "rev-parse", "HEAD")
	mustGit(w, "push", "-q", "origin", "main")

	// 5. 提交列表来自托管正本
	var cs repoCommitsResp
	s.rest("GET", "/api/v1/projects/"+p.ID+"/repo/commits", nil, &cs, 200)
	require.Len(t, cs.Commits, 2)
	assert.Equal(t, "hello hosted", cs.Commits[0].Subject)
	assert.Equal(t, head, cs.Commits[0].Sha)
	assert.Equal(t, "Initial commit", cs.Commits[1].Subject)

	// 6. 接收端 5 秒内恰好收到一次带签名的 POST
	var got hostedHook
	select {
	case got = <-hooks:
	case <-time.After(5 * time.Second):
		t.Fatal("推送后 5 秒内没有收到 Webhook")
	}
	assert.Equal(t, "push", got.header.Get("X-Oryn-Event"))
	assert.Equal(t, "application/json", got.header.Get("Content-Type"))
	m := hmac.New(sha256.New, []byte("s3cret"))
	m.Write(got.body)
	assert.Equal(t, "sha256="+hex.EncodeToString(m.Sum(nil)), got.header.Get("X-Oryn-Signature-256"))
	var payload struct {
		Event   string `json:"event"`
		Project struct {
			ID       string `json:"id"`
			CloneURL string `json:"cloneUrl"`
		} `json:"project"`
		Pusher struct {
			Kind     string `json:"kind"`
			ThreadID string `json:"threadId"`
		} `json:"pusher"`
		Refs []struct {
			Ref    string `json:"ref"`
			OldSha string `json:"oldSha"`
			NewSha string `json:"newSha"`
		} `json:"refs"`
	}
	require.NoError(t, json.Unmarshal(got.body, &payload), "%s", got.body)
	assert.Equal(t, "push", payload.Event)
	assert.Equal(t, p.ID, payload.Project.ID)
	assert.Equal(t, p.CloneURL, payload.Project.CloneURL)
	assert.Equal(t, "owner", payload.Pusher.Kind)
	assert.Empty(t, payload.Pusher.ThreadID)
	require.Len(t, payload.Refs, 1)
	assert.Equal(t, "refs/heads/main", payload.Refs[0].Ref)
	assert.Equal(t, initial, payload.Refs[0].OldSha)
	assert.Equal(t, head, payload.Refs[0].NewSha)
	select {
	case extra := <-hooks:
		t.Fatalf("一次推送收到了第二次 Webhook：%s", extra.body)
	case <-time.After(300 * time.Millisecond):
	}

	// 7. 最近一次投递记为 200
	require.Eventually(t, func() bool {
		s.rest("GET", "/api/v1/projects/"+p.ID, nil, &p, 200)
		return p.Webhook != nil && p.Webhook.LastStatus != nil && *p.Webhook.LastStatus == 200
	}, 5*time.Second, 50*time.Millisecond, "webhook.lastStatus 应为 200")

	// 8. 归档后只读：推送被拒，克隆照常
	s.rest("PATCH", "/api/v1/projects/"+p.ID, map[string]any{"archived": true}, nil, 200)
	require.NoError(t, os.WriteFile(filepath.Join(w, "b.txt"), []byte("late\n"), 0o644))
	mustGit(w, "add", "b.txt")
	mustGit(w, "commit", "-q", "-m", "after archive")
	out, err := git(w, "push", "origin", "main")
	require.Error(t, err, "归档后推送应失败：%s", out)
	assert.Contains(t, out, "仓库只读")
	mustGit(tmp, "clone", "-q", remote, filepath.Join(tmp, "w2"))

	// 9. 删除项目连托管仓库一起删
	dir := filepath.Join(s.cfg.DataDir, "repos", p.ID+".git")
	_, err = os.Stat(dir)
	require.NoError(t, err, "删除前托管仓库目录存在")
	s.rest("DELETE", "/api/v1/projects/"+p.ID, nil, nil, 204)
	_, err = os.Stat(dir)
	assert.True(t, os.IsNotExist(err), "删除项目后托管仓库目录仍在：%v", err)
}
