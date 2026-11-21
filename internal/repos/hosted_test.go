package repos

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

var testIdentity = Identity{Name: "A", Email: "a@x"}

func TestInitHostedInitialCommit(t *testing.T) {
	e := newTestEnv(t, nil, nil, nil)
	ctx := context.Background()
	id := e.project(t, "")

	adopted, err := e.svc.InitHosted(ctx, id, "main", true, testIdentity)
	require.NoError(t, err)
	assert.False(t, adopted)
	dir := e.svc.dir(id)

	head := gitCmd(t, dir, "rev-parse", "refs/heads/main")
	assert.Regexp(t, `^[0-9a-f]{40}$`, head)
	assert.Equal(t, "1", gitCmd(t, dir, "rev-list", "--count", "main"))
	assert.Equal(t, "A\x00a@x\x00A\x00Initial commit", gitCmd(t, dir, "log", "--format=%an%x00%ae%x00%cn%x00%s"))
	assert.Empty(t, gitCmd(t, dir, "ls-tree", "main"))
	assert.Equal(t, "refs/heads/main", gitCmd(t, dir, "symbolic-ref", "HEAD"))
	assert.Equal(t, "true", gitCmd(t, dir, "config", "core.logAllRefUpdates"))

	marker, err := os.ReadFile(filepath.Join(dir, hostedMarker))
	require.NoError(t, err)
	assert.Equal(t, id+"\n", string(marker))
	require.Error(t, removeRepoDir(dir, false), "removeRepoDir 不能删托管正本")
	assert.True(t, exists(dir))
	tmps, _ := filepath.Glob(filepath.Join(e.svc.cfg.Dir, id+".git.tmp-*"))
	assert.Empty(t, tmps, "临时目录没有清理")

	// 再调一次：认领已有的正本，内容不动
	adopted, err = e.svc.InitHosted(ctx, id, "dev", true, Identity{Name: "B", Email: "b@x"})
	require.NoError(t, err)
	assert.True(t, adopted)
	assert.Equal(t, head, gitCmd(t, dir, "rev-parse", "refs/heads/main"))
	assert.Equal(t, "refs/heads/main", gitCmd(t, dir, "symbolic-ref", "HEAD"))
}

func TestInitHostedEmpty(t *testing.T) {
	e := newTestEnv(t, nil, nil, nil)
	id := e.project(t, "")

	_, err := e.svc.InitHosted(context.Background(), id, "trunk", false, testIdentity)
	require.NoError(t, err)
	dir := e.svc.dir(id)
	assert.Empty(t, gitCmd(t, dir, "for-each-ref"), "没有初始提交时不应有任何 ref")
	assert.Equal(t, "refs/heads/trunk", gitCmd(t, dir, "symbolic-ref", "HEAD"))
	assert.True(t, exists(filepath.Join(dir, hostedMarker)))
}

func TestInitHostedErrors(t *testing.T) {
	e := newTestEnv(t, nil, nil, nil)
	ctx := context.Background()

	// 目录存在但不是托管正本（镜像）→ repo_exists，目录不动
	id := e.project(t, "")
	dir := e.svc.dir(id)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "refs", "heads"), 0o700))
	_, err := e.svc.InitHosted(ctx, id, "main", true, testIdentity)
	assert.True(t, errors.Is(err, ErrRepoExists), "err = %v", err)
	assert.False(t, exists(filepath.Join(dir, hostedMarker)))
	assert.True(t, errors.Is(e.svc.SetHostedHead(ctx, id, "main"), ErrRepoExists))

	// 不合法的基线分支
	id2 := e.project(t, "")
	_, err = e.svc.InitHosted(ctx, id2, "-x", true, testIdentity)
	assert.Equal(t, "invalid_request", errCode(err))
	assert.False(t, exists(e.svc.dir(id2)))

	// git 中途失败（空身份让 commit-tree 拒绝）→ 返回错误，不留目录也不留临时目录
	_, err = e.svc.InitHosted(ctx, id2, "main", true, Identity{})
	require.Error(t, err)
	assert.False(t, exists(e.svc.dir(id2)))
	tmps, _ := filepath.Glob(filepath.Join(e.svc.cfg.Dir, id2+".git.tmp-*"))
	assert.Empty(t, tmps)

	// 托管仓库不存在
	assert.Equal(t, "repo_not_ready", errCode(e.svc.SetHostedHead(ctx, id2, "main")))
	assert.Equal(t, "repo_not_ready", errCode(e.svc.RecordHosted(ctx, id2, "main")))
}

