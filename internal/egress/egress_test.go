package egress

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeR struct {
	pol     Policy
	secrets map[string][2]string // id → 值, 允许主机（逗号分隔）
	git     map[string]string
	gh      string      // 非空时 GitHubToken 返回它
	rec     *writeRec   // 非 nil 时记录 GitHub 写操作回调
	gl      *gateFake   // 门禁现查的状态（gate_test.go）；nil 时一律不放行
	mem     *memFake    // 记忆通道（memory_test.go）；nil 时没有记忆通道
	hosted  *hostedFake // 托管仓库通道（hosted_test.go）；nil 时没有通道
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
func (f fakeR) GitHubToken() (string, bool)      { return f.gh, f.gh != "" }

func (f fakeR) GitHubTokenRejected(tok string) {
	if f.rec == nil {
		return
	}
	f.rec.mu.Lock()
	defer f.rec.mu.Unlock()
	f.rec.rejected = append(f.rec.rejected, tok)
}

func (f fakeR) GitHubWriteBegin(w GitHubWrite) string {
	if f.rec == nil {
		return ""
	}
	f.rec.mu.Lock()
	defer f.rec.mu.Unlock()
	f.rec.begins = append(f.rec.begins, w)
	return f.rec.gate
}

func (f fakeR) GitHubWriteEnd(w GitHubWrite, status int, keys []string) {
	if f.rec == nil {
		return
	}
	f.rec.mu.Lock()
	defer f.rec.mu.Unlock()
	f.rec.ends = append(f.rec.ends, writeEnd{w: w, status: status, keys: keys})
}

// writeRec 记录 GitHubWriteBegin / GitHubWriteEnd / GitHubTokenRejected；gate 非空时 Begin 一律拒绝。
type writeRec struct {
	mu       sync.Mutex
	gate     string
	begins   []GitHubWrite
	ends     []writeEnd
	rejected []string
}

type writeEnd struct {
	w      GitHubWrite
	status int
	keys   []string
}

func (r *writeRec) take() ([]GitHubWrite, []writeEnd) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, e := r.begins, r.ends
	r.begins, r.ends = nil, nil
	return b, e
}

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

