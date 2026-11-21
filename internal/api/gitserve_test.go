package api_test

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/auth"
)

// ownerGit 以主人本机的方式执行 git：凭据助手按 oryn clone 的写法先清空、再只应答设备令牌 tok。
func ownerGit(t *testing.T, dir, tok string, args ...string) (string, error) {
	t.Helper()
	helper := "!f(){ echo username=x; echo password=" + tok + "; }; f"
	cmd := exec.Command("git", append([]string{"-c", "credential.helper=", "-c", "credential.helper=" + helper}, args...)...)
	cmd.Dir = dir
	home := t.TempDir()
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+home, "XDG_CONFIG_HOME="+home,
		"GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@x")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustOwnerGit(t *testing.T, dir, tok string, args ...string) string {
	t.Helper()
	out, err := ownerGit(t, dir, tok, args...)
	require.NoError(t, err, "git %v: %s", args, out)
	return out
}

// gitGet 直接发一个 /git/ 请求；set 可加凭据与头。
func gitGet(t *testing.T, e *h2Env, path string, set func(*http.Request)) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	require.NoError(t, err)
	if set != nil {
		set(req)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp
}

// 主人入口 /git/<id>.git：设备令牌（Basic 密码位）克隆、推送；错令牌、吊销、只带 Cookie 都 401；
// 归档后只读；非托管、不存在、带转义的路径 404。
func TestGitServeOwner(t *testing.T) {
	e := newRepoEnv(t)
	ctx := context.Background()
	dev, tok, err := auth.CreateDevice(ctx, e.st, "laptop", "cli", "")
	require.NoError(t, err)
	cred := h2Cred{bearer: tok}

	r, p := postProject(t, e, cred, map[string]any{"name": "g", "hosted": true})
	require.Equal(t, 201, r.status, string(r.body))
	url := e.srv.URL + "/git/" + p.ID + ".git"
	refs := "/git/" + p.ID + ".git/info/refs?service=git-upload-pack"

	// 克隆、提交、推送
	work := t.TempDir()
	mustOwnerGit(t, work, tok, "clone", url, "w")
	w := filepath.Join(work, "w")
	assert.Equal(t, "Initial commit\n", mustOwnerGit(t, w, tok, "log", "--format=%s"))
	require.NoError(t, os.WriteFile(filepath.Join(w, "a.txt"), []byte("hi\n"), 0o600))
	mustOwnerGit(t, w, tok, "add", "a.txt")
	mustOwnerGit(t, w, tok, "commit", "-q", "-m", "hello hosted")
	mustOwnerGit(t, w, tok, "push", "-q", "origin", "main")
	assert.Equal(t, "2", bareGit(t, hostedDir(e, p.ID), "rev-list", "--count", "main"))

	// 错令牌：git 认证失败；第一次 401 带 WWW-Authenticate
	out, err := ownerGit(t, work, "odt_wrong", "clone", url, "bad")
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, 128, ee.ExitCode())
	assert.Contains(t, out, "Authentication failed")
	resp := gitGet(t, e, refs, nil)
	assert.Equal(t, 401, resp.StatusCode)
	assert.Equal(t, `Basic realm="Oryn"`, resp.Header.Get("WWW-Authenticate"))

	// 来自浏览器的匿名请求：401 但不回 WWW-Authenticate（不弹原生口令框）
	resp = gitGet(t, e, refs, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	assert.Equal(t, 401, resp.StatusCode)
	assert.Empty(t, resp.Header.Values("WWW-Authenticate"))

	// 只带会话 Cookie → 401（/git/ 从不读 Cookie）
	cookie := e.webSession()
	resp = gitGet(t, e, refs, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie}) })
	assert.Equal(t, 401, resp.StatusCode)

	// Bearer 同样可以
	resp = gitGet(t, e, refs, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) })
	assert.Equal(t, 200, resp.StatusCode)

	// 非托管项目、不存在的 id、带转义或多余段的路径 → 404（带有效令牌）
	plain := createRepoProject(t, e, cred, "plain", "")
	withTok := func(r *http.Request) { r.SetBasicAuth("x", tok) }
	for _, path := range []string{
		"/git/" + plain + ".git/info/refs?service=git-upload-pack",
		"/git/prj_01JCZ00000000000000000NONE.git/info/refs?service=git-upload-pack",
		"/git/prj_%2e%2e.git/info/refs?service=git-upload-pack",
		"/git/" + p.ID + ".git/objects/info/packs",
		"/git/" + p.ID + ".git/../" + p.ID + ".git/info/refs",
	} {
		assert.Equal(t, 404, gitGet(t, e, path, withTok).StatusCode, path)
	}

	// 归档后：克隆照常，推送被拒
	r = e.do("PATCH", "/api/v1/projects/"+p.ID, cred, map[string]any{"archived": true})
	require.Equal(t, 200, r.status, string(r.body))
	mustOwnerGit(t, work, tok, "clone", url, "again")
	require.NoError(t, os.WriteFile(filepath.Join(w, "b.txt"), []byte("b\n"), 0o600))
	mustOwnerGit(t, w, tok, "add", "b.txt")
	mustOwnerGit(t, w, tok, "commit", "-q", "-m", "after archive")
	out, err = ownerGit(t, w, tok, "push", "origin", "main")
	require.Error(t, err)
	assert.Contains(t, out, "项目已归档，仓库只读")
	assert.Equal(t, "2", bareGit(t, hostedDir(e, p.ID), "rev-list", "--count", "main"))

	// 吊销设备之后 → 401
	require.NoError(t, e.svc.RevokeDevice(ctx, dev.ID))
	assert.Equal(t, 401, gitGet(t, e, refs, withTok).StatusCode)
	_, err = ownerGit(t, work, tok, "clone", url, "revoked")
	assert.Error(t, err)
}