func TestInitHostedGitMissing(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.Git = "/nonexistent/git" }, nil, nil)
	id := e.project(t, "")
	_, err := e.svc.InitHosted(context.Background(), id, "main", true, testIdentity)
	assert.Equal(t, "repo_unavailable", errCode(err))
	assert.Equal(t, "repo_unavailable", errCode(e.svc.SetHostedHead(context.Background(), id, "main")))
}

func TestSetHostedHeadAndRecord(t *testing.T) {
	e := newTestEnv(t, nil, nil, nil)
	ctx := context.Background()
	id := e.project(t, "")
	_, err := e.svc.InitHosted(ctx, id, "main", true, testIdentity)
	require.NoError(t, err)
	dir := e.svc.dir(id)

	require.NoError(t, e.svc.SetHostedHead(ctx, id, "dev"))
	assert.Equal(t, "refs/heads/dev", gitCmd(t, dir, "symbolic-ref", "HEAD"))
	assert.Equal(t, "invalid_request", errCode(e.svc.SetHostedHead(ctx, id, "a..b")))

	require.NoError(t, e.svc.RecordHosted(ctx, id, "dev"))
	row := e.row(t, id)
	require.NotNil(t, row)
	assert.Equal(t, string(KindHosted), row.Kind)
	assert.Equal(t, "dev", deref(row.DefaultBranch))
	assert.Positive(t, row.SizeBytes)
	assert.Nil(t, row.RemoteUrl)
	assert.Nil(t, row.SyncedAt)

	// 再记一次（改回 main）覆盖同一行
	require.NoError(t, e.svc.RecordHosted(ctx, id, "main"))
	assert.Equal(t, "main", deref(e.row(t, id).DefaultBranch))
}

// hostedProject 建一个托管项目（hosted=1、baseBranch=base），不建仓库目录。
func (e *testEnv) hostedProject(t *testing.T, base string) string {
	t.Helper()
	id := ids.New("prj")
	now := time.Now().UnixMilli()
	if err := e.st.Q.CreateProject(context.Background(), db.CreateProjectParams{
		ID: id, Name: strings.ToLower(id), BaseBranch: &base, Hosted: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestHostedStatusSelfHeal(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.PublicURL = "https://oryn.test/" }, nil, nil)
	ctx := context.Background()
	id := e.hostedProject(t, "main")

	// 目录还不在 → absent
	st := e.status(t, id)
	assert.Equal(t, KindHosted, st.Kind)
	assert.Equal(t, StateAbsent, st.State)
	assert.Equal(t, "https://oryn.test/git/"+id+".git", deref(st.CloneURL))

	// InitHosted 之后不调 RecordHosted（模拟崩在两步之间）→ Status 补上行
	_, err := e.svc.InitHosted(ctx, id, "main", true, testIdentity)
	require.NoError(t, err)
	assert.Nil(t, e.row(t, id))
	st = e.status(t, id)
	assert.Equal(t, StateReady, st.State)
	assert.False(t, st.Empty)
	assert.False(t, st.Syncing)
	assert.Nil(t, st.SyncedAt)
	assert.Nil(t, st.LastError)
	assert.Equal(t, "main", deref(st.BaseBranch))
	assert.Equal(t, "main", deref(st.DefaultBranch))
	assert.Positive(t, deref(st.SizeBytes))
	row := e.row(t, id)
	require.NotNil(t, row)
	assert.Equal(t, string(KindHosted), row.Kind)

	// 改了基线但 SetHostedHead / RecordHosted 没跑 → Status 补齐 HEAD 与行
	e.exec(t, `UPDATE projects SET base_branch = 'dev' WHERE id = ?`, id)
	st = e.status(t, id)
	assert.Equal(t, "dev", deref(st.BaseBranch))
	assert.Equal(t, "dev", deref(st.DefaultBranch))
	assert.Equal(t, "refs/heads/dev", gitCmd(t, e.svc.dir(id), "symbolic-ref", "HEAD"))
	assert.False(t, st.Empty, "main 上仍有初始提交")

	// 托管仓库可以读
	list, err := e.svc.Commits(ctx, id, "main", "", "", nil)
	require.NoError(t, err)
	require.Len(t, list.Commits, 1)
	assert.Equal(t, "Initial commit", list.Commits[0].Subject)
}

func TestHostedShortCircuits(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.MaxBytes = 1 }, nil, nil)
	ctx := context.Background()
	id := e.hostedProject(t, "main")
	_, err := e.svc.InitHosted(ctx, id, "main", false, testIdentity)
	require.NoError(t, err)
	require.NoError(t, e.svc.RecordHosted(ctx, id, "main"))
	dir := e.svc.dir(id)

	_, err = e.svc.Sync(ctx, id)
	assert.Equal(t, "repo_hosted", errCode(err))

	st := e.status(t, id)
	assert.Equal(t, KindHosted, st.Kind)
	assert.Equal(t, StateReady, st.State)
	assert.True(t, st.Empty)

	// 不排队、不拉取、不比较地址、不按大小删除
	e.svc.Kick(id)
	assert.False(t, e.svc.flying(id))
	e.svc.ProjectChanged(ctx, id)
	e.settle(t, id)
	assert.True(t, exists(filepath.Join(dir, hostedMarker)), "托管仓库不能被镜像逻辑删除")
	assert.NotNil(t, e.row(t, id))
	assert.Zero(t, e.infoRefs())
}

