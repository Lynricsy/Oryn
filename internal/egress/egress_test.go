package egress

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeR struct {
	pol     Policy
	secrets map[string][2]string // id → 值, 允许主机（逗号分隔）
	git     map[string]string
}

func (f fakeR) Policy() Policy { return f.pol }
func (f fakeR) SecretByID(id string) (string, []string, bool) {
	v, ok := f.secrets[id]
	if !ok {
		return "", nil, false
	}
	var hosts []string
	if v[1] != "" {
		hosts = strings.Split(v[1], ",")
	}
	return v[0], hosts, true
}
func (f fakeR) GitToken(h string) (string, bool) { v, ok := f.git[h]; return v, ok }

func do(t *testing.T, method, url string, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func TestProxyInjectsSecretAndStreams(t *testing.T) {
	release := make(chan struct{})
	var gotAuth, gotPath, gotQuery string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotQuery = r.Header.Get("Authorization"), r.URL.Path, r.URL.Query().Get("key")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Echo", r.Header.Get("Authorization"))
		fmt.Fprint(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		<-release
		fmt.Fprint(w, "data: echo REAL-KEY-123 end\n\n")
	}))
	defer up.Close()
	upHost := strings.Split(strings.TrimPrefix(up.URL, "http://"), ":")[0]

	h := NewHandler("orb-x", fakeR{
		pol: Policy{Routes: map[string]Route{
			"custom": {Origin: up.URL, SecretIDs: []string{"s1"}},
			"other":  {Origin: up.URL, SecretIDs: []string{"s3"}},
		}},
		secrets: map[string][2]string{
			"s1": {"REAL-KEY-123", upHost}, "s2": {"OTHER", "nope.example.com"}, "s3": {"THIRD", ""},
		},
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp := do(t, "POST", srv.URL+"/llm/custom/v1/messages?key=oryn-ph-s1", map[string]string{"Authorization": "Bearer oryn-ph-s1"})
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	line, err := br.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "data: one\n", line, "首块在上游结束前即到达 = 未被缓冲")
	close(release)
	rest, _ := io.ReadAll(br)
	assert.Equal(t, "Bearer REAL-KEY-123", gotAuth)
	assert.Equal(t, "/v1/messages", gotPath)
	assert.Equal(t, "REAL-KEY-123", gotQuery)
	assert.NotContains(t, string(rest), "REAL-KEY-123", "响应体回显被脱敏")
	assert.Contains(t, string(rest), "oryn-ph-s1")
	assert.Equal(t, "Bearer oryn-ph-s1", resp.Header.Get("X-Echo"), "响应头回显被脱敏")

	cases := []struct {
		name string
		url  string
		hdr  map[string]string
		code int
	}{
		{"绑定到别的路由的密钥", "/llm/custom/v1", map[string]string{"x-api-key": "oryn-ph-s3"}, 403},
		{"主机不匹配", "/llm/other/v1", map[string]string{"x-api-key": "oryn-ph-s2"}, 403},
		{"非凭据请求头", "/llm/custom/v1", map[string]string{"Origin": "oryn-ph-s1"}, 403},
		{"非凭据 query", "/llm/custom/v1?redirect=oryn-ph-s1", nil, 403},
		{"路径中的占位符", "/llm/custom/oryn-ph-s1", nil, 403},
		{"未知路由", "/llm/missing/x", nil, 404},
	}
	for _, c := range cases {
		r := do(t, "POST", srv.URL+c.url, c.hdr)
		r.Body.Close()
		assert.Equal(t, c.code, r.StatusCode, c.name)
	}
}

func TestGitRoute(t *testing.T) {
	h := NewHandler("orb-x", fakeR{
		git: map[string]string{"github.com": "tok", "gitlab.com": "t2"},
		pol: Policy{GitRepos: map[string][]string{"github.com": {"Owner/Repo.git"}}},
	})
	req := func(path string) *http.Request { return httptest.NewRequest("GET", path, nil) }
	tg, ok := h.resolve(req("/git/github.com/owner/repo.git/info/refs?service=git-upload-pack"))
	require.True(t, ok)
	assert.Equal(t, "https://github.com/owner/repo.git/info/refs", tg.url.String())
	assert.Equal(t, "Basic eC1hY2Nlc3MtdG9rZW46dG9r", tg.auth)
	_, ok = h.resolve(req("/git/github.com/owner/repo/git-receive-pack"))
	assert.True(t, ok)
	_, ok = h.resolve(req("/git/github.com/owner/other.git/info/refs?service=git-upload-pack"))
	assert.False(t, ok, "限定仓库外的仓库不放行")
	_, ok = h.resolve(req("/git/github.com/owner/repo/settings"))
	assert.False(t, ok, "非 smart-HTTP 端点不放行")
	_, ok = h.resolve(req("/git/gitlab.com/any/thing.git/git-upload-pack"))
	assert.True(t, ok, "未限定的主机放行全部仓库")
	_, ok = h.resolve(req("/git/bitbucket.org/x/y/git-upload-pack"))
	assert.False(t, ok, "无令牌的 git 主机不放行")

	srv := httptest.NewServer(h)
	defer srv.Close()
	r := do(t, "POST", srv.URL+"/git/gitlab.com/a/b/git-upload-pack", map[string]string{"Authorization": "Bearer oryn-ph-s1"})
	r.Body.Close()
	assert.Equal(t, 403, r.StatusCode, "git 路由不接受占位符")
}

func TestRedactReaderSplitAcrossChunks(t *testing.T) {
	pr, pw := io.Pipe()
	rr := newRedactReader(pr, [][2]string{{"SECRETVALUE", "oryn-ph-x"}})
	go func() {
		_, _ = pw.Write([]byte("abc SECR"))
		_, _ = pw.Write([]byte("ETVALUE def\n\n"))
		_ = pw.Close()
	}()
	b, err := io.ReadAll(rr)
	require.NoError(t, err)
	assert.Equal(t, "abc oryn-ph-x def\n\n", string(b))
}
