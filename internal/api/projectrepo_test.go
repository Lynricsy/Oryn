package api_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/github"
	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/repos"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
)

// requireGitAPI 在找不到 git 时跳过；ORYN_REQUIRE_GIT=1（CI）时改为失败。
func requireGitAPI(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		if os.Getenv("ORYN_REQUIRE_GIT") == "1" {
			t.Fatal("ORYN_REQUIRE_GIT=1，但找不到 git")
		}
		t.Skip("没有 git")
	}
}

// newRepoEnv 同 newGitHubEnvWith，另给假 GitHub 建 acme/widgets 裸仓库并挂项目仓库镜像服务（M24）。
func newRepoEnv(t *testing.T) *h2Env {
	t.Helper()
	requireGitAPI(t)
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.New(bytes.Repeat([]byte{5}, 32))
	require.NoError(t, err)
	home, err := agentcfg.NewHome(dir)
	require.NoError(t, err)
	projectHomes, err := agentcfg.NewProjectHomes(dir)
	require.NoError(t, err)
	svc, err := auth.NewService(st, dir, h2PublicURL)
	require.NoError(t, err)
	fake := githubtest.New()
	t.Cleanup(fake.Close)
	fake.GitRoot = t.TempDir()
	require.NoError(t, fake.InitRepo("acme/widgets"))
	cfg := config.DefaultOrynd()
	cfg.PublicURL, cfg.DataDir = h2PublicURL, dir
	cfg.GitHub = config.GitHub{APIURL: fake.URL(), WebURL: fake.URL()}
	gh := github.New(github.Config{PublicURL: h2PublicURL, APIURL: fake.URL(), WebURL: fake.URL(), Box: box}, st, nil, nil, nil)
	vault := secrets.NewVault(st, box)
	rs, err := repos.New(repos.Config{Dir: cfg.ReposDir(), GitHubWebURL: fake.URL()}, st, vault, gh)
	require.NoError(t, err)
	t.Cleanup(rs.Close)
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st, AuthMiddleware: auth.Middleware(st)}}
	e := &h2Env{t: t, st: st, dataDir: dir, vault: vault, tickets: auth.NewTickets(), svc: svc}
	s.Auth = &api.AuthDeps{Service: svc, Tickets: e.tickets, Vault: e.vault, Home: home, ProjectHomes: projectHomes}
	s.GitHub = &api.GitHubDeps{Service: gh}
	s.Repos = &api.ReposDeps{Service: rs}
	e.srv = httptest.NewServer(api.NewRouter(s))
	t.Cleanup(e.srv.Close)
	return e
}

func createRepoProject(t *testing.T, e *h2Env, cred h2Cred, name, repoURL string) string {
	t.Helper()
	body := map[string]any{"name": name, "shallowClone": false}
	if repoURL != "" {
		body["repoUrl"] = repoURL
	}
	r := e.do("POST", "/api/v1/projects", cred, body)
	require.Equal(t, 201, r.status, string(r.body))
	var p apigen.Project
	r.json(t, &p)
	return p.ID
}

