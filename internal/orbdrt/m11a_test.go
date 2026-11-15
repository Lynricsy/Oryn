package orbdrt

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// ---- repo.update（M11a 2.3）----

// gitOut 在 dir 里执行 git 并返回去掉首尾空白的 stdout。
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
	out, err := cmd.Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

// upstreamRepo 建一个本地裸仓库 origin.git 与推送用的工作副本 src；src 的 .oryn/setup 每次运行在
// $HOME/setup-count.txt 追加一行。返回裸仓库路径、工作副本路径与首个提交。
func upstreamRepo(t *testing.T) (bare, src, first string) {
	t.Helper()
	root := t.TempDir()
	bare, src = filepath.Join(root, "origin.git"), filepath.Join(root, "src")
	gitRun(t, root, "init", "-q", "--bare", bare)
	gitRun(t, root, "init", "-q", src)
	require.NoError(t, os.MkdirAll(filepath.Join(src, ".oryn"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, ".oryn", "setup"),
		[]byte("#!/bin/sh\necho ran >> \"$HOME/setup-count.txt\"\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "a.txt"), []byte("1\n"), 0o644))
	gitRun(t, src, "add", "-A")
	gitRun(t, src, "commit", "-q", "-m", "one")
	gitRun(t, src, "push", "-q", bare, "HEAD:refs/heads/main")
	gitRun(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	return bare, src, gitOut(t, src, "rev-parse", "HEAD")
}

// pushCommit 在 src 上加一个提交并推到 bare 的 main，返回新提交。
func pushCommit(t *testing.T, bare, src, content string) string {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(src, "a.txt"), []byte(content), 0o644))
	gitRun(t, src, "commit", "-q", "-am", "next")
	gitRun(t, src, "push", "-q", bare, "HEAD:refs/heads/main")
	return gitOut(t, src, "rev-parse", "HEAD")
}

// setupCall 发一次 setup.run，返回应答与本次的 setup 阶段 setup.result 通知。
func setupCall(t *testing.T, cp *fakeCP, params apigen.LinkCtlSetupRunParams) (apigen.LinkCtlSetupRunResult, apigen.LinkCtlSetupResultParams) {
	t.Helper()
	from := cp.notifCount()
	var res apigen.LinkCtlSetupRunResult
	require.NoError(t, cp.call(t, "setup.run", params, &res))
	_, n := waitResult(t, cp, from, apigen.SetupPhaseSetup)
	return res, n
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func TestRepoUpdate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		shallow bool
		branch  *string
	}{
		{name: "full clone, default branch"},
		{name: "shallow clone, named branch", shallow: true, branch: new("main")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp := newFakeCP(t)
			rt := startRuntime(t, cp, nil)
			bare, src, c1 := upstreamRepo(t)
			url := bare
			if tc.shallow {
				url = "file://" + bare // 本地路径的 clone 会忽略 --depth
			}
			params := apigen.LinkCtlSetupRunParams{
				Workdir: "~/workspace/repo",
				Repo:    &apigen.LinkCtlSetupRepo{URL: url, Shallow: tc.shallow, Branch: tc.branch, Update: new(true)},
			}
			workdir := filepath.Join(rt.home, "workspace", "repo")
			marker := filepath.Join(rt.home, ".oryn", "setup.done")
			count := func() int {
				b, _ := os.ReadFile(filepath.Join(rt.home, "setup-count.txt"))
				return strings.Count(string(b), "ran\n")
			}

			// 首次：clone 后直接跑 setup，不做 update。
			res, n := setupCall(t, cp, params)
			assert.True(t, res.Ok)
			assert.True(t, res.SetupRan)
			assert.False(t, deref(res.Prepared))
			assert.False(t, deref(res.RepoUpdated))
			assert.Equal(t, c1, deref(res.BaseCommit))
			assert.False(t, deref(n.Prepared))
			assert.Equal(t, c1, deref(n.BaseCommit))
			assert.Equal(t, 1, count())
			if tc.shallow {
				assert.Equal(t, "true", gitOut(t, workdir, "rev-parse", "--is-shallow-repository"))
			}

			// HEAD 未变：标记保留、不重跑 setup。
			res, n = setupCall(t, cp, params)
			assert.True(t, res.Ok)
			assert.False(t, res.SetupRan)
			assert.True(t, deref(res.Prepared))
			assert.False(t, deref(res.RepoUpdated))
			assert.Equal(t, c1, deref(res.BaseCommit))
			assert.True(t, deref(n.Prepared))
			assert.False(t, deref(n.RepoUpdated))
			assert.False(t, n.SetupRan)
			assert.FileExists(t, marker)
			assert.Equal(t, 1, count())

			// 基线有新提交：移动检出、删标记、增量重跑 setup（成功后标记重新写出）。
			c2 := pushCommit(t, bare, src, "2\n")
			res, n = setupCall(t, cp, params)
			assert.True(t, res.Ok)
			assert.True(t, res.SetupRan)
			assert.True(t, deref(res.Prepared))
			assert.True(t, deref(res.RepoUpdated))
			assert.Equal(t, c2, deref(res.BaseCommit))
			assert.True(t, deref(n.Prepared))
			assert.True(t, deref(n.RepoUpdated))
			assert.True(t, n.SetupRan)
			// 快进且 setup 输入没变（浅克隆要补 shallow-since 才判得出快进）：可以在旧快照上增量刷新。
			assert.False(t, deref(res.SetupChanged))
			assert.False(t, deref(n.SetupChanged))
			assert.Equal(t, c2, deref(n.BaseCommit))
			assert.Equal(t, c2, gitOut(t, workdir, "rev-parse", "HEAD"))
			b, err := os.ReadFile(filepath.Join(workdir, "a.txt"))
			require.NoError(t, err)
			assert.Equal(t, "2\n", string(b))
			assert.FileExists(t, marker)
			assert.Equal(t, 2, count())

			// fetch 失败：只记日志，沿用当前提交，结果仍 ok。
			require.NoError(t, os.Rename(bare, bare+".gone"))
			res, n = setupCall(t, cp, params)
			assert.True(t, res.Ok)
			assert.Nil(t, res.SetupError)
			assert.False(t, res.SetupRan)
			assert.False(t, deref(res.RepoUpdated))
			assert.Equal(t, c2, deref(res.BaseCommit))
			assert.True(t, n.Ok)
			assert.Equal(t, 2, count())
			log, err := os.ReadFile(filepath.Join(rt.home, ".cache", "oryn", "logs", "setup.log"))
			require.NoError(t, err)
			assert.Contains(t, string(log), "repo update: fetch failed")
		})
	}
}

