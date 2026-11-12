package orbdrt

import (
	"bytes"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// gitIn 在 dir 里执行真实 git（HOME 指向测试 $HOME，不读用户自己的全局配置）。
func gitIn(t *testing.T, home, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"HOME="+home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=tester@example.com",
		"GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=tester@example.com",
		"GIT_AUTHOR_DATE=2026-01-02T03:04:05Z", "GIT_COMMITTER_DATE=2026-01-02T03:04:05Z",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path string, data string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(data), 0o644))
}

// gitFixture：$HOME/origin.git（裸仓库，main 上一个基线提交 B）克隆到 $HOME/workspace/repo，
// 本地再提交 C1（未推送），并留下暂存 / 部分暂存 / 未暂存 / 未跟踪 / 重命名 / 二进制 / 删除各一例。
type gitFixture struct {
	home, origin, repo string
	base, c1           string
}

func newGitFixture(t *testing.T, home string) gitFixture {
	t.Helper()
	f := gitFixture{home: home, origin: filepath.Join(home, "origin.git"), repo: filepath.Join(home, "workspace", "repo")}
	seed := filepath.Join(home, "seed")
	require.NoError(t, os.MkdirAll(seed, 0o755))
	gitIn(t, home, home, "init", "-q", "--bare", "-b", "main", f.origin)
	gitIn(t, home, seed, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(seed, "a.txt"), "orig\n")
	writeFile(t, filepath.Join(seed, "old.txt"), "one\ntwo\nthree\nfour\nfive\n")
	writeFile(t, filepath.Join(seed, "del.txt"), "bye\n")
	writeFile(t, filepath.Join(seed, "staged.txt"), "s1\n")
	writeFile(t, filepath.Join(seed, "mixed.txt"), "m1\n")
	writeFile(t, filepath.Join(seed, "img.bin"), "\x00\x01\x02")
	writeFile(t, filepath.Join(seed, ".gitignore"), "ignored.log\n")
	gitIn(t, home, seed, "add", "-A")
	gitIn(t, home, seed, "commit", "-q", "-m", "base")
	gitIn(t, home, seed, "remote", "add", "origin", f.origin)
	gitIn(t, home, seed, "push", "-q", "origin", "main")
	gitIn(t, home, home, "clone", "-q", f.origin, f.repo)
	f.base = gitIn(t, home, f.repo, "rev-parse", "HEAD")

	writeFile(t, filepath.Join(f.repo, "committed.txt"), "c1\nc2\n")
	gitIn(t, home, f.repo, "add", "committed.txt")
	gitIn(t, home, f.repo, "commit", "-q", "-m", "add committed.txt")
	f.c1 = gitIn(t, home, f.repo, "rev-parse", "HEAD")

	writeFile(t, filepath.Join(f.repo, "staged.txt"), "s2\n")
	gitIn(t, home, f.repo, "add", "staged.txt")
	writeFile(t, filepath.Join(f.repo, "mixed.txt"), "m2\n")
	gitIn(t, home, f.repo, "add", "mixed.txt")
	writeFile(t, filepath.Join(f.repo, "mixed.txt"), "m3\n")
	writeFile(t, filepath.Join(f.repo, "a.txt"), "changed\n")
	gitIn(t, home, f.repo, "mv", "old.txt", "renamed.txt")
	writeFile(t, filepath.Join(f.repo, "img.bin"), "\x00\x09\x09\x09")
	require.NoError(t, os.Remove(filepath.Join(f.repo, "del.txt")))
	writeFile(t, filepath.Join(f.repo, "new.txt"), "n1\nn2")
	writeFile(t, filepath.Join(f.repo, "ignored.log"), "noise\n")
	return f
}

func statusOf(t *testing.T, o *orbHTTP, q url.Values) apigen.GitStatus {
	t.Helper()
	var st apigen.GitStatus
	o.json(http.MethodGet, "/git/status", q, nil, http.StatusOK, &st)
	return st
}

func filesByPath(st apigen.GitStatus) map[string]apigen.GitFileChange {
	m := map[string]apigen.GitFileChange{}
	for _, f := range st.Files {
		m[f.Path] = f
	}
	return m
}

