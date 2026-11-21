package orbdrt

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

const testRunnerToken = "ort_test"

// runnerFixture 是一台 runner：临时 HOME（同时设为进程的 $HOME）、一个声明的目录 work（含 git 仓库 repo）
// 与声明目录之外的 outside。
type runnerFixture struct {
	home, base, work, outside string
	cp                        *fakeCP
	rt                        *Runtime
}

// rgit 在 dir 里执行 git（带提交身份，不读写任何全局配置）。
func rgit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// newRunnerHome 设好临时 HOME 与目录（不起运行时）。
func newRunnerHome(t *testing.T) *runnerFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	f := &runnerFixture{
		home:    filepath.Join(root, "home"),
		base:    filepath.Join(root, "src"),
		outside: filepath.Join(root, "outside"),
	}
	f.work = filepath.Join(f.base, "repo")
	for _, d := range []string{f.home, f.work, f.outside} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	t.Setenv("HOME", f.home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.home, ".config"))
	require.NoError(t, os.WriteFile(filepath.Join(f.outside, "secret.txt"), []byte("secret"), 0o644))
	return f
}

func (f *runnerFixture) initRepo(t *testing.T) {
	t.Helper()
	rgit(t, f.work, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(f.work, "README.md"), []byte("hi\n"), 0o644))
	rgit(t, f.work, "add", "README.md")
	rgit(t, f.work, "commit", "-q", "-m", "init")
}

// startRunnerRT 以 runner 形态起 orbd（声明的目录 = f.work），并像控制面那样在 hello 后先发一次 omphome.apply
// （M21：此前 agent.start 回 config_pending）。
func startRunnerRT(t *testing.T, f *runnerFixture, mutate func(*Config)) {
	t.Helper()
	startRunnerRTPending(t, f, mutate)
	require.NoError(t, f.cp.call(t, string(apigen.MethodOmpHomeApply), apigen.LinkCtlOmpHomeApplyParams{OmpHome: []apigen.LinkCtlOmpHomeFile{}}, nil))
}

// startRunnerRTPending 同 startRunnerRT，但不下发配置。
func startRunnerRTPending(t *testing.T, f *runnerFixture, mutate func(*Config)) {
	t.Helper()
	f.cp = newFakeCP(t)
	f.cp.token.Store(testRunnerToken)
	f.rt = startRuntime(t, f.cp, func(c *Config) {
		c.OrbID = ""
		c.Token = testRunnerToken
		c.Home = f.home
		c.Runner = &RunnerConfig{Dirs: []string{f.work}}
		if mutate != nil {
			mutate(c)
		}
	})
}

func helloOf(t *testing.T, cp *fakeCP) apigen.LinkCtlHelloParams {
	t.Helper()
	_, raw := cp.waitNotif(t, 0, "hello", nil)
	var h apigen.LinkCtlHelloParams
	require.NoError(t, json.Unmarshal(raw, &h))
	return h
}

// --runner：经 whoami 取得 orb id；启动即常驻监听 egress（M21）；不建 orbd.sock；不写 ~/.agents、~/.ssh，不碰
// git 全局配置，ORYN_AGENT_BIN 给出时不下载 Core；只写自带的扩展与 runner 版 oryn-library 技能并监听 runner.sock（M14）。
func TestRunnerStartupTouchesNothingOrbSpecific(t *testing.T) {
	f := newRunnerHome(t)
	f.initRepo(t)
	egress := freeAddr(t)
	startRunnerRT(t, f, func(c *Config) { c.EgressAddr = egress })
	h := helloOf(t, f.cp)

	assert.Equal(t, testRunnerOrb, f.rt.cfg.OrbID, "orb id comes from whoami")
	assert.Equal(t, fakeompBin, f.rt.cfg.AgentBin)
	assert.Equal(t, egress, f.rt.EgressAddr())
	_, err := net.Listen("tcp", egress)
	require.Error(t, err, "the runner listens on the egress address from startup")

	for _, p := range []string{
		filepath.Join(f.home, ".agents"),
		filepath.Join(f.home, ".omp"),
		filepath.Join(f.home, ".ssh"),
		filepath.Join(f.home, ".oryn", "core"),
		orbsock.Path(f.home),
		filepath.Join(f.home, ".gitconfig"),
		filepath.Join(f.home, ".config", "git"),
	} {
		_, err := os.Lstat(p)
		assert.ErrorIs(t, err, os.ErrNotExist, "%s must not be created on a runner", p)
	}
	for _, p := range []string{orbsock.RunnerPath(f.home), runnerExtensionPath(f.home), runnerLibrarySkillPath(f.home)} {
		require.Eventually(t, func() bool { _, err := os.Lstat(p); return err == nil }, 5*time.Second, 10*time.Millisecond, "%s exists on a runner", p)
	}
	assert.Equal(t, filepath.Join(f.home, ".oryn", "omp", "agent", "skills", "oryn-library", "SKILL.md"), runnerLibrarySkillPath(f.home))
	assert.Equal(t, string(runnerLibrarySkill), readFile(t, runnerLibrarySkillPath(f.home)))
	assert.Contains(t, string(runnerLibrarySkill), "oryn_library_put")
	fi, err := os.Lstat(orbsock.RunnerPath(f.home))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	require.NotNil(t, h.Runner)
	host, _ := os.Hostname()
	assert.Equal(t, host, h.Runner.Hostname)
	assert.Equal(t, []apigen.RunnerDir{{Path: f.work, Name: "repo", Git: true}}, h.Runner.Dirs)
}

// hello 的特性随 flag、PATH 与 SSH 端口变化：fs、attachments、metrics、memory、omphome-apply；git 在 PATH 里加 git；
// --terminal 加 terminal；SSH 网关端口监听成功加 ssh（M21）。
func TestRunnerHelloFeatures(t *testing.T) {
	t.Run("git", func(t *testing.T) {
		f := newRunnerHome(t)
		startRunnerRT(t, f, nil)
		assert.Equal(t, []string{"fs", "attachments", "metrics", "memory", "omphome-apply", "git", "ssh"}, helloOf(t, f.cp).Features)
	})
	t.Run("terminal", func(t *testing.T) {
		f := newRunnerHome(t)
		startRunnerRT(t, f, func(c *Config) { c.Runner.Terminal = true })
		assert.Equal(t, []string{"fs", "attachments", "metrics", "memory", "omphome-apply", "git", "terminal", "ssh"}, helloOf(t, f.cp).Features)
	})
	t.Run("no git", func(t *testing.T) {
		f := newRunnerHome(t)
		t.Setenv("PATH", t.TempDir())
		startRunnerRT(t, f, nil)
		h := helloOf(t, f.cp)
		assert.Equal(t, []string{"fs", "attachments", "metrics", "memory", "omphome-apply", "ssh"}, h.Features)
		assert.False(t, h.Runner.Dirs[0].Git, "work is not a repository")
	})
	t.Run("ssh port busy", func(t *testing.T) {
		f := newRunnerHome(t)
		busy, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = busy.Close() })
		startRunnerRT(t, f, func(c *Config) { c.SSHAddr = busy.Addr().String() })
		assert.NotContains(t, helloOf(t, f.cp).Features, "ssh")
		assert.Nil(t, f.rt.sshLn)
	})
}

