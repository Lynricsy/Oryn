package orbdrt

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// ---- Git LFS（M24 2.22）----

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

// lfsGit 以 home 作 $HOME（不读系统配置）在 dir 里执行 git。
func lfsGit(t *testing.T, home, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+home)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

// lfsInstall 在 home 里写 git-lfs 的 filter 配置（相当于镜像里的 git lfs install --system --skip-repo）。
func lfsInstall(t *testing.T, home string) {
	t.Helper()
	lfsGit(t, home, home, "lfs", "install", "--skip-repo")
}

// lfsUpstream 建一个 file:// 裸仓库：withLFS 时含 LFS 文件 big.bin（内容 "hello lfs\n"，对象经 git lfs push
// 写进裸仓库的 lfs/objects，即 standalone file 传输），否则只有普通文件。返回克隆地址与裸仓库路径。
func lfsUpstream(t *testing.T, withLFS bool) (url, bare string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	require.NoError(t, os.MkdirAll(home, 0o755))
	lfsInstall(t, home)
	bare, src := filepath.Join(root, "origin.git"), filepath.Join(root, "src")
	url = "file://" + bare
	lfsGit(t, home, root, "init", "-q", "--bare", bare)
	lfsGit(t, home, root, "init", "-q", src)
	if withLFS {
		lfsGit(t, home, src, "lfs", "track", "*.bin")
		require.NoError(t, os.WriteFile(filepath.Join(src, "big.bin"), []byte("hello lfs\n"), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(src, "a.txt"), []byte("1\n"), 0o644))
	lfsGit(t, home, src, "add", "-A")
	lfsGit(t, home, src, "commit", "-q", "-m", "one")
	if withLFS {
		lfsGit(t, home, src, "lfs", "push", "--all", url, "main")
	}
	lfsGit(t, home, src, "push", "-q", url, "HEAD:refs/heads/main")
	lfsGit(t, home, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	return url, bare
}

func setupLog(t *testing.T, rt *Runtime) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(rt.home, ".cache", "oryn", "logs", "setup.log"))
	require.NoError(t, err)
	return string(b)
}

func TestSetupLFSPull(t *testing.T) {
	requireGitLFS(t)
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	lfsInstall(t, rt.home)
	url, _ := lfsUpstream(t, true)

	res, n := setupCall(t, cp, apigen.LinkCtlSetupRunParams{Repo: &apigen.LinkCtlSetupRepo{URL: url}})
	assert.True(t, res.Ok, deref(res.SetupError))
	assert.True(t, n.Ok)
	b, err := os.ReadFile(filepath.Join(rt.home, "workspace", "repo", "big.bin"))
	require.NoError(t, err)
	assert.Equal(t, "hello lfs\n", string(b))
	assert.Contains(t, setupLog(t, rt), "git lfs pull: 1 LFS file(s) checked out")
}

// TestSetupLFSMissingObjects：上游缺对象时 clone 仍成功（不 smudge），文件留作指针，setup 记为失败。
func TestSetupLFSMissingObjects(t *testing.T) {
	requireGitLFS(t)
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	lfsInstall(t, rt.home)
	url, bare := lfsUpstream(t, true)
	require.NoError(t, os.RemoveAll(filepath.Join(bare, "lfs", "objects")))

	res, n := setupCall(t, cp, apigen.LinkCtlSetupRunParams{Repo: &apigen.LinkCtlSetupRepo{URL: url}})
	workdir := filepath.Join(rt.home, "workspace", "repo")
	assert.True(t, hasCheckout(workdir))
	b, err := os.ReadFile(filepath.Join(workdir, "big.bin"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(b), "version https://git-lfs.github.com/spec/v1\n"), string(b))
	assert.False(t, res.Ok)
	require.NotNil(t, res.SetupError)
	assert.Contains(t, *res.SetupError, "git lfs pull")
	assert.False(t, n.Ok)
	assert.Equal(t, res.SetupError, n.Error)
	assert.Contains(t, setupLog(t, rt), "warning: git lfs pull failed")
}

func TestSetupLFSNoLFSFiles(t *testing.T) {
	requireGitLFS(t)
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	lfsInstall(t, rt.home)
	url, _ := lfsUpstream(t, false)

	res, _ := setupCall(t, cp, apigen.LinkCtlSetupRunParams{Repo: &apigen.LinkCtlSetupRepo{URL: url}})
	assert.True(t, res.Ok, deref(res.SetupError))
	assert.NotContains(t, setupLog(t, rt), "git lfs")
}

// TestSetupLFSNotInstalled：PATH 里只有 git（镜像没装 git-lfs，也没有 filter 配置）时文件留作指针，setup 记为失败。
// 屏蔽系统级 git 配置：Debian/Ubuntu 的 git-lfs 包会在 /etc/gitconfig 写入 required 的 lfs filter，
// 宿主装了它时克隆会因找不到 git-lfs 直接失败，测不到「没有 filter 配置」这个场景。
func TestSetupLFSNotInstalled(t *testing.T) {
	requireGitLFS(t)
	gitBin, err := exec.LookPath("git")
	require.NoError(t, err)
	onlyGit := t.TempDir()
	require.NoError(t, os.Symlink(gitBin, filepath.Join(onlyGit, "git")))
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	url, _ := lfsUpstream(t, true)

	res, _ := setupCall(t, cp, apigen.LinkCtlSetupRunParams{
		Repo: &apigen.LinkCtlSetupRepo{URL: url},
		Env:  map[string]string{"PATH": onlyGit, "GIT_CONFIG_NOSYSTEM": "1"},
	})
	assert.False(t, res.Ok)
	require.NotNil(t, res.SetupError)
	assert.Contains(t, *res.SetupError, "git-lfs is not installed")
	assert.Contains(t, setupLog(t, rt), "git-lfs is not installed")
}