// 推送锁：占着锁时第二个推送在自己的 ctx 取消后立即返回；等满上限回 503 + Retry-After；info/refs 不等锁；
// 托管入口也会自愈缺失的 project_repos 行。
func TestServeHostedGitPushLock(t *testing.T) {
	e := newTestEnv(t, nil, nil, nil)
	ctx := context.Background()
	id := e.hostedProject(t, "main")
	_, err := e.svc.InitHosted(ctx, id, "main", true, testIdentity)
	require.NoError(t, err)

	push := func(ctx context.Context) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/r.git/git-receive-pack", strings.NewReader("0000"))
		r.Header.Set("Content-Type", "application/x-git-receive-pack-request")
		w := httptest.NewRecorder()
		e.svc.ServeHostedGit(w, r, id, string(EndpointReceivePack), egress.HostedPusher{})
		return w
	}
	release, err := e.svc.pushLocks.lock(ctx, id)
	require.NoError(t, err)

	cctx, cancel := context.WithCancel(ctx)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- push(cctx) }()
	select {
	case <-done:
		t.Fatal("推送锁被占着时不该返回")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case w := <-done:
		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	case <-time.After(2 * time.Second):
		t.Fatal("取消 ctx 后等锁的推送没有返回")
	}

	e.svc.pushLocks.wait = 50 * time.Millisecond
	w := push(ctx)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "5", w.Header().Get("Retry-After"))
	assert.Contains(t, w.Body.String(), "仓库正忙")

	// 推送的发现请求不拿锁
	r := httptest.NewRequest(http.MethodGet, "/r.git/info/refs?service=git-receive-pack", nil)
	w = httptest.NewRecorder()
	e.svc.ServeHostedGit(w, r, id, string(EndpointInfoRefs), egress.HostedPusher{})
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.NotNil(t, e.row(t, id), "入口补写了 project_repos 行")

	release()
	w = push(ctx)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// 入口的前置检查：非托管 / 不存在 → 404，归档项目的推送（含发现请求）→ 403，克隆照常。
func TestServeHostedGitChecks(t *testing.T) {
	e := newTestEnv(t, nil, nil, nil)
	ctx := context.Background()
	id := e.hostedProject(t, "main")
	_, err := e.svc.InitHosted(ctx, id, "main", true, testIdentity)
	require.NoError(t, err)
	plain := e.project(t, "")

	get := func(pid, service string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/r.git/info/refs?service="+service, nil)
		w := httptest.NewRecorder()
		e.svc.ServeHostedGit(w, r, pid, string(EndpointInfoRefs), egress.HostedPusher{})
		return w
	}
	assert.Equal(t, http.StatusNotFound, get(plain, "git-upload-pack").Code)
	assert.Equal(t, http.StatusNotFound, get("prj_01JCZ00000000000000000NONE", "git-upload-pack").Code)

	e.exec(t, "UPDATE projects SET archived_at = 1 WHERE id = ?", id)
	w := get(id, "git-receive-pack")
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "项目已归档，仓库只读")
	assert.Equal(t, http.StatusOK, get(id, "git-upload-pack").Code)
}
