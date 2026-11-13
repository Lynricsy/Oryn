package syncclient

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 本文件是 sync 的数据安全回归测试：任何可能丢失本地数据的路径都必须拒绝，且本地文件逐字保留。

// assertUntouched：本地仍在原分支原提交上，工作区干净，没有状态文件与 refs/oryn。
func (f *fixture) assertUntouched(t *testing.T) {
	t.Helper()
	assert.Equal(t, "main", gitT(t, f.local, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, f.base, gitT(t, f.local, "rev-parse", "HEAD"))
	assert.NoFileExists(t, filepath.Join(f.local, ".git", stateName))
	assert.Empty(t, syncRefs(t, f.local))
	assert.NoFileExists(t, filepath.Join(f.local, "committed.txt"), "orb 的内容没有落到本地")
}

func (f *fixture) readState(t *testing.T) state {
	t.Helper()
	g, err := openRepo(f.local)
	require.NoError(t, err)
	s, err := g.readState()
	require.NoError(t, err)
	return *s
}

// B1：第一轮应用后只改本地、orb 不变，直接取消 → 不还原、不覆盖，返回本地改动错误。
func TestMirrorCancelAfterApplyKeepsLocalEdits(t *testing.T) {
	f := newFixture(t)
	m := startMirror(t, f)
	_, err, finished := m.step()
	require.False(t, finished, "%v", err)
	f.assertMirrors(t)

	write(t, filepath.Join(f.local, "a.txt"), "mine\n")
	write(t, filepath.Join(f.local, "brand-new.txt"), "new\n")
	m.cancel()
	err = <-m.done
	require.ErrorIs(t, err, ErrLocalEdits)
	assert.Contains(t, err.Error(), "a.txt")
	assert.Contains(t, err.Error(), "--discard")
	assert.Equal(t, "mine\n", read(t, filepath.Join(f.local, "a.txt")))
	assert.Equal(t, "new\n", read(t, filepath.Join(f.local, "brand-new.txt")))
	assert.FileExists(t, filepath.Join(f.local, ".git", stateName), "状态保留")

	// 显式丢弃才还原
	require.NoError(t, Restore(f.local, RestoreOptions{Discard: true}))
	f.assertUntouched(t)
	assert.Empty(t, gitT(t, f.local, "status", "--porcelain"))
}

// B1：取消发生在第一次 Prepare 期间（本地从未被镜像改动）→ 只撤掉状态，不做任何破坏性还原。
func TestMirrorCancelDuringFirstPrepare(t *testing.T) {
	f := newFixture(t)
	m := startMirror(t, f)
	<-f.remote.entered
	write(t, filepath.Join(f.local, "a.txt"), "mine\n")
	write(t, filepath.Join(f.local, "brand-new.txt"), "new\n")
	m.cancel()
	require.NoError(t, <-m.done)
	assert.Equal(t, "mine\n", read(t, filepath.Join(f.local, "a.txt")))
	assert.Equal(t, "new\n", read(t, filepath.Join(f.local, "brand-new.txt")))
	f.assertUntouched(t)
}

// B2：首轮下载 bundle 期间本地被改动 → 不应用，文件逐字保留。
func TestMirrorFirstRoundLocalEditDuringDownload(t *testing.T) {
	f := newFixture(t)
	m := startMirror(t, f)
	f.remote.onBundle = func() {
		write(t, filepath.Join(f.local, "a.txt"), "mine\n")
		write(t, filepath.Join(f.local, "extra.txt"), "x\n")
	}
	_, err, finished := m.step()
	require.True(t, finished)
	require.ErrorIs(t, err, ErrLocalEdits)
	assert.Equal(t, "mine\n", read(t, filepath.Join(f.local, "a.txt")))
	assert.Equal(t, "x\n", read(t, filepath.Join(f.local, "extra.txt")))
	f.assertUntouched(t)
}

// B2：后续轮次下载期间本地被改动 → 停止，不覆盖，状态保留。
func TestMirrorLaterRoundLocalEditDuringDownload(t *testing.T) {
	f := newFixture(t)
	m := startMirror(t, f)
	_, err, finished := m.step()
	require.False(t, finished, "%v", err)

	write(t, filepath.Join(f.orb, "a.txt"), "orb again\n")
	f.remote.onBundle = func() { write(t, filepath.Join(f.local, "a.txt"), "mine\n") }
	_, err, finished = m.step()
	require.True(t, finished)
	require.ErrorIs(t, err, ErrLocalEdits)
	assert.Equal(t, "mine\n", read(t, filepath.Join(f.local, "a.txt")))
	assert.FileExists(t, filepath.Join(f.local, ".git", stateName))
	s := f.readState(t)
	assert.True(t, s.Applied)
	assert.Empty(t, s.Pending, "没有开始应用")
}

// B2：Apply 的 orb HEAD 与本地相同、只有未提交改动；下载期间本地编辑同一文件 → restore 不得覆盖。
func TestApplyLocalEditDuringDownload(t *testing.T) {
	f := newFixture(t)
	gitT(t, f.orb, "reset", "-q", "--soft", "HEAD~1")
	f.remote.onBundle = func() { write(t, filepath.Join(f.local, "a.txt"), "mine\n") }
	err := Apply(context.Background(), f.remote, f.opts())
	require.ErrorIs(t, err, ErrLocalEdits)
	assert.Equal(t, "mine\n", read(t, filepath.Join(f.local, "a.txt")))
	assert.NoFileExists(t, filepath.Join(f.local, "new.txt"))
	f.assertUntouched(t)
}

// B3 / F1：本地被忽略的 .env 与快照里的同名文件冲突 → 镜像拒绝，不覆盖。
func TestMirrorRefusesIgnoredConflict(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.local, ".git", "info", "exclude"), ".env\n")
	write(t, filepath.Join(f.local, ".env"), "MINE\n")
	write(t, filepath.Join(f.orb, ".env"), "orb\n")
	m := startMirror(t, f)
	_, err, finished := m.step()
	require.True(t, finished)
	require.ErrorIs(t, err, ErrIgnoredConflict)
	assert.Contains(t, err.Error(), ".env")
	assert.Equal(t, "MINE\n", read(t, filepath.Join(f.local, ".env")))
	f.assertUntouched(t)
}