// 同一用户第二个实例：flock 失败、报错退出，且不清掉第一个实例的上传临时目录。
func TestRunnerSecondInstanceRefused(t *testing.T) {
	f := newRunnerHome(t)
	startRunnerRT(t, f, nil)
	f.cp.ctlNow(t)
	marker := filepath.Join(f.rt.tmpDir(), "upload-in-progress")
	require.NoError(t, os.WriteFile(marker, nil, 0o600))

	_, err := New(Config{URL: f.cp.srv.URL, Token: testRunnerToken, Home: f.home, AgentBin: fakeompBin, Runner: &RunnerConfig{Dirs: []string{f.work}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already running")
	assert.FileExists(t, marker)
}

// 吊销（或未知）的令牌：whoami 401，启动失败；锁随之释放，换对令牌即可启动。
func TestRunnerWhoamiRejected(t *testing.T) {
	f := newRunnerHome(t)
	cp := newFakeCP(t)
	cp.token.Store(testRunnerToken)
	cfg := Config{URL: cp.srv.URL, Token: "ort_revoked", Home: f.home, AgentBin: fakeompBin, Runner: &RunnerConfig{Dirs: []string{f.work}}}
	_, err := New(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rejected")

	cfg.Token = testRunnerToken
	rt, err := New(cfg)
	require.NoError(t, err)
	assert.Equal(t, testRunnerOrb, rt.cfg.OrbID)
	rt.runner.unlock()
}

// --dir：必须存在且是目录；存 EvalSymlinks 之后的绝对路径。
func TestRunnerDirValidation(t *testing.T) {
	f := newRunnerHome(t)
	_, err := resolveRunnerDirs([]string{filepath.Join(f.base, "missing")})
	assert.Error(t, err)
	_, err = resolveRunnerDirs([]string{filepath.Join(f.outside, "secret.txt")})
	assert.ErrorContains(t, err, "not a directory")
	_, err = resolveRunnerDirs(nil)
	assert.Error(t, err)

	link := filepath.Join(f.base, "alias")
	require.NoError(t, os.Symlink(f.work, link))
	got, err := resolveRunnerDirs([]string{link, f.work})
	require.NoError(t, err)
	assert.Equal(t, []string{f.work}, got, "resolved and deduplicated")
}

func TestRunnerConfigFromEnv(t *testing.T) {
	f := newRunnerHome(t)
	t.Setenv("ORYN_URL", "http://127.0.0.1:7600")
	t.Setenv("ORYN_RUNNER_TOKEN", "")

	_, err := RunnerConfigFromEnv("v", RunnerOptions{Dirs: []string{f.work}})
	assert.ErrorContains(t, err, "ORYN_RUNNER_TOKEN", "no env and no default token file")

	def := filepath.Join(f.home, ".config", "oryn", "runner.token")
	require.NoError(t, os.MkdirAll(filepath.Dir(def), 0o700))
	require.NoError(t, os.WriteFile(def, []byte("ort_fromfile\n"), 0o600))
	cfg, err := RunnerConfigFromEnv("v", RunnerOptions{Dirs: []string{f.work}, Terminal: true})
	require.NoError(t, err)
	assert.Equal(t, "ort_fromfile", cfg.Token)
	assert.Equal(t, &RunnerConfig{Dirs: []string{f.work}, Terminal: true}, cfg.Runner)
	assert.Empty(t, cfg.OrbID)

	other := filepath.Join(f.home, "tok")
	require.NoError(t, os.WriteFile(other, []byte("ort_other"), 0o600))
	cfg, err = RunnerConfigFromEnv("v", RunnerOptions{Dirs: []string{f.work}, TokenFile: other})
	require.NoError(t, err)
	assert.Equal(t, "ort_other", cfg.Token)

	t.Setenv("ORYN_RUNNER_TOKEN", "ort_env")
	cfg, err = RunnerConfigFromEnv("v", RunnerOptions{Dirs: []string{f.work}, TokenFile: other})
	require.NoError(t, err)
	assert.Equal(t, "ort_env", cfg.Token, "the environment wins over the file")

	t.Setenv("ORYN_RUNNER_TOKEN", "oot_orbtoken")
	_, err = RunnerConfigFromEnv("v", RunnerOptions{Dirs: []string{f.work}})
	assert.ErrorContains(t, err, "ort_")

	t.Setenv("ORYN_URL", "")
	_, err = RunnerConfigFromEnv("v", RunnerOptions{Dirs: []string{f.work}})
	assert.ErrorContains(t, err, "ORYN_URL")
}

func TestPlainRemoteURL(t *testing.T) {
	assert.True(t, plainRemoteURL("http://10.0.0.5:7600"))
	assert.True(t, plainRemoteURL("http://cp.example.com"))
	assert.False(t, plainRemoteURL("http://127.0.0.1:7600"))
	assert.False(t, plainRemoteURL("http://localhost:7600"))
	assert.False(t, plainRemoteURL("http://[::1]:7600"))
	assert.False(t, plainRemoteURL("https://cp.example.com"))
}

// runner 不处理 setup / resume / 快照 / 关机：not_supported；orb 形态的 worktree.* 同样 not_supported。
func TestRunnerCtlNotSupported(t *testing.T) {
	f := newRunnerHome(t)
	startRunnerRT(t, f, nil)
	for _, m := range []string{"setup.run", "resume.run", "snapshot.prepare", "shutdown", "portal.configure", "profile.mode"} {
		err := f.cp.call(t, m, map[string]any{}, nil)
		assert.Equal(t, CodeNotSupported, ctlCode(err), m)
	}

	cp := newFakeCP(t)
	startRuntime(t, cp, nil)
	for _, m := range []string{"worktree.add", "worktree.remove"} {
		err := cp.call(t, m, map[string]any{}, nil)
		assert.Equal(t, CodeNotSupported, ctlCode(err), m)
	}
}

type agentDump struct {
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
	Cwd  string            `json:"cwd"`
}

// agent.start：cwd 必填且在声明的目录之内（符号链接逃逸被拒）；会话目录只能在 ~/.oryn/threads 下；
// 合法时 omp 以该目录为 cwd、`-e` 恰是 runner 自带的扩展、环境有 ORYN_RUNNER=1 与指向 runner.sock 的
// ORYN_ARTIFACT_SOCK，而没有 ORYN_ORB / 令牌。M21：独立配置根 PI_CONFIG_DIR=.oryn/omp、ORYN_ISOLATE_USER_SOURCES=1
// （下发的 env 覆盖不了，继承来的 PI_CODING_AGENT_DIR / profile 被去掉）与 ORYN_SSH=1。
func TestRunnerAgentStartCwd(t *testing.T) {
	f := newRunnerHome(t)
	t.Setenv("ORYN_RUNNER_TOKEN", testRunnerToken) // 进程环境里的令牌不得进入 agent
	t.Setenv("ORYN_ORB", "1")
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(f.home, ".omp", "agent"))
	t.Setenv("PI_PROFILE", "work")
	escape := filepath.Join(f.work, "escape")
	require.NoError(t, os.Symlink(f.outside, escape))
	startRunnerRT(t, f, nil)
	dump := filepath.Join(f.home, "dump")
	start := func(cwd, sessionDir string) error {
		return f.cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{
			ThreadID: thread1, Cwd: cwd, SessionDir: sessionDir,
			Env: map[string]string{"FAKEOMP_DUMP_DIR": dump, "PI_CONFIG_DIR": ".omp", "ORYN_ISOLATE_USER_SOURCES": "0"}, InitFrames: initFrames(thread1),
		}, nil)
	}
	assert.Equal(t, CodeDirNotServed, ctlCode(start("", "")))
	assert.Equal(t, CodeDirNotServed, ctlCode(start(f.outside, "")))
	assert.Equal(t, CodeDirNotServed, ctlCode(start(escape, "")), "symlink out of the declared dir")
	assert.Equal(t, CodeDirNotServed, ctlCode(start(filepath.Join(escape, "sub"), "")))
	assert.Equal(t, CodeDirNotServed, ctlCode(start(f.work+"/../outside", "")))
	assert.Equal(t, CodeDirNotServed, ctlCode(start("relative/dir", "")))
	assert.Equal(t, "invalid_params", ctlCode(start(f.work, f.outside)), "session dir outside ~/.oryn/threads")
	assert.Equal(t, "invalid_params", ctlCode(start(f.work, "~/.oryn/threads")))

	require.NoError(t, start(f.work, "~/.oryn/threads/"+thread1))
	f.cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))
	var rec agentDump
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(filepath.Join(dump, thread1+".json"))
		return err == nil && json.Unmarshal(b, &rec) == nil
	}, 10*time.Second, 20*time.Millisecond)
	assert.Equal(t, f.work, rec.Cwd)
	i := slices.Index(rec.Args, "-e")
	require.GreaterOrEqual(t, i, 0, "argv %v", rec.Args)
	require.Less(t, i+1, len(rec.Args))
	assert.Equal(t, runnerExtensionPath(f.home), rec.Args[i+1])
	assert.Equal(t, 1, strings.Count(strings.Join(rec.Args, "\x00"), "\x00-e\x00"), "只加载一个扩展")
	assert.Equal(t, orbsock.RunnerPath(f.home), rec.Env["ORYN_ARTIFACT_SOCK"])
	assert.Equal(t, "1", rec.Env["ORYN_RUNNER"])
	assert.Equal(t, f.home, rec.Env["HOME"])
	for _, k := range []string{"ORYN_ORB", "ORYN_ORB_ID", "ORYN_RUNNER_TOKEN", "ORYN_ORB_TOKEN", "PI_CODING_AGENT_DIR", "PI_PROFILE"} {
		assert.NotContains(t, rec.Env, k)
	}
	assert.Equal(t, ".oryn/omp", rec.Env["PI_CONFIG_DIR"])
	assert.Equal(t, "1", rec.Env["ORYN_ISOLATE_USER_SOURCES"])
	assert.Equal(t, "1", rec.Env["ORYN_SSH"])
}

