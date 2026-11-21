package orbdrt

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/egress"
)

// 真实令牌：只在假控制面（egress.Handler 的 Resolver）与假上游里出现，orb 的 $HOME 里不能有。
const (
	ghRemoteGitToken = "gho_REAL-GIT-TOKEN-7f3a"
	ghRemoteAPIToken = "gho_REAL-API-TOKEN-91c2"
)

// ghRemoteResolver 是假控制面上 egress.Handler 的 Resolver：github.com 由 GitHub 集成开放（M15），门禁未配置。
type ghRemoteResolver struct {
	egress.NoGateLookup
	egress.NoMemory
	egress.NoHostedGit
	pol egress.Policy

	mu     sync.Mutex
	begins []egress.GitHubWrite
}

func (r *ghRemoteResolver) Policy() egress.Policy                      { return r.pol }
func (r *ghRemoteResolver) SecretByID(string) (string, []string, bool) { return "", nil, false }
func (r *ghRemoteResolver) GitToken(host string) (string, bool) {
	return ghRemoteGitToken, host == "github.com"
}
func (r *ghRemoteResolver) GitHubToken() (string, bool) { return ghRemoteAPIToken, true }
func (r *ghRemoteResolver) GitHubTokenRejected(string)  {}
func (r *ghRemoteResolver) ThreadGates() (egress.ThreadGates, error) {
	return egress.ThreadGates{}, nil
}
func (r *ghRemoteResolver) GitHubWriteBegin(w egress.GitHubWrite) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.begins = append(r.begins, w)
	return ""
}
func (r *ghRemoteResolver) GitHubWriteEnd(egress.GitHubWrite, int, []string) {}

// ghRemoteUpstream 是假的 github.com：`git http-backend` 提供 acme/widgets 与 fork me/widgets，只认 egress 注入的令牌。
func ghRemoteUpstream(t *testing.T, gitBin string) (url, root string) {
	t.Helper()
	root = t.TempDir()
	seed := t.TempDir()
	gitRun(t, seed, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(seed, "README.md"), []byte("widgets\n"), 0o644))
	gitRun(t, seed, "add", ".")
	gitRun(t, seed, "commit", "-q", "-m", "init")
	for _, repo := range []string{"acme/widgets", "me/widgets"} {
		dir := filepath.Join(root, repo)
		require.NoError(t, os.MkdirAll(filepath.Dir(dir), 0o755))
		gitRun(t, seed, "clone", "-q", "--bare", seed, dir)
		gitRun(t, dir, "config", "http.receivepack", "true")
		// 与 github.com 一样，带不带 `.git` 后缀都是同一个仓库。
		require.NoError(t, os.Symlink(filepath.Base(dir), dir+".git"))
	}
	backend := &cgi.Handler{
		Path: gitBin, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "HOME=" + root},
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+ghRemoteGitToken))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != want {
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, root
}

