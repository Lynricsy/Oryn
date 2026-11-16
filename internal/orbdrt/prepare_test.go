package orbdrt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// prepRepo 是 github_prepare 测试用的仓库：bare origin（默认分支 main，已有一个提交）与它的克隆。
type prepRepo struct {
	t      *testing.T
	root   string
	origin string
	dir    string
	env    []string
}

// newPrepRepo 建 bare origin 并克隆（克隆会设好 origin/HEAD），随后切到 feature 分支（没有新提交）。
func newPrepRepo(t *testing.T) *prepRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	cfg := filepath.Join(root, "gitconfig")
	require.NoError(t, os.WriteFile(cfg,
		[]byte("[user]\n\tname = Orb Bot\n\temail = bot@example.com\n[init]\n\tdefaultBranch = main\n[commit]\n\tgpgsign = false\n"), 0o644))
	env := []string{"HOME=" + root, "GIT_CONFIG_GLOBAL=" + cfg, "GIT_CONFIG_NOSYSTEM=1"}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "HOME=") {
			env = append(env, kv)
		}
	}
	r := &prepRepo{t: t, root: root, origin: filepath.Join(root, "origin.git"), dir: filepath.Join(root, "work"), env: env}
	seed := filepath.Join(root, "seed")
	r.gitAt(root, nil, "init", "--bare", "-b", "main", r.origin)
	r.gitAt(root, nil, "init", "-b", "main", seed)
	require.NoError(t, os.WriteFile(filepath.Join(seed, "README"), []byte("seed\n"), 0o644))
	r.gitAt(seed, nil, "add", "README")
	r.gitAt(seed, nil, "commit", "-m", "init")
	r.gitAt(seed, nil, "push", r.origin, "main")
	r.gitAt(root, nil, "clone", r.origin, r.dir)
	r.git("checkout", "-b", "feature")
	return r
}