// M14 1.7：runner.sock 只认 artifact.call（原样转成 ctl artifact.call）与 library.put（M21，见 TestRunnerLibraryPut），
// thread.* / setup.* 等回 method_not_found。
func TestRunnerArtifactSocket(t *testing.T) {
	f := newRunnerHome(t)
	startRunnerRT(t, f, nil)
	f.cp.waitNotif(t, 0, "hello", nil)
	forwarded := make(chan apigen.LinkCtlArtifactCallParams, 1)
	f.cp.extra = func(method string, params json.RawMessage) (any, bool, error) {
		if method != string(apigen.MethodArtifactCall) {
			return nil, false, nil
		}
		var p apigen.LinkCtlArtifactCallParams
		_ = json.Unmarshal(params, &p)
		forwarded <- p
		return apigen.LinkCtlArtifactCallResult{Text: "ok"}, true, nil
	}
	sock := orbsock.RunnerPath(f.home)
	waitSock(t, sock)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, m := range []string{orbsock.MethodThreadNew, orbsock.MethodScheduleSet, orbsock.MethodSetupRerun, orbsock.MethodSshCall, orbsock.MethodGithubCall, orbsock.MethodBoxCall} {
		_, err := orbsock.Call(ctx, sock, m, map[string]any{"prompt": "x"}, nil)
		var se *orbsock.Error
		require.ErrorAs(t, err, &se, m)
		assert.Equal(t, orbsock.CodeMethodNotFound, se.Code, m)
	}

	raw, err := orbsock.Call(ctx, sock, orbsock.MethodArtifactCall, apigen.LinkCtlArtifactCallParams{
		ThreadID: thread1, Op: "list_artifacts", Params: map[string]any{"query": "报告"},
	}, nil)
	require.NoError(t, err)
	var res apigen.LinkCtlArtifactCallResult
	require.NoError(t, json.Unmarshal(raw, &res))
	assert.Equal(t, "ok", res.Text)
	select {
	case p := <-forwarded:
		assert.Equal(t, apigen.LinkCtlArtifactCallParams{ThreadID: thread1, Op: "list_artifacts", Params: map[string]any{"query": "报告"}}, p)
	default:
		t.Fatal("artifact.call 没有转到控制面")
	}
}