// TestRepoUpdateNotRequested：没有 update 时已有检出原样保留（即使基线有新提交）。
func TestRepoUpdateNotRequested(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	bare, src, c1 := upstreamRepo(t)
	params := apigen.LinkCtlSetupRunParams{Repo: &apigen.LinkCtlSetupRepo{URL: bare}}
	setupCall(t, cp, params)
	pushCommit(t, bare, src, "2\n")
	res, n := setupCall(t, cp, params)
	assert.False(t, res.SetupRan)
	assert.False(t, deref(res.RepoUpdated))
	assert.Equal(t, c1, deref(res.BaseCommit))
	assert.Equal(t, c1, deref(n.BaseCommit))
	assert.Equal(t, c1, gitOut(t, filepath.Join(rt.home, "workspace", "repo"), "rev-parse", "HEAD"))
}

// ---- 嵌套 Docker（M11a 4.3）----

// fakeDocker 在 PATH 最前面放假的 sudo / dockerd / docker：
//   - sudo 只接受绝对路径的程序（orbd 必须先解析成绝对路径），记录调用（程序名取 basename）；dockerd 换成假 dockerd；
//     pkill 只给假 dockerd 发信号；rm 只记录不执行（测试进程可能是 root，绝不能碰宿主的 /var/run/docker.pid 或真实的 dockerd）。
//   - 假 dockerd 写 pid 文件、在 events.log 记 start，收到 TERM 记 term 后退出。
//   - 假 docker：ps -q 输出 containers 文件内容，stop 清空它；info 在 notready 文件存在时像守护进程未就绪那样失败。
type fakeDocker struct{ dir string }

