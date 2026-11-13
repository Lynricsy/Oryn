package syncclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

const testThread = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"

// gitEnv 让测试里的 git（含被测代码 os.Environ() 继承的部分）不读用户配置、身份固定。
// 全局配置指向临时空文件而不是 /dev/null：以 root 运行的 git 写全局配置时会把目标文件整个替换掉。
func gitEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	global := filepath.Join(home, ".gitconfig")
	require.NoError(t, os.WriteFile(global, nil, 0o644))
	for k, v := range map[string]string{
		"HOME": home, "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": global,
		"GIT_AUTHOR_NAME": "Tester", "GIT_AUTHOR_EMAIL": "tester@example.com",
		"GIT_COMMITTER_NAME": "Tester", "GIT_COMMITTER_EMAIL": "tester@example.com",
	} {
		t.Setenv(k, v)
	}
}

func gitRun(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %v: %w: %s", args, err, stderr.String())
	}
	return strings.TrimSpace(out.String()), nil
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitRun(dir, nil, args...)
	require.NoError(t, err)
	return out
}

func write(t *testing.T, path, data string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(data), 0o644))
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

// orbRemote 在一个临时「orb 仓库」上按计划 4.3 的命令生成同步状态与 bundle（orbd /sync/prepare 的测试替身）。
// gate 非空时每次 Prepare 先发 entered 再等 gate（镜像测试据此逐轮推进）。
type orbRemote struct {
	dir, bundles string
	n            atomic.Int64
	gate         chan struct{}
	entered      chan bool
}

func (o *orbRemote) Prepare(ctx context.Context, req apigen.OrbSyncPrepareRequest, bg bool) (apigen.OrbSyncState, error) {
	var st apigen.OrbSyncState
	if o.gate != nil {
		o.entered <- bg
		select {
		case <-o.gate:
		case <-ctx.Done():
			return st, ctx.Err()
		}
	}
	g := func(env []string, args ...string) string {
		out, err := gitRun(o.dir, env, args...)
		if err != nil {
			panic(err)
		}
		return out
	}
	head := g(nil, "rev-parse", "HEAD")
	idx := filepath.Join(g(nil, "rev-parse", "--absolute-git-dir"), "oryn-sync.index")
	defer os.Remove(idx)
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(idx), "index"))
	_ = os.WriteFile(idx, b, 0o600)
	env := []string{"GIT_INDEX_FILE=" + idx}
	g(env, "add", "-A", "--", ".")
	st = apigen.OrbSyncState{Root: o.dir, Head: head, Snapshot: head, Common: []string{}}
	if tree := g(env, "write-tree"); tree != g(nil, "rev-parse", "HEAD^{tree}") {
		st.Snapshot, st.Dirty = g([]string{"GIT_AUTHOR_DATE=@0 +0000", "GIT_COMMITTER_DATE=@0 +0000"},
			"commit-tree", tree, "-p", head, "-m", "oryn sync snapshot"), true
	}
	g(nil, "update-ref", "refs/oryn/sync/snapshot", st.Snapshot)
	g(nil, "update-ref", "refs/oryn/sync/head", head)
	if br, err := gitRun(o.dir, nil, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		st.Branch = &br
	}
	for _, h := range req.Have {
		if _, err := gitRun(o.dir, nil, "cat-file", "-e", h+"^{commit}"); err == nil {
			st.Common = append(st.Common, h)
		}
	}
	if slices.Contains(req.Have, st.Snapshot) {
		return st, nil
	}
	if len(st.Common) == 0 {
		return st, &APIError{Status: 409, Code: "sync_no_common", Message: "no common commits"}
	}
	id := fmt.Sprintf("%016x", o.n.Add(1))
	g(nil, append([]string{"bundle", "create", "--quiet", filepath.Join(o.bundles, id), "refs/oryn/sync/snapshot", "refs/oryn/sync/head", "--not"}, st.Common...)...)
	st.Bundle = &apigen.OrbSyncBundle{ID: id}
	return st, nil
}