// fs.copyTree / fs.move 在 runner 上只能动 ~/.oryn/threads 之下。
func TestRunnerFsOpsConfined(t *testing.T) {
	f := newRunnerHome(t)
	startRunnerRT(t, f, nil)
	err := f.cp.call(t, "fs.move", apigen.LinkCtlFsMoveParams{From: filepath.Join(f.work, "README.md"), To: "~/.oryn/threads/x"}, nil)
	assert.Equal(t, "invalid_params", ctlCode(err))
	err = f.cp.call(t, "fs.copyTree", apigen.LinkCtlFsCopyTreeParams{From: "~/.oryn/threads/a", To: "~/notes"}, nil)
	assert.Equal(t, "invalid_params", ctlCode(err))

	src := filepath.Join(f.home, ".oryn", "threads", "a")
	require.NoError(t, os.MkdirAll(src, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "s.jsonl"), []byte("{}\n"), 0o644))
	require.NoError(t, f.cp.call(t, "fs.copyTree", apigen.LinkCtlFsCopyTreeParams{From: "~/.oryn/threads/a", To: "~/.oryn/threads/b"}, nil))
	assert.FileExists(t, filepath.Join(f.home, ".oryn", "threads", "b", "s.jsonl"))
}

// 本机 API：缺 X-Oryn-Workdir 400、越界 403；范围是该目录（`..` 与符号链接逃逸被拒）；/sync、/services 404。
func TestRunnerLocalAPIScope(t *testing.T) {
	f := newRunnerHome(t)
	f.initRepo(t)
	require.NoError(t, os.Symlink(f.outside, filepath.Join(f.work, "escape")))
	startRunnerRT(t, f, nil)
	o := newOrbHTTP(t, f.cp, f.rt)
	wd := map[string]string{WorkdirHeader: f.work}

	resp, b := o.do(http.MethodGet, "/fs/roots", nil, nil, nil)
	assert.Equal(t, "workdir_required", o.errCode(resp, b, http.StatusBadRequest))
	resp, b = o.do(http.MethodGet, "/git/status", nil, nil, nil)
	assert.Equal(t, "workdir_required", o.errCode(resp, b, http.StatusBadRequest))
	resp, b = o.do(http.MethodGet, "/fs/roots", nil, nil, map[string]string{WorkdirHeader: f.outside})
	assert.Equal(t, CodeDirNotServed, o.errCode(resp, b, http.StatusForbidden))
	resp, b = o.do(http.MethodGet, "/fs/roots", nil, nil, map[string]string{WorkdirHeader: filepath.Join(f.work, "escape")})
	assert.Equal(t, CodeDirNotServed, o.errCode(resp, b, http.StatusForbidden), "workdir through a symlink out of the dir")

	resp, b = o.do(http.MethodGet, "/fs/roots", nil, nil, wd)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	var roots apigen.FsRoots
	require.NoError(t, json.Unmarshal(b, &roots))
	assert.Equal(t, apigen.FsRoots{Home: f.work, Workdir: f.work}, roots)

	resp, b = o.do(http.MethodGet, "/fs/file", pathQ(filepath.Join(f.work, "README.md")), nil, wd)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	assert.Equal(t, "hi\n", string(b))
	for _, p := range []string{
		filepath.Join(f.work, "..", "outside", "secret.txt"),
		filepath.Join(f.work, "escape", "secret.txt"),
		"~/.bashrc",
		filepath.Join(f.base),
	} {
		resp, b = o.do(http.MethodGet, "/fs/file", pathQ(p), nil, wd)
		assert.Equal(t, "outside_home", o.errCode(resp, b, http.StatusForbidden), p)
	}
	resp, b = o.do(http.MethodGet, "/fs/list", pathQ(f.work), nil, wd)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	var ls apigen.FsListing
	require.NoError(t, json.Unmarshal(b, &ls))
	for _, e := range ls.Entries {
		if e.Name == "escape" {
			assert.Nil(t, e.TargetType, "symlink target outside the scope is not resolved")
		}
	}
	// 写：范围之内可写；工作目录本身不可删。
	resp, b = o.put(filepath.Join(f.work, "new.txt"), []byte("x"), nil, wd)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	resp, b = o.put(filepath.Join(f.outside, "new.txt"), []byte("x"), nil, wd)
	assert.Equal(t, "outside_home", o.errCode(resp, b, http.StatusForbidden))
	resp, b = o.do(http.MethodPost, "/fs/delete", nil, strings.NewReader(`{"path":"`+f.work+`"}`), wd)
	assert.Equal(t, "protected_path", o.errCode(resp, b, http.StatusForbidden))

	resp, b = o.do(http.MethodGet, "/git/status", nil, nil, wd)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	var st apigen.GitStatus
	require.NoError(t, json.Unmarshal(b, &st))
	assert.True(t, st.Repo)
	assert.Len(t, st.Files, 2, "escape and new.txt are untracked")

	resp, b = o.do(http.MethodPost, "/sync/prepare", nil, strings.NewReader(`{}`), wd)
	assert.Equal(t, "not_found", o.errCode(resp, b, http.StatusNotFound))
	resp, b = o.do(http.MethodPost, "/services/web/start", nil, nil, wd)
	assert.Equal(t, "not_found", o.errCode(resp, b, http.StatusNotFound))
}