// R5：代理形式的请求（orb 内 git 经 `http.http://<host>/.proxy` 发来，请求行是 `http://<host>/<owner>/<repo>/…`）
// 与 /git/<host>/… 走同一条 git 路由：同样的仓库限定、只读规则、规范路径检查与 NoGit。
func TestGitRouteProxyForm(t *testing.T) {
	req := func(target string) *http.Request { return httptest.NewRequest("GET", target, nil) }
	toks := map[string]string{"github.com": "tok", "gitlab.com": "t2"}

	proj := NewHandler("orb-x", fakeR{git: toks, pol: Policy{GitRepos: map[string][]string{"github.com": {"Owner/Repo.git"}}}})
	tg, st := proj.resolve(req("http://GitHub.com/owner/repo.git/info/refs?service=git-upload-pack"))
	require.Equal(t, http.StatusOK, st)
	assert.Equal(t, "https://github.com/owner/repo.git/info/refs", tg.url.String())
	assert.Equal(t, "Basic eC1hY2Nlc3MtdG9rZW46dG9r", tg.auth)
	for target, want := range map[string]int{
		"http://github.com/Owner/Repo/git-receive-pack":                       200,
		"http://github.com/owner/repo/info/refs?service=git-receive-pack":     200,
		"http://github.com/owner/other.git/info/refs?service=git-upload-pack": 403,
		"http://github.com/owner/repo-other/git-upload-pack":                  403,
		"http://gitlab.com/any/thing.git/git-upload-pack":                     403,
		"http://github.com/owner/repo/settings":                               403,
		"http://github.com/":                                                  400,
		// 只认 http 绝对地址，其它 absolute-form 一律 400，不落到 origin-form 的路由；端口是主机键的一部分（同 /git/<host:port>/，
		// 这里不是项目仓库所在主机，403）。
		"http://github.com:8080/owner/repo/git-upload-pack": 403,
		"https://github.com/owner/repo/git-upload-pack":     400,
		// 代理形式只走 git 路由：路径像 /git/、/llm/、/gh/ 也按 owner/repo 解析，不是 smart-HTTP 端点就 403。
		"http://github.com/git/github.com/owner/repo/git-upload-pack": 403,
		"http://x/llm/custom/v1/messages":                             403,
		"http://github.com/gh/api.github.com/user":                    403,
	} {
		_, st := proj.resolve(req(target))
		assert.Equal(t, want, st, target)
	}

	ro := NewHandler("orb-x", fakeR{git: toks})
	for target, want := range map[string]int{
		"http://gitlab.com/any/thing.git/git-upload-pack":          200,
		"http://github.com/a/b/git-receive-pack":                   403,
		"http://github.com/a/b/info/refs?service=git-receive-pack": 403,
		"http://bitbucket.org/x/y/git-upload-pack":                 404,
	} {
		_, st := ro.resolve(req(target))
		assert.Equal(t, want, st, target)
	}
	// 带端口的 PAT 主机：端口是主机键的一部分，上游保留端口。
	ported := NewHandler("orb-x", fakeR{git: map[string]string{"git.corp.example:8443": "t3"}})
	tg, st = ported.resolve(req("http://git.corp.example:8443/a/b/git-upload-pack"))
	require.Equal(t, http.StatusOK, st)
	assert.Equal(t, "https://git.corp.example:8443/a/b/git-upload-pack", tg.url.String())

	noGit := NewHandler("orb-x", fakeR{git: toks, pol: Policy{NoGit: true}})
	_, st = noGit.resolve(req("http://github.com/a/b/git-upload-pack"))
	assert.Equal(t, http.StatusForbidden, st)

	// 端到端：真实的代理形式请求行转发到上游时换成 https 与注入的令牌；点段照样 400；owner 叫 memory 的仓库走 git；
	// 模型与 /gh/ 路由在 origin-form 下可用，代理形式下一律 403。
	var gotURL, gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL, gotAuth = r.Host+r.URL.RequestURI(), r.Header.Get("Authorization")
	}))
	defer up.Close()
	h := NewHandler("orb-x", fakeR{git: toks, gh: "ghtok", pol: Policy{
		Routes: map[string]Route{"custom": {Origin: up.URL}},
		GitHub: &GitHubAccess{GitOrigin: up.URL, APIOrigin: up.URL},
	}})
	srv := httptest.NewServer(h)
	defer srv.Close()
	raw := func(target string) int {
		t.Helper()
		gotURL, gotAuth = "", ""
		conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
		require.NoError(t, err)
		defer conn.Close()
		fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: github.com\r\nProxy-Connection: Keep-Alive\r\nConnection: close\r\n\r\n", target)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err, target)
		resp.Body.Close()
		return resp.StatusCode
	}
	upHost := strings.TrimPrefix(up.URL, "http://")
	assert.Equal(t, http.StatusOK, raw("http://github.com/acme/widgets/info/refs?service=git-upload-pack"))
	assert.Equal(t, upHost+"/acme/widgets/info/refs?service=git-upload-pack", gotURL)
	assert.Equal(t, "Basic eC1hY2Nlc3MtdG9rZW46dG9r", gotAuth)
	assert.Equal(t, http.StatusOK, raw("http://github.com/memory/repo/info/refs?service=git-upload-pack"), "owner 叫 memory 的仓库走 git 路由")
	assert.Equal(t, upHost+"/memory/repo/info/refs?service=git-upload-pack", gotURL)
	assert.Equal(t, "Basic eC1hY2Nlc3MtdG9rZW46dG9r", gotAuth)
	for _, target := range []string{
		"http://github.com/acme/widgets/../../victim/private/info/refs?service=git-upload-pack",
		"http://github.com/acme/widgets/%2e%2e/%2e%2e/victim/private/git-upload-pack",
		"http://github.com/acme//widgets/git-upload-pack",
	} {
		assert.Equal(t, http.StatusBadRequest, raw(target), target)
	}
	assert.Equal(t, http.StatusOK, raw("/llm/custom/v1/messages"), "对照：origin-form 的模型路由可用")
	assert.Equal(t, http.StatusOK, raw("/gh/api.github.com/user"), "对照：origin-form 的 /gh/ 路由可用")
	for _, target := range []string{
		"http://x/llm/custom/v1/messages",
		"http://github.com/gh/api.github.com/user",
		"http://api.github.com/gh/api.github.com/user",
		"http://github.com/memory/t/T-1/v1/default/banks",
	} {
		assert.Equal(t, http.StatusForbidden, raw(target), target)
		assert.Empty(t, gotURL, "%s 没有到达上游", target)
	}
}