func (o *orbRemote) Bundle(_ context.Context, id string, w io.Writer) error {
	f, err := os.Open(filepath.Join(o.bundles, id))
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// fixture：origin（裸）← orb 克隆（未推送提交 + 改动 / 删除 / 二进制 / 未跟踪 / 被忽略）与本地干净克隆。
type fixture struct {
	origin, orb, local string
	base, orbHead      string
	remote             *orbRemote
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	gitEnv(t)
	root := t.TempDir()
	f := &fixture{origin: filepath.Join(root, "origin.git"), orb: filepath.Join(root, "orb"), local: filepath.Join(root, "local")}
	seed := filepath.Join(root, "seed")
	gitT(t, root, "init", "-q", "--bare", "-b", "main", f.origin)
	gitT(t, root, "init", "-q", "-b", "main", seed)
	write(t, filepath.Join(seed, "a.txt"), "orig\n")
	write(t, filepath.Join(seed, "del.txt"), "bye\n")
	write(t, filepath.Join(seed, "img.bin"), "\x00\x01\x02")
	write(t, filepath.Join(seed, ".gitignore"), "*.log\n")
	gitT(t, seed, "add", "-A")
	gitT(t, seed, "commit", "-q", "-m", "base")
	gitT(t, seed, "push", "-q", f.origin, "main")
	gitT(t, root, "clone", "-q", f.origin, f.orb)
	gitT(t, root, "clone", "-q", f.origin, f.local)
	f.base = gitT(t, f.local, "rev-parse", "HEAD")

	write(t, filepath.Join(f.orb, "committed.txt"), "c1\n")
	gitT(t, f.orb, "add", "committed.txt")
	gitT(t, f.orb, "commit", "-q", "-m", "unpushed")
	f.orbHead = gitT(t, f.orb, "rev-parse", "HEAD")
	write(t, filepath.Join(f.orb, "a.txt"), "changed\n")
	write(t, filepath.Join(f.orb, "img.bin"), "\x00\x09\xff\x00")
	require.NoError(t, os.Remove(filepath.Join(f.orb, "del.txt")))
	write(t, filepath.Join(f.orb, "new.txt"), "n1\n")
	write(t, filepath.Join(f.orb, "noise.log"), "orb only\n")
	f.remote = &orbRemote{dir: f.orb, bundles: t.TempDir()}
	return f
}

func (f *fixture) opts() Options {
	return Options{Dir: f.local, ThreadID: testThread}
}

// assertMirrors：本地 HEAD、status 与文件内容与 orb 一致（被忽略文件除外）。
func (f *fixture) assertMirrors(t *testing.T) {
	t.Helper()
	assert.Equal(t, gitT(t, f.orb, "rev-parse", "HEAD"), gitT(t, f.local, "rev-parse", "HEAD"))
	assert.Equal(t, gitT(t, f.orb, "status", "--porcelain"), gitT(t, f.local, "status", "--porcelain"))
	for _, n := range []string{"a.txt", "img.bin", "new.txt", "committed.txt"} {
		assert.Equal(t, read(t, filepath.Join(f.orb, n)), read(t, filepath.Join(f.local, n)), n)
	}
	assert.NoFileExists(t, filepath.Join(f.local, "del.txt"))
	assert.NoFileExists(t, filepath.Join(f.local, "noise.log"), "被忽略的文件不镜像")
}

func syncRefs(t *testing.T, dir string) string {
	return gitT(t, dir, "for-each-ref", "refs/oryn")
}

func TestApply(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, Apply(context.Background(), f.remote, f.opts()))
	f.assertMirrors(t)
	assert.Equal(t, "main", gitT(t, f.local, "symbolic-ref", "--short", "HEAD"), "快进到同名分支")
	assert.Empty(t, syncRefs(t, f.local))
	assert.NoFileExists(t, filepath.Join(f.local, ".git", stateName), "一次性应用不写状态文件")

	// 再应用一次：本地已脏 → ErrDirty，文件不动
	err := Apply(context.Background(), f.remote, f.opts())
	require.ErrorIs(t, err, ErrDirty)
	assert.Equal(t, "changed\n", read(t, filepath.Join(f.local, "a.txt")))
}