func newFakeDocker(t *testing.T) *fakeDocker {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	scripts := map[string]string{
		"sudo": `#!/bin/sh
[ "$1" = "-n" ] && shift
prog=$1; shift
case "$prog" in
/*) ;;
*) echo "sudo: relative program $prog" >> "$FAKE_DOCKER_DIR/calls.log"; exit 1 ;;
esac
echo "sudo ${prog##*/}${*:+ $*}" >> "$FAKE_DOCKER_DIR/calls.log"
case "${prog##*/}" in
dockerd) exec "$FAKE_DOCKER_DIR/bin/dockerd" "$@" ;;
pkill) pid=$(cat "$FAKE_DOCKER_DIR/dockerd.pid" 2>/dev/null) || exit 1; kill -s "${1#-}" "$pid" 2>/dev/null; exit 0 ;;
rm) exit 0 ;;
*) exit 1 ;;
esac
`,
		"dockerd": `#!/bin/sh
echo $$ > "$FAKE_DOCKER_DIR/dockerd.pid"
echo start >> "$FAKE_DOCKER_DIR/events.log"
trap 'echo term >> "$FAKE_DOCKER_DIR/events.log"; exit 0' TERM
while :; do sleep 1 & wait $!; done
`,
		"docker": `#!/bin/sh
echo "docker $*" >> "$FAKE_DOCKER_DIR/calls.log"
case "$1" in
ps) cat "$FAKE_DOCKER_DIR/containers" 2>/dev/null ;;
stop) : > "$FAKE_DOCKER_DIR/containers" ;;
info)
	if [ -e "$FAKE_DOCKER_DIR/notready" ]; then
		echo "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?" >&2
		exit 1
	fi
	echo 29.0.0 ;;
esac
`,
	}
	for name, body := range scripts {
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755))
	}
	t.Setenv("FAKE_DOCKER_DIR", dir)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &fakeDocker{dir: dir}
}

func (f *fakeDocker) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(f.dir, name))
	return string(b)
}

func (f *fakeDocker) starts() int { return strings.Count(f.read("events.log"), "start\n") }

func (f *fakeDocker) pid(t *testing.T) int {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSpace(f.read("dockerd.pid")))
	require.NoError(t, err)
	return pid
}

func helloFeatures(t *testing.T, cp *fakeCP) []string {
	t.Helper()
	_, raw := cp.waitNotif(t, 0, "hello", nil)
	var h apigen.LinkCtlHelloParams
	require.NoError(t, json.Unmarshal(raw, &h))
	return h.Features
}