// M9：协调宿主（NoGit）的 git 路由一律 403，模型路由照常。
func TestNoGitPolicy(t *testing.T) {
	req := func(path string) *http.Request { return httptest.NewRequest("GET", path, nil) }
	h := NewHandler("orb-x", fakeR{git: map[string]string{"github.com": "tok"}, pol: Policy{
		NoGit:  true,
		Routes: map[string]Route{"anthropic": {Origin: "https://api.anthropic.com", SecretIDs: []string{"s1"}}},
	}})
	for _, p := range []string{
		"/git/github.com/a/b/info/refs?service=git-upload-pack",
		"/git/github.com/a/b/git-upload-pack",
		"/git/gitlab.com/x/y/git-upload-pack",
	} {
		_, st := h.resolve(req(p))
		assert.Equal(t, http.StatusForbidden, st, p)
	}
	tg, st := h.resolve(req("/llm/anthropic/v1/messages"))
	require.Equal(t, http.StatusOK, st)
	assert.Equal(t, "https://api.anthropic.com/v1/messages", tg.url.String())
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

// M15：/gh/api.github.com/… 注入身份槽位的令牌转发，保留路径里的 %2F，响应回显的令牌被抹掉。
func TestGitHubRoute(t *testing.T) {
	var gotPath, gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.EscapedPath(), r.Header.Get("Authorization")
		fmt.Fprint(w, "echo "+r.Header.Get("Authorization"))
	}))
	defer up.Close()
	gh := &GitHubAccess{APIOrigin: up.URL, GitOrigin: "https://github.com"}
	srv := httptest.NewServer(NewHandler("orb-x", fakeR{gh: "T0KEN", pol: Policy{GitHub: gh}}))
	defer srv.Close()
	r := do(t, "GET", srv.URL+"/gh/api.github.com/repos/a/b/contents/x%2Fy", nil)
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	require.Equal(t, http.StatusOK, r.StatusCode)
	assert.Equal(t, "/repos/a/b/contents/x%2Fy", gotPath)
	assert.Equal(t, "Bearer T0KEN", gotAuth)
	assert.Equal(t, "echo Bearer oryn-redacted", string(body))

	for name, c := range map[string]struct {
		r    fakeR
		path string
		code int
	}{
		"未开放 GitHub": {fakeR{gh: "T0KEN"}, "/gh/api.github.com/user", http.StatusForbidden},
		"其它主机":       {fakeR{gh: "T0KEN", pol: Policy{GitHub: gh}}, "/gh/gist.github.com/x", http.StatusNotFound},
		"令牌不可用":      {fakeR{pol: Policy{GitHub: gh}}, "/gh/api.github.com/user", http.StatusBadGateway},
	} {
		s := httptest.NewServer(NewHandler("orb-x", c.r))
		resp := do(t, "GET", s.URL+c.path, nil)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		s.Close()
		assert.Equal(t, c.code, resp.StatusCode, name)
		if c.code == http.StatusBadGateway {
			assert.Contains(t, string(b), "GitHub 令牌不可用", name)
		}
	}
}

// M14：GitOrigin 把 github.com 的 git 上游指向别处（测试与验收用的假 GitHub）。
func TestGitHubGitOrigin(t *testing.T) {
	var gotPath, gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
	}))
	defer up.Close()
	h := NewHandler("orb-x", fakeR{git: map[string]string{"github.com": "tok"}, pol: Policy{GitHub: &GitHubAccess{APIOrigin: up.URL, GitOrigin: up.URL}}})
	srv := httptest.NewServer(h)
	defer srv.Close()
	r := do(t, "GET", srv.URL+"/git/github.com/a/b/info/refs?service=git-upload-pack", nil)
	r.Body.Close()
	require.Equal(t, http.StatusOK, r.StatusCode)
	assert.Equal(t, "/a/b/info/refs", gotPath)
	assert.Equal(t, "Basic eC1hY2Nlc3MtdG9rZW46dG9r", gotAuth)
}

