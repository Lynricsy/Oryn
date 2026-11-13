package orbdrt

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

func syncPrepareReq(o *orbHTTP, body apigen.OrbSyncPrepareRequest, want int) (apigen.OrbSyncState, string) {
	o.t.Helper()
	if want == http.StatusOK {
		var st apigen.OrbSyncState
		o.json(http.MethodPost, "/sync/prepare", nil, body, want, &st)
		return st, ""
	}
	var e apigen.Error
	o.json(http.MethodPost, "/sync/prepare", nil, body, want, &e)
	return apigen.OrbSyncState{}, e.Code
}

func TestSyncPrepareAndBundle(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	o := newOrbHTTP(t, cp, rt)
	f := newGitFixture(t, rt.home)
	setWorkdir(rt, f.repo)

	statusBefore := gitIn(t, f.home, f.repo, "status", "--porcelain")
	cachedBefore := gitIn(t, f.home, f.repo, "diff", "--cached", "--name-status")
	st, _ := syncPrepareReq(o, apigen.OrbSyncPrepareRequest{Have: []string{f.base}}, http.StatusOK)
	assert.Equal(t, f.repo, st.Root)
	require.NotNil(t, st.Branch)
	assert.Equal(t, "main", *st.Branch)
	assert.Equal(t, f.c1, st.Head)
	assert.True(t, st.Dirty)
	assert.NotEqual(t, st.Head, st.Snapshot)
	assert.Equal(t, []string{f.base}, st.Common)
	require.NotNil(t, st.Bundle)
	assert.Regexp(t, `^[0-9a-f]{16}$`, st.Bundle.ID)

	// 真实 index 与工作区不变
	assert.Equal(t, statusBefore, gitIn(t, f.home, f.repo, "status", "--porcelain"))
	assert.Equal(t, cachedBefore, gitIn(t, f.home, f.repo, "diff", "--cached", "--name-status"))
	assert.NoFileExists(t, filepath.Join(f.repo, ".git", syncIndexName), "临时 index 用完即删")

	// 快照：父提交 = HEAD，树 = 工作区（含未跟踪，不含被忽略），身份与时间固定
	assert.Equal(t, f.c1, gitIn(t, f.home, f.repo, "rev-parse", st.Snapshot+"^"))
	files := strings.Split(gitIn(t, f.home, f.repo, "ls-tree", "-r", "--name-only", st.Snapshot), "\n")
	assert.Contains(t, files, "new.txt")
	assert.Contains(t, files, "renamed.txt")
	assert.NotContains(t, files, "ignored.log")
	assert.NotContains(t, files, "del.txt")
	assert.NotContains(t, files, "old.txt")
	assert.Equal(t, "m3", gitIn(t, f.home, f.repo, "show", st.Snapshot+":mixed.txt"), "部分暂存的文件取工作区版本")
	assert.Equal(t, "Oryn Sync <sync@oryn.invalid> 0", gitIn(t, f.home, f.repo, "log", "-1", "--format=%an <%ae> %at", st.Snapshot))
	assert.Equal(t, st.Snapshot, gitIn(t, f.home, f.repo, "rev-parse", syncRefSnapshot))
	assert.Equal(t, f.c1, gitIn(t, f.home, f.repo, "rev-parse", syncRefHead))

	// 同一状态再调一次：同一个快照 sha
	again, _ := syncPrepareReq(o, apigen.OrbSyncPrepareRequest{Have: []string{f.base}}, http.StatusOK)
	assert.Equal(t, st.Snapshot, again.Snapshot)

	// 「本地」克隆取回 bundle：fetch 成功且快照与 head 都在
	resp, b := o.do(http.MethodGet, "/sync/bundle", url.Values{"id": {st.Bundle.ID}}, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	assert.Equal(t, int64(len(b)), st.Bundle.Size)
	local := filepath.Join(t.TempDir(), "local")
	gitIn(t, f.home, f.home, "clone", "-q", f.origin, local)
	bundle := filepath.Join(t.TempDir(), "x.bundle")
	require.NoError(t, os.WriteFile(bundle, b, 0o600))
	gitIn(t, f.home, local, "fetch", "-q", bundle, "+refs/oryn/sync/*:refs/oryn/sync/k/*")
	gitIn(t, f.home, local, "cat-file", "-e", st.Snapshot+"^{commit}")
	gitIn(t, f.home, local, "cat-file", "-e", f.c1+"^{commit}")

	// have 已含快照 → 不建 bundle
	have, _ := syncPrepareReq(o, apigen.OrbSyncPrepareRequest{Have: []string{st.Snapshot, f.base}}, http.StatusOK)
	assert.Nil(t, have.Bundle)
	assert.Equal(t, []string{st.Snapshot, f.base}, have.Common)

	// 干净的工作区：snapshot = head、dirty=false
	gitIn(t, f.home, f.repo, "stash", "-q", "--include-untracked")
	clean, _ := syncPrepareReq(o, apigen.OrbSyncPrepareRequest{Have: []string{f.base}}, http.StatusOK)
	assert.False(t, clean.Dirty)
	assert.Equal(t, f.c1, clean.Snapshot)
	require.NotNil(t, clean.Bundle)

	// 无共同提交 → sync_no_common；--force 且非浅克隆 → 完整 bundle
	unknown := strings.Repeat("ab", 20)
	_, code := syncPrepareReq(o, apigen.OrbSyncPrepareRequest{Have: []string{unknown}}, http.StatusConflict)
	assert.Equal(t, "sync_no_common", code)
	force := true
	full, _ := syncPrepareReq(o, apigen.OrbSyncPrepareRequest{Have: []string{unknown}, Force: &force}, http.StatusOK)
	require.NotNil(t, full.Bundle)
	assert.Empty(t, full.Common)

	// 非法 have / 超量 have → 400
	_, code = syncPrepareReq(o, apigen.OrbSyncPrepareRequest{Have: []string{"HEAD"}}, http.StatusBadRequest)
	assert.Equal(t, "invalid_request", code)
	many := make([]string, maxSyncHave+1)
	for i := range many {
		many[i] = unknown
	}
	_, code = syncPrepareReq(o, apigen.OrbSyncPrepareRequest{Have: many}, http.StatusBadRequest)
	assert.Equal(t, "invalid_request", code)

	// bundle id：非法 → 400；不存在 → 404 sync_bundle_gone
	resp, b = o.do(http.MethodGet, "/sync/bundle", url.Values{"id": {"../../etc"}}, nil, nil)
	assert.Equal(t, "invalid_request", o.errCode(resp, b, http.StatusBadRequest))
	resp, b = o.do(http.MethodGet, "/sync/bundle", url.Values{"id": {"0123456789abcdef"}}, nil, nil)
	assert.Equal(t, "sync_bundle_gone", o.errCode(resp, b, http.StatusNotFound))
}

func TestSyncPrepareRefusals(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	o := newOrbHTTP(t, cp, rt)
	f := newGitFixture(t, rt.home)

	// 浅克隆 + force + 无共同提交 → sync_shallow
	gitIn(t, f.home, f.repo, "push", "-q", "origin", "main")
	shallow := filepath.Join(rt.home, "workspace", "shallow")
	gitIn(t, f.home, f.home, "clone", "-q", "--depth", "1", "file://"+f.origin, shallow)
	require.FileExists(t, filepath.Join(shallow, ".git", "shallow"))
	force := true
	unknown := strings.Repeat("cd", 20)
	_, code := syncPrepareReq(o, apigen.OrbSyncPrepareRequest{Root: &shallow, Have: []string{unknown}, Force: &force}, http.StatusConflict)
	assert.Equal(t, "sync_shallow", code)
	// 浅克隆但有共同提交：照常打包
	writeFile(t, filepath.Join(shallow, "s.txt"), "s\n")
	st, _ := syncPrepareReq(o, apigen.OrbSyncPrepareRequest{Root: &shallow, Have: []string{f.c1}}, http.StatusOK)
	assert.Equal(t, f.c1, st.Head)
	assert.True(t, st.Dirty)
	require.NotNil(t, st.Bundle)

	// HEAD 未出生 → sync_no_commits
	empty := filepath.Join(rt.home, "workspace", "empty")
	gitIn(t, f.home, f.home, "init", "-q", empty)
	_, code = syncPrepareReq(o, apigen.OrbSyncPrepareRequest{Root: &empty}, http.StatusConflict)
	assert.Equal(t, "sync_no_commits", code)

	// 不在仓库内 → not_a_repo
	plain := filepath.Join(rt.home, "plain")
	require.NoError(t, os.MkdirAll(plain, 0o755))
	_, code = syncPrepareReq(o, apigen.OrbSyncPrepareRequest{Root: &plain}, http.StatusConflict)
	assert.Equal(t, "not_a_repo", code)
}