func TestNestedDockerd(t *testing.T) {
	fd := newFakeDocker(t) // 先于 startRuntime：PATH 的恢复排在运行时清理之后
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, func(c *Config) {
		c.NestedDocker = true
		c.DockerdBackoff = func(int) time.Duration { return 10 * time.Millisecond }
	})
	feats := helloFeatures(t, cp)
	assert.Contains(t, feats, "docker")
	assert.Contains(t, feats, "repo-update")

	// 启动前清旧 pid 文件，再以 sudo 启动 dockerd。
	require.Eventually(t, func() bool { return fd.starts() == 1 }, 10*time.Second, 10*time.Millisecond)
	calls := fd.read("calls.log")
	rmAt := strings.Index(calls, "sudo rm -f -- /var/run/docker.pid /run/docker/containerd/containerd.pid\n")
	startAt := strings.Index(calls, "sudo dockerd\n")
	require.GreaterOrEqual(t, rmAt, 0, calls)
	require.Greater(t, startAt, rmAt, calls)
	assert.FileExists(t, filepath.Join(rt.home, ".cache", "oryn", "logs", "dockerd.log"))

	// 崩溃后按退避重启。
	require.NoError(t, syscall.Kill(fd.pid(t), syscall.SIGKILL))
	require.Eventually(t, func() bool { return fd.starts() == 2 }, 10*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return strings.Count(fd.read("calls.log"), "sudo rm -f") == 2 }, 10*time.Second, 10*time.Millisecond)

	// snapshot.prepare：有运行中的内层容器时 docker stop，没有时只 ps。
	require.NoError(t, os.WriteFile(filepath.Join(fd.dir, "containers"), []byte("abc123\ndef456\n"), 0o644))
	var pr apigen.LinkCtlSnapshotPrepareResult
	require.NoError(t, cp.call(t, string(apigen.MethodSnapshotPrepare), map[string]any{}, &pr))
	assert.True(t, pr.Ok)
	assert.Contains(t, fd.read("calls.log"), "docker stop -t 10 abc123 def456\n")
	require.NoError(t, cp.call(t, string(apigen.MethodSnapshotPrepare), map[string]any{}, &pr))
	assert.True(t, pr.Ok)
	assert.Equal(t, 1, strings.Count(fd.read("calls.log"), "docker stop"))
	assert.Equal(t, 2, strings.Count(fd.read("calls.log"), "docker ps -q"))

	// Shutdown：经 sudo pkill 发 TERM，dockerd 退出后不再重启。
	rt.Shutdown(time.Second)
	assert.Contains(t, fd.read("calls.log"), "sudo pkill -TERM -x dockerd\n")
	assert.True(t, strings.HasSuffix(fd.read("events.log"), "term\n"), fd.read("events.log"))
	assert.Equal(t, 2, fd.starts())
	rt.Shutdown(time.Second) // 可重复调用
	assert.Equal(t, 1, strings.Count(fd.read("calls.log"), "sudo pkill"))
}

// TestNestedDockerdDisabled：未启用时不启动 dockerd、hello 不含 docker，snapshot.prepare 直接成功、不调用 docker。
func TestNestedDockerdDisabled(t *testing.T) {
	fd := newFakeDocker(t)
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	feats := helloFeatures(t, cp)
	assert.NotContains(t, feats, "docker")
	assert.Contains(t, feats, "repo-update")
	var pr apigen.LinkCtlSnapshotPrepareResult
	require.NoError(t, cp.call(t, string(apigen.MethodSnapshotPrepare), map[string]any{}, &pr))
	assert.True(t, pr.Ok)
	rt.Shutdown(time.Second)
	assert.Empty(t, fd.read("calls.log"))
	assert.Empty(t, fd.read("events.log"))
}

func TestConfigFromEnvNestedDocker(t *testing.T) {
	t.Setenv("ORYN_URL", "http://x")
	t.Setenv("ORYN_ORB_ID", "orb-x")
	t.Setenv("ORYN_ORB_TOKEN", "oot_x")
	t.Setenv("ORYN_NESTED_DOCKER", "1")
	cfg, err := ConfigFromEnv("v")
	require.NoError(t, err)
	assert.True(t, cfg.NestedDocker)
	t.Setenv("ORYN_NESTED_DOCKER", "")
	cfg, err = ConfigFromEnv("v")
	require.NoError(t, err)
	assert.False(t, cfg.NestedDocker)
}

// TestOrbExtensionDockerGuide：扩展按 ORYN_NESTED_DOCKER 选择 Docker 说明。
func TestOrbExtensionDockerGuide(t *testing.T) {
	ext := string(orbExtension)
	assert.Contains(t, ext, `process.env.ORYN_NESTED_DOCKER === "1"`)
	assert.Contains(t, ext, "dockerd.log")
	assert.Contains(t, ext, "改用带 Docker 的 orb 类型")
}
