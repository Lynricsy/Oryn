package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/github"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/threads/threadstest"
)

// PUT / DELETE threads/{id}/github/pulls/{number}：暂停回流与取消关联，返回最新的线程。
func TestThreadPullAPI(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "data"))
	require.NoError(t, err)
	orbs, err := threadstest.NewOrbs(st, fakeomp(t), filepath.Join(dir, "orbs"))
	require.NoError(t, err)
	svc := threads.New(threads.Config{DataDir: filepath.Join(dir, "data")}, st, orbs)
	gh := github.New(github.Config{PublicURL: h2PublicURL, APIURL: "http://127.0.0.1:1", WebURL: "http://127.0.0.1:1"}, st, svc, nil, nil)
	cfg := config.DefaultOrynd()
	cfg.DataDir = filepath.Join(dir, "data")
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st}}
	s.Threads = &api.ThreadsDeps{Service: svc}
	s.GitHub = &api.GitHubDeps{Service: gh}
	srv := httptest.NewServer(api.NewRouter(s))
	t.Cleanup(func() {
		srv.Close()
		orbs.Close()
		svc.Close()
		_ = st.Close()
	})
	e := &threadsEnv{t: t, st: st, svc: svc, srv: srv, dir: dir}

	prj := ids.New("prj")
	repoURL := "https://github.com/acme/widgets"
	require.NoError(t, st.Q.CreateProject(ctx, db.CreateProjectParams{ID: prj, Name: "w", RepoUrl: &repoURL, CreatedAt: 1, UpdatedAt: 1}))
	repoID, ownerID, instID := int64(42), int64(900), int64(10)
	require.NoError(t, st.Q.SetProjectGithub(ctx, db.SetProjectGithubParams{GithubRepoID: &repoID, GithubOwnerID: &ownerID, GithubInstallationID: &instID, ID: prj}))
	th := ids.Thread()
	require.NoError(t, st.Q.CreateThread(ctx, db.CreateThreadParams{
		ID: th, ProjectID: &prj, Title: "t", TitleSource: "auto", State: "idle", StateChangedAt: 1, SessionDir: "d", CreatedAt: 1, UpdatedAt: 1, Kind: "work",
	}))
	require.NoError(t, st.Q.UpsertGithubPull(ctx, db.UpsertGithubPullParams{
		RepoID: 42, Number: 12, RepoFullName: "acme/widgets", Title: "Fix login", State: "open", SameRepo: true,
		HeadRef: "fix", HeadSha: "abc", BaseRef: "main", AuthorLogin: "alice", HtmlUrl: "https://github.com/acme/widgets/pull/12", UpdatedAt: 1,
	}))
	_, err = st.Q.LinkThreadPull(ctx, db.LinkThreadPullParams{ThreadID: th, RepoID: 42, Number: 12, LinkedAt: 1})
	require.NoError(t, err)

	code, body := e.do("PUT", "/api/v1/threads/"+th+"/github/pulls/12", map[string]any{"listening": false})
	require.Equal(t, http.StatusOK, code, string(body))
	var got apigen.Thread
	require.NoError(t, json.Unmarshal(body, &got))
	require.Len(t, got.Pulls, 1)
	assert.False(t, got.Pulls[0].Listening)
	assert.EqualValues(t, 12, got.Pulls[0].Number)

	code, body = e.do("PUT", "/api/v1/threads/"+th+"/github/pulls/99", map[string]any{"listening": true})
	assert.Equal(t, http.StatusNotFound, code, string(body))

	code, body = e.do("DELETE", "/api/v1/threads/"+th+"/github/pulls/12", nil)
	require.Equal(t, http.StatusOK, code, string(body))
	got = apigen.Thread{}
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Empty(t, got.Pulls)
	assert.NotContains(t, string(body), `"pulls"`)

	code, _ = e.do("DELETE", "/api/v1/threads/"+th+"/github/pulls/12", nil)
	assert.Equal(t, http.StatusNotFound, code)
}