func TestGitStatusAndDiff(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	o := newOrbHTTP(t, cp, rt)
	f := newGitFixture(t, rt.home)
	setWorkdir(rt, f.repo)

	// scope=all（默认）：基线经 origin/HEAD 解析为 main。
	st := statusOf(t, o, nil)
	require.True(t, st.Repo)
	assert.Equal(t, apigen.All, st.Scope)
	require.NotNil(t, st.Root)
	assert.Equal(t, f.repo, *st.Root)
	require.NotNil(t, st.Branch)
	assert.Equal(t, "main", *st.Branch)
	require.NotNil(t, st.Head)
	assert.Equal(t, f.c1, *st.Head)
	require.NotNil(t, st.Base)
	assert.Equal(t, apigen.GitBase{Name: "main", Ref: "refs/remotes/origin/main", MergeBase: f.base}, *st.Base)
	assert.False(t, st.Truncated)
	none, full, partial := apigen.GitFileChangeStagedNone, apigen.GitFileChangeStagedFull, apigen.GitFileChangeStagedPartial
	old := "old.txt"
	want := map[string]apigen.GitFileChange{
		"a.txt":         {Path: "a.txt", Status: apigen.GitFileChangeStatusModified, Additions: new(1), Deletions: new(1), Staged: none},
		"committed.txt": {Path: "committed.txt", Status: apigen.GitFileChangeStatusAdded, Additions: new(2), Deletions: new(0), Staged: none},
		"del.txt":       {Path: "del.txt", Status: apigen.GitFileChangeStatusDeleted, Additions: new(0), Deletions: new(1), Staged: none},
		"img.bin":       {Path: "img.bin", Status: apigen.GitFileChangeStatusModified, Binary: true, Staged: none},
		"mixed.txt":     {Path: "mixed.txt", Status: apigen.GitFileChangeStatusModified, Additions: new(1), Deletions: new(1), Staged: partial},
		"new.txt":       {Path: "new.txt", Status: apigen.GitFileChangeStatusUntracked, Additions: new(2), Deletions: new(0), Staged: none},
		"renamed.txt":   {Path: "renamed.txt", OldPath: &old, Status: apigen.GitFileChangeStatusRenamed, Additions: new(0), Deletions: new(0), Staged: full},
		"staged.txt":    {Path: "staged.txt", Status: apigen.GitFileChangeStatusModified, Additions: new(1), Deletions: new(1), Staged: full},
	}
	assert.Equal(t, want, filesByPath(st))
	var order []string
	for _, c := range st.Files {
		order = append(order, c.Path)
	}
	assert.IsIncreasing(t, order, "按路径排序")

	// scope=uncommitted：已提交的 committed.txt 不在其中。
	st = statusOf(t, o, url.Values{"scope": {"uncommitted"}})
	m := filesByPath(st)
	assert.NotContains(t, m, "committed.txt")
	assert.Contains(t, m, "a.txt")
	assert.Contains(t, m, "new.txt")

	// scope=commit：只有该提交的文件，无暂存状态、无未跟踪文件；短 sha 同样可用。
	st = statusOf(t, o, url.Values{"scope": {"commit"}, "commit": {f.c1[:10]}})
	assert.Equal(t, []apigen.GitFileChange{{Path: "committed.txt", Status: apigen.GitFileChangeStatusAdded, Additions: new(2), Deletions: new(0), Staged: none}}, st.Files)
	// 根提交：父提交取空树。
	st = statusOf(t, o, url.Values{"scope": {"commit"}, "commit": {f.base}})
	assert.Len(t, st.Files, 7)
	resp, b := o.do(http.MethodGet, "/git/status", url.Values{"scope": {"commit"}, "commit": {"deadbeef00"}}, nil, nil)
	assert.Equal(t, "not_found", o.errCode(resp, b, http.StatusNotFound))

	diff := func(q url.Values) apigen.GitFileDiff {
		t.Helper()
		var d apigen.GitFileDiff
		o.json(http.MethodGet, "/git/diff", q, nil, http.StatusOK, &d)
		return d
	}
	d := diff(url.Values{"path": {"a.txt"}})
	assert.Equal(t, apigen.Text, d.Kind)
	assert.Contains(t, d.Patch, "--- a/a.txt\n+++ b/a.txt\n")
	assert.Contains(t, d.Patch, "-orig\n+changed\n")
	assert.False(t, d.Truncated)

	d = diff(url.Values{"path": {"renamed.txt"}, "oldPath": {"old.txt"}})
	assert.Contains(t, d.Patch, "rename from old.txt")
	assert.Contains(t, d.Patch, "rename to renamed.txt")
	require.NotNil(t, d.OldPath)

	d = diff(url.Values{"path": {"img.bin"}})
	assert.Equal(t, apigen.GitFileDiff{Path: "img.bin", Kind: apigen.Binary}, d)

	d = diff(url.Values{"path": {"new.txt"}})
	assert.Equal(t, apigen.Text, d.Kind)
	assert.Contains(t, d.Patch, "+n1\n+n2")
	assert.Contains(t, d.Patch, "new file mode")

	d = diff(url.Values{"path": {"del.txt"}})
	assert.Contains(t, d.Patch, "deleted file mode")
	assert.Contains(t, d.Patch, "-bye")

	// commit 范围的 diff。
	d = diff(url.Values{"path": {"committed.txt"}, "scope": {"commit"}, "commit": {f.c1}})
	assert.Contains(t, d.Patch, "+c1\n+c2\n")
	// 工作区改动不出现在 commit 范围里。
	d = diff(url.Values{"path": {"a.txt"}, "scope": {"commit"}, "commit": {f.c1}})
	assert.Empty(t, d.Patch)

	// 大于 4MiB 的未跟踪文件：large。
	writeFile(t, filepath.Join(f.repo, "huge.txt"), strings.Repeat("y\n", 2<<20+1))
	d = diff(url.Values{"path": {"huge.txt"}})
	assert.Equal(t, apigen.GitFileDiff{Path: "huge.txt", Kind: apigen.Large}, d)
	st = statusOf(t, o, nil)
	huge := filesByPath(st)["huge.txt"]
	assert.Nil(t, huge.Additions, "超过 1MiB 的未跟踪文件不给行数")
	assert.False(t, huge.Binary)

	// 已跟踪文件的 patch 超过 1MiB：在换行处截断。
	var big strings.Builder
	for i := range 40000 {
		big.WriteString(strings.Repeat("z", 30))
		big.WriteString(strings.Repeat("0", i%7))
		big.WriteByte('\n')
	}
	writeFile(t, filepath.Join(f.repo, "a.txt"), big.String())
	d = diff(url.Values{"path": {"a.txt"}})
	assert.True(t, d.Truncated)
	assert.LessOrEqual(t, len(d.Patch), 1<<20)
	assert.True(t, strings.HasSuffix(d.Patch, "\n"))
	assert.True(t, strings.HasPrefix(d.Patch, "diff --git a/a.txt b/a.txt\n"))
}