// ghRemoteAPI 是假的 api.github.com：只答 `gh repo view` 的 RepositoryInfo 查询，并记下 gh 查询的 owner/name。
func ghRemoteAPI(t *testing.T) (url string, queried func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string            `json:"query"`
			Variables map[string]string `json:"variables"`
		}
		if r.URL.Path != "/graphql" || r.Header.Get("Authorization") != "Bearer "+ghRemoteAPIToken ||
			json.NewDecoder(r.Body).Decode(&body) != nil || !strings.Contains(body.Query, "RepositoryInfo") {
			http.Error(w, `{"message":"unexpected request"}`, http.StatusNotFound)
			return
		}
		nwo := body.Variables["owner"] + "/" + body.Variables["name"]
		mu.Lock()
		got = append(got, nwo)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{"nameWithOwner": nwo}}})
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// R5：orb 内检出里不带 -R 的 gh 子命令从 git remote 推断仓库。真实的 git 与 gh 经 orbd 写的 git / gh 配置、
// orbd 的 egress 监听、link 的 egress 流到达假控制面上真实的 egress.Handler，再到假 github.com / api.github.com。
// 覆盖 origin、fork + upstream、按仓库的 insteadOf 与整主机的 insteadOf；clone / fetch / push 都走 egress；
// 真实令牌不进 orb。找不到 git 或 gh 时跳过。
func TestGhResolvesRepoFromRemotes(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 不在 PATH 中")
	}
	ghBin, err := exec.LookPath("gh")
	if err != nil {
		t.Skip("gh 不在 PATH 中")
	}
	if v, err := exec.Command(ghBin, "--version").Output(); err == nil {
		t.Logf("%s", bytes.SplitN(v, []byte("\n"), 2)[0])
	}
	if v, err := exec.Command(gitBin, "--version").Output(); err == nil {
		t.Logf("%s", strings.TrimSpace(string(v)))
	}

	// 对照：M15 之前 insteadOf 改写成 `http://127.0.0.1:<port>/git/github.com/…`，`git remote -v` 显示这个地址，
	// gh 不认这个主机（缺陷本身）。
	t.Run("legacy-form-fails", func(t *testing.T) {
		home := t.TempDir()
		r := &Runtime{home: home}
		require.NoError(t, r.applyGh(&apigen.LinkCtlSetupGh{Login: "octo"}))
		work := filepath.Join(home, "repo")
		require.NoError(t, os.MkdirAll(work, 0o755))
		gitRun(t, work, "init", "-q")
		gitRun(t, work, "remote", "add", "origin", "http://127.0.0.1:43215/git/github.com/acme/widgets")
		cmd := exec.Command(ghBin, "repo", "view", "--json", "nameWithOwner")
		cmd.Dir = work
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GH_CONFIG_DIR=" + r.ghDir(),
			"GIT_CONFIG_NOSYSTEM=1", "GH_NO_UPDATE_NOTIFIER=1", "GH_PROMPT_DISABLED=1", "GH_TELEMETRY=0", "NO_COLOR=1"}
		out, err := cmd.CombinedOutput()
		require.Error(t, err)
		assert.Contains(t, string(out), "none of the git remotes configured for this repository point to a known GitHub host")
	})

	cases := []struct {
		name     string
		gitHosts []string
		fork     bool // origin 是 me/widgets，upstream 是 acme/widgets
	}{
		{"project-origin", []string{"github.com/acme/widgets"}, false},
		{"project-fork-upstream", []string{"github.com/me/widgets", "github.com/acme/widgets"}, true},
		{"host-wide-origin", []string{"github.com"}, false},
		{"host-wide-fork-upstream", []string{"github.com"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			upURL, upRoot := ghRemoteUpstream(t, gitBin)
			apiURL, queried := ghRemoteAPI(t)
			res := &ghRemoteResolver{pol: egress.Policy{
				GitRepos: map[string][]string{"github.com": {"acme/widgets"}},
				GitHub:   &egress.GitHubAccess{APIOrigin: apiURL, GitOrigin: upURL},
			}}
			cp := newFakeCP(t)
			cp.egressH.Store(http.Handler(egress.NewHandler("orb-test", res)))
			rt := startRuntime(t, cp, nil)
			require.NoError(t, cp.call(t, "resume.run", apigen.LinkCtlSetupRunParams{
				GitHosts: c.gitHosts, OmpHome: []apigen.LinkCtlOmpHomeFile{}, Env: map[string]string{},
				Gh: &apigen.LinkCtlSetupGh{Login: "octo"},
			}, nil))

			// orb 内的进程环境：只有 orbd 写的 ~/.gitconfig 与 GH_CONFIG_DIR，没有任何令牌变量。
			env := []string{
				"HOME=" + rt.home, "PATH=" + os.Getenv("PATH"), "GH_CONFIG_DIR=" + rt.ghDir(),
				"GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GH_NO_UPDATE_NOTIFIER=1",
				"GH_NO_EXTENSION_UPDATE_NOTIFIER=1", "GH_PROMPT_DISABLED=1", "GH_TELEMETRY=0", "NO_COLOR=1",
				"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			}
			run := func(dir, bin string, args ...string) string {
				t.Helper()
				cmd := exec.Command(bin, args...)
				cmd.Dir, cmd.Env = dir, env
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s %s: %s", filepath.Base(bin), strings.Join(args, " "), out)
				return strings.TrimSpace(string(out))
			}
			work := filepath.Join(rt.home, "workspace", "repo")
			origin := "https://github.com/acme/widgets"
			if c.fork {
				origin = "https://github.com/me/widgets.git"
			}
			run(rt.home, gitBin, "clone", "-q", origin, work)
			if c.fork {
				run(work, gitBin, "remote", "add", "upstream", "https://github.com/acme/widgets.git")
				run(work, gitBin, "fetch", "-q", "upstream")
			}
			remotes := run(work, gitBin, "remote", "-v")
			t.Logf("git remote -v:\n%s", remotes)
			assert.Contains(t, remotes, "http://github.com/acme/widgets", "remote 显示的是真实主机与 owner/repo")
			assert.NotContains(t, remotes, "127.0.0.1")

			out := run(work, ghBin, "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
			assert.Equal(t, "acme/widgets", out, "gh 不带 -R 从 remote 推断出仓库")
			assert.Equal(t, []string{"acme/widgets"}, queried())

			// 推送经 egress 的 receive-pack 门禁路径（GitHubWriteBegin）到达上游。
			require.NoError(t, os.WriteFile(filepath.Join(work, "change.txt"), []byte("x\n"), 0o644))
			run(work, gitBin, "add", ".")
			run(work, gitBin, "commit", "-q", "-m", "change")
			run(work, gitBin, "push", "-q", "origin", "HEAD:refs/heads/feat")
			pushed := filepath.Join(upRoot, "acme/widgets")
			if c.fork {
				pushed = filepath.Join(upRoot, "me/widgets")
			}
			head := run(work, gitBin, "rev-parse", "HEAD")
			got, err := exec.Command(gitBin, "--git-dir", pushed, "rev-parse", "refs/heads/feat").Output()
			require.NoError(t, err)
			assert.Equal(t, head, strings.TrimSpace(string(got)))
			res.mu.Lock()
			require.Len(t, res.begins, 1)
			assert.Equal(t, egress.MethodPush, res.begins[0].Method)
			assert.Equal(t, strings.TrimPrefix(strings.TrimSuffix(origin, ".git"), "https://github.com/"), res.begins[0].Repo)
			res.mu.Unlock()

			// 真实令牌从不落到 orb 里（git 配置、gh 配置、检出与日志都算）。
			require.NoError(t, filepath.WalkDir(rt.home, func(p string, d fs.DirEntry, err error) error {
				if err != nil || !d.Type().IsRegular() {
					return err
				}
				b, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				for _, tok := range []string{ghRemoteGitToken, ghRemoteAPIToken} {
					assert.False(t, bytes.Contains(b, []byte(tok)), "%s 含真实令牌", p)
				}
				return nil
			}))
		})
	}

	// NO_PROXY=*：github.com 失败关闭，不写 http 改写与代理，remote 保持 https；gh 仍能从 https remote 推断出仓库
	// （API 走 gh.sock → egress，不受 NO_PROXY 影响）。
	t.Run("no-proxy-star", func(t *testing.T) {
		apiURL, queried := ghRemoteAPI(t)
		res := &ghRemoteResolver{pol: egress.Policy{GitHub: &egress.GitHubAccess{APIOrigin: apiURL}}}
		cp := newFakeCP(t)
		cp.egressH.Store(http.Handler(egress.NewHandler("orb-test", res)))
		rt := startRuntime(t, cp, nil)
		require.NoError(t, cp.call(t, "resume.run", apigen.LinkCtlSetupRunParams{
			GitHosts: []string{"github.com"}, OmpHome: []apigen.LinkCtlOmpHomeFile{}, Env: map[string]string{"NO_PROXY": "*"},
			Gh: &apigen.LinkCtlSetupGh{Login: "octo"},
		}, nil))
		cfg, _ := os.ReadFile(filepath.Join(rt.home, ".gitconfig"))
		assert.NotContains(t, string(cfg), "http://github.com", "没有 http 改写与代理条目")

		env := []string{"HOME=" + rt.home, "PATH=" + os.Getenv("PATH"), "GH_CONFIG_DIR=" + rt.ghDir(), "NO_PROXY=*",
			"GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GH_NO_UPDATE_NOTIFIER=1", "GH_PROMPT_DISABLED=1", "GH_TELEMETRY=0", "NO_COLOR=1"}
		run := func(dir, bin string, args ...string) string {
			t.Helper()
			cmd := exec.Command(bin, args...)
			cmd.Dir, cmd.Env = dir, env
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s %s: %s", filepath.Base(bin), strings.Join(args, " "), out)
			return strings.TrimSpace(string(out))
		}
		work := filepath.Join(rt.home, "workspace", "repo")
		require.NoError(t, os.MkdirAll(work, 0o755))
		run(work, gitBin, "init", "-q")
		run(work, gitBin, "remote", "add", "origin", "https://github.com/acme/widgets.git")
		remotes := run(work, gitBin, "remote", "-v")
		t.Logf("git remote -v:\n%s", remotes)
		assert.Contains(t, remotes, "https://github.com/acme/widgets.git (fetch)")
		assert.Equal(t, "acme/widgets", run(work, ghBin, "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner"))
		assert.Equal(t, []string{"acme/widgets"}, queried())
	})
}
