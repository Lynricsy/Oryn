package orbdrt

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// M11a 审查修复（切片 B）的回归测试。

// ---- OW-1：dockerd API 就绪屏障 ----

// dockerdRuntime 起一个启用嵌套 Docker 的运行时；waiting 在 setup / resume 开始等待 dockerd 时收到信号。
func dockerdRuntime(t *testing.T, readyTimeout time.Duration) (*fakeDocker, *fakeCP, *Runtime, chan struct{}) {
	t.Helper()
	fd := newFakeDocker(t)
	require.NoError(t, os.WriteFile(filepath.Join(fd.dir, "notready"), nil, 0o644))
	waiting := make(chan struct{}, 4)
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, func(c *Config) {
		c.NestedDocker = true
		c.DockerdBackoff = func(int) time.Duration { return 10 * time.Millisecond }
		c.DockerdProbeInterval = 10 * time.Millisecond
		c.DockerdReadyTimeout = readyTimeout
		c.OnDockerdWait = func() { waiting <- struct{}{} }
	})
	return fd, cp, rt, waiting
}

// dockerSetupScript 先留下运行痕迹，再要求 docker 可用。
const dockerSetupScript = "touch \"$HOME/ran\"\ndocker info >/dev/null && touch \"$HOME/docker-ok\"\n"

func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestDockerdReadyBarrier：dockerd 已启动但 API 还不可用时收到的 setup.run 先等就绪，脚本里的 docker 命令成功。
func TestDockerdReadyBarrier(t *testing.T) {
	fd, cp, rt, waiting := dockerdRuntime(t, time.Minute)
	type out struct {
		res apigen.LinkCtlSetupRunResult
		err error
	}
	done := make(chan out, 1)
	go func() {
		var res apigen.LinkCtlSetupRunResult
		err := cp.call(t, "setup.run", apigen.LinkCtlSetupRunParams{Scripts: apigen.LinkCtlSetupScripts{Setup: new(dockerSetupScript)}}, &res)
		done <- out{res, err}
	}()
	awaitSignal(t, waiting, "setup to wait for dockerd")
	assert.NoFileExists(t, filepath.Join(rt.home, "ran"), "scripts must not start before dockerd is ready")
	require.NoError(t, os.Remove(filepath.Join(fd.dir, "notready")))
	o := <-done
	require.NoError(t, o.err)
	assert.True(t, o.res.Ok, "%v", deref(o.res.SetupError))
	assert.True(t, o.res.SetupRan)
	assert.FileExists(t, filepath.Join(rt.home, "docker-ok"))

	// 已就绪：之后的 resume 不再等待（不触发等待钩子）。
	from := cp.notifCount()
	var res apigen.LinkCtlSetupRunResult
	require.NoError(t, cp.call(t, "resume.run", apigen.LinkCtlSetupRunParams{
		Scripts: apigen.LinkCtlSetupScripts{Resume: new("docker info >/dev/null && touch \"$HOME/resume-ok\"\n")},
	}, &res))
	_, n := waitResult(t, cp, from, apigen.SetupPhaseResume)
	assert.True(t, n.Ok, "%v", deref(n.Error))
	assert.FileExists(t, filepath.Join(rt.home, "resume-ok"))
	assert.Empty(t, waiting)
}

// TestDockerdReadyTimeout：dockerd 一直不就绪时 setup 与 resume 有界失败，错误指明 dockerd，脚本不运行。
func TestDockerdReadyTimeout(t *testing.T) {
	_, cp, rt, _ := dockerdRuntime(t, 200*time.Millisecond)
	res, n := setupCall(t, cp, apigen.LinkCtlSetupRunParams{Scripts: apigen.LinkCtlSetupScripts{Setup: new(dockerSetupScript)}})
	assert.False(t, res.Ok)
	require.NotNil(t, res.SetupError)
	assert.Contains(t, *res.SetupError, "dockerd not ready after 200ms")
	assert.Contains(t, *res.SetupError, "Cannot connect to the Docker daemon")
	assert.False(t, n.Ok)
	assert.NoFileExists(t, filepath.Join(rt.home, "ran"))
	assert.NoFileExists(t, rt.setupMarker())

	from := cp.notifCount()
	require.NoError(t, cp.call(t, "resume.run", apigen.LinkCtlSetupRunParams{
		Scripts: apigen.LinkCtlSetupScripts{Resume: new("touch \"$HOME/resume-ran\"\n")},
	}, &res))
	_, n = waitResult(t, cp, from, apigen.SetupPhaseResume)
	assert.False(t, n.Ok)
	assert.Contains(t, deref(n.Error), "dockerd not ready")
	assert.NoFileExists(t, filepath.Join(rt.home, "resume-ran"))
}