// M15：uploads.github.com 同样注入令牌（gh release upload），上游取 UploadsOrigin；客户端的 Accept-Encoding 被去掉，
// 由 Transport 自己协商解压，响应脱敏读到明文。
func TestGitHubUploadsAndEncoding(t *testing.T) {
	var gotPath, gotAuth, gotEnc string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotEnc = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Accept-Encoding")
		fmt.Fprint(w, "echo "+r.Header.Get("Authorization"))
	}))
	defer up.Close()
	srv := httptest.NewServer(NewHandler("orb-x", fakeR{gh: "T0KEN", pol: Policy{GitHub: &GitHubAccess{APIOrigin: "https://api.invalid", UploadsOrigin: up.URL}}}))
	defer srv.Close()
	r := do(t, "POST", srv.URL+"/gh/uploads.github.com/repos/a/b/releases/1/assets?name=x.tgz", map[string]string{"Accept-Encoding": "br"})
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	require.Equal(t, http.StatusOK, r.StatusCode)
	assert.Equal(t, "/repos/a/b/releases/1/assets", gotPath)
	assert.Equal(t, "Bearer T0KEN", gotAuth)
	assert.NotEqual(t, "br", gotEnc, "客户端的 Accept-Encoding 不应透传")
	assert.Equal(t, "echo Bearer oryn-redacted", string(body))
}

// M15：开放 GitHub 时 github.com 的 git 不按仓库限制（任意仓库可推送）；其它 PAT 主机仍按项目仓库白名单。
func TestGitHubGitOpen(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer up.Close()
	toks := map[string]string{"github.com": "tok", "gitlab.example.com": "tok2"}
	pol := Policy{
		GitRepos: map[string][]string{"gitlab.example.com": {"team/proj"}},
		GitHub:   &GitHubAccess{APIOrigin: up.URL, GitOrigin: up.URL},
	}
	srv := httptest.NewServer(NewHandler("orb-x", fakeR{git: toks, pol: pol}))
	defer srv.Close()
	for path, code := range map[string]int{
		"/git/github.com/someone/else/git-receive-pack":                        http.StatusOK,
		"/git/github.com/someone/else/info/refs?service=git-receive-pack":      http.StatusOK,
		"/git/gitlab.example.com/other/repo/info/refs?service=git-upload-pack": http.StatusForbidden,
	} {
		method := "GET"
		if strings.HasSuffix(path, "git-receive-pack") {
			method = "POST"
		}
		r := do(t, method, srv.URL+path, nil)
		r.Body.Close()
		assert.Equal(t, code, r.StatusCode, path)
	}
}

// R2：注入的 GitHub 令牌被上游回 401 时调用 GitHubTokenRejected（GET 与写请求、/gh/ 与 github.com 的 git 路由都调），
// 传回的是注入的那个令牌；200、403 不调。github.com 以外的 git 主机（PAT）不设 ghToken，不会走到回调。
func TestGitHubTokenRejected(t *testing.T) {
	var code atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(code.Load()))
	}))
	defer up.Close()
	rec := &writeRec{}
	pol := Policy{GitHub: &GitHubAccess{APIOrigin: up.URL, GitOrigin: up.URL, UploadsOrigin: up.URL}}
	toks := map[string]string{"github.com": "gho_git"}
	srv := httptest.NewServer(NewHandler("orb-x", fakeR{gh: "T0KEN", git: toks, pol: pol, rec: rec}))
	defer srv.Close()
	rejected := func() []string {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		out := rec.rejected
		rec.rejected = nil
		return out
	}
	for _, c := range []struct {
		name, method, path string
		status             int
		want               []string
	}{
		{"gh GET 401", "GET", "/gh/api.github.com/user", http.StatusUnauthorized, []string{"T0KEN"}},
		{"gh GET 200", "GET", "/gh/api.github.com/user", http.StatusOK, nil},
		{"gh GET 403", "GET", "/gh/api.github.com/user", http.StatusForbidden, nil},
		{"gh 写 401", "POST", "/gh/api.github.com/repos/a/b/issues/1/comments", http.StatusUnauthorized, []string{"T0KEN"}},
		{"uploads 401", "POST", "/gh/uploads.github.com/repos/a/b/releases/1/assets?name=x", http.StatusUnauthorized, []string{"T0KEN"}},
		{"git info/refs 401", "GET", "/git/github.com/a/b/info/refs?service=git-upload-pack", http.StatusUnauthorized, []string{"gho_git"}},
		{"git info/refs 403", "GET", "/git/github.com/a/b/info/refs?service=git-upload-pack", http.StatusForbidden, nil},
	} {
		code.Store(int32(c.status))
		var r *http.Response
		if c.method == "POST" {
			r, _ = post(t, srv.URL+c.path, []byte(`{"body":"x"}`), map[string]string{"Content-Type": "application/json"})
		} else {
			r = do(t, c.method, srv.URL+c.path, nil)
			r.Body.Close()
		}
		assert.Equal(t, c.status, r.StatusCode, c.name)
		assert.Equal(t, c.want, rejected(), c.name)
	}
}

