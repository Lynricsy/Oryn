package repos

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// readEnv 起一个镜像已就绪的项目；prep 在首次克隆前改上游裸仓库。
func readEnv(t *testing.T, gh GitHub, prep func(bare string)) (*testEnv, string, string) {
	t.Helper()
	if gh == nil {
		gh = fakeGitHub{}
	}
	e := newTestEnv(t, nil, nil, gh)
	bare := e.fake.BarePath("acme/widgets")
	if prep != nil {
		prep(bare)
	}
	pid := e.project(t, widgetsURL)
	st := e.settle(t, pid)
	require.Equal(t, StateReady, st.State, "lastError=%+v", st.LastError)
	return e, pid, bare
}

func headOf(t *testing.T, bare, ref string) string {
	t.Helper()
	return gitCmd(t, bare, "--git-dir", bare, "rev-parse", ref)
}

func TestReadRefsPaging(t *testing.T) {
	e, pid, _ := readEnv(t, nil, func(bare string) {
		for i := range 59 {
			gitCmd(t, bare, "--git-dir", bare, "branch", "b"+strconv.Itoa(100+i), "main")
		}
		gitCmd(t, bare, "--git-dir", bare, "tag", "v1", "main")
		gitCmd(t, bare, "--git-dir", bare, "tag", "-a", "v2", "-m", "release two", "main")
	})
	ctx := context.Background()
	first, err := e.svc.Refs(ctx, pid, apigen.RepoRefBranch, "", nil)
	require.NoError(t, err)
	require.Len(t, first.Refs, 50)
	assert.Equal(t, "main", first.Refs[0].Name)
	assert.True(t, first.Refs[0].Default)
	assert.Equal(t, "initial commit", first.Refs[0].Commit.Subject)
	assert.Equal(t, "fake@example.com", first.Refs[0].Commit.AuthorEmail)
	require.NotNil(t, first.NextCursor)
	second, err := e.svc.Refs(ctx, pid, apigen.RepoRefBranch, *first.NextCursor, nil)
	require.NoError(t, err)
	assert.Len(t, second.Refs, 10)
	assert.Nil(t, second.NextCursor)

	main := first.Refs[0].Commit.Sha
	tags, err := e.svc.Refs(ctx, pid, apigen.RepoRefTag, "", new(10))
	require.NoError(t, err)
	require.Len(t, tags.Refs, 2)
	byName := map[string]apigen.ProjectRepoRef{}
	for _, r := range tags.Refs {
		byName[r.Name] = r
	}
	assert.Equal(t, main, byName["v1"].Commit.Sha)
	assert.False(t, *byName["v1"].Annotated)
	assert.Equal(t, main, byName["v2"].Commit.Sha, "附注标签剥到提交")
	assert.True(t, *byName["v2"].Annotated)
	assert.Equal(t, "initial commit", byName["v2"].Commit.Subject)

	_, err = e.svc.Refs(ctx, pid, "bogus", "", nil)
	assert.Equal(t, "invalid_request", errCode(err))
	_, err = e.svc.Refs(ctx, pid, apigen.RepoRefBranch, "", new(201))
	assert.Equal(t, "invalid_request", errCode(err))
}

func TestReadCommitsPagingAndPath(t *testing.T) {
	e, pid, bare := readEnv(t, nil, func(bare string) {
		commitTo(t, bare, "main", map[string]string{"docs/a.md": "a\n"}, "add docs")
		commitTo(t, bare, "main", map[string]string{"src/b.go": "package b\n"}, "add src")
	})
	ctx := context.Background()
	page, err := e.svc.Commits(ctx, pid, "", "", "", new(2))
	require.NoError(t, err)
	assert.Equal(t, "main", page.Ref)
	require.Len(t, page.Commits, 2)
	assert.Equal(t, "add src", page.Commits[0].Subject)
	assert.Equal(t, "add docs", page.Commits[1].Subject)
	assert.Len(t, page.Commits[0].Parents, 1)
	require.NotNil(t, page.NextCursor)
	assert.Equal(t, page.Commit+":2", *page.NextCursor)

	// 上游 main 前进后，第二页仍落在原来的头提交上
	commitTo(t, bare, "main", map[string]string{"c.txt": "c\n"}, "add c")
	e.sync(t, pid)
	next, err := e.svc.Commits(ctx, pid, "", "", *page.NextCursor, new(2))
	require.NoError(t, err)
	require.Len(t, next.Commits, 1)
	assert.Equal(t, "initial commit", next.Commits[0].Subject)
	assert.Empty(t, next.Commits[0].Parents)
	assert.Nil(t, next.NextCursor)
	fresh, err := e.svc.Commits(ctx, pid, "main", "", "", nil)
	require.NoError(t, err)
	assert.Equal(t, "add c", fresh.Commits[0].Subject)

	// 按路径过滤
	docs, err := e.svc.Commits(ctx, pid, "", "docs", "", nil)
	require.NoError(t, err)
	require.Len(t, docs.Commits, 1)
	assert.Equal(t, "add docs", docs.Commits[0].Subject)
}