// TestDockerdReadyShutdown：等待 dockerd 期间 Shutdown 立即解除等待（不等到超时）。
func TestDockerdReadyShutdown(t *testing.T) {
	_, cp, rt, waiting := dockerdRuntime(t, time.Hour)
	done := make(chan apigen.LinkCtlSetupRunResult, 1)
	go func() {
		var res apigen.LinkCtlSetupRunResult
		_ = cp.call(t, "setup.run", apigen.LinkCtlSetupRunParams{Scripts: apigen.LinkCtlSetupScripts{Setup: new(dockerSetupScript)}}, &res)
		done <- res
	}()
	awaitSignal(t, waiting, "setup to wait for dockerd")
	go rt.Shutdown(time.Second)
	res := <-done
	assert.False(t, res.Ok)
	assert.Contains(t, deref(res.SetupError), "shutting down")
	assert.NoFileExists(t, filepath.Join(rt.home, "ran"))
}

// ---- OW-2：merge 之后核对 HEAD ----

// TestRepoUpdateHeadAhead：工作副本 HEAD 领先于基线（setup 留下了未推送的本地提交，含分离 HEAD），
// merge --ff-only 会报 Already up to date；更新必须真正切到基线提交，第二次更新不再动、不重跑 setup。
func TestRepoUpdateHeadAhead(t *testing.T) {
	for _, detached := range []bool{false, true} {
		t.Run(map[bool]string{false: "branch", true: "detached"}[detached], func(t *testing.T) {
			cp := newFakeCP(t)
			rt := startRuntime(t, cp, nil)
			bare, _, c1 := upstreamRepo(t)
			params := apigen.LinkCtlSetupRunParams{Repo: &apigen.LinkCtlSetupRepo{URL: bare, Update: new(true)}}
			setupCall(t, cp, params)
			workdir := filepath.Join(rt.home, "workspace", "repo")
			if detached {
				gitRun(t, workdir, "checkout", "-q", "--detach")
			}
			require.NoError(t, os.WriteFile(filepath.Join(workdir, "generated.txt"), []byte("x\n"), 0o644))
			gitRun(t, workdir, "add", "-A")
			gitRun(t, workdir, "commit", "-q", "-m", "local")
			local := gitOut(t, workdir, "rev-parse", "HEAD")
			require.NotEqual(t, c1, local)

			res, n := setupCall(t, cp, params)
			assert.Equal(t, c1, gitOut(t, workdir, "rev-parse", "HEAD"))
			assert.Equal(t, c1, deref(res.BaseCommit))
			assert.True(t, deref(res.RepoUpdated))
			assert.True(t, res.SetupRan)
			assert.True(t, deref(res.SetupChanged), "dropping a local commit is not a fast-forward")
			assert.True(t, deref(n.SetupChanged))
			assert.NoFileExists(t, filepath.Join(workdir, "generated.txt"))

			res, _ = setupCall(t, cp, params)
			assert.False(t, deref(res.RepoUpdated))
			assert.False(t, res.SetupRan)
			assert.False(t, deref(res.SetupChanged))
			assert.Equal(t, c1, deref(res.BaseCommit))
		})
	}
}

// ---- S-P2-1：setupChanged ----