func TestApplyNewBranchAndDetach(t *testing.T) {
	f := newFixture(t)
	gitT(t, f.orb, "checkout", "-q", "-b", "feature")
	require.NoError(t, Apply(context.Background(), f.remote, f.opts()))
	f.assertMirrors(t)
	assert.Equal(t, "feature", gitT(t, f.local, "symbolic-ref", "--short", "HEAD"), "本地没有同名分支 → 新建")
	assert.Equal(t, f.base, gitT(t, f.local, "rev-parse", "main"), "main 不动")

	g := newFixture(t)
	o := g.opts()
	o.Detach = true
	require.NoError(t, Apply(context.Background(), g.remote, o))
	g.assertMirrors(t)
	_, err := gitRun(g.local, nil, "symbolic-ref", "--quiet", "HEAD")
	assert.Error(t, err, "--detach → 游离 HEAD")
	assert.Equal(t, g.base, gitT(t, g.local, "rev-parse", "main"))
}

func TestApplyDivergedBranchDetaches(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.local, "local.txt"), "mine\n")
	gitT(t, f.local, "add", "local.txt")
	gitT(t, f.local, "commit", "-q", "-m", "local only")
	localMain := gitT(t, f.local, "rev-parse", "HEAD")
	var logs []string
	o := f.opts()
	o.Log = func(format string, a ...any) { logs = append(logs, fmt.Sprintf(format, a...)) }
	require.NoError(t, Apply(context.Background(), f.remote, o))
	f.assertMirrors(t)
	_, err := gitRun(f.local, nil, "symbolic-ref", "--quiet", "HEAD")
	assert.Error(t, err, "分叉 → 游离检出")
	assert.Equal(t, localMain, gitT(t, f.local, "rev-parse", "main"), "本地分支不被改写")
	assert.Contains(t, strings.Join(logs, "\n"), "已分叉")
}

func TestApplyNoCommon(t *testing.T) {
	gitEnv(t)
	f := newFixture(t)
	other := filepath.Join(t.TempDir(), "other")
	gitT(t, t.TempDir(), "init", "-q", "-b", "main", other)
	write(t, filepath.Join(other, "x"), "x\n")
	gitT(t, other, "add", "x")
	gitT(t, other, "commit", "-q", "-m", "unrelated")
	o := f.opts()
	o.Dir = other
	err := Apply(context.Background(), f.remote, o)
	require.Error(t, err)
	assert.Equal(t, "sync_no_common", Code(err))
	assert.Contains(t, err.Error(), "git fetch origin")

	o.Dir = t.TempDir()
	assert.ErrorIs(t, Apply(context.Background(), f.remote, o), ErrNotARepo)
}

// mirrorRun 在后台跑 Mirror，逐轮放行 Prepare。
type mirrorRun struct {
	t      *testing.T
	f      *fixture
	cancel context.CancelFunc
	done   chan error
}

func startMirror(t *testing.T, f *fixture) *mirrorRun {
	t.Helper()
	f.remote.gate = make(chan struct{})
	f.remote.entered = make(chan bool, 1)
	ctx, cancel := context.WithCancel(context.Background())
	m := &mirrorRun{t: t, f: f, cancel: cancel, done: make(chan error, 1)}
	go func() { m.done <- Mirror(ctx, f.remote, f.opts()) }()
	t.Cleanup(cancel)
	return m
}

// step 放行当前这一轮的 Prepare，并等到下一轮 Prepare 开始（即这一轮已应用完）或 Mirror 返回。
func (m *mirrorRun) step() (bg bool, err error, finished bool) {
	m.t.Helper()
	select {
	case bg = <-m.f.remote.entered:
	case err := <-m.done:
		return false, err, true
	}
	m.f.remote.gate <- struct{}{}
	select {
	case <-m.f.remote.entered:
		// 下一轮已开始：放回去给下一次 step 读
		m.f.remote.entered <- true
		return bg, nil, false
	case err := <-m.done:
		return bg, err, true
	}
}