func TestGitCommitsAndBaseResolution(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	o := newOrbHTTP(t, cp, rt)
	f := newGitFixture(t, rt.home)
	q := url.Values{"root": {"~/workspace/repo"}}

	var cl apigen.GitCommitList
	o.json(http.MethodGet, "/git/commits", q, nil, http.StatusOK, &cl)
	require.Len(t, cl.Commits, 1)
	c := cl.Commits[0]
	assert.Equal(t, f.c1, c.Sha)
	assert.Equal(t, f.c1[:len(c.ShortSha)], c.ShortSha)
	assert.Equal(t, "add committed.txt", c.Subject)
	assert.Equal(t, "Tester", c.AuthorName)
	assert.Equal(t, "tester@example.com", c.AuthorEmail)
	assert.EqualValues(t, 1767323045000, c.TS)
	assert.False(t, cl.Truncated)

	for i := range 3 {
		writeFile(t, filepath.Join(f.repo, "more.txt"), strings.Repeat("m", i+1))
		gitIn(t, f.home, f.repo, "add", "more.txt")
		gitIn(t, f.home, f.repo, "commit", "-q", "-m", "more")
	}
	o.json(http.MethodGet, "/git/commits", url.Values{"root": {f.repo}, "limit": {"2"}}, nil, http.StatusOK, &cl)
	assert.Len(t, cl.Commits, 2)
	assert.True(t, cl.Truncated)
	resp, b := o.do(http.MethodGet, "/git/commits", url.Values{"root": {f.repo}, "limit": {"0"}}, nil, nil)
	assert.Equal(t, "invalid_request", o.errCode(resp, b, http.StatusBadRequest))

	// 路径 2：没有 origin/HEAD → main（refs/remotes/origin/main）。
	gitIn(t, f.home, f.repo, "remote", "set-head", "origin", "-d")
	st := statusOf(t, o, q)
	require.NotNil(t, st.Base)
	assert.Equal(t, "main", st.Base.Name)
	assert.Equal(t, "refs/remotes/origin/main", st.Base.Ref)
	assert.Equal(t, f.base, st.Base.MergeBase)

	// 显式 base：本地分支 feature（refs/heads/feature）。
	gitIn(t, f.home, f.repo, "branch", "feature", f.c1)
	st = statusOf(t, o, url.Values{"root": {f.repo}, "base": {"feature"}})
	require.NotNil(t, st.Base)
	assert.Equal(t, apigen.GitBase{Name: "feature", Ref: "refs/heads/feature", MergeBase: f.c1}, *st.Base)
	o.json(http.MethodGet, "/git/commits", url.Values{"root": {f.repo}, "base": {"feature"}}, nil, http.StatusOK, &cl)
	assert.Len(t, cl.Commits, 3, "feature..HEAD")

	// 解析不到的 base：省略 base，范围退回 uncommitted。
	st = statusOf(t, o, url.Values{"root": {f.repo}, "base": {"nope"}})
	assert.Nil(t, st.Base)
	assert.NotContains(t, filesByPath(st), "committed.txt")

	// 路径 3：无远端、本地只有 master。
	solo := filepath.Join(rt.home, "solo")
	require.NoError(t, os.MkdirAll(solo, 0o755))
	gitIn(t, f.home, solo, "init", "-q", "-b", "master")
	writeFile(t, filepath.Join(solo, "x"), "1\n")
	gitIn(t, f.home, solo, "add", "x")
	gitIn(t, f.home, solo, "commit", "-q", "-m", "x")
	gitIn(t, f.home, solo, "checkout", "-q", "-b", "topic")
	st = statusOf(t, o, url.Values{"root": {solo}})
	require.NotNil(t, st.Base)
	assert.Equal(t, "master", st.Base.Name)
	assert.Equal(t, "refs/heads/master", st.Base.Ref)
	assert.False(t, st.Base.Approximate)

	// 浅克隆：基线与 HEAD 没有共同的可见历史 → approximate，mergeBase = 基线引用本身。
	seed := filepath.Join(rt.home, "seed")
	gitIn(t, f.home, seed, "checkout", "-q", "-b", "topic")
	writeFile(t, filepath.Join(seed, "t.txt"), "t\n")
	gitIn(t, f.home, seed, "add", "t.txt")
	gitIn(t, f.home, seed, "commit", "-q", "-m", "topic")
	gitIn(t, f.home, seed, "push", "-q", "origin", "topic")
	gitIn(t, f.home, seed, "checkout", "-q", "main")
	writeFile(t, filepath.Join(seed, "m.txt"), "m\n")
	gitIn(t, f.home, seed, "add", "m.txt")
	gitIn(t, f.home, seed, "commit", "-q", "-m", "main moves")
	gitIn(t, f.home, seed, "push", "-q", "origin", "main")
	mainSha := gitIn(t, f.home, seed, "rev-parse", "main")
	shallow := filepath.Join(rt.home, "shallow")
	gitIn(t, f.home, rt.home, "clone", "-q", "--depth", "1", "--branch", "topic", "file://"+f.origin, shallow)
	gitIn(t, f.home, shallow, "fetch", "-q", "--depth", "1", "origin", "main:refs/remotes/origin/main")
	st = statusOf(t, o, url.Values{"root": {shallow}, "base": {"main"}})
	require.NotNil(t, st.Base)
	assert.Equal(t, apigen.GitBase{Name: "main", Ref: "refs/remotes/origin/main", MergeBase: mainSha, Approximate: true}, *st.Base)
}