// TestRepoUpdateSetupChanged：快进且 setup 输入不变 → false；非快进（force push）→ true；
// 快进但改了 .oryn/setup → true。完整克隆与浅克隆（file:// 才真正浅）都覆盖。
func TestRepoUpdateSetupChanged(t *testing.T) {
	for _, shallow := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "shallow"}[shallow], func(t *testing.T) {
			cp := newFakeCP(t)
			rt := startRuntime(t, cp, nil)
			bare, src, c1 := upstreamRepo(t)
			url := bare
			if shallow {
				url = "file://" + bare
			}
			params := apigen.LinkCtlSetupRunParams{Repo: &apigen.LinkCtlSetupRepo{URL: url, Shallow: shallow, Update: new(true)}}
			res, _ := setupCall(t, cp, params)
			assert.False(t, deref(res.SetupChanged), "fresh clone")
			workdir := filepath.Join(rt.home, "workspace", "repo")
			step := func(want string, changed bool) {
				t.Helper()
				res, n := setupCall(t, cp, params)
				assert.Equal(t, want, gitOut(t, workdir, "rev-parse", "HEAD"))
				assert.True(t, deref(res.RepoUpdated))
				assert.Equal(t, changed, deref(res.SetupChanged))
				assert.Equal(t, changed, deref(n.SetupChanged))
			}

			step(pushCommit(t, bare, src, "2\n"), false)

			// force push：基线换成不以当前 HEAD 为祖先的提交，setup 输入没变也算变化。
			gitRun(t, src, "reset", "-q", "--hard", c1)
			require.NoError(t, os.WriteFile(filepath.Join(src, "a.txt"), []byte("3\n"), 0o644))
			gitRun(t, src, "commit", "-q", "-am", "rewrite")
			gitRun(t, src, "push", "-q", "-f", bare, "HEAD:refs/heads/main")
			step(gitOut(t, src, "rev-parse", "HEAD"), true)

			// 快进，但 .oryn/setup 变了。
			require.NoError(t, os.WriteFile(filepath.Join(src, ".oryn", "setup"),
				[]byte("#!/bin/sh\necho ran >> \"$HOME/setup-count.txt\"\necho v2 >/dev/null\n"), 0o755))
			gitRun(t, src, "commit", "-q", "-am", "setup v2")
			gitRun(t, src, "push", "-q", bare, "HEAD:refs/heads/main")
			step(gitOut(t, src, "rev-parse", "HEAD"), true)

			// 只去掉可执行位也算（决定脚本是否运行）。
			gitRun(t, src, "update-index", "--chmod=-x", ".oryn/setup")
			require.NoError(t, os.Chmod(filepath.Join(src, ".oryn", "setup"), 0o644))
			gitRun(t, src, "commit", "-q", "-m", "setup not executable")
			gitRun(t, src, "push", "-q", bare, "HEAD:refs/heads/main")
			step(gitOut(t, src, "rev-parse", "HEAD"), true)

			step(pushCommit(t, bare, src, "4\n"), false)
		})
	}
}

// ---- S-P2-5：gitRev 校验 ----

// TestGitRevRejectsNonHex：可信 PATH 里的 git 吐出控制字符时 baseCommit 为空，不会原样上报。
func TestGitRevRejectsNonHex(t *testing.T) {
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"+
		"for a in \"$@\"; do [ \"$a\" = rev-parse ] && { printf '\\033[2J\\033[Hdeadbeef\\n'; exit 0; }; done\n"+
		"exec "+realGit+" \"$@\"\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	bare, _, _ := upstreamRepo(t)
	res, n := setupCall(t, cp, apigen.LinkCtlSetupRunParams{Repo: &apigen.LinkCtlSetupRepo{URL: bare}})
	assert.True(t, res.Ok)
	assert.Nil(t, res.BaseCommit)
	assert.Nil(t, n.BaseCommit)
	assert.Empty(t, rt.gitRev(context.Background(), filepath.Join(rt.home, "workspace", "repo"), os.Environ(), "HEAD"))

	assert.True(t, commitHashRe.MatchString(strings.Repeat("a", 40)))
	assert.True(t, commitHashRe.MatchString(strings.Repeat("0", 64)))
	for _, bad := range []string{strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("A", 40), "\x1b[2J" + strings.Repeat("a", 40)} {
		assert.False(t, commitHashRe.MatchString(bad), "%q", bad)
	}
}

// ---- S-P1-1 纵深防御：子进程按可信目录解析 ----