// B3 / F1：Apply 的三种冲突（orb 已跟踪同名文件、文件占了本地被忽略的目录、目录占了本地被忽略的文件）与大小写不敏感。
func TestApplyRefusesIgnoredConflict(t *testing.T) {
	cases := []struct {
		name, exclude, local string
		orb                  func(t *testing.T, orb string)
	}{
		{".env tracked in orb HEAD", ".env\n", ".env", func(t *testing.T, orb string) {
			write(t, filepath.Join(orb, ".env"), "orb\n")
			gitT(t, orb, "add", ".env")
			gitT(t, orb, "commit", "-q", "-m", "env")
		}},
		{"orb file over local ignored dir", "build/\n", "build/out.txt", func(t *testing.T, orb string) {
			write(t, filepath.Join(orb, "build"), "orb\n")
		}},
		{"orb dir over local ignored file", "vendor\n", "vendor", func(t *testing.T, orb string) {
			write(t, filepath.Join(orb, "vendor", "lib.txt"), "orb\n")
		}},
		{"ignorecase", ".ENV\n", ".ENV", func(t *testing.T, orb string) {
			write(t, filepath.Join(orb, ".env"), "orb\n")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			if c.name == "ignorecase" {
				gitT(t, f.local, "config", "core.ignorecase", "true")
			}
			write(t, filepath.Join(f.local, ".git", "info", "exclude"), c.exclude)
			write(t, filepath.Join(f.local, c.local), "MINE\n")
			c.orb(t, f.orb)
			err := Apply(context.Background(), f.remote, f.opts())
			require.ErrorIs(t, err, ErrIgnoredConflict)
			assert.Contains(t, err.Error(), c.local)
			assert.Equal(t, "MINE\n", read(t, filepath.Join(f.local, c.local)))
			f.assertUntouched(t)
			assert.Empty(t, gitT(t, f.local, "status", "--porcelain"))
		})
	}
}

// F1：镜像写出、但被本地忽略的文件（git status 看不见）跟随 orb 更新与删除，纳入改动检查，还原时删除。
func TestMirrorOwnedIgnoredFiles(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.local, "mine.secret"), "keep\n")
	write(t, filepath.Join(f.local, ".git", "info", "exclude"), ".env.local\nmine.secret\n")
	write(t, filepath.Join(f.orb, ".env.local"), "orb v1\n")
	envLocal := filepath.Join(f.local, ".env.local")
	m := startMirror(t, f)

	_, err, finished := m.step()
	require.False(t, finished, "%v", err)
	assert.Equal(t, "orb v1\n", read(t, envLocal))
	assert.NotContains(t, gitT(t, f.local, "status", "--porcelain"), ".env.local", "本地 git status 看不见")
	assert.Equal(t, []string{".env.local"}, f.readState(t).Owned)

	write(t, filepath.Join(f.orb, ".env.local"), "orb v2\n")
	_, err, finished = m.step()
	require.False(t, finished, "%v", err)
	assert.Equal(t, "orb v2\n", read(t, envLocal))

	require.NoError(t, os.Remove(filepath.Join(f.orb, ".env.local")))
	_, err, finished = m.step()
	require.False(t, finished, "%v", err)
	assert.NoFileExists(t, envLocal, "orb 删除 → 本地也删除")
	assert.Empty(t, f.readState(t).Owned)

	write(t, filepath.Join(f.orb, ".env.local"), "orb v3\n")
	_, err, finished = m.step()
	require.False(t, finished, "%v", err)
	assert.Equal(t, "orb v3\n", read(t, envLocal))

	m.cancel()
	require.NoError(t, <-m.done)
	assert.NoFileExists(t, envLocal, "还原时删除镜像写出的被忽略文件")
	assert.Equal(t, "keep\n", read(t, filepath.Join(f.local, "mine.secret")), "本地自己的被忽略文件不动")
	f.assertUntouched(t)
	assert.Empty(t, gitT(t, f.local, "status", "--porcelain"))
}