// worktree.add：重名（目录或分支已存在）依次加 -2、-3…；登记的 worktree 可作 cwd。
// worktree.remove：有未提交改动时 worktree_dirty 且目录仍在；干净时删目录与分支；分支未合并时保留分支。
func TestRunnerWorktrees(t *testing.T) {
	f := newRunnerHome(t)
	f.initRepo(t)
	startRunnerRT(t, f, nil)
	add := func(repo, name string) (apigen.LinkCtlWorktreeAddResult, error) {
		var res apigen.LinkCtlWorktreeAddResult
		err := f.cp.call(t, "worktree.add", apigen.LinkCtlWorktreeAddParams{Repo: repo, Name: name}, &res)
		return res, err
	}
	remove := func(dir string) (apigen.LinkCtlWorktreeRemoveResult, error) {
		var res apigen.LinkCtlWorktreeRemoveResult
		err := f.cp.call(t, "worktree.remove", apigen.LinkCtlWorktreeRemoveParams{Dir: dir}, &res)
		return res, err
	}

	_, err := add(f.work, "-bad")
	assert.Equal(t, "invalid_params", ctlCode(err))
	_, err = add(f.work, strings.Repeat("a", 64))
	assert.Equal(t, "invalid_params", ctlCode(err))
	_, err = add(f.outside, "x")
	assert.Equal(t, CodeDirNotServed, ctlCode(err))

	a, err := add(f.work, "feat")
	require.NoError(t, err)
	assert.Equal(t, apigen.LinkCtlWorktreeAddResult{Dir: filepath.Join(f.base, "repo-feat"), Branch: "feat"}, a)
	assert.FileExists(t, filepath.Join(a.Dir, "README.md"))
	b2, err := add(f.work, "feat")
	require.NoError(t, err)
	assert.Equal(t, apigen.LinkCtlWorktreeAddResult{Dir: filepath.Join(f.base, "repo-feat-2"), Branch: "feat-2"}, b2)
	// 目录已存在（不是 worktree）→ 后缀；分支已存在 → 后缀。
	require.NoError(t, os.MkdirAll(filepath.Join(f.base, "repo-taken"), 0o755))
	c, err := add(f.work, "taken")
	require.NoError(t, err)
	assert.Equal(t, apigen.LinkCtlWorktreeAddResult{Dir: filepath.Join(f.base, "repo-taken-2"), Branch: "taken-2"}, c)
	rgit(t, f.work, "branch", "exists")
	d, err := add(f.work, "exists")
	require.NoError(t, err)
	assert.Equal(t, apigen.LinkCtlWorktreeAddResult{Dir: filepath.Join(f.base, "repo-exists-2"), Branch: "exists-2"}, d)

	var reg []worktreeEntry
	raw, err := os.ReadFile(filepath.Join(f.home, ".oryn", "orbd", "worktrees.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &reg))
	assert.Contains(t, reg, worktreeEntry{Repo: f.work, Dir: a.Dir, Branch: "feat"})

	// 登记的 worktree（在声明目录之外）可作工作目录。
	_, ok := f.rt.allowed(a.Dir)
	assert.True(t, ok)
	_, ok = f.rt.allowed(filepath.Join(f.base, "repo-taken"))
	assert.False(t, ok, "a sibling that is not a registered worktree")

	// 未提交改动：拒绝，目录与登记都保留。
	require.NoError(t, os.WriteFile(filepath.Join(a.Dir, "wip.txt"), []byte("wip"), 0o644))
	_, err = remove(a.Dir)
	assert.Equal(t, CodeWorktreeDirty, ctlCode(err))
	assert.Contains(t, err.Error(), "untracked")
	assert.FileExists(t, filepath.Join(a.Dir, "wip.txt"))
	_, ok = f.rt.allowed(a.Dir)
	assert.True(t, ok, "still registered")

	// 干净：删目录、删分支、注销。
	require.NoError(t, os.Remove(filepath.Join(a.Dir, "wip.txt")))
	res, err := remove(a.Dir)
	require.NoError(t, err)
	assert.Equal(t, apigen.LinkCtlWorktreeRemoveResult{Ok: true}, res)
	assert.NoDirExists(t, a.Dir)
	assert.Empty(t, rgit(t, f.work, "branch", "--list", "feat"))
	_, ok = f.rt.allowed(a.Dir)
	assert.False(t, ok)
	_, err = remove(a.Dir)
	assert.Equal(t, "not_found", ctlCode(err))

	// 分支有未合并的提交：worktree 删掉、分支保留。
	require.NoError(t, os.WriteFile(filepath.Join(b2.Dir, "f.txt"), []byte("f"), 0o644))
	rgit(t, b2.Dir, "add", "f.txt")
	rgit(t, b2.Dir, "commit", "-q", "-m", "work")
	res, err = remove(b2.Dir)
	require.NoError(t, err)
	require.NotNil(t, res.BranchKept)
	assert.True(t, *res.BranchKept)
	assert.NoDirExists(t, b2.Dir)
	assert.Equal(t, "feat-2", strings.TrimSpace(strings.TrimPrefix(rgit(t, f.work, "branch", "--list", "feat-2"), "*")))

	// 目录已被手工删掉：照常注销。
	require.NoError(t, os.RemoveAll(c.Dir))
	res, err = remove(c.Dir)
	require.NoError(t, err)
	assert.True(t, res.Ok)

	_, err = os.Lstat(filepath.Join(f.home, ".gitconfig"))
	assert.ErrorIs(t, err, os.ErrNotExist, "git global config untouched")
}

// /proc 指标源：CPU 取 /proc/stat 首行的差分（单核 100%），内存取 MemTotal − MemAvailable。
func TestProcMetricsSource(t *testing.T) {
	dir := t.TempDir()
	stat := filepath.Join(dir, "stat")
	mi := filepath.Join(dir, "meminfo")
	writeStat := func(user, system, irq, softirq, steal int) {
		f := strconv.Itoa
		line := strings.Join([]string{"cpu ", f(user), "0", f(system), "999999", "5000", f(irq), f(softirq), f(steal), "77", "0"}, " ")
		require.NoError(t, os.WriteFile(stat, []byte(line+"\ncpu0 1 2 3 4\nintr 1\n"), 0o644))
	}
	require.NoError(t, os.WriteFile(mi, []byte("MemTotal:        8192000 kB\nMemFree:          100000 kB\nMemAvailable:    2048000 kB\n"), 0o644))
	writeStat(100, 40, 5, 5, 0) // 150 节拍 = 1.5s

	src := procSource{stat: stat, memInfo: mi}
	usec, ok := src.cpuUsageUsec()
	require.True(t, ok)
	assert.Equal(t, uint64(1_500_000), usec)
	used, limit := src.memory()
	assert.Equal(t, uint64(6144000*1024), used)
	assert.Equal(t, uint64(8192000*1024), limit)

	cg := filepath.Join(dir, "cgroup") // 没有 memory.current → /proc 源
	require.NoError(t, os.MkdirAll(cg, 0o755))
	m := newMetricsReader(Config{CgroupDir: cg, ProcStat: stat, MemInfo: mi, DiskPath: dir, NetDir: dir})
	require.IsType(t, procSource{}, m.src)
	t0 := time.Unix(1000, 0)
	m.prevAt = t0
	writeStat(200, 90, 5, 5, 50) // +200 节拍 = 2s
	s := m.sample(t0.Add(time.Second))
	assert.InDelta(t, 200.0, s.CPUPct, 0.01, "2 CPU-seconds in 1s = two full cores")
	assert.InDelta(t, 6000.0, s.MemUsedMB, 0.01)
	assert.InDelta(t, 8000.0, s.MemLimitMB, 0.01)
	assert.Positive(t, s.DiskUsedMB)

	require.NoError(t, os.WriteFile(filepath.Join(cg, "memory.current"), []byte("1048576\n"), 0o644))
	assert.IsType(t, cgroupSource{}, newMetricsReader(Config{CgroupDir: cg, ProcStat: stat, MemInfo: mi}).src)
	assert.IsType(t, procSource{}, newMetricsReader(Config{CgroupDir: cg, ProcStat: stat, MemInfo: mi, Runner: &RunnerConfig{}}).src,
		"a runner always reads /proc")
}

// gatedBody 是可阻塞的上传体：第一次 Read（服务端开始收上传体，预检已过）时关闭 reading，等 release 后才给出数据。
type gatedBody struct {
	reading, release chan struct{}
	once             sync.Once
	data             []byte
	done             bool
}

func (g *gatedBody) Read(p []byte) (int, error) {
	g.once.Do(func() {
		close(g.reading)
		<-g.release
	})
	if g.done {
		return 0, io.EOF
	}
	g.done = true
	return copy(p, g.data), io.EOF
}

// R4：上传期间把线程工作目录换成指向范围外的链接：范围固定为请求开始时 allowed 解析出的真实路径，
// 收完上传体后的复核按它判定，403 且 outside 里没有新文件。
func TestRunnerUploadScopePinnedAcrossWorkdirSwap(t *testing.T) {
	f := newRunnerHome(t)
	wd := filepath.Join(f.work, "wd")
	require.NoError(t, os.MkdirAll(wd, 0o755))
	startRunnerRT(t, f, nil)
	o := newOrbHTTP(t, f.cp, f.rt)
	// Expect: 100-continue：客户端等服务端开始读上传体才发送，gatedBody 的第一次 Read 就意味着预检已过。
	o.c.Transport.(*http.Transport).ExpectContinueTimeout = time.Minute

	body := &gatedBody{reading: make(chan struct{}), release: make(chan struct{}), data: []byte("pwned")}
	req, err := http.NewRequest(http.MethodPut, "http://orb/fs/file?"+pathQ(filepath.Join(wd, "file.txt")).Encode(), body)
	require.NoError(t, err)
	req.Header.Set(WorkdirHeader, wd)
	req.Header.Set("Expect", "100-continue")
	type result struct {
		status int
		body   []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := o.c.Do(req)
		if err != nil {
			done <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		done <- result{resp.StatusCode, b, err}
	}()
	select {
	case <-body.reading:
	case r := <-done:
		t.Fatalf("the upload finished before its body was read: %d %s %v", r.status, r.body, r.err)
	case <-time.After(30 * time.Second):
		t.Fatal("the server never started reading the upload")
	}
	require.NoError(t, os.Rename(wd, wd+".old"))
	require.NoError(t, os.Symlink(f.outside, wd))
	close(body.release)

	r := <-done
	require.NoError(t, r.err)
	require.Equal(t, http.StatusForbidden, r.status, string(r.body))
	var e apigen.Error
	require.NoError(t, json.Unmarshal(r.body, &e))
	assert.Equal(t, "outside_home", e.Code)
	assert.NoFileExists(t, filepath.Join(f.outside, "file.txt"))
	assert.NoFileExists(t, filepath.Join(wd+".old", "file.txt"))
}

// R4：线程工作目录本身是范围内的符号链接时照常可用：范围是它的真实路径，经链接给出的路径照常读写。
func TestRunnerSymlinkWorkdirInScope(t *testing.T) {
	f := newRunnerHome(t)
	real := filepath.Join(f.work, "wd")
	require.NoError(t, os.MkdirAll(real, 0o755))
	lnk := filepath.Join(f.work, "lnk")
	require.NoError(t, os.Symlink(real, lnk))
	startRunnerRT(t, f, nil)
	o := newOrbHTTP(t, f.cp, f.rt)
	hdr := map[string]string{WorkdirHeader: lnk}

	resp, b := o.do(http.MethodGet, "/fs/roots", nil, nil, hdr)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	var roots apigen.FsRoots
	require.NoError(t, json.Unmarshal(b, &roots))
	assert.Equal(t, apigen.FsRoots{Home: real, Workdir: real}, roots)

	resp, b = o.put(filepath.Join(lnk, "new.txt"), []byte("hello"), nil, hdr)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	got, err := os.ReadFile(filepath.Join(real, "new.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
	resp, b = o.do(http.MethodGet, "/fs/file", pathQ(filepath.Join(lnk, "new.txt")), nil, hdr)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	assert.Equal(t, "hello", string(b))
	resp, b = o.do(http.MethodPost, "/fs/delete", nil, strings.NewReader(`{"path":"`+lnk+`"}`), hdr)
	assert.Equal(t, "protected_path", o.errCode(resp, b, http.StatusForbidden), "the workdir link itself is not deletable")
}

// ---- M21 ----

// 首个 omphome.apply 之前 agent.start 回 config_pending（不建会话目录、不起进程），之后照常启动。
func TestRunnerConfigPending(t *testing.T) {
	f := newRunnerHome(t)
	startRunnerRTPending(t, f, nil)
	helloOf(t, f.cp)
	start := func() error {
		return f.cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{ThreadID: thread1, Cwd: f.work, InitFrames: initFrames(thread1)}, nil)
	}
	err := start()
	assert.Equal(t, CodeConfigPending, ctlCode(err), "%v", err)
	assert.NoDirExists(t, filepath.Join(f.home, ".oryn", "threads", thread1))

	require.NoError(t, f.cp.call(t, string(apigen.MethodOmpHomeApply), apigen.LinkCtlOmpHomeApplyParams{
		OmpHome: []apigen.LinkCtlOmpHomeFile{ompFile("config.yml", "x")},
	}, nil))
	require.NoError(t, start())
	f.cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))
}

// omphome.apply 写进 ~/.oryn/omp/agent（清单 ~/.oryn/omp-home.manifest.json），预先放好的用户 ~/.omp/agent/config.yml
// 内容与 mtime 不变；清理只删上次下发的文件（omp 自己的文件与 runner 的 oryn-library 技能保留）；带来的 projectConfig
// 被忽略（runner 的项目层按线程随 agent.start 下发）；SSH 写 ~/.oryn/omp/agent/ssh.json、~/.oryn/omp/ssh/known_hosts 与 ~/.oryn/ssh/config，
// 不碰 ~/.ssh。
func TestRunnerOmpHomeApply(t *testing.T) {
	f := newRunnerHome(t)
	userCfg := filepath.Join(f.home, ".omp", "agent", "config.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(userCfg), 0o755))
	require.NoError(t, os.WriteFile(userCfg, []byte("user: mine\n"), 0o644))
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(userCfg, old, old))
	startRunnerRTPending(t, f, nil)
	helloOf(t, f.cp)
	apply := func(p apigen.LinkCtlOmpHomeApplyParams) {
		t.Helper()
		var res apigen.LinkCtlOkResult
		require.NoError(t, f.cp.call(t, string(apigen.MethodOmpHomeApply), p, &res))
		assert.True(t, res.Ok)
	}
	base := filepath.Join(f.home, ".oryn", "omp", "agent")
	assert.Equal(t, base, f.rt.ompAgentDir())

	apply(apigen.LinkCtlOmpHomeApplyParams{
		OmpHome:       []apigen.LinkCtlOmpHomeFile{ompFile("config.yml", "central"), ompFile("skills/x/SKILL.md", "x")},
		ProjectConfig: &apigen.LinkCtlProjectConfig{Files: []apigen.LinkCtlOmpHomeFile{ompFile("AGENTS.md", "p")}},
		SSH:           sshCfg(apigen.LinkCtlSSHHost{Name: "web", Description: "root@10.0.0.1:22"}),
	})
	assert.Equal(t, "central", readFile(t, filepath.Join(base, "config.yml")))
	assert.Equal(t, "x", readFile(t, filepath.Join(base, "skills", "x", "SKILL.md")))
	assert.NoDirExists(t, filepath.Join(f.home, ".oryn", "project-config"))
	assert.JSONEq(t, `["config.yml","skills/x/SKILL.md"]`, readFile(t, filepath.Join(f.home, ".oryn", "omp-home.manifest.json")))

	port := f.rt.sshPort()
	require.NotZero(t, port)
	assert.Contains(t, readFile(t, filepath.Join(base, "ssh.json")), fmt.Sprintf(`"port": %d`, port))
	assert.Contains(t, readFile(t, filepath.Join(base, "ssh.json")), `"web"`)
	kh := filepath.Join(f.home, ".oryn", "omp", "ssh", "known_hosts")
	assert.Equal(t, fmt.Sprintf("[127.0.0.1]:%d %s oryn-ssh-gateway\n", port, testGatewayKey), readFile(t, kh))
	fi, err := os.Stat(filepath.Dir(kh))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
	auth := fmt.Sprintf("  IdentityFile ${ORYN_SSH_KEY}\n  IdentitiesOnly yes\n  UserKnownHostsFile \"%s\"\n", kh)
	wantCfg := fmt.Sprintf("# 由 Oryn 生成，每次下发都会重写\n\nHost web\n  HostName 127.0.0.1\n  Port %d\n  User web\n%s\nHost 127.0.0.1\n%s", port, auth, auth)
	assert.Equal(t, wantCfg, readFile(t, filepath.Join(f.home, ".oryn", "ssh", "config")))
	assert.Equal(t, wantCfg, readFile(t, filepath.Join(f.home, ".oryn", "omp", "ssh", "config")), "Core 隔离模式的 -F 与 bash 用同一份")
	assert.NoDirExists(t, filepath.Join(f.home, ".ssh"))

	// omp 自己写的文件；第二次下发不再带 skills/x：只删它。
	require.NoError(t, os.WriteFile(filepath.Join(base, "agent.db"), []byte("omp"), 0o644))
	apply(apigen.LinkCtlOmpHomeApplyParams{OmpHome: []apigen.LinkCtlOmpHomeFile{ompFile("config.yml", "central2")}})
	assert.Equal(t, "central2", readFile(t, filepath.Join(base, "config.yml")))
	assert.NoFileExists(t, filepath.Join(base, "skills", "x", "SKILL.md"))
	assert.FileExists(t, filepath.Join(base, "agent.db"))
	assert.FileExists(t, runnerLibrarySkillPath(f.home), "runner 自带的技能不在清单里，不被清理")
	assert.FileExists(t, filepath.Join(base, "ssh.json"), "不带 ssh 时不动 SSH 配置")

	assert.Equal(t, "user: mine\n", readFile(t, userCfg))
	fi, err = os.Stat(userCfg)
	require.NoError(t, err)
	assert.True(t, fi.ModTime().Equal(old), "用户的 ~/.omp/agent/config.yml 未被触碰")
	entries, err := os.ReadDir(filepath.Join(f.home, ".omp", "agent"))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "~/.omp/agent 里只有用户自己的文件")
}

// 出站通道启动即常驻：模型中转与记忆的请求都经 link 的 egress 流原样转给控制面（放行由控制面决定）。
func TestRunnerEgressAlwaysOn(t *testing.T) {
	f := newRunnerHome(t)
	egress := freeAddr(t)
	startRunnerRT(t, f, func(c *Config) { c.EgressAddr = egress })
	h := helloOf(t, f.cp)
	for _, feat := range []string{"memory", "omphome-apply", "ssh"} {
		assert.Contains(t, h.Features, feat)
	}
	for _, path := range []string{"/llm/anthropic/v1/messages", "/memory/t/" + thread1 + "/v1/default/banks/x"} {
		req, err := http.NewRequest(http.MethodGet, "http://"+egress+path, nil)
		require.NoError(t, err)
		req.Header.Set("X-Test", "runner")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err, path)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		assert.Equal(t, "egress GET "+path+" runner", string(body))
	}
}