func TestGitStageUnstage(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	o := newOrbHTTP(t, cp, rt)
	f := newGitFixture(t, rt.home)
	// 以 `-` 开头的文件名是普通路径（路径一律走 :(literal) pathspec）。
	writeFile(t, filepath.Join(f.repo, "-dash-name"), "d\n")
	setWorkdir(rt, f.repo)

	o.json(http.MethodPost, "/git/stage", nil, map[string]any{"paths": []string{"a.txt", "new.txt", "del.txt", "-dash-name"}}, http.StatusNoContent, nil)
	m := filesByPath(statusOf(t, o, nil))
	assert.Equal(t, apigen.GitFileChangeStagedFull, m["a.txt"].Staged)
	assert.Equal(t, apigen.GitFileChangeStatusAdded, m["new.txt"].Status, "暂存后不再是未跟踪")
	assert.Equal(t, apigen.GitFileChangeStagedFull, m["new.txt"].Staged)
	assert.Equal(t, apigen.GitFileChangeStagedFull, m["del.txt"].Staged)
	assert.Equal(t, "A  new.txt", gitIn(t, f.home, f.repo, "status", "--porcelain", "--", "new.txt"))

	o.json(http.MethodPost, "/git/unstage", nil, map[string]any{"root": f.repo, "paths": []string{"a.txt", "new.txt", "staged.txt"}}, http.StatusNoContent, nil)
	m = filesByPath(statusOf(t, o, nil))
	assert.Equal(t, apigen.GitFileChangeStagedNone, m["a.txt"].Staged)
	assert.Equal(t, apigen.GitFileChangeStagedNone, m["staged.txt"].Staged)
	assert.Equal(t, apigen.GitFileChangeStatusUntracked, m["new.txt"].Status)
	assert.Empty(t, gitIn(t, f.home, f.repo, "diff", "--cached", "--name-only", "--", "a.txt"), "a.txt 已不在暂存区")

	// index.lock 被占用 → 409 git_busy。
	lock := filepath.Join(f.repo, ".git", "index.lock")
	require.NoError(t, os.WriteFile(lock, nil, 0o644))
	resp, b := o.do(http.MethodPost, "/git/stage", nil, strings.NewReader(`{"paths":["a.txt"]}`), nil)
	assert.Equal(t, "git_busy", o.errCode(resp, b, http.StatusConflict))
	require.NoError(t, os.Remove(lock))

	// HEAD 未出生的仓库：stage → full；unstage 用 rm --cached → 回到未跟踪。
	fresh := filepath.Join(rt.home, "fresh")
	require.NoError(t, os.MkdirAll(fresh, 0o755))
	gitIn(t, f.home, fresh, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(fresh, "first.txt"), "1\n")
	q := url.Values{"root": {fresh}}
	st := statusOf(t, o, q)
	assert.Nil(t, st.Head)
	assert.Nil(t, st.Base)
	assert.Equal(t, apigen.GitFileChangeStatusUntracked, filesByPath(st)["first.txt"].Status)
	o.json(http.MethodPost, "/git/stage", nil, map[string]any{"root": fresh, "paths": []string{"first.txt"}}, http.StatusNoContent, nil)
	got := filesByPath(statusOf(t, o, q))["first.txt"]
	assert.Equal(t, apigen.GitFileChangeStatusAdded, got.Status)
	assert.Equal(t, apigen.GitFileChangeStagedFull, got.Staged)
	o.json(http.MethodPost, "/git/unstage", nil, map[string]any{"root": fresh, "paths": []string{"first.txt"}}, http.StatusNoContent, nil)
	assert.Equal(t, apigen.GitFileChangeStatusUntracked, filesByPath(statusOf(t, o, q))["first.txt"].Status)
	var cl apigen.GitCommitList
	o.json(http.MethodGet, "/git/commits", q, nil, http.StatusOK, &cl)
	assert.Empty(t, cl.Commits)
}