// F1：本地改了镜像写出的被忽略文件 → 下一轮停止、取消时不还原。
func TestMirrorOwnedIgnoredFileEditDetected(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.local, ".git", "info", "exclude"), ".env.local\n")
	write(t, filepath.Join(f.orb, ".env.local"), "orb v1\n")
	envLocal := filepath.Join(f.local, ".env.local")
	m := startMirror(t, f)
	_, err, finished := m.step()
	require.False(t, finished, "%v", err)

	write(t, envLocal, "mine\n")
	write(t, filepath.Join(f.orb, ".env.local"), "orb v2\n")
	_, err, finished = m.step()
	require.True(t, finished)
	require.ErrorIs(t, err, ErrLocalEdits)
	assert.Contains(t, err.Error(), ".env.local")
	assert.Equal(t, "mine\n", read(t, envLocal))

	require.ErrorIs(t, Restore(f.local, RestoreOptions{}), ErrLocalEdits)
	assert.Equal(t, "mine\n", read(t, envLocal))
	require.NoError(t, Restore(f.local, RestoreOptions{Discard: true}))
	assert.NoFileExists(t, envLocal)
	f.assertUntouched(t)
}

// 还原的目标提交里有本地被忽略的文件路径（镜像期间用户新建）→ 拒绝还原，文件保留。
func TestRestoreRefusesIgnoredConflict(t *testing.T) {
	f := newFixture(t)
	gitT(t, f.orb, "commit", "-q", "-am", "drop del.txt")
	m := startMirror(t, f)
	_, err, finished := m.step()
	require.False(t, finished, "%v", err)
	// orb 的提交里已没有 del.txt（原提交里有）；本地把它加入忽略并写入自己的内容
	write(t, filepath.Join(f.local, ".git", "info", "exclude"), "del.txt\n")
	write(t, filepath.Join(f.local, "del.txt"), "MINE\n")
	m.cancel()
	err = <-m.done
	require.ErrorIs(t, err, ErrIgnoredConflict)
	assert.Equal(t, "MINE\n", read(t, filepath.Join(f.local, "del.txt")))
	require.ErrorIs(t, Restore(f.local, RestoreOptions{Discard: true}), ErrIgnoredConflict, "--discard 也不覆盖被忽略的文件")
	assert.Equal(t, "MINE\n", read(t, filepath.Join(f.local, "del.txt")))
	assert.FileExists(t, filepath.Join(f.local, ".git", stateName))
}

// F8：orb 返回的状态形态不合法 → 在下载与改动本地之前拒绝。
func TestRejectsMalformedState(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := map[string]func(*apigen.OrbSyncState){
		"head revision expression": func(st *apigen.OrbSyncState) { st.Head = "HEAD@{1}" },
		"snapshot search":          func(st *apigen.OrbSyncState) { st.Snapshot = ":/oryn" },
		"short sha":                func(st *apigen.OrbSyncState) { st.Head = st.Head[:12] },
		"branch option":            func(st *apigen.OrbSyncState) { st.Branch = str("-f") },
		"branch dotdot":            func(st *apigen.OrbSyncState) { st.Branch = str("a..b") },
		"branch previous":          func(st *apigen.OrbSyncState) { st.Branch = str("@{-1}") },
		"bundle id":                func(st *apigen.OrbSyncState) { st.Bundle.ID = "../../x" },
		"bundle size zero":         func(st *apigen.OrbSyncState) { st.Bundle.Size = 0 },
		"bundle size huge":         func(st *apigen.OrbSyncState) { st.Bundle.Size = maxBundle + 1 },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.remote.mutate = mut
			downloaded := false
			f.remote.onBundle = func() { downloaded = true }
			err := Apply(context.Background(), f.remote, f.opts())
			require.ErrorIs(t, err, ErrBadState)
			assert.False(t, downloaded, "校验在下载之前")
			f.assertUntouched(t)
			assert.Empty(t, gitT(t, f.local, "status", "--porcelain"))
		})
	}
}