func TestProjectRepoAPI(t *testing.T) {
	e := newRepoEnv(t)
	dev := h2DeviceToken(t, e)

	// 项目不存在 → 404
	r := e.do("GET", "/api/v1/projects/prj_01JCZ00000000000000000NONE/repo", dev, nil)
	assert.Equal(t, 404, r.status, string(r.body))
	assert.Equal(t, "not_found", r.code(t))

	// 没有仓库地址：kind=none、state=absent；同步 → 409 no_repo
	bare := createRepoProject(t, e, dev, "bare", "")
	r = e.do("GET", "/api/v1/projects/"+bare+"/repo", dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var st apigen.ProjectRepoStatus
	r.json(t, &st)
	assert.Equal(t, apigen.ProjectRepoStatusKindNone, st.Kind)
	assert.Equal(t, apigen.ProjectRepoStatusStateAbsent, st.State)
	assert.True(t, st.Available)
	r = e.do("POST", "/api/v1/projects/"+bare+"/repo/sync", dev, nil)
	assert.Equal(t, 409, r.status, string(r.body))
	assert.Equal(t, "no_repo", r.code(t))

	// 有仓库地址：首次读取开始克隆，随后 ready
	id := createRepoProject(t, e, dev, "widgets", "https://github.com/acme/widgets")
	r = e.do("GET", "/api/v1/projects/"+id+"/repo", dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &st)
	assert.Equal(t, apigen.ProjectRepoStatusKindMirror, st.Kind)
	assert.Contains(t, []apigen.ProjectRepoStatusState{apigen.ProjectRepoStatusStateCloning, apigen.ProjectRepoStatusStateReady}, st.State)
	require.Eventually(t, func() bool {
		r := e.do("GET", "/api/v1/projects/"+id+"/repo", dev, nil)
		var cur apigen.ProjectRepoStatus
		r.json(t, &cur)
		st = cur
		return r.status == 200 && cur.State == apigen.ProjectRepoStatusStateReady && !cur.Syncing
	}, 30*time.Second, 100*time.Millisecond)
	assert.Equal(t, "main", *st.DefaultBranch)
	assert.Equal(t, "main", *st.BaseBranch)
	assert.Equal(t, "https://github.com/acme/widgets", *st.CloneURL)
	assert.False(t, st.Empty)
	assert.Nil(t, st.LastError)
	mirror := filepath.Join(e.dataDir, "repos", id+".git")
	assert.DirExists(t, mirror)

	// 网页会话的同步要带 CSRF 头
	sess := e.webSession()
	r = e.do("POST", "/api/v1/projects/"+id+"/repo/sync", h2Cred{cookie: sess}, nil)
	assert.Equal(t, 403, r.status, string(r.body))
	r = e.do("POST", "/api/v1/projects/"+id+"/repo/sync", h2Cred{cookie: sess, csrf: true}, nil)
	require.Equal(t, 202, r.status, string(r.body))
	r.json(t, &st)
	assert.Equal(t, apigen.ProjectRepoStatusStateReady, st.State)

	// 删除项目连镜像一起删
	require.Equal(t, 204, e.do("DELETE", "/api/v1/projects/"+id, dev, nil).status)
	assert.NoDirExists(t, mirror)
}

// readyRepoProject 建 acme/widgets 项目并等镜像就绪。
func readyRepoProject(t *testing.T, e *h2Env, dev h2Cred) string {
	t.Helper()
	id := createRepoProject(t, e, dev, "widgets", "https://github.com/acme/widgets")
	require.Eventually(t, func() bool {
		var st apigen.ProjectRepoStatus
		r := e.do("GET", "/api/v1/projects/"+id+"/repo", dev, nil)
		r.json(t, &st)
		return st.State == apigen.ProjectRepoStatusStateReady && !st.Syncing
	}, 30*time.Second, 100*time.Millisecond)
	return id
}

// rawGet 发 GET 并返回整个响应（要看响应头）。
func rawGet(t *testing.T, e *h2Env, path string, dev h2Cred, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest("GET", e.srv.URL+path, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+dev.bearer)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, b
}

func TestProjectRepoReadAPI(t *testing.T) {
	e := newRepoEnv(t)
	dev := h2DeviceToken(t, e)
	id := readyRepoProject(t, e, dev)
	base := "/api/v1/projects/" + id + "/repo"

	r := e.do("GET", base+"/tree", dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var tree apigen.ProjectRepoTree
	r.json(t, &tree)
	assert.Equal(t, "main", tree.Ref)
	require.Len(t, tree.Entries, 1)
	assert.Equal(t, "README.md", tree.Entries[0].Name)
	assert.Equal(t, apigen.RepoEntryFile, tree.Entries[0].Type)
	require.NotNil(t, tree.Entries[0].LastCommit)
	assert.Equal(t, "initial commit", tree.Entries[0].LastCommit.Subject)

	resp, body := rawGet(t, e, base+"/blob?path=README.md", dev, nil)
	require.Equal(t, 200, resp.StatusCode, string(body))
	assert.Equal(t, "# acme/widgets\n", string(body))
	etag := resp.Header.Get("ETag")
	assert.Equal(t, `"`+tree.Entries[0].Oid+`"`, etag)
	assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	assert.Equal(t, "sandbox", resp.Header.Get("Content-Security-Policy"))
	assert.Contains(t, resp.Header.Get("Content-Disposition"), "attachment")
	assert.Equal(t, "private, no-cache", resp.Header.Get("Cache-Control"))
	resp, _ = rawGet(t, e, base+"/blob?path=README.md", dev, map[string]string{"If-None-Match": etag})
	assert.Equal(t, 304, resp.StatusCode)
	resp, body = rawGet(t, e, base+"/blob?path=README.md&ref="+tree.Commit, dev, nil)
	require.Equal(t, 200, resp.StatusCode, string(body))
	assert.Equal(t, "private, max-age=31536000, immutable", resp.Header.Get("Cache-Control"))

	r = e.do("GET", base+"/commits", dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var list apigen.ProjectRepoCommitList
	r.json(t, &list)
	require.Len(t, list.Commits, 1)
	assert.Equal(t, "initial commit", list.Commits[0].Subject)
	r = e.do("GET", base+"/commits/"+list.Commits[0].Sha, dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var detail apigen.ProjectRepoCommitDetail
	r.json(t, &detail)
	require.Len(t, detail.Files, 1)
	r = e.do("GET", base+"/commits/"+list.Commits[0].Sha+"/diff?path=README.md", dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var diff apigen.GitFileDiff
	r.json(t, &diff)
	assert.Contains(t, diff.Patch, "+# acme/widgets")

	r = e.do("GET", base+"/refs?kind=branch", dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var refs apigen.ProjectRepoRefList
	r.json(t, &refs)
	require.Len(t, refs.Refs, 1)
	assert.True(t, refs.Refs[0].Default)

	// 坏输入与找不到
	for path, want := range map[string]struct {
		status int
		code   string
	}{
		base + "/tree?ref=-x":                        {400, "invalid_request"},
		base + "/tree?path=../a":                     {400, "invalid_request"},
		base + "/commits/zz":                         {400, "invalid_request"},
		base + "/refs?kind=bogus":                    {400, "invalid_request"},
		base + "/tree?ref=nope":                      {404, "ref_not_found"},
		base + "/tree?path=nope":                     {404, "path_not_found"},
		base + "/blob?path=nope":                     {404, "path_not_found"},
		base + "/commits/" + strings.Repeat("e", 40): {404, "commit_not_found"},
	} {
		r = e.do("GET", path, dev, nil)
		assert.Equal(t, want.status, r.status, path+": "+string(r.body))
		assert.Equal(t, want.code, r.code(t), path)
	}
	bare := createRepoProject(t, e, dev, "bare", "")
	r = e.do("GET", "/api/v1/projects/"+bare+"/repo/tree", dev, nil)
	assert.Equal(t, 409, r.status, string(r.body))
	assert.Equal(t, "no_repo", r.code(t))
}

// 没有装配仓库服务时接口回 501。
func TestProjectRepoAPIUnconfigured(t *testing.T) {
	e := newH2Env(t)
	dev := h2DeviceToken(t, e)
	id := createRepoProject(t, e, dev, "p", "")
	r := e.do("GET", "/api/v1/projects/"+id+"/repo", dev, nil)
	assert.Equal(t, 501, r.status, string(r.body))
}
