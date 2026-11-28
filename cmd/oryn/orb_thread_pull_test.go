package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// pullGit 在 dir 里执行 git（固定身份、不读系统配置，可追加环境变量），失败即终止测试。
func pullGit(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com"), env...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// fakePullSource 扮演「控制面 + 对方 orb 的 /sync/prepare」：把 src 的工作区快照成以 HEAD 为父的提交，
// 按 have 里 src 已有的提交排除对象，打成 bundle 写到 bundlePath；快照已在 have 里时不打包。
func fakePullSource(t *testing.T, src string) orbsock.Handler {
	return func(_ context.Context, method string, params json.RawMessage, _ *orbsock.Responder) (any, error) {
		if method != orbsock.MethodThreadPull {
			return nil, orbsock.Errorf(orbsock.CodeMethodNotFound, "%s", method)
		}
		var p apigen.LinkCtlThreadPullParams
		require.NoError(t, json.Unmarshal(params, &p))
		head := pullGit(t, src, nil, "rev-parse", "HEAD")
		idx := []string{"GIT_INDEX_FILE=" + filepath.Join(t.TempDir(), "index")}
		pullGit(t, src, idx, "read-tree", "HEAD")
		pullGit(t, src, idx, "add", "-A", "--", ".")
		tree := pullGit(t, src, idx, "write-tree")
		snap := head
		if tree != pullGit(t, src, nil, "rev-parse", "HEAD^{tree}") {
			snap = pullGit(t, src, nil, "commit-tree", tree, "-p", head, "-m", "oryn sync snapshot")
		}
		pullGit(t, src, nil, "update-ref", "refs/oryn/sync/head", head)
		pullGit(t, src, nil, "update-ref", "refs/oryn/sync/snapshot", snap)
		branch := "main"
		res := apigen.LinkCtlThreadPullResult{Branch: &branch, Head: head, Snapshot: snap, Dirty: snap != head}
		if slices.Contains(p.Have, snap) {
			return res, nil
		}
		args := []string{"bundle", "create", "--quiet", p.BundlePath, "refs/oryn/sync/snapshot", "refs/oryn/sync/head", "--not"}
		for _, h := range p.Have {
			if exec.Command("git", "-C", src, "cat-file", "-e", h+"^{commit}").Run() == nil {
				args = append(args, h)
			}
		}
		pullGit(t, src, nil, args...)
		res.Bundled = true
		return res, nil
	}
}

// 对方线程有一个未推送的提交和一个未跟踪的新文件：取回后 head / snapshot 两个 ref 对上对方的提交，snapshot 里有那个
// 新文件；本仓库的分支、工作区与 git 目录都不变。再取一次时对象已在本地（不发 bundle），只重写 ref。
func TestThreadPull(t *testing.T) {
	var src string
	f := startOrbThreadSock(t, func(ctx context.Context, method string, params json.RawMessage, out *orbsock.Responder) (any, error) {
		return fakePullSource(t, src)(ctx, method, params, out)
	})
	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	pullGit(t, root, nil, "init", "-q", "-b", "main", src)
	require.NoError(t, os.WriteFile(filepath.Join(src, "a.txt"), []byte("base\n"), 0o644))
	pullGit(t, src, nil, "add", "a.txt")
	pullGit(t, src, nil, "commit", "-q", "-m", "base")
	pullGit(t, root, nil, "clone", "-q", src, dst)
	base := pullGit(t, dst, nil, "rev-parse", "HEAD")

	require.NoError(t, os.WriteFile(filepath.Join(src, "b.txt"), []byte("feature\n"), 0o644))
	pullGit(t, src, nil, "add", "b.txt")
	pullGit(t, src, nil, "commit", "-q", "-m", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(src, "c.txt"), []byte("wip\n"), 0o644))
	srcHead := pullGit(t, src, nil, "rev-parse", "HEAD")

	ctx := context.Background()
	res, err := threadPull(ctx, dst, "T-child")
	require.NoError(t, err)
	assert.True(t, res.Bundled)
	assert.True(t, res.Dirty)
	assert.Equal(t, srcHead, res.Head)
	ns := "refs/oryn/threads/T-child/"
	assert.Equal(t, srcHead, pullGit(t, dst, nil, "rev-parse", ns+"head"))
	assert.Equal(t, res.Snapshot, pullGit(t, dst, nil, "rev-parse", ns+"snapshot"))
	assert.Equal(t, "wip", pullGit(t, dst, nil, "show", ns+"snapshot:c.txt"), "未提交的改动在快照里")
	assert.Equal(t, "feature", pullGit(t, dst, nil, "show", ns+"head:b.txt"))

	assert.Equal(t, base, pullGit(t, dst, nil, "rev-parse", "HEAD"), "当前分支不动")
	assert.Empty(t, pullGit(t, dst, nil, "status", "--porcelain"), "工作区不动")
	assert.Equal(t, ns+"head\n"+ns+"snapshot", pullGit(t, dst, nil, "for-each-ref", "--format=%(refname)", strings.TrimSuffix(ns, "/")),
		"只留 head / snapshot")
	assert.Empty(t, pullGit(t, dst, nil, "for-each-ref", "refs/oryn/pull-tmp"), "本次的临时 ref 已删")
	left, _ := filepath.Glob(filepath.Join(dst, ".git", "oryn-pull-*"))
	assert.Empty(t, left, "临时 bundle 目录已删")

	reqs := f.requests()
	require.Len(t, reqs, 1)
	var p apigen.LinkCtlThreadPullParams
	require.NoError(t, json.Unmarshal(reqs[0].Params, &p))
	assert.Equal(t, "T-caller", p.ThreadID)
	assert.Equal(t, "T-child", p.Target)
	assert.Equal(t, base, p.Have[0], "have 先列本仓库的 HEAD")
	assert.True(t, filepath.IsAbs(p.BundlePath))

	// 再取一次：上次的快照已在 have 里，不发 bundle，ref 照样对上
	res2, err := threadPull(ctx, dst, "T-child")
	require.NoError(t, err)
	assert.False(t, res2.Bundled)
	assert.Equal(t, res.Snapshot, pullGit(t, dst, nil, "rev-parse", ns+"snapshot"))

	// 发布是一个 ref 事务：snapshot 写不进去（与已有 ref 目录冲突）时 head 也保持上一次的值，临时 ref 照样清掉
	pullGit(t, dst, nil, "update-ref", "-d", ns+"snapshot")
	pullGit(t, dst, nil, "update-ref", ns+"snapshot/block", base)
	require.NoError(t, os.WriteFile(filepath.Join(src, "d.txt"), []byte("more\n"), 0o644))
	pullGit(t, src, nil, "add", "d.txt")
	pullGit(t, src, nil, "commit", "-q", "-m", "more")
	_, err = threadPull(ctx, dst, "T-child")
	require.ErrorContains(t, err, "两者都未改动", "失败发生在发布这一步")
	assert.Equal(t, srcHead, pullGit(t, dst, nil, "rev-parse", ns+"head"), "head 不能单独更新")
	assert.Empty(t, pullGit(t, dst, nil, "for-each-ref", "refs/oryn/pull-tmp"), "失败时临时 ref 也已删")
}