// F8：bundle 比声明的大或小 → 拒绝导入。
func TestBundleSizeMustMatchDeclared(t *testing.T) {
	for name, delta := range map[string]int64{"larger": -1, "smaller": +1} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.remote.mutate = func(st *apigen.OrbSyncState) { st.Bundle.Size += delta }
			err := Apply(context.Background(), f.remote, f.opts())
			require.ErrorIs(t, err, ErrBadState)
			assert.False(t, strings.Contains(gitT(t, f.local, "rev-list", "--all"), f.orbHead), "orb 的对象没有导入")
			f.assertUntouched(t)
		})
	}
}

// floodRemote 声明一个小 bundle，实际不停地写；记录被接受的字节数与写入是否被拒。
type floodRemote struct {
	*orbRemote
	accepted int64
	refused  error
}

func (fr *floodRemote) Bundle(_ context.Context, _ string, w io.Writer) error {
	chunk := make([]byte, 32<<10)
	for range 256 { // 至多 8MiB
		n, err := w.Write(chunk)
		fr.accepted += int64(n)
		if err != nil {
			fr.refused = err
			return err
		}
	}
	return nil
}

// F8：orb 写出的字节超过声明的大小时立即中止下载（不写满本地磁盘）。
func TestBundleDownloadCappedAtDeclaredSize(t *testing.T) {
	f := newFixture(t)
	r := &floodRemote{orbRemote: f.remote}
	err := Apply(context.Background(), r, f.opts())
	require.ErrorIs(t, err, ErrBadState)
	require.Error(t, r.refused, "写入被拒绝")
	assert.ErrorIs(t, r.refused, ErrBadState)
	assert.Less(t, r.accepted, int64(1<<20), "超过声明大小后不再接受字节")
	f.assertUntouched(t)
}

// evilRemote 返回一个树里含 `.git` 条目的快照（fsck 的 hasDotgit）。
type evilRemote struct {
	*orbRemote
	bad string
}

func (e *evilRemote) Prepare(context.Context, apigen.OrbSyncPrepareRequest, bool) (apigen.OrbSyncState, error) {
	o := e.orbRemote
	head := strings.TrimSpace(gitMust(o.dir, "rev-parse", "HEAD"))
	base := strings.TrimSpace(gitMust(o.dir, "rev-parse", "HEAD~1"))
	blob := gitMustIn(o.dir, "evil\n", "hash-object", "-w", "--stdin")
	tree := gitMustIn(o.dir, fmt.Sprintf("100644 blob %s\t.git\n100644 blob %s\tok.txt\n", blob, blob), "mktree")
	e.bad = gitMust(o.dir, "commit-tree", tree, "-p", head, "-m", "evil")
	gitMust(o.dir, "update-ref", "refs/oryn/sync/snapshot", e.bad)
	gitMust(o.dir, "update-ref", "refs/oryn/sync/head", head)
	file := filepath.Join(o.bundles, "00000000000000ee")
	gitMust(o.dir, "bundle", "create", "--quiet", file, "refs/oryn/sync/snapshot", "refs/oryn/sync/head", "--not", base)
	fi, err := os.Stat(file)
	if err != nil {
		return apigen.OrbSyncState{}, err
	}
	return apigen.OrbSyncState{Root: o.dir, Head: head, Snapshot: e.bad, Dirty: true, Common: []string{base},
		Bundle: &apigen.OrbSyncBundle{ID: "00000000000000ee", Size: fi.Size()}}, nil
}

func gitMust(dir string, args ...string) string { return gitMustIn(dir, "", args...) }

func gitMustIn(dir, stdin string, args ...string) string {
	out, err := runGitRaw(dir, nil, []byte(stdin), args...)
	if err != nil {
		panic(err)
	}
	return strings.TrimSpace(string(out))
}

// F8：导入 bundle 时做 fsck，坏对象不进本地对象库。
func TestFetchRejectsCorruptObjects(t *testing.T) {
	f := newFixture(t)
	r := &evilRemote{orbRemote: f.remote}
	err := Apply(context.Background(), r, f.opts())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hasDotgit")
	_, cerr := gitRun(f.local, nil, "cat-file", "-e", r.bad)
	assert.Error(t, cerr, "坏提交没有导入")
	f.assertUntouched(t)
}