// 出站端口被占用：runner 启动失败并提示 ORYN_EGRESS_ADDR；实例锁随之释放。
func TestRunnerEgressPortBusy(t *testing.T) {
	f := newRunnerHome(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = busy.Close() })
	cp := newFakeCP(t)
	cp.token.Store(testRunnerToken)
	cfg := Config{URL: cp.srv.URL, Token: testRunnerToken, Home: f.home, AgentBin: fakeompBin, EgressAddr: busy.Addr().String(), SSHAddr: "127.0.0.1:0", Runner: &RunnerConfig{Dirs: []string{f.work}}}
	_, err = New(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ORYN_EGRESS_ADDR")

	cfg.EgressAddr = "127.0.0.1:0"
	rt, err := New(cfg)
	require.NoError(t, err, "锁已释放")
	_ = rt.egressLn.Close()
	_ = rt.sshLn.Close()
	rt.runner.unlock()
}

// ORYN_EGRESS_ADDR 改了监听地址：下发的 models.yml 里按默认出站地址渲染的 /llm/ baseUrl 前缀改写为实际地址（其余内容、
// 其它文件不动，清单照常，重复下发结果相同），按改写后的 baseUrl 发出的请求经 link 的 egress 流转给控制面。
func TestRunnerEgressAddrRewritesModels(t *testing.T) {
	f := newRunnerHome(t)
	egress := freeAddr(t)
	require.NotEqual(t, agentcfg.EgressAddr, egress)
	startRunnerRTPending(t, f, func(c *Config) { c.EgressAddr = egress })
	helloOf(t, f.cp)
	def := "http://" + agentcfg.EgressAddr
	models := "providers:\n  anth:\n    baseUrl: " + def + "/llm/anth/v1\n    apiKey: \"oryn-ph-sid1\"\n  other:\n    baseUrl: " + def + "/other\n"
	want := "providers:\n  anth:\n    baseUrl: http://" + egress + "/llm/anth/v1\n    apiKey: \"oryn-ph-sid1\"\n  other:\n    baseUrl: " + def + "/other\n"
	cfgYml := "note: " + def + "/llm/x\n"
	base := filepath.Join(f.home, ".oryn", "omp", "agent")
	for range 2 {
		require.NoError(t, f.cp.call(t, string(apigen.MethodOmpHomeApply), apigen.LinkCtlOmpHomeApplyParams{
			OmpHome: []apigen.LinkCtlOmpHomeFile{ompFile("config.yml", cfgYml), ompFile(agentcfg.ModelsFile, models)},
		}, nil))
		assert.Equal(t, want, readFile(t, filepath.Join(base, agentcfg.ModelsFile)))
		assert.Equal(t, cfgYml, readFile(t, filepath.Join(base, "config.yml")), "只改写 models.yml")
		assert.JSONEq(t, `["config.yml","models.yml"]`, readFile(t, filepath.Join(f.home, ".oryn", "omp-home.manifest.json")))
	}

	req, err := http.NewRequest(http.MethodGet, "http://"+egress+"/llm/anth/v1/messages", nil)
	require.NoError(t, err)
	req.Header.Set("X-Test", "runner")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, "egress GET /llm/anth/v1/messages runner", string(body))
}