func TestReadCommitDetailAndDiff(t *testing.T) {
	big := strings.Repeat("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcde\n", 20000) // 1.25 MiB
	e, pid, bare := readEnv(t, nil, func(bare string) {
		commitTo(t, bare, "main", map[string]string{"README.md": "# acme/widgets\nmore\nlines\n", "bin.dat": "a\x00b", "big.txt": big}, "second\n\nwith a body")
	})
	ctx := context.Background()
	head := headOf(t, bare, "main")
	d, err := e.svc.Commit(ctx, pid, head[:10])
	require.NoError(t, err)
	assert.Equal(t, head, d.Sha)
	assert.Equal(t, "second", d.Subject)
	assert.Equal(t, "with a body", d.Body)
	assert.Equal(t, "t", d.CommitterName)
	require.Len(t, d.Files, 3)
	assert.Equal(t, []string{"README.md", "big.txt", "bin.dat"}, []string{d.Files[0].Path, d.Files[1].Path, d.Files[2].Path})
	assert.Equal(t, apigen.GitFileChangeStatusModified, d.Files[0].Status)
	assert.True(t, d.Files[2].Binary)
	assert.Equal(t, 2+20000, d.Additions)
	assert.Equal(t, 0, d.Deletions)
	assert.False(t, d.Truncated)

	// 根提交相对空树
	root, err := e.svc.Commit(ctx, pid, d.Parents[0])
	require.NoError(t, err)
	require.Len(t, root.Files, 1)
	assert.Equal(t, apigen.GitFileChangeStatusAdded, root.Files[0].Status)
	assert.Equal(t, 1, root.Additions)

	diff, err := e.svc.CommitDiff(ctx, pid, head, "README.md", "")
	require.NoError(t, err)
	assert.Equal(t, apigen.Text, diff.Kind)
	assert.Contains(t, diff.Patch, "+more\n")
	diff, err = e.svc.CommitDiff(ctx, pid, head, "big.txt", "")
	require.NoError(t, err)
	assert.True(t, diff.Truncated)
	assert.LessOrEqual(t, len(diff.Patch), patchMax)
	assert.True(t, strings.HasSuffix(diff.Patch, "\n"))
	diff, err = e.svc.CommitDiff(ctx, pid, head, "bin.dat", "")
	require.NoError(t, err)
	assert.Equal(t, apigen.Binary, diff.Kind)
	assert.Empty(t, diff.Patch)

	_, err = e.svc.Commit(ctx, pid, strings.Repeat("e", 40))
	assert.Equal(t, "commit_not_found", errCode(err))
}

// commitSymlink 在上游 main 上提交一个符号链接。
func commitSymlink(t *testing.T, bare, name, target string) {
	t.Helper()
	work := t.TempDir()
	gitCmd(t, work, "clone", "-q", bare, ".")
	require.NoError(t, os.Symlink(target, filepath.Join(work, name)))
	gitCmd(t, work, "add", "-A")
	gitCmd(t, work, "commit", "-q", "-m", "link")
	gitCmd(t, work, "push", "-q", "origin", "main")
}

