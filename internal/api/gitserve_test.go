package api_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/lfs"
)

// requireGitLFS：找不到 git-lfs 时，ORYN_REQUIRE_GIT_LFS=1 则失败，否则跳过。
func requireGitLFS(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git-lfs"); err != nil {
		if os.Getenv("ORYN_REQUIRE_GIT_LFS") == "1" {
			t.Fatalf("找不到 git-lfs：%v", err)
		}
		t.Skip("找不到 git-lfs")
	}
}

// lfsOwnerGit 以主人本机的方式执行带 Git LFS 的 git：HOME 固定（git lfs install 写在里面），克隆地址
// https://oryn.test/ 经 insteadOf 指向测试服务器，git-lfs 对传输地址同样改写（lfs.transfer.enablehrefrewrite）。
type lfsOwnerGit struct {
	home, srvURL, tok string
}

func newLFSOwnerGit(t *testing.T, e *h2Env, tok string) *lfsOwnerGit {
	t.Helper()
	g := &lfsOwnerGit{home: t.TempDir(), srvURL: e.srv.URL, tok: tok}
	_, err := g.run(t.TempDir(), "lfs", "install", "--skip-repo")
	require.NoError(t, err)
	return g
}

func (g *lfsOwnerGit) run(dir string, args ...string) (string, error) {
	helper := "!f(){ echo username=x; echo password=" + g.tok + "; }; f"
	pre := []string{"-c", "credential.helper=", "-c", "credential.helper=" + helper,
		"-c", "url." + g.srvURL + "/.insteadOf=https://oryn.test/", "-c", "lfs.transfer.enablehrefrewrite=true"}
	cmd := exec.Command("git", append(pre, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+g.home, "XDG_CONFIG_HOME="+g.home,
		"GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@x")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (g *lfsOwnerGit) must(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := g.run(dir, args...)
	require.NoError(t, err, "git %v: %s", args, out)
	return out
}

// 托管仓库的 Git LFS（主人本机）：推送上传对象、新克隆取回真实内容、锁校验被静默关闭、
// 不带凭据的 batch 回 401 + LFS-Authenticate、归档后推送新 LFS 文件被拒。
func TestGitServeLFS(t *testing.T) {
	requireGitLFS(t)
	e := newRepoEnv(t)
	ctx := context.Background()
	_, tok, err := auth.CreateDevice(ctx, e.st, "laptop", "cli", "")
	require.NoError(t, err)
	cred := h2Cred{bearer: tok}
	r, p := postProject(t, e, cred, map[string]any{"name": "lfs", "hosted": true})
	require.Equal(t, 201, r.status, string(r.body))
	cloneURL := deref(p.CloneURL)
	require.True(t, strings.HasPrefix(cloneURL, "https://oryn.test/"), cloneURL)
	g := newLFSOwnerGit(t, e, tok)

	work := t.TempDir()
	g.must(t, work, "clone", "-q", cloneURL, "w")
	w := filepath.Join(work, "w")
	g.must(t, w, "lfs", "track", "*.bin")
	content := make([]byte, 1<<20)
	_, err = io.ReadFull(rand.Reader, content)
	require.NoError(t, err)
	sum := sha256.Sum256(content)
	oid := hex.EncodeToString(sum[:])
	require.NoError(t, os.WriteFile(filepath.Join(w, "a.bin"), content, 0o600))
	g.must(t, w, "add", ".gitattributes", "a.bin")
	g.must(t, w, "commit", "-q", "-m", "lfs file")
	out := g.must(t, w, "push", "origin", "main")
	assert.NotContains(t, out, "locking API", "锁接口 404 应被静默处理")
	stored, err := os.ReadFile(lfs.ObjectPath(filepath.Join(hostedDir(e, p.ID), "lfs", "objects"), oid))
	require.NoError(t, err, "托管库里应有该对象")
	assert.Equal(t, content, stored)
	assert.Contains(t, g.must(t, w, "config", "--local", "--get-regexp", `^lfs\..*\.locksverify$`), "false")

	// 新克隆：检出时取回真实内容
	g.must(t, work, "clone", "-q", cloneURL, "w2")
	got, err := os.ReadFile(filepath.Join(work, "w2", "a.bin"))
	require.NoError(t, err)
	assert.Equal(t, content, got)

	// 不带凭据的 batch → 401 + LFS-Authenticate，JSON 错误体
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/git/"+p.ID+".git/info/lfs/objects/batch",
		strings.NewReader(`{"operation":"download","objects":[]}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", lfs.MediaType)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, 401, resp.StatusCode)
	assert.Equal(t, `Basic realm="Oryn"`, resp.Header.Get("LFS-Authenticate"))
	assert.Equal(t, `Basic realm="Oryn"`, resp.Header.Get("WWW-Authenticate"))
	assert.Equal(t, lfs.MediaType, resp.Header.Get("Content-Type"))
	var msg struct{ Message string }
	require.NoError(t, json.Unmarshal(body, &msg))
	assert.Equal(t, "oryn: 需要设备令牌（oryn login 后用 oryn clone 克隆）", msg.Message)

	// 归档后推送新的 LFS 文件 → 失败
	r = e.do("PATCH", "/api/v1/projects/"+p.ID, cred, map[string]any{"archived": true})
	require.Equal(t, 200, r.status, string(r.body))
	require.NoError(t, os.WriteFile(filepath.Join(w, "b.bin"), []byte("after archive\n"), 0o600))
	g.must(t, w, "add", "b.bin")
	g.must(t, w, "commit", "-q", "-m", "after archive")
	out, err = g.run(w, "push", "origin", "main")
	require.Error(t, err, out)
	assert.Contains(t, out, "仓库只读")
}

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