func TestBinResolver(t *testing.T) {
	home := t.TempDir()
	trusted, sibling := t.TempDir(), home+"-sibling"
	for _, d := range []string{filepath.Join(home, ".local", "bin"), trusted, sibling} {
		require.NoError(t, os.MkdirAll(d, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "tool"), []byte("#!/bin/sh\n"), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(home, ".local", "bin", "onlyhome"), []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(trusted, "noexec"), []byte("#!/bin/sh\n"), 0o644))
	path := strings.Join([]string{filepath.Join(home, ".local", "bin"), "", "relative/bin", home, home + "/x/../.local/bin", trusted, sibling}, string(os.PathListSeparator))
	b := newBinResolver(path, home)
	assert.Equal(t, []string{trusted, sibling}, b.dirs)

	p, err := b.look("tool")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(trusted, "tool"), p)
	_, err = b.look("onlyhome")
	assert.Error(t, err)
	_, err = b.look("noexec")
	assert.Error(t, err)
	p, err = b.look("/abs/omp")
	require.NoError(t, err)
	assert.Equal(t, "/abs/omp", p)

	// 解析失败的命令 Start 时报错，不会退回进程 PATH（那里第一个就是 $HOME/.local/bin）。
	cmd := b.command(context.Background(), "onlyhome")
	assert.ErrorContains(t, cmd.Run(), "trusted PATH")
}

// TestTrustedBinaries：$HOME/.local/bin 排在 PATH 最前且放着同名的 omp / git / sudo / docker / dockerd / bash 时，
// orbd 自己起的子进程一个也不经过它们（版本探测、git、clone、dockerd 监督与探测、登录环境、脚本解释器）。
// 脚本内部命令按脚本自己的 PATH 解析，不在此列。
func TestTrustedBinaries(t *testing.T) {
	home := t.TempDir()
	localBin := filepath.Join(home, ".local", "bin")
	require.NoError(t, os.MkdirAll(localBin, 0o755))
	names := []string{"omp", "git", "sudo", "docker", "dockerd", "bash"}
	for _, name := range names {
		require.NoError(t, os.WriteFile(filepath.Join(localBin, name),
			[]byte("#!/bin/sh\ntouch '"+filepath.Join(home, "pwned-"+name)+"'\nexit 1\n"), 0o755))
	}
	fd := newFakeDocker(t)
	bare, _, c1 := upstreamRepo(t) // 先于改 PATH：测试自己的 git 调用走真实 git
	t.Setenv("PATH", localBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, func(c *Config) {
		c.Home = home
		c.AgentBin = "omp"
		c.NestedDocker = true
		c.DockerdBackoff = func(int) time.Duration { return 10 * time.Millisecond }
		c.DockerdProbeInterval = 10 * time.Millisecond
	})
	res, _ := setupCall(t, cp, apigen.LinkCtlSetupRunParams{
		Repo:     &apigen.LinkCtlSetupRepo{URL: bare},
		GitHosts: []string{"example.com"},
		Scripts:  apigen.LinkCtlSetupScripts{Setup: new("echo setup >/dev/null\n")},
	})
	assert.True(t, res.Ok, "%v", deref(res.SetupError))
	assert.Equal(t, c1, deref(res.BaseCommit))
	assert.Equal(t, 1, fd.starts())
	assert.Contains(t, fd.read("calls.log"), "sudo dockerd\n")
	assert.NotContains(t, fd.read("calls.log"), "relative program")
	rt.Shutdown(time.Second)
	for _, name := range names {
		assert.NoFileExists(t, filepath.Join(home, "pwned-"+name))
	}
}

// ---- S-P1-2：deferResume ----