func TestReadTree(t *testing.T) {
	e, pid, _ := readEnv(t, nil, func(bare string) {
		commitTo(t, bare, "main", map[string]string{"docs/guide.md": "# guide\n", "docs/b.txt": "b\n", "Zeta.txt": "z\n"}, "add docs")
		commitTo(t, bare, "main", map[string]string{"docs/b.txt": "b2\n", "app.bin": "\x00\x01"}, "touch b")
		commitSymlink(t, bare, "link", "docs/guide.md")
	})
	ctx := context.Background()
	root, err := e.svc.Tree(ctx, pid, "", "", true)
	require.NoError(t, err)
	assert.Equal(t, "main", root.Ref)
	assert.Equal(t, apigen.RepoEntryDir, root.Self.Type)
	var names []string
	byName := map[string]apigen.ProjectRepoTreeEntry{}
	for _, en := range root.Entries {
		names = append(names, en.Name)
		byName[en.Name] = en
	}
	assert.Equal(t, []string{"docs", "app.bin", "link", "README.md", "Zeta.txt"}, names, "目录在前，按名不分大小写")
	assert.True(t, root.LastCommitsComplete)
	assert.Equal(t, "initial commit", byName["README.md"].LastCommit.Subject)
	assert.Equal(t, "touch b", byName["docs"].LastCommit.Subject)
	assert.Equal(t, "add docs", byName["Zeta.txt"].LastCommit.Subject)
	assert.Equal(t, apigen.RepoEntrySymlink, byName["link"].Type)
	assert.Equal(t, "docs/guide.md", *byName["link"].Target)
	assert.Equal(t, 0o644, byName["README.md"].Mode)
	assert.Equal(t, int64(len("# acme/widgets\n")), byName["README.md"].Size)
	assert.Equal(t, 0o755, byName["docs"].Mode)

	sub, err := e.svc.Tree(ctx, pid, "main", "docs", true)
	require.NoError(t, err)
	require.Len(t, sub.Entries, 2)
	assert.Equal(t, "docs/b.txt", sub.Entries[0].Path)
	assert.Equal(t, "touch b", sub.Entries[0].LastCommit.Subject)
	assert.Equal(t, "add docs", sub.Entries[1].LastCommit.Subject)

	file, err := e.svc.Tree(ctx, pid, "", "docs/guide.md", true)
	require.NoError(t, err)
	assert.Equal(t, apigen.RepoEntryFile, file.Self.Type)
	assert.Empty(t, file.Entries)
	assert.True(t, *file.Self.Text)
	assert.True(t, strings.HasPrefix(*file.Self.Mime, "text/"), *file.Self.Mime) // .md 取决于系统的 mime 表，退回内容探测
	bin, err := e.svc.Tree(ctx, pid, "", "app.bin", false)
	require.NoError(t, err)
	assert.False(t, *bin.Self.Text)

	// 读提交数上限：找不全的子项省略 lastCommit
	e.svc.treeWalkMax = 1
	clear(e.svc.treeCache)
	part, err := e.svc.Tree(ctx, pid, "", "", true)
	require.NoError(t, err)
	assert.False(t, part.LastCommitsComplete)
	assert.Nil(t, part.Entries[len(part.Entries)-1].LastCommit)

	_, err = e.svc.Tree(ctx, pid, "", "nope", true)
	assert.Equal(t, "path_not_found", errCode(err))
}

func TestReadBlob(t *testing.T) {
	e, pid, bare := readEnv(t, nil, func(bare string) {
		commitTo(t, bare, "main", map[string]string{"docs/page.html": "<b>x</b>", "img.png": "\x89PNG\r\n\x1a\n"}, "files")
	})
	ctx := context.Background()
	b, err := e.svc.Blob(ctx, pid, "", "README.md", "")
	require.NoError(t, err)
	body, err := io.ReadAll(b.Body)
	require.NoError(t, err)
	require.NoError(t, b.Body.Close())
	assert.Equal(t, "# acme/widgets\n", string(body))
	assert.Equal(t, int64(len(body)), b.Size)
	assert.False(t, b.Immutable)
	assert.False(t, b.Inline)

	nm, err := e.svc.Blob(ctx, pid, "", "README.md", `W/"x", "`+b.OID+`"`)
	require.NoError(t, err)
	assert.True(t, nm.NotModified)
	assert.Nil(t, nm.Body)

	head := headOf(t, bare, "main")
	html, err := e.svc.Blob(ctx, pid, head, "docs/page.html", "")
	require.NoError(t, err)
	_ = html.Body.Close()
	assert.Equal(t, "text/plain; charset=utf-8", html.ContentType)
	assert.True(t, html.Immutable, "ref 是完整 sha")
	img, err := e.svc.Blob(ctx, pid, "", "img.png", "")
	require.NoError(t, err)
	_ = img.Body.Close()
	assert.True(t, img.Inline)
	assert.Equal(t, "image/png", img.ContentType)

	_, err = e.svc.Blob(ctx, pid, "", "docs", "")
	assert.Equal(t, "not_a_file", errCode(err))
	e.svc.maxBlobBytes = 4
	_, err = e.svc.Blob(ctx, pid, "", "README.md", "")
	assert.Equal(t, "too_large", errCode(err))
}

