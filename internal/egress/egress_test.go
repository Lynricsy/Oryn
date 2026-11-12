package egress

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	req := func(path string) *http.Request { return httptest.NewRequest("GET", path, nil) }
	toks := map[string]string{"github.com": "tok", "gitlab.com": "t2"}

	// 有项目仓库：只放行该仓库（读写），同主机其它仓库与其它主机一律拒绝。
	proj := NewHandler("orb-x", fakeR{git: toks, pol: Policy{GitRepos: map[string][]string{"github.com": {"Owner/Repo.git"}}}})
	tg, st := proj.resolve(req("/git/github.com/owner/repo.git/info/refs?service=git-upload-pack"))
	require.Equal(t, http.StatusOK, st)
	assert.Equal(t, "https://github.com/owner/repo.git/info/refs", tg.url.String())
	assert.Equal(t, "Basic eC1hY2Nlc3MtdG9rZW46dG9r", tg.auth)
	for path, want := range map[string]int{
		"/git/github.com/Owner/Repo/git-receive-pack":                       200,
		"/git/github.com/owner/repo/info/refs?service=git-receive-pack":     200,
		"/git/github.com/owner/other.git/info/refs?service=git-upload-pack": 403,
		"/git/github.com/owner/repo-other/git-upload-pack":                  403,
		"/git/github.com/owner/repo/sub/git-upload-pack":                    403,
		"/git/gitlab.com/any/thing.git/git-upload-pack":                     403,
		"/git/github.com/owner/repo/settings":                               404,
		"/git/github.com/owner/repo/info/refs":                              404,
	} {
		_, st := proj.resolve(req(path))
		assert.Equal(t, want, st, path)
	}

	// 无项目：任意有令牌主机上的任意仓库只读，拒绝推送。
	ro := NewHandler("orb-x", fakeR{git: toks})
	for path, want := range map[string]int{
		"/git/gitlab.com/any/thing.git/git-upload-pack":          200,
		"/git/github.com/a/b/info/refs?service=git-upload-pack":  200,
		"/git/github.com/a/b/git-receive-pack":                   403,
		"/git/github.com/a/b/info/refs?service=git-receive-pack": 403,
		"/git/bitbucket.org/x/y/git-upload-pack":                 404,
	} {
		_, st := ro.resolve(req(path))
		assert.Equal(t, want, st, path)
	}

	srv := httptest.NewServer(proj)
	defer srv.Close()
	r := do(t, "POST", srv.URL+"/git/github.com/owner/repo/git-upload-pack", map[string]string{"Authorization": "Bearer oryn-ph-s1"})
	r.Body.Close()
	assert.Equal(t, 403, r.StatusCode, "git 路由不接受占位符")
}

// M3：点段、空段与编码形式的点段都不能绕过仓库限定。
func TestGitRouteRejectsNonCanonicalPaths(t *testing.T) {
	h := NewHandler("orb-x", fakeR{git: map[string]string{"github.com": "tok"},
		pol: Policy{GitRepos: map[string][]string{"github.com": {"owner/repo"}}}})
	srv := httptest.NewServer(h)
	defer srv.Close()
	for _, p := range []string{
		"/git/github.com/owner/repo/../../victim/private/info/refs?service=git-upload-pack",
		"/git/github.com/owner/repo/%2e%2e/%2e%2e/victim/private/git-upload-pack",
		"/git/github.com/owner/repo/./git-upload-pack",
		"/git/github.com/owner//repo/git-upload-pack",
		"/git/github.com/owner/repo%2F..%2F..%2Fvictim%2Fx/git-upload-pack",
	} {
		// 直接构造原始请求行，避免客户端预先规范化路径
		conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
		require.NoError(t, err)
		fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n", p)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err, p)
		resp.Body.Close()
		conn.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, p)
	}
}

// M1（egress 侧）：hosts 为空的密钥只能作为它自己路由的 apiKey 注入；其它引用须 hosts 非空且匹配。
func TestSecretHostBinding(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	upHost := strings.Split(strings.TrimPrefix(up.URL, "http://"), ":")[0]
	h := NewHandler("orb-x", fakeR{
		pol: Policy{Routes: map[string]Route{
			"p": {Origin: up.URL, SecretIDs: []string{"own"}, HostBoundIDs: []string{"free", "bound", "elsewhere"}},
		}},
		secrets: map[string][2]string{
			"own": {"OWN", ""}, "free": {"FREE", ""}, "bound": {"BOUND", upHost}, "elsewhere": {"X", "other.example.com"},
		},
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	for id, want := range map[string]int{"own": 200, "bound": 200, "free": 403, "elsewhere": 403} {
		r := do(t, "POST", srv.URL+"/llm/p/v1", map[string]string{"X-Api-Key": "oryn-ph-" + id})
		r.Body.Close()
		assert.Equal(t, want, r.StatusCode, id)
	}
}

// OD4：鉴权过的 Upgrade 请求在上游返回 101 后必须保持双向可写（不被脱敏读取器包住）。
func TestUpgradeThroughProxy(t *testing.T) {
	var gotKey string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprint(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: echo\r\nConnection: Upgrade\r\nX-Echo: REAL\r\n\r\n")
		_ = brw.Flush()
		line, err := brw.ReadString('\n')
		if err != nil {
			return
		}
		fmt.Fprint(brw, "echo:"+line)
		_ = brw.Flush()
	}))
	defer up.Close()
	h := NewHandler("orb-x", fakeR{
		pol:     Policy{Routes: map[string]Route{"ws": {Origin: up.URL, SecretIDs: []string{"s1"}}}},
		secrets: map[string][2]string{"s1": {"REAL", ""}},
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	defer conn.Close()
	fmt.Fprint(conn, "GET /llm/ws/stream HTTP/1.1\r\nHost: x\r\nUpgrade: echo\r\nConnection: Upgrade\r\nX-Api-Key: oryn-ph-s1\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	assert.Equal(t, "oryn-ph-s1", resp.Header.Get("X-Echo"), "响应头照样脱敏")
	assert.Equal(t, "REAL", gotKey)
	fmt.Fprint(conn, "hello\n")
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := br.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "echo:hello\n", line, "升级后的连接双向可用")
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