// runner 的出站地址只能是带端口的回环地址：其余地址启动失败并指明 ORYN_EGRESS_ADDR（在拿实例锁之前）。
func TestRunnerEgressAddrLoopbackOnly(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:0", "127.1.2.3:7811", "[::1]:7811"} {
		assert.NoError(t, checkRunnerEgressAddr(ok), ok)
	}
	for _, bad := range []string{"0.0.0.0:7811", "192.168.1.2:7811", "[::]:7811", "localhost:7811", "127.0.0.1", ":7811"} {
		assert.Error(t, checkRunnerEgressAddr(bad), bad)
	}

	f := newRunnerHome(t)
	cp := newFakeCP(t)
	cp.token.Store(testRunnerToken)
	cfg := Config{URL: cp.srv.URL, Token: testRunnerToken, Home: f.home, AgentBin: fakeompBin, EgressAddr: "0.0.0.0:0", SSHAddr: "127.0.0.1:0", Runner: &RunnerConfig{Dirs: []string{f.work}}}
	_, err := New(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ORYN_EGRESS_ADDR")
	assert.Contains(t, err.Error(), "loopback")

	cfg.EgressAddr = "127.0.0.1:0"
	rt, err := New(cfg)
	require.NoError(t, err, "没有留下实例锁")
	_ = rt.egressLn.Close()
	_ = rt.sshLn.Close()
	rt.runner.unlock()
}

