package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// fakeAPI 记录收到的请求，按 "方法 路径" 分派到 routes。
type fakeAPI struct {
	t      *testing.T
	mu     sync.Mutex
	reqs   []recordedReq
	routes map[string]func(w http.ResponseWriter, body []byte)
	srv    *httptest.Server
}

type recordedReq struct {
	Method, Path, RawQuery string
	Body                   []byte
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{t: t, routes: map[string]func(http.ResponseWriter, []byte){}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		assert.Equal(t, "Bearer odt_test", r.Header.Get("Authorization"))
		f.mu.Lock()
		f.reqs = append(f.reqs, recordedReq{r.Method, r.URL.Path, r.URL.RawQuery, b})
		h := f.routes[r.Method+" "+r.URL.Path]
		f.mu.Unlock()
		if h == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"not_found","message":"no route"}`))
			return
		}
		h(w, b)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) client() *apiClient {
	return &apiClient{base: f.srv.URL, token: "odt_test", http: f.srv.Client()}
}

func (f *fakeAPI) json(route string, status int, v any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[route] = func(w http.ResponseWriter, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
}

func (f *fakeAPI) requests() []recordedReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedReq(nil), f.reqs...)
}

func sampleThread(id string) apigen.Thread {
	return apigen.Thread{ID: id, Title: "t", State: apigen.ThreadStateIdle, Labels: []string{}, TitleSource: "auto"}
}

func TestThreadNewRequestBody(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST /api/v1/threads", 201, apigen.ThreadEnvelope{Thread: sampleThread("T-1")})
	f.json("GET /api/v1/projects", 200, apigen.ProjectList{Projects: []apigen.Project{{ID: "prj_A", Name: "demo"}}})
	ctx := context.Background()

	th, err := threadNew(ctx, f.client(), "hi", threadNewOptions{orb: "orb-x", title: "T", labels: []string{"a", "b"}, project: "demo"})
	require.NoError(t, err)
	assert.Equal(t, "T-1", th.ID)
	reqs := f.requests()
	require.Len(t, reqs, 2)
	assert.JSONEq(t, `{"prompt":"hi","title":"T","labels":["a","b"],"projectId":"prj_A","executor":{"kind":"orb","orbId":"orb-x"}}`, string(reqs[1].Body))

	_, err = threadNew(ctx, f.client(), "hi", threadNewOptions{provider: "local", orbType: "small"})
	require.NoError(t, err)
	reqs = f.requests()
	assert.JSONEq(t, `{"prompt":"hi","executor":{"kind":"new_orb","provider":"local","type":"small"}}`, string(reqs[len(reqs)-1].Body))

	_, err = threadNew(ctx, f.client(), "hi", threadNewOptions{orb: "orb-x", provider: "local"})
	assert.ErrorContains(t, err, "不能同时使用")
	_, err = threadNew(ctx, f.client(), "hi", threadNewOptions{project: "demo", noProject: true})
	assert.Error(t, err)
	assert.Len(t, f.requests(), 3, "参数冲突时不发请求")
}

func TestThreadListQuery(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /api/v1/threads", 200, apigen.ThreadList{Threads: []apigen.Thread{sampleThread("T-1")}})
	f.json("GET /api/v1/projects", 200, apigen.ProjectList{Projects: []apigen.Project{{ID: "prj_A", Name: "demo"}}})
	ctx := context.Background()

	list, err := threadList(ctx, f.client(), threadListOptions{})
	require.NoError(t, err)
	assert.Len(t, list, 1)
	_, err = threadList(ctx, f.client(), threadListOptions{archived: true, state: "idle", project: "demo", query: "fix bug"})
	require.NoError(t, err)
	_, err = threadList(ctx, f.client(), threadListOptions{project: "none"})
	require.NoError(t, err)
	_, err = threadList(ctx, f.client(), threadListOptions{state: "bogus"})
	assert.ErrorContains(t, err, "未知状态")

	var queries []string
	for _, r := range f.requests() {
		if r.Path == "/api/v1/threads" {
			queries = append(queries, r.RawQuery)
		}
	}
	assert.Equal(t, []string{"", "archived=true&project=prj_A&q=fix+bug&state=idle", "project=none"}, queries)
}

func TestThreadArchive(t *testing.T) {
	f := newFakeAPI(t)
	for _, id := range []string{"T-1", "T-2"} {
		f.json("PATCH /api/v1/threads/"+id, 200, sampleThread(id))
	}
	var errb bytes.Buffer
	out, err := threadArchive(context.Background(), f.client(), []string{"T-1", "T-2"}, false, &errb)
	require.NoError(t, err)
	assert.Len(t, out, 2)
	assert.Equal(t, "已归档 T-1\n已归档 T-2\n", errb.String())
	errb.Reset()
	_, err = threadArchive(context.Background(), f.client(), []string{"T-1", "T-404"}, true, &errb)
	assert.ErrorContains(t, err, "T-404")
	assert.Equal(t, "已取消归档 T-1\n", errb.String())

	reqs := f.requests()
	require.Len(t, reqs, 4)
	assert.Equal(t, http.MethodPatch, reqs[0].Method)
	assert.JSONEq(t, `{"archived":true}`, string(reqs[0].Body))
	assert.JSONEq(t, `{"archived":false}`, string(reqs[2].Body))
}

func TestProviderRm(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /api/v1/providers", 200, apigen.ProviderList{Providers: []apigen.Provider{{ID: "prov_1", Name: "local"}, {ID: "prov_2", Name: "tmp"}}})
	f.mu.Lock()
	f.routes["DELETE /api/v1/providers/prov_2"] = func(w http.ResponseWriter, _ []byte) { w.WriteHeader(http.StatusNoContent) }
	f.mu.Unlock()
	var errb bytes.Buffer
	require.NoError(t, providerRm(context.Background(), f.client(), "tmp", &errb))
	assert.Equal(t, "已删除 tmp\n", errb.String())
	require.NoError(t, providerRm(context.Background(), f.client(), "prov_2", &errb), "也接受 id")
	assert.ErrorContains(t, providerRm(context.Background(), f.client(), "nope", &errb), "不存在")
	var deletes int
	for _, r := range f.requests() {
		if r.Method == http.MethodDelete {
			deletes++
			assert.Equal(t, "/api/v1/providers/prov_2", r.Path)
		}
	}
	assert.Equal(t, 2, deletes)
}

func TestNormalizeRepoURL(t *testing.T) {
	want := "github.com/Lynricsy/Oryn"
	for _, s := range []string{
		"https://github.com/Lynricsy/Oryn.git", "git@github.com:Lynricsy/Oryn.git", "ssh://git@GitHub.com/Lynricsy/Oryn",
		"https://user:tok@github.com/Lynricsy/Oryn/", "http://127.0.0.1:7811/git/github.com/Lynricsy/Oryn.git",
	} {
		got := normalizeRepoURL(s)
		if strings.HasPrefix(s, "http://127.0.0.1") {
			assert.NotEqual(t, want, got, s)
			continue
		}
		assert.Equal(t, want, got, s)
	}
}