func TestGitNotARepoAndValidation(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	o := newOrbHTTP(t, cp, rt)
	ws := filepath.Join(rt.home, "workspace")
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "plain"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "proj"), 0o755))
	gitIn(t, rt.home, filepath.Join(ws, "proj"), "init", "-q", "-b", "main")

	// 缺省工作目录（~/workspace）不是仓库：repo=false 与候选子目录。
	st := statusOf(t, o, nil)
	assert.Equal(t, apigen.GitStatus{Repo: false, Scope: apigen.All, Files: []apigen.GitFileChange{}, Candidates: []string{filepath.Join(ws, "proj")}}, st)
	resp, b := o.do(http.MethodGet, "/git/diff", url.Values{"path": {"x"}}, nil, nil)
	assert.Equal(t, "not_a_repo", o.errCode(resp, b, http.StatusConflict))
	resp, b = o.do(http.MethodGet, "/git/commits", nil, nil, nil)
	assert.Equal(t, "not_a_repo", o.errCode(resp, b, http.StatusConflict))
	resp, b = o.do(http.MethodPost, "/git/stage", nil, strings.NewReader(`{"paths":["x"]}`), nil)
	assert.Equal(t, "not_a_repo", o.errCode(resp, b, http.StatusConflict))

	// $HOME 之外的 root。
	resp, b = o.do(http.MethodGet, "/git/status", url.Values{"root": {"/etc"}}, nil, nil)
	assert.Equal(t, "outside_home", o.errCode(resp, b, http.StatusForbidden))

	repoQ := func(kv ...string) url.Values {
		q := url.Values{"root": {filepath.Join(ws, "proj")}}
		for i := 0; i+1 < len(kv); i += 2 {
			q.Set(kv[i], kv[i+1])
		}
		return q
	}
	for _, c := range []struct {
		path string
		q    url.Values
		code string
	}{
		{"/git/status", repoQ("base", "-main"), "invalid_request"},
		{"/git/status", repoQ("base", "main..evil"), "invalid_request"},
		{"/git/status", repoQ("commit", "--all", "scope", "commit"), "invalid_request"},
		{"/git/status", repoQ("commit", "HEAD", "scope", "commit"), "invalid_request"},
		{"/git/status", repoQ("scope", "commit"), "invalid_request"},
		{"/git/status", repoQ("scope", "bogus"), "invalid_request"},
		{"/git/commits", repoQ("base", "-x"), "invalid_request"},
		{"/git/diff", repoQ("path", "../x"), "invalid_path"},
		{"/git/diff", repoQ("path", "a/../../x"), "invalid_path"},
		{"/git/diff", repoQ("path", "/etc/passwd"), "invalid_path"},
		{"/git/diff", repoQ(), "invalid_path"},
		{"/git/diff", repoQ("path", "ok", "oldPath", "../x"), "invalid_path"},
	} {
		resp, b := o.do(http.MethodGet, c.path, c.q, nil, nil)
		assert.Equal(t, c.code, o.errCode(resp, b, http.StatusBadRequest), "%s %v", c.path, c.q)
	}
	for _, body := range []string{
		`{"root":"` + filepath.Join(ws, "proj") + `","paths":[]}`,
		`{"root":"` + filepath.Join(ws, "proj") + `","paths":[""]}`,
		`{"root":"` + filepath.Join(ws, "proj") + `","paths":["../x"]}`,
		`{"root":"` + filepath.Join(ws, "proj") + `","paths":["/abs"]}`,
		`{"root":"` + filepath.Join(ws, "proj") + `","paths":["a\u0000b"]}`,
	} {
		resp, b := o.do(http.MethodPost, "/git/stage", nil, bytes.NewReader([]byte(body)), nil)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "%s: %s", body, b)
	}
}