// runner 的 SSH 网关端口：连接变成 link 上的 ssh 流，两个方向的字节都能到达。
func TestRunnerSSHPortBridgesToLink(t *testing.T) {
	f := newRunnerHome(t)
	startRunnerRT(t, f, nil)
	require.NotNil(t, f.rt.sshLn)
	c, err := net.Dial("tcp", f.rt.sshLn.Addr().String())
	require.NoError(t, err)
	defer c.Close()
	_, err = c.Write([]byte("SSH-2.0-test\r\n"))
	require.NoError(t, err)
	var st *link.Stream
	select {
	case st = <-f.cp.sshs:
	case <-time.After(10 * time.Second):
		t.Fatal("没有收到 ssh 流")
	}
	defer st.Close()
	line, err := bufio.NewReader(st).ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "SSH-2.0-test\r\n", line)
	_, err = st.Write([]byte("SSH-2.0-Oryn\r\n"))
	require.NoError(t, err)
	got := make([]byte, len("SSH-2.0-Oryn\r\n"))
	require.NoError(t, c.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err = io.ReadFull(c, got)
	require.NoError(t, err)
	assert.Equal(t, "SSH-2.0-Oryn\r\n", string(got))
}

// runner.sock 的 library.put：只收调用方线程工作目录内的普通文件（目录外、目录、文件或目录符号链接逃逸、相对路径、
// 没有 agent 的线程一律 invalid_params，且不转给控制面）；成功时只把该文件登记为可经 library.fetch 取回，结束后撤销。
func TestRunnerLibraryPut(t *testing.T) {
	f := newRunnerHome(t)
	startRunnerRT(t, f, nil)
	require.NoError(t, f.cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{ThreadID: thread1, Cwd: f.work, InitFrames: initFrames(thread1)}, nil))
	f.cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))

	report := filepath.Join(f.work, "report.txt")
	require.NoError(t, os.WriteFile(report, []byte("hi"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(f.work, "sub"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(f.outside, "secret.txt"), filepath.Join(f.work, "leak")))
	require.NoError(t, os.Symlink(f.outside, filepath.Join(f.work, "out")))

	var (
		mu     sync.Mutex
		seen   []apigen.LinkCtlLibraryPutParams
		staged map[string]int
	)
	f.cp.extra = func(method string, params json.RawMessage) (any, bool, error) {
		if method != string(apigen.MethodLibraryPut) {
			return nil, false, nil
		}
		var p apigen.LinkCtlLibraryPutParams
		_ = json.Unmarshal(params, &p)
		f.rt.lib.mu.Lock()
		now := maps.Clone(f.rt.lib.staged)
		f.rt.lib.mu.Unlock()
		mu.Lock()
		seen, staged = append(seen, p), now
		mu.Unlock()
		return map[string]any{"id": "af_1", "name": "r.txt", "size": 2}, true, nil
	}
	sock := orbsock.RunnerPath(f.home)
	waitSock(t, sock)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	put := func(thread, path string) (json.RawMessage, error) {
		name := "r.txt"
		return orbsock.Call(ctx, sock, orbsock.MethodLibraryPut, apigen.LinkCtlLibraryPutParams{ThreadID: thread, Path: path, Name: &name}, nil)
	}

	for _, c := range []struct{ thread, path, why string }{
		{thread1, filepath.Join(f.outside, "secret.txt"), "outside the working directory"},
		{thread1, f.work, "the working directory itself"},
		{thread1, filepath.Join(f.work, "sub"), "a directory"},
		{thread1, filepath.Join(f.work, "leak"), "a file symlink out"},
		{thread1, filepath.Join(f.work, "out", "secret.txt"), "a directory symlink out"},
		{thread1, "report.txt", "a relative path"},
		{thread1, filepath.Join(f.work, "missing.txt"), "a missing file"},
		{projThread2, report, "a thread without an agent"},
	} {
		_, err := put(c.thread, c.path)
		var se *orbsock.Error
		require.ErrorAs(t, err, &se, c.why)
		assert.Equal(t, orbsock.CodeInvalidParams, se.Code, c.why)
	}
	mu.Lock()
	assert.Empty(t, seen, "rejected requests never reach the control plane")
	mu.Unlock()

	raw, err := put(thread1, report)
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"af_1","name":"r.txt","size":2}`, string(raw))
	mu.Lock()
	require.Len(t, seen, 1)
	assert.Equal(t, report, seen[0].Path)
	assert.Equal(t, thread1, seen[0].ThreadID)
	assert.Equal(t, map[string]int{report: 1}, staged, "只登记这一个文件")
	mu.Unlock()
	f.rt.lib.mu.Lock()
	assert.Empty(t, f.rt.lib.staged, "请求结束后撤销登记")
	f.rt.lib.mu.Unlock()
}