// TestSetupDeferResume：deferResume 时 setup.run 不跑 resume、不发 resume 结果；之后单独的 resume.run 才跑。
func TestSetupDeferResume(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	assert.Contains(t, helloFeatures(t, cp), "setup-defer-resume")
	scripts := apigen.LinkCtlSetupScripts{Setup: new("touch \"$HOME/setup-ran\"\n"), Resume: new("touch \"$HOME/resume-ran\"\n")}
	res, n := setupCall(t, cp, apigen.LinkCtlSetupRunParams{Scripts: scripts, DeferResume: new(true)})
	assert.True(t, res.Ok)
	assert.True(t, res.SetupRan)
	assert.True(t, n.Ok)
	assert.FileExists(t, filepath.Join(rt.home, "setup-ran"))
	assert.NoFileExists(t, filepath.Join(rt.home, "resume-ran"))
	for _, r := range cp.results() {
		assert.NotEqual(t, apigen.SetupPhaseResume, r.Phase, "no resume result before resume.run")
	}

	from := cp.notifCount()
	require.NoError(t, cp.call(t, "resume.run", apigen.LinkCtlSetupRunParams{Scripts: scripts}, &res))
	_, n = waitResult(t, cp, from, apigen.SetupPhaseResume)
	assert.True(t, n.Ok)
	assert.FileExists(t, filepath.Join(rt.home, "resume-ran"))
}

// ---- TLS 客户端 ----

func certPEM(der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// unrelatedCAPEM 生成一张与 httptest 证书无关的自签 CA。
func unrelatedCAPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "unrelated test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return certPEM(der)
}

// TestLinkTLSPinnedCA：ORYN_ORB_CA_PEM 是服务端证书时 link 经 TLS 建立（收到 hello）。
func TestLinkTLSPinnedCA(t *testing.T) {
	cp := newFakeCPTLS(t)
	require.True(t, strings.HasPrefix(cp.srv.URL, "https://"))
	startRuntime(t, cp, func(c *Config) { c.OrbCAPEM = certPEM(cp.srv.Certificate().Raw) })
	assert.Contains(t, helloFeatures(t, cp), "setup-defer-resume")
}

// TestOrbHTTPClient：给了 CA 时 RootCAs 恰好就是这张 CA（不合并系统根），换一张无关的 CA 拨号失败；
// 没给（或只有空白，快照镜像里被清空成 ""）时用系统根；PEM 无效时 New 失败。
func TestOrbHTTPClient(t *testing.T) {
	cp := newFakeCPTLS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hc, err := orbHTTPClient(certPEM(cp.srv.Certificate().Raw))
	require.NoError(t, err)
	want := x509.NewCertPool()
	want.AddCert(cp.srv.Certificate())
	assert.True(t, want.Equal(hc.Transport.(*http.Transport).TLSClientConfig.RootCAs), "only the pinned CA")

	other, err := orbHTTPClient(unrelatedCAPEM(t))
	require.NoError(t, err)
	_, err = link.DialClient(ctx, link.URL(cp.srv.URL), testToken, other)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "certificate signed by unknown authority")

	for _, empty := range []string{"", "  \n\t"} {
		hc, err := orbHTTPClient(empty)
		require.NoError(t, err)
		assert.Nil(t, hc, "system roots")
	}
	rt := startRuntime(t, cp, func(c *Config) { c.OrbCAPEM = "\n" })
	assert.Nil(t, rt.httpClient)
	// 系统根不认识 httptest 的证书：同一地址拨号失败，说明确实走了证书校验而不是跳过。
	_, err = link.DialClient(ctx, link.URL(cp.srv.URL), testToken, rt.httpClient)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "certificate")

	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte{1, 2, 3}}))
	good := certPEM(cp.srv.Certificate().Raw)
	for name, bad := range map[string]string{
		"garbage":          "not a pem",
		"private key":      keyPEM,
		"broken cert":      certPEM([]byte{1, 2, 3}),
		"trailing garbage": good + "junk\n",
	} {
		_, err := New(Config{URL: "https://cp.invalid", OrbID: "orb-x", Token: "oot_x", Home: t.TempDir(), EgressAddr: "127.0.0.1:0", OrbCAPEM: bad})
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "ORYN_ORB_CA_PEM", name)
	}
}

func TestConfigFromEnvOrbCA(t *testing.T) {
	t.Setenv("ORYN_URL", "https://x")
	t.Setenv("ORYN_ORB_ID", "orb-x")
	t.Setenv("ORYN_ORB_TOKEN", "oot_x")
	t.Setenv("ORYN_ORB_CA_PEM", "PEM")
	cfg, err := ConfigFromEnv("v")
	require.NoError(t, err)
	assert.Equal(t, "PEM", cfg.OrbCAPEM)
}