// ghProxy 起一个开放 GitHub 的代理（API 与 git 上游都是 up），返回代理地址与写操作记录。
func ghProxy(t *testing.T, up string) (string, *writeRec) {
	t.Helper()
	rec := &writeRec{}
	pol := Policy{GitHub: &GitHubAccess{APIOrigin: up, GitOrigin: up, UploadsOrigin: up}}
	srv := httptest.NewServer(NewHandler("orb-x", fakeR{gh: "T0KEN", git: map[string]string{"github.com": "tok"}, pol: pol, rec: rec}))
	t.Cleanup(srv.Close)
	return srv.URL, rec
}

func post(t *testing.T, url string, body []byte, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

// M15 2.3：REST 评论在转发前预登记（类别、规范化路径、正文哈希），请求体原样转发；响应顶层的 node_id 与 html_url 的锚点作为补充键；
// /repositories/{id} 形态的仓库取自响应；GET 不算写操作。
func TestGitHubWriteREST(t *testing.T) {
	var gotBody []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":555,"node_id":"IC_555","url":"https://api.github.com/repos/Acme/Widgets/issues/comments/555",`+
			`"html_url":"https://github.com/acme/widgets/issues/5#issuecomment-555","body":"x","token":"T0KEN"}`)
	}))
	defer up.Close()
	px, rec := ghProxy(t, up.URL)

	body := []byte(`{"body":"  hi\r\nthere  "}`)
	resp, out := post(t, px+"/gh/api.github.com/repos/Acme/Widgets/issues/5/comments", body, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, body, gotBody, "请求体原样转发")
	assert.Contains(t, out, `"token":"oryn-redacted"`, "读过的响应体仍然脱敏")
	begins, ends := rec.take()
	require.Len(t, begins, 1)
	assert.Equal(t, GitHubWrite{
		Method: "POST", Category: CatCommentCreate, Target: "/repos/acme/widgets/issues/5/comments",
		Op: "POST /repos/{owner}/{repo}/issues/{number}/comments", Repo: "acme/widgets", BodySHA256: BodyHash("hi\nthere"),
	}, begins[0])
	require.Len(t, ends, 1)
	assert.Equal(t, http.StatusCreated, ends[0].status)
	assert.ElementsMatch(t, []string{"node:IC_555", "comment:555"}, ends[0].keys)

	resp, _ = post(t, px+"/gh/api.github.com/repositories/42/issues/5/comments", body, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	begins, ends = rec.take()
	require.Len(t, begins, 1)
	assert.Empty(t, begins[0].Repo)
	require.Len(t, ends, 1)
	assert.Equal(t, "acme/widgets", ends[0].w.Repo, "R23：路径里没有仓库时取响应的 url")

	r := do(t, "GET", px+"/gh/api.github.com/repos/acme/widgets/issues/5/comments", nil)
	r.Body.Close()
	begins, ends = rec.take()
	assert.Empty(t, begins)
	assert.Empty(t, ends)
}

// M15 2.3：GraphQL 按 operationName 选 operation，展开 fragment，别名不影响 mutation 名；每个根字段一个写操作；
// query 不审计；GraphQL 的响应不读（没有补充键）。
func TestGitHubWriteGraphQL(t *testing.T) {
	var gotBody []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"c":{"commentEdge":{"node":{"url":"https://github.com/a/b/issues/1#issuecomment-9"}}}}}`)
	}))
	defer up.Close()
	px, rec := ghProxy(t, up.URL)
	url := px + "/gh/api.github.com/graphql"

	doc := `query Viewer { viewer { login } }