func TestMirrorTwoRoundsThenRestore(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.local, "ignored-local.log"), "keep me\n")
	m := startMirror(t, f)

	bg, err, finished := m.step()
	require.False(t, finished, "%v", err)
	assert.False(t, bg, "第一轮前台（会唤醒 orb）")
	f.assertMirrors(t)
	assert.FileExists(t, filepath.Join(f.local, ".git", stateName))
	assert.Equal(t, "keep me\n", read(t, filepath.Join(f.local, "ignored-local.log")), "被忽略的本地文件不受影响")

	// orb 再改一行、加一个提交
	write(t, filepath.Join(f.orb, "a.txt"), "changed again\n")
	write(t, filepath.Join(f.orb, "second.txt"), "s\n")
	gitT(t, f.orb, "add", "second.txt")
	gitT(t, f.orb, "commit", "-q", "-m", "second")
	bg, err, finished = m.step()
	require.False(t, finished, "%v", err)
	assert.True(t, bg, "之后的轮询走 bg=1")
	f.assertMirrors(t)
	assert.Equal(t, "s\n", read(t, filepath.Join(f.local, "second.txt")))

	// 没有变化的一轮：什么都不做
	_, err, finished = m.step()
	require.False(t, finished, "%v", err)
	f.assertMirrors(t)

	m.cancel()
	require.NoError(t, <-m.done)
	assert.Equal(t, "main", gitT(t, f.local, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, f.base, gitT(t, f.local, "rev-parse", "HEAD"))
	assert.Empty(t, gitT(t, f.local, "status", "--porcelain"))
	assert.Empty(t, syncRefs(t, f.local))
	assert.NoFileExists(t, filepath.Join(f.local, ".git", stateName))
	assert.Equal(t, "keep me\n", read(t, filepath.Join(f.local, "ignored-local.log")))
}

func TestMirrorStopsOnLocalEdits(t *testing.T) {
	f := newFixture(t)
	m := startMirror(t, f)
	_, err, finished := m.step()
	require.False(t, finished, "%v", err)

	write(t, filepath.Join(f.local, "a.txt"), "edited locally\n")
	write(t, filepath.Join(f.orb, "a.txt"), "orb edit\n")
	_, err, finished = m.step()
	require.True(t, finished)
	require.ErrorIs(t, err, ErrLocalEdits)
	assert.Equal(t, "edited locally\n", read(t, filepath.Join(f.local, "a.txt")), "本地改动未被覆盖")
	assert.FileExists(t, filepath.Join(f.local, ".git", stateName), "状态文件保留")

	// 残留状态 → ErrStaleState；Restore 后可重新开始
	f.remote.gate = nil
	require.ErrorIs(t, Apply(context.Background(), f.remote, f.opts()), ErrStaleState)
	require.ErrorIs(t, Mirror(context.Background(), f.remote, f.opts()), ErrStaleState)
	require.NoError(t, Restore(f.local))
	assert.Equal(t, f.base, gitT(t, f.local, "rev-parse", "HEAD"))
	assert.Empty(t, gitT(t, f.local, "status", "--porcelain"))
	assert.Empty(t, syncRefs(t, f.local))
	require.NoError(t, Apply(context.Background(), f.remote, f.opts()))
	assert.Equal(t, "orb edit\n", read(t, filepath.Join(f.local, "a.txt")))
}

func TestMirrorFirstRoundErrorLeavesNoState(t *testing.T) {
	f := newFixture(t)
	r := &failingRemote{err: &APIError{Status: 409, Code: "orbd_outdated", Message: "old"}}
	err := Mirror(context.Background(), r, f.opts())
	require.Error(t, err)
	assert.Equal(t, "orbd_outdated", Code(err))
	assert.NoFileExists(t, filepath.Join(f.local, ".git", stateName))
	assert.Equal(t, f.base, gitT(t, f.local, "rev-parse", "HEAD"))
}

type failingRemote struct{ err error }

func (r *failingRemote) Prepare(context.Context, apigen.OrbSyncPrepareRequest, bool) (apigen.OrbSyncState, error) {
	return apigen.OrbSyncState{}, r.err
}

func (r *failingRemote) Bundle(context.Context, string, io.Writer) error { return errors.New("unused") }