func TestReadBadInput(t *testing.T) {
	e, pid, _ := readEnv(t, nil, nil)
	ctx := context.Background()
	_, err := e.svc.Tree(ctx, pid, "-x", "", false)
	assert.Equal(t, "invalid_request", errCode(err))
	for _, p := range []string{"../a", "/a", "a//b", "-a", "a/./b", "a\x00b"} {
		_, err = e.svc.Tree(ctx, pid, "", p, false)
		assert.Equal(t, "invalid_request", errCode(err), p)
	}
	_, err = e.svc.Commit(ctx, pid, "zz")
	assert.Equal(t, "invalid_request", errCode(err))
	_, err = e.svc.Commits(ctx, pid, "", "", "bogus", nil)
	assert.Equal(t, "invalid_request", errCode(err))
	_, err = e.svc.Tree(ctx, pid, "nope", "", false)
	assert.Equal(t, "ref_not_found", errCode(err))
	_, err = e.svc.Blob(ctx, pid, "", "", "")
	assert.Equal(t, "invalid_request", errCode(err))

	// 没有仓库地址 → no_repo；已归档且没克隆过 → repo_not_ready
	_, err = e.svc.Tree(ctx, e.project(t, ""), "", "", false)
	assert.Equal(t, "no_repo", errCode(err))
	arch := e.project(t, widgetsURL)
	e.exec(t, `UPDATE projects SET archived_at = 1 WHERE id = ?`, arch)
	_, err = e.svc.Commits(ctx, arch, "", "", "", nil)
	assert.Equal(t, "repo_not_ready", errCode(err))
}

func TestReadThreadLinks(t *testing.T) {
	calls := &atomic.Int32{}
	gh := fakeGitHub{pulls: map[string]string{}, calls: calls}
	e, pid, bare := readEnv(t, gh, func(bare string) {
		commitTo(t, bare, "main", map[string]string{"a": "1"}, "one")
		commitTo(t, bare, "main", map[string]string{"a": "2"}, "two")
		commitTo(t, bare, "main", map[string]string{"a": "3"}, "three")
	})
	ctx := context.Background()
	shas := strings.Fields(gitCmd(t, bare, "--git-dir", bare, "rev-list", "--max-count=3", "main"))
	three, two, one := shas[0], shas[1], shas[2]
	pushed, sampled, pulled := e.thread(t, pid), e.thread(t, pid), e.thread(t, pid)
	e.exec(t, `UPDATE threads SET title = 'pulled thread' WHERE id = ?`, pulled)
	// three：线程采样到，又有推送记录 → push 优先；two：只有采样
	for _, sha := range []string{three, two} {
		require.NoError(t, e.st.Q.InsertThreadCommit(ctx, db.InsertThreadCommitParams{ThreadID: sampled, Sha: sha, Ts: 1}))
	}
	require.NoError(t, e.st.Q.LinkRepoCommitThread(ctx, db.LinkRepoCommitThreadParams{ProjectID: pid, Sha: three, ThreadID: &pushed, Source: "push", LinkedAt: 1}))

	list, err := e.svc.Commits(ctx, pid, "", "", "", nil)
	require.NoError(t, err)
	got := map[string]*apigen.ProjectRepoThreadLink{}
	for _, c := range list.Commits {
		got[c.Sha] = c.Thread
	}
	require.NotNil(t, got[three])
	assert.Equal(t, apigen.ProjectRepoThreadLink{ID: pushed, Title: "t", Source: apigen.RepoThreadPush}, *got[three])
	require.NotNil(t, got[two])
	assert.Equal(t, sampled, got[two].ID)
	assert.Equal(t, apigen.RepoThreadSampled, got[two].Source)
	assert.Nil(t, got[one])
	assert.Zero(t, calls.Load(), "列表页不反查 GitHub")

	// 不是 GitHub 项目时详情也不反查
	d, err := e.svc.Commit(ctx, pid, one)
	require.NoError(t, err)
	assert.Nil(t, d.Thread)
	assert.Zero(t, calls.Load())

	// GitHub 项目：反查一次并缓存
	e.exec(t, `UPDATE projects SET github_repo_id = 42 WHERE id = ?`, pid)
	gh.pulls[one] = pulled
	for range 2 {
		d, err = e.svc.Commit(ctx, pid, one)
		require.NoError(t, err)
		require.NotNil(t, d.Thread)
		assert.Equal(t, apigen.ProjectRepoThreadLink{ID: pulled, Title: "pulled thread", Source: apigen.RepoThreadPull}, *d.Thread)
	}
	assert.Equal(t, int32(1), calls.Load())

	// 没有线程：24 小时内不再反查
	root := strings.TrimSpace(gitCmd(t, bare, "--git-dir", bare, "rev-list", "--max-parents=0", "main"))
	for range 2 {
		d, err = e.svc.Commit(ctx, pid, root)
		require.NoError(t, err)
		assert.Nil(t, d.Thread)
	}
	assert.Equal(t, int32(2), calls.Load())
	e.clock.Advance(25 * time.Hour)
	_, err = e.svc.Commit(ctx, pid, root)
	require.NoError(t, err)
	assert.Equal(t, int32(3), calls.Load())
}