func (r *prepRepo) gitAt(dir string, extra []string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(append([]string{}, r.env...), extra...)
	out, err := cmd.CombinedOutput()
	require.NoError(r.t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func (r *prepRepo) git(args ...string) string { return r.gitAt(r.dir, nil, args...) }

// commit 写一个文件并提交；extra 是附加的环境（作者 / 日期）。
func (r *prepRepo) commit(file, msg string, extra ...string) string {
	r.t.Helper()
	require.NoError(r.t, os.WriteFile(filepath.Join(r.dir, file), []byte(file+" "+msg+"\n"), 0o644))
	r.gitAt(r.dir, nil, "add", file)
	r.gitAt(r.dir, extra, "commit", "-q", "-m", msg)
	return r.git("rev-parse", "HEAD")
}

func (r *prepRepo) req(level string) PrepareRequest {
	return PrepareRequest{Dir: r.dir, Level: level, Env: r.env}
}

func (r *prepRepo) run(req PrepareRequest) PrepareResult {
	r.t.Helper()
	res, err := RunPrepare(context.Background(), req)
	require.NoError(r.t, err)
	return res
}

// failedAt 断言结果失败于 step。
func failedAt(t *testing.T, res PrepareResult, step string) {
	t.Helper()
	require.False(t, res.OK, "expected failure at %s, got %+v", step, res)
	assert.Empty(t, res.SHA)
	require.NotEmpty(t, res.Steps)
	last := res.Steps[len(res.Steps)-1]
	assert.Equal(t, step, last.Name)
	assert.False(t, last.OK)
	assert.True(t, strings.HasPrefix(res.Failure, step+": "), res.Failure)
}

func TestPrepareDetachedHead(t *testing.T) {
	r := newPrepRepo(t)
	r.commit("a.txt", "feat: a")
	r.git("checkout", "-q", "--detach")
	res := r.run(r.req(PrepareLevelPush))
	failedAt(t, res, "head")
	assert.Contains(t, res.Failure, "detached")
}

func TestPrepareOnDefaultBranch(t *testing.T) {
	r := newPrepRepo(t)
	r.git("checkout", "-q", "main")
	r.commit("a.txt", "feat: a")
	res := r.run(r.req(PrepareLevelPush))
	failedAt(t, res, "branch")
	assert.Contains(t, res.Failure, `"main"`)
}

func TestPrepareReleaseOnDefaultBranch(t *testing.T) {
	r := newPrepRepo(t)
	r.git("checkout", "-q", "main")
	head := r.commit("a.txt", "chore: bump version to 1.2.3")
	req := r.req(PrepareLevelPush)
	req.Release = true
	res := r.run(req)
	require.True(t, res.OK, "%+v", res)
	assert.Equal(t, head, res.SHA)
}

// origin/HEAD 缺失时回退到 origin/main。
func TestPrepareDefaultBranchFallback(t *testing.T) {
	r := newPrepRepo(t)
	r.git("symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	r.git("checkout", "-q", "main")
	r.commit("a.txt", "feat: a")
	failedAt(t, r.run(r.req(PrepareLevelPush)), "branch")
}

func TestPrepareEmptyRange(t *testing.T) {
	r := newPrepRepo(t)
	res := r.run(r.req(PrepareLevelPush))
	failedAt(t, res, "range")
	assert.Contains(t, res.Failure, "origin/main..HEAD")
}

func TestPrepareDirtyTree(t *testing.T) {
	r := newPrepRepo(t)
	r.commit("a.txt", "feat: a")
	require.NoError(t, os.WriteFile(filepath.Join(r.dir, "stray.txt"), []byte("x"), 0o644))
	res := r.run(r.req(PrepareLevelPush))
	failedAt(t, res, "clean")
	assert.Contains(t, res.Steps[len(res.Steps)-1].Output, "stray.txt")
}

func TestPrepareIdentity(t *testing.T) {
	r := newPrepRepo(t)
	r.commit("a.txt", "feat: a")
	r.commit("b.txt", "feat: b", "GIT_AUTHOR_NAME=Someone Else", "GIT_AUTHOR_EMAIL=else@example.com")

	req := r.req(PrepareLevelPush)
	req.Identity = &apigen.GitIdentity{Name: "Orb Bot", Email: "bot@example.com"}
	res := r.run(req)
	failedAt(t, res, "author")
	out := res.Steps[len(res.Steps)-1].Output
	assert.Contains(t, out, "else@example.com")
	assert.NotContains(t, out, "bot@example.com")

	r.git("reset", "-q", "--hard", "HEAD~1")
	res = r.run(req)
	require.True(t, res.OK, "%+v", res)
}

func TestPrepareCheckFailure(t *testing.T) {
	r := newPrepRepo(t)
	r.commit("a.txt", "feat: a")
	req := r.req(PrepareLevelPush)
	req.Gates.CheckCmd = new("echo boom; exit 3")
	res := r.run(req)
	failedAt(t, res, "check")
	assert.Contains(t, res.Steps[len(res.Steps)-1].Output, "boom")
	assert.Contains(t, res.Failure, "exit status 3")
}

func TestPrepareCheckTimeoutKillsGroup(t *testing.T) {
	r := newPrepRepo(t)
	r.commit("a.txt", "feat: a")
	old := prepareCheckTimeout
	prepareCheckTimeout = 300 * time.Millisecond
	t.Cleanup(func() { prepareCheckTimeout = old })
	req := r.req(PrepareLevelPush)
	// 后台子进程占着输出管道：只杀 sh 不杀进程组时 Run 要等到 WaitDelay。
	req.Gates.CheckCmd = new("sleep 30 & sleep 30")
	start := time.Now()
	res := r.run(req)
	failedAt(t, res, "check")
	assert.Contains(t, res.Failure, "timed out")
	assert.Less(t, time.Since(start), 4*time.Second)
}

func TestPrepareFixCmdAmends(t *testing.T) {
	r := newPrepRepo(t)
	before := r.commit("a.txt", "feat: a")
	req := r.req(PrepareLevelPush)
	req.Gates.FixCmd = new("echo fixed > fix.txt")
	res := r.run(req)
	require.True(t, res.OK, "%+v", res)
	head := r.git("rev-parse", "HEAD")
	assert.Equal(t, head, res.SHA)
	assert.NotEqual(t, before, head)
	assert.Equal(t, "1", r.git("rev-list", "--count", "origin/main..HEAD"), "fix must be amended into HEAD")
	// `git show --name-only` 按路径排序。
	assert.Equal(t, "a.txt\nfix.txt", r.git("show", "--format=", "--name-only", "HEAD"))
	assert.Equal(t, "feat: a", r.git("log", "-1", "--format=%B"))
	assert.Empty(t, r.git("status", "--porcelain"))

	// 修复命令不产生改动时 HEAD 不变。
	res = r.run(req)
	require.True(t, res.OK, "%+v", res)
	assert.Equal(t, head, res.SHA)
}

// 工作树原本不干净时修复命令的改动不并入，第 6 步报告。
func TestPrepareFixCmdDirtyBefore(t *testing.T) {
	r := newPrepRepo(t)
	head := r.commit("a.txt", "feat: a")
	require.NoError(t, os.WriteFile(filepath.Join(r.dir, "stray.txt"), []byte("x"), 0o644))
	req := r.req(PrepareLevelPush)
	req.Gates.FixCmd = new("echo fixed > fix.txt")
	failedAt(t, r.run(req), "clean")
	assert.Equal(t, head, r.git("rev-parse", "HEAD"))
}

func TestPrepareLiteralNewlineRepair(t *testing.T) {
	r := newPrepRepo(t)
	dateA := []string{"GIT_AUTHOR_NAME=Ann", "GIT_AUTHOR_EMAIL=ann@example.com", "GIT_AUTHOR_DATE=2025-03-04T05:06:07+0800",
		"GIT_COMMITTER_NAME=Cid", "GIT_COMMITTER_EMAIL=cid@example.com", "GIT_COMMITTER_DATE=2025-03-05T01:02:03-0700"}
	dateB := []string{"GIT_AUTHOR_DATE=2025-04-01T00:00:00Z", "GIT_COMMITTER_DATE=2025-04-02T00:00:00Z"}
	oldA := r.commit("a.txt", `fix: thing\n\nlonger body\nline two`, dateA...)
	oldB := r.commit("b.txt", "feat: b", dateB...)
	const fmtMeta = "%T|%an|%ae|%ad|%cn|%ce|%cd"
	metaA := r.git("log", "-1", "--date=raw", "--format="+fmtMeta, oldA)
	metaB := r.git("log", "-1", "--date=raw", "--format="+fmtMeta, oldB)

	res := r.run(r.req(PrepareLevelPush))
	require.True(t, res.OK, "%+v", res)
	newB := r.git("rev-parse", "HEAD")
	assert.Equal(t, newB, res.SHA)
	assert.NotEqual(t, oldB, newB)
	newA := r.git("rev-parse", "HEAD~1")
	assert.NotEqual(t, oldA, newA)
	assert.Equal(t, r.git("rev-parse", "origin/main"), r.git("rev-parse", "HEAD~2"))

	assert.Equal(t, "fix: thing\n\nlonger body\nline two", r.git("log", "-1", "--format=%B", newA))
	assert.Equal(t, "feat: b", r.git("log", "-1", "--format=%B", newB))
	assert.Equal(t, metaA, r.git("log", "-1", "--date=raw", "--format="+fmtMeta, newA))
	assert.Equal(t, metaB, r.git("log", "-1", "--date=raw", "--format="+fmtMeta, newB))
	assert.Equal(t, "refs/heads/feature", r.git("symbolic-ref", "HEAD"))
	assert.Empty(t, r.git("status", "--porcelain"))

	// 再跑一次：没有字面量 \n，什么都不改。
	res = r.run(r.req(PrepareLevelPush))
	require.True(t, res.OK, "%+v", res)
	assert.Equal(t, newB, res.SHA)
}

func TestPreparePRLevelRunsTest(t *testing.T) {
	r := newPrepRepo(t)
	r.commit("a.txt", "feat: a")
	marker := filepath.Join(r.root, "test-ran")
	req := r.req(PrepareLevelPush)
	req.Gates.CheckCmd = new("true")
	req.Gates.TestCmd = new("touch " + marker)

	res := r.run(req)
	require.True(t, res.OK, "%+v", res)
	assert.NoFileExists(t, marker, "push level must not run the test command")

	req.Level = PrepareLevelPR
	res = r.run(req)
	require.True(t, res.OK, "%+v", res)
	assert.FileExists(t, marker)
	assert.Equal(t, "test", res.Steps[len(res.Steps)-1].Name)

	req.Gates.TestCmd = new("echo nope >&2; exit 1")
	failedAt(t, r.run(req), "test")
}

func TestPrepareSkipChecks(t *testing.T) {
	r := newPrepRepo(t)
	head := r.commit("a.txt", "feat: a")
	marker := filepath.Join(r.root, "fix-ran")
	req := r.req(PrepareLevelPR)
	req.SkipChecks = true
	req.Gates = apigen.GitHubGates{FixCmd: new("touch " + marker), CheckCmd: new("exit 1"), TestCmd: new("exit 1")}

	_, err := RunPrepare(context.Background(), req)
	require.Error(t, err, "skip without reason")

	req.Reason = "CI is down"
	res := r.run(req)
	require.True(t, res.OK, "%+v", res)
	assert.True(t, res.Skipped)
	assert.Equal(t, head, res.SHA)
	assert.NoFileExists(t, marker)

	// 结构检查照做。
	require.NoError(t, os.WriteFile(filepath.Join(r.dir, "stray.txt"), []byte("x"), 0o644))
	res = r.run(req)
	failedAt(t, res, "clean")
	assert.False(t, res.Skipped)
}

func TestPrepareInvalidRequest(t *testing.T) {
	_, err := RunPrepare(context.Background(), PrepareRequest{Dir: t.TempDir(), Level: "merge"})
	require.Error(t, err)
}
