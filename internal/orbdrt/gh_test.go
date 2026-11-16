package orbdrt

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
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
)

// setup.run 带 gh：写 gh 配置、脚本环境有 GH_CONFIG_DIR、不导出 GH_TOKEN；之后不带 gh 的 resume.run 删除目录。
func TestSetupGhConfig(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	home := rt.home
	script := "printf '%s' \"$GH_CONFIG_DIR\" > \"$HOME/ghdir.txt\"\nprintf '%s' \"${GH_TOKEN:-none}\" > \"$HOME/ghtoken.txt\"\n"
	params := apigen.LinkCtlSetupRunParams{
		Workdir:  "~/workspace",
		GitHosts: []string{},
		Scripts:  apigen.LinkCtlSetupScripts{Setup: &script},
		OmpHome:  []apigen.LinkCtlOmpHomeFile{},
		Env:      map[string]string{},
		Gh:       &apigen.LinkCtlSetupGh{Login: "octo"},
	}
	var res apigen.LinkCtlSetupRunResult
	require.NoError(t, cp.call(t, "setup.run", params, &res))
	require.True(t, res.Ok, "setup 失败：%v", res.SetupError)

	ghDir := filepath.Join(home, ".oryn", "gh")
	read := func(p string) string {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		return string(b)
	}
	assert.Equal(t, ghDir, read(filepath.Join(home, "ghdir.txt")))
	assert.Equal(t, "none", read(filepath.Join(home, "ghtoken.txt")))
	assert.Contains(t, read(filepath.Join(ghDir, "config.yml")), "http_unix_socket: "+filepath.Join(home, ".oryn", "gh.sock")+"\n")
	hosts := read(filepath.Join(ghDir, "hosts.yml"))
	assert.Contains(t, hosts, "oauth_token: oryn-proxied\n")
	assert.Contains(t, hosts, "user: octo\n")
	assert.JSONEq(t, `{"login":"octo"}`, read(filepath.Join(ghDir, "identity.json")))
	fi, err := os.Stat(filepath.Join(ghDir, "hosts.yml"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	env := rt.baseEnv()
	assert.Equal(t, ghDir, env["GH_CONFIG_DIR"])
	_, hasToken := env["GH_TOKEN"]
	assert.False(t, hasToken)

	params.Gh = nil
	require.NoError(t, cp.call(t, "resume.run", params, &res))
	assert.NoDirExists(t, ghDir)
	_, has := rt.baseEnv()["GH_CONFIG_DIR"]
	assert.False(t, has)

	// 非法登录名（会注入 YAML）被拒绝。
	params.Gh = &apigen.LinkCtlSetupGh{Login: "octo\n    oauth_token: x"}
	require.Error(t, cp.call(t, "setup.run", params, &res))
}

// ghTestRuntime 起一个只有 gh 代理的最小 Runtime：egress 指向一个记录请求的假服务。
func ghTestRuntime(t *testing.T, egress http.Handler) (*Runtime, string) {
	t.Helper()
	fake := httptest.NewServer(egress)
	t.Cleanup(fake.Close)
	home, err := os.MkdirTemp("", "ghrt")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	r := &Runtime{home: home, log: slog.Default(), egressLn: fake.Listener}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.serveGh(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	sock := r.ghSockPath()
	require.Eventually(t, func() bool {
		c, err := net.Dial("unix", sock)
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	}, 5*time.Second, 20*time.Millisecond)
	return r, sock
}

// gh.sock 上发往 api.github.com 的请求转给 egress 的 /gh/api.github.com/…，原始编码的路径保留。
func TestGhProxyRoutesToEgress(t *testing.T) {
	var mu sync.Mutex
	var got []string
	_, sock := ghTestRuntime(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.EscapedPath())
		mu.Unlock()
		_, _ = w.Write([]byte(`{"login":"octo"}`))
	}))
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}}
	for _, u := range []string{"http://api.github.com/user", "http://api.github.com/repos/a/b/contents/x%2Fy", "http://uploads.github.com/repos/a/b/releases/1/assets"} {
		resp, err := client.Get(u)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"GET /gh/api.github.com/user", "GET /gh/api.github.com/repos/a/b/contents/x%2Fy", "GET /gh/uploads.github.com/repos/a/b/releases/1/assets"}, got)
}

// 真实 gh 读 hosts.yml 并经套接字发明文 HTTP（找不到 gh 时跳过）。
func TestGhCLIUsesSocket(t *testing.T) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Skip("gh 不在 PATH 中")
	}
	var auth string
	var mu sync.Mutex
	r, _ := ghTestRuntime(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		auth = req.Header.Get("Authorization")
		mu.Unlock()
		if req.URL.Path != "/gh/api.github.com/user" {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"login": "octo"})
	}))
	require.NoError(t, r.applyGh(&apigen.LinkCtlSetupGh{Login: "octo"}))
	cmd := exec.Command(gh, "api", "user", "--jq", ".login")
	cmd.Env = []string{"HOME=" + r.home, "GH_CONFIG_DIR=" + r.ghDir(), "PATH=" + os.Getenv("PATH"),
		"GH_NO_UPDATE_NOTIFIER=1", "GH_PROMPT_DISABLED=1", "NO_COLOR=1"}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Equal(t, "octo", strings.TrimSpace(string(out)))
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, auth, "oryn-proxied", "gh 带的是占位令牌，由 egress 替换")
}

// 仓库改名后 resume.run 把 origin 改到新地址（即使旧地址被 insteadOf 改写成 egress 代理）；旧端口的 insteadOf 被清掉；
// origin 是无关地址时不变。
func TestSetupFollowsRenamedOrigin(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	work := filepath.Join(rt.home, "workspace", "repo")
	require.NoError(t, os.MkdirAll(work, 0o755))
	gitRun(t, work, "init", "-q")
	gitRun(t, work, "remote", "add", "origin", "https://github.com/acme/widgets.git")
	gitconfig := filepath.Join(rt.home, ".gitconfig")
	stale := "url.http://127.0.0.1:1/git/github.com/acme/widgets.insteadof"
	out, err := exec.Command("git", "config", "--file", gitconfig, stale, "https://github.com/acme/widgets").CombinedOutput()
	require.NoError(t, err, string(out))
	params := apigen.LinkCtlSetupRunParams{
		Workdir:  "~/workspace/repo",
		Repo:     &apigen.LinkCtlSetupRepo{URL: "https://github.com/acme/gadgets", PreviousUrls: []string{"https://github.com/Acme/Widgets"}},
		GitHosts: []string{},
		OmpHome:  []apigen.LinkCtlOmpHomeFile{},
		Env:      map[string]string{},
	}
	var res apigen.LinkCtlSetupRunResult
	require.NoError(t, cp.call(t, "resume.run", params, &res))
	assert.Equal(t, "https://github.com/acme/gadgets", strings.TrimSpace(gitOut(t, work, "config", "--get", "remote.origin.url")))
	_, err = exec.Command("git", "config", "--file", gitconfig, "--get", stale).Output()
	assert.Error(t, err, "旧端口的 insteadOf 已清掉")

	gitRun(t, work, "remote", "set-url", "origin", "https://example.com/other.git")
	require.NoError(t, cp.call(t, "resume.run", params, &res))
	assert.Equal(t, "https://example.com/other.git", strings.TrimSpace(gitOut(t, work, "remote", "get-url", "origin")))
}