mutation CommentCreate($input: AddCommentInput!) { ...Root }
fragment Root on Mutation { c: addComment(input: $input) { commentEdge { node { url } } } }`
	body := gqlBody(t, doc, map[string]any{"input": map[string]any{"subjectId": "I_kwDOA", "body": "LGTM\r\n"}}, "CommentCreate")
	resp, _ := post(t, url, body, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, body, gotBody)
	begins, ends := rec.take()
	require.Len(t, begins, 1)
	assert.Equal(t, GitHubWrite{Method: "POST", Category: CatCommentCreate, Target: "I_kwDOA", Op: "addComment", BodySHA256: BodyHash("LGTM")}, begins[0])
	require.Len(t, ends, 1)
	assert.Equal(t, http.StatusOK, ends[0].status)
	assert.Empty(t, ends[0].keys, "GraphQL 的响应不读")

	// 同一文档里的 query 不审计
	resp, _ = post(t, url, gqlBody(t, doc, nil, "Viewer"), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	begins, ends = rec.take()
	assert.Empty(t, begins)
	assert.Empty(t, ends)

	// gh pr create + gh pr review 的形态：一个文档两个根字段
	two := `mutation { pr: createPullRequest(input: {repositoryId: "R_1", baseRefName: "main", headRefName: "me:fix", title: "t", body: "b"}) { pullRequest { id url } }
rv: addPullRequestReview(input: {pullRequestId: "PR_1", event: COMMENT, body: "nice"}) { clientMutationId } }`
	resp, _ = post(t, url, gqlBody(t, two, nil, ""), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	begins, ends = rec.take()
	require.Len(t, begins, 2)
	assert.Equal(t, CatPRCreate, begins[0].Category)
	assert.Equal(t, "createPullRequest", begins[0].Op)
	assert.Equal(t, "R_1", begins[0].Target)
	assert.Equal(t, BodyHash("b"), begins[0].BodySHA256)
	assert.Equal(t, CatReview, begins[1].Category)
	assert.Equal(t, "addPullRequestReview", begins[1].Op)
	assert.Equal(t, "PR_1", begins[1].Target)
	assert.Len(t, ends, 2)
}

// M15 2.3：上游返回 gzip 时（客户端的 Accept-Encoding 已去掉，由 Transport 协商并解压）照样取到补充键，orb 收到明文。
func TestGitHubWriteGzipUpstream(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		js := `{"id":777,"node_id":"PRRC_777","html_url":"https://github.com/a/b/pull/3#discussion_r777"}`
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, js)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusCreated)
		zw := gzip.NewWriter(w)
		fmt.Fprint(zw, js)
		_ = zw.Close()
	}))
	defer up.Close()
	px, rec := ghProxy(t, up.URL)
	resp, out := post(t, px+"/gh/api.github.com/repos/a/b/pulls/3/comments", []byte(`{"body":"nit","path":"x.go","line":1}`),
		map[string]string{"Accept-Encoding": "gzip"})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Content-Encoding"))
	assert.Contains(t, out, "#discussion_r777")
	begins, ends := rec.take()
	require.Len(t, begins, 1)
	assert.Equal(t, CatReviewComment, begins[0].Category)
	require.Len(t, ends, 1)
	assert.ElementsMatch(t, []string{"node:PRRC_777", "review_comment:777"}, ends[0].keys)
}

// M15 2.3：超过 ghWriteBodyMax 或带压缩编码的请求体只按路径审计（Unparsed），门禁关闭时完整转发；
// GitHubWriteBegin 拒绝时回 403 `oryn: <原因>`，不触达上游，审计记 403 与门禁结论。
func TestGitHubWriteOversize(t *testing.T) {
	var calls int
	var gotLen int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, _ := io.ReadAll(r.Body)
		gotLen = len(b)
		w.WriteHeader(http.StatusCreated)
	}))
	defer up.Close()
	px, rec := ghProxy(t, up.URL)
	big := []byte(`{"body":"` + strings.Repeat("a", ghWriteBodyMax) + `"}`)
	url := px + "/gh/api.github.com/repos/a/b/issues/5/comments"

	resp, _ := post(t, url, big, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, len(big), gotLen, "超限请求体完整转发")
	begins, ends := rec.take()
	require.Len(t, begins, 1)
	assert.True(t, begins[0].Unparsed)
	assert.Equal(t, CatCommentCreate, begins[0].Category)
	assert.Equal(t, "POST /repos/{owner}/{repo}/issues/{number}/comments", begins[0].Op)
	assert.Empty(t, begins[0].BodySHA256)
	require.Len(t, ends, 1)
	assert.Equal(t, http.StatusCreated, ends[0].status)

	resp, _ = post(t, url, []byte("\x1f\x8b junk"), map[string]string{"Content-Encoding": "gzip"})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	begins, _ = rec.take()
	require.Len(t, begins, 1)
	assert.True(t, begins[0].Unparsed, "压缩编码的请求体不解析")

	rec.gate = "请求体超过上限"
	calls = 0
	resp, out := post(t, url, big, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Contains(t, out, "oryn: 请求体超过上限")
	assert.Zero(t, calls)
	_, ends = rec.take()
	require.Len(t, ends, 1)
	assert.Equal(t, http.StatusForbidden, ends[0].status)
	assert.Equal(t, "请求体超过上限", ends[0].w.Gate)
}

// M15 2.3：github.com 的 git-receive-pack 解析命令段（每个 ref 一条），请求体原样转发；Begin 拒绝时读完请求体，
// 按协商的 side-band 回 `ng <ref> oryn: <原因>`；其它主机的推送不经 GitHub 写操作回调。
func TestReceivePackWrites(t *testing.T) {
	var gotBody []byte
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	}))
	defer up.Close()
	px, rec := ghProxy(t, up.URL)
	body := append(pkt(oidA+" "+oidB+" refs/heads/feat\x00report-status side-band-64k agent=git/2", oidZ+" "+oidA+" refs/tags/v1", "0000"),
		"PACK\x00\x00\x00\x02rest"...)
	url := px + "/git/github.com/Acme/Widgets.git/git-receive-pack"

	resp, _ := post(t, url, body, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, body, gotBody, "请求体原样转发")
	begins, ends := rec.take()
	require.Len(t, begins, 1)
	assert.Equal(t, GitHubWrite{
		Method: MethodPush, Op: "git-receive-pack", Repo: "acme/widgets",
		Refs: []RefUpdate{{Old: oidA, New: oidB, Ref: "refs/heads/feat"}, {Old: oidZ, New: oidA, Ref: "refs/tags/v1"}},
	}, begins[0])
	require.Len(t, ends, 1)
	assert.Equal(t, http.StatusOK, ends[0].status)

	// 命令段解析不了（push-cert）：只审计
	resp, _ = post(t, url, pkt("push-cert\x00report-status", "0000"), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	begins, _ = rec.take()
	require.Len(t, begins, 1)
	assert.True(t, begins[0].Unparsed)

	rec.gate = "没有 github_prepare 通过记录"
	calls = 0
	resp, out := post(t, url, body, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/x-git-receive-pack-result", resp.Header.Get("Content-Type"))
	assert.Equal(t, string(ReportStatusNG([]string{"refs/heads/feat", "refs/tags/v1"}, "oryn: 没有 github_prepare 通过记录", true)), out)
	assert.Zero(t, calls)
	_, ends = rec.take()
	require.Len(t, ends, 1)
	assert.Equal(t, "没有 github_prepare 通过记录", ends[0].w.Gate)
	rec.gate = ""

	// 其它主机（PAT）的推送不回调
	other := httptest.NewServer(NewHandler("orb-x", fakeR{
		git: map[string]string{"gitlab.example.com": "tok"}, rec: rec,
		pol: Policy{GitRepos: map[string][]string{"gitlab.example.com": {"a/b"}}},
	}))
	defer other.Close()
	post(t, other.URL+"/git/gitlab.example.com/a/b/git-receive-pack", body, nil)
	begins, ends = rec.take()
	assert.Empty(t, begins)
	assert.Empty(t, ends)
}
