package egress

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// echoServer 在 127.0.0.1 上起一个回显 TCP 服务，返回 host:port。
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	return ln.Addr().String()
}

// rawProxy 向代理发一个原始请求（请求行与紧随其后的字节一次写出），读完响应头，返回状态码、响应正文的读取器与连接。
func rawProxy(t *testing.T, proxyURL, head string) (int, *bufio.Reader, net.Conn) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(proxyURL, "http://"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, err = io.WriteString(conn, head)
	require.NoError(t, err)
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	require.NoError(t, err)
	var proto string
	var code int
	_, err = fmt.Sscanf(line, "%s %d", &proto, &code)
	require.NoError(t, err, line)
	for {
		l, err := br.ReadString('\n')
		require.NoError(t, err)
		if l == "\r\n" {
			break
		}
	}
	return code, br, conn
}

func TestPublicAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"93.184.216.34":          true,
		"8.8.8.8":                true,
		"2606:2800:220:1::1":     true,
		"::ffff:93.184.216.34":   true,
		"100.63.255.255":         true,
		"100.128.0.1":            true,
		"198.17.255.255":         true,
		"198.20.0.1":             true,
		"127.0.0.1":              false,
		"::1":                    false,
		"10.1.2.3":               false,
		"172.16.0.1":             false,
		"192.168.1.1":            false,
		"fd12:3456:789a::1":      false,
		"169.254.169.254":        false,
		"fe80::1":                false,
		"fe80::1%eth0":           false,
		"224.0.0.1":              false,
		"ff02::1":                false,
		"ff0e::1":                false,
		"0.0.0.0":                false,
		"::":                     false,
		"0.1.2.3":                false,
		"100.64.0.1":             false,
		"100.127.255.254":        false,
		"198.18.0.1":             false,
		"198.19.255.255":         false,
		"::ffff:127.0.0.1":       false,
		"::ffff:10.0.0.1":        false,
		"::ffff:169.254.169.254": false,
		"::ffff:100.64.0.1":      false,
	} {
		assert.Equal(t, want, publicAddr(netip.MustParseAddr(addr)), addr)
	}
	// Control 收到的是实际连接的 ip:port；解析失败按非公网处理
	assert.ErrorIs(t, publicOnly("tcp4", "127.0.0.1:443", nil), errNotPublic)
	assert.ErrorIs(t, publicOnly("tcp6", "[::1]:443", nil), errNotPublic)
	assert.ErrorIs(t, publicOnly("tcp", "garbage", nil), errNotPublic)
	assert.NoError(t, publicOnly("tcp4", "93.184.216.34:443", nil))
}

// 普通 orb（NoGit 为假）与没有 Web 的协调宿主：CONNECT 一律 403；普通 orb 的 absolute-form 仍只走 git 路由。
func TestWebOff(t *testing.T) {
	echo := echoServer(t)
	for name, pol := range map[string]Policy{
		"coordinator": {NoGit: true},
		"orb":         {},
	} {
		srv := httptest.NewServer(NewHandler("orb-x", fakeR{git: map[string]string{"github.com": "tok"}, pol: pol}))
		code, br, _ := rawProxy(t, srv.URL, fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echo, echo))
		assert.Equal(t, http.StatusForbidden, code, name)
		if name == "coordinator" {
			body, _ := br.ReadString('\n')
			assert.Contains(t, body, "web access is turned off")
			code, _, _ = rawProxy(t, srv.URL, fmt.Sprintf("GET http://%s/x HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", echo, echo))
			assert.Equal(t, http.StatusForbidden, code, "协调宿主没开联网时转发 403")
		} else {
			code, _, _ = rawProxy(t, srv.URL, fmt.Sprintf("GET http://%s/x HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", echo, echo))
			assert.Equal(t, http.StatusForbidden, code, "普通 orb 的代理形式只认 git 端点")
		}
		srv.Close()
	}
	// 普通 orb 即使策略里带了 Web 也不转发非 git 请求（Web 只对 NoGit 的协调宿主有意义）
	srv := httptest.NewServer(NewHandler("orb-x", fakeR{pol: Policy{Web: &WebPolicy{AllowHosts: []string{"127.0.0.1"}}}}))
	defer srv.Close()
	code, _, _ := rawProxy(t, srv.URL, fmt.Sprintf("GET http://%s/x HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", echo, echo))
	assert.Equal(t, http.StatusForbidden, code)
}

func TestWebConnect(t *testing.T) {
	echo := echoServer(t)
	_, port, _ := net.SplitHostPort(echo)

	// 回环地址不是公网：403，正文是固定文案
	deny := httptest.NewServer(NewHandler("orb-x", fakeR{pol: Policy{NoGit: true, Web: &WebPolicy{}}}))
	defer deny.Close()
	code, br, _ := rawProxy(t, deny.URL, fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echo, echo))
	assert.Equal(t, http.StatusForbidden, code)
	body, _ := br.ReadString('\n')
	assert.Contains(t, body, "destination is not a public address")
	// 经域名到回环同样被拒（判定在连接建立时对实际 IP 做）
	code, _, _ = rawProxy(t, deny.URL, "CONNECT localhost:"+port+" HTTP/1.1\r\nHost: localhost:"+port+"\r\n\r\n")
	assert.Equal(t, http.StatusForbidden, code)
	// 目标不是 host:port
	code, _, _ = rawProxy(t, deny.URL, "CONNECT example.com HTTP/1.1\r\nHost: example.com\r\n\r\n")
	assert.Equal(t, http.StatusBadRequest, code)

	// 写进 AllowHosts 后（主机或 host:port 两种形式）隧道可读写；紧跟 CONNECT 发出的字节（已被服务端预读）也会转发
	for _, allow := range []string{"127.0.0.1", echo, "LOCALHOST"} {
		target := echo
		if allow == "LOCALHOST" {
			allow, target = "localhost", "LocalHost:"+port
		}
		srv := httptest.NewServer(NewHandler("orb-x", fakeR{pol: Policy{NoGit: true, Web: &WebPolicy{AllowHosts: []string{allow}}}}))
		code, br, conn := rawProxy(t, srv.URL, fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\nearly-", target, target))
		require.Equal(t, http.StatusOK, code, allow)
		_, err := io.WriteString(conn, "late")
		require.NoError(t, err)
		got := make([]byte, len("early-late"))
		_, err = io.ReadFull(br, got)
		require.NoError(t, err, allow)
		assert.Equal(t, "early-late", string(got), allow)
		_ = conn.Close()
		srv.Close()
	}

	// 放行的目标拨号失败（端口没有监听）：502
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	closed := ln.Addr().String()
	_ = ln.Close()
	srv := httptest.NewServer(NewHandler("orb-x", fakeR{pol: Policy{NoGit: true, Web: &WebPolicy{AllowHosts: []string{"127.0.0.1"}}}}))
	defer srv.Close()
	code, _, _ = rawProxy(t, srv.URL, fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", closed, closed))
	assert.Equal(t, http.StatusBadGateway, code)
}

func TestWebForward(t *testing.T) {
	var gotURI, gotHost, gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI, gotHost, gotAuth = r.URL.RequestURI(), r.Host, r.Header.Get("Authorization")
		fmt.Fprint(w, "hello")
	}))
	defer up.Close()
	upHost := strings.TrimPrefix(up.URL, "http://")
	req := func(proxy, target string) (int, string) {
		code, br, _ := rawProxy(t, proxy, fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nProxy-Connection: Keep-Alive\r\nAuthorization: Bearer mine\r\nConnection: close\r\n\r\n", target, upHost))
		body, _ := io.ReadAll(br)
		return code, string(body)
	}

	// NoGit && Web：原样转发（不改路径与 query，也不动客户端自己的 Authorization）
	allow := httptest.NewServer(NewHandler("orb-x", fakeR{pol: Policy{NoGit: true, Web: &WebPolicy{AllowHosts: []string{"127.0.0.1"}}}}))
	defer allow.Close()
	code, body := req(allow.URL, up.URL+"/a/b?c=d")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "hello", body)
	assert.Equal(t, "/a/b?c=d", gotURI)
	assert.Equal(t, upHost, gotHost)
	assert.Equal(t, "Bearer mine", gotAuth)
	// git 端点形状的路径在协调宿主上也只是普通转发
	code, _ = req(allow.URL, up.URL+"/owner/repo/info/refs?service=git-upload-pack")
	assert.Equal(t, http.StatusOK, code)

	// 没有放行回环：403
	gotURI = ""
	deny := httptest.NewServer(NewHandler("orb-x", fakeR{pol: Policy{NoGit: true, Web: &WebPolicy{}}}))
	defer deny.Close()
	code, body = req(deny.URL, up.URL+"/a")
	assert.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, body, "destination is not a public address")
	assert.Empty(t, gotURI, "没有到达上游")

	// 只接受 http 绝对地址
	code, _ = req(allow.URL, "https://"+upHost+"/a")
	assert.Equal(t, http.StatusBadRequest, code)

	// origin-form 的模型路由在协调宿主上照常（不受正向代理分支影响）
	h := NewHandler("orb-x", fakeR{pol: Policy{NoGit: true, Web: &WebPolicy{}, Routes: map[string]Route{"custom": {Origin: up.URL}}}})
	tg, st := h.resolve(httptest.NewRequest("GET", "/llm/custom/v1/messages", nil))
	require.Equal(t, http.StatusOK, st)
	assert.Equal(t, up.URL+"/v1/messages", tg.url.String())
}

func TestMCPRoute(t *testing.T) {
	release := make(chan struct{})
	var gotPath, gotQuery, gotAuth, gotExtra, gotTok string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		gotAuth, gotExtra = r.Header.Get("Authorization"), r.Header.Get("X-Extra")
		gotTok = r.Header.Get(MCPTokenHeader)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		<-release
		fmt.Fprint(w, "data: echo Bearer real-secret-token end\n\n")
	}))
	defer up.Close()
	h := NewHandler("orb-x", fakeR{pol: Policy{NoGit: true, MCPToken: assistantTok, MCP: map[string]MCPRoute{
		"m0": {URL: up.URL + "/base/?tenant=t1", Headers: map[string]string{"Authorization": "Bearer real-secret-token", "X-Extra": "x"}},
		"m1": {URL: up.URL + "/plain"},
	}}})
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/mcp/m0/sub/path?x=1", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer from-sandbox")
	req.Header.Set(MCPTokenHeader, assistantTok)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	br := bufio.NewReader(resp.Body)
	// 第一个事件在上游放行第二个之前就到达：SSE 逐块转发
	line, err := br.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "data: one\n", line)
	close(release)
	rest, err := io.ReadAll(br)
	require.NoError(t, err)
	assert.Equal(t, "\ndata: echo oryn-redacted end\n\n", string(rest), "上游回显的凭据被抹掉")
	assert.Equal(t, "/base/sub/path", gotPath)
	assert.Equal(t, "tenant=t1&x=1", gotQuery)
	assert.Equal(t, "Bearer real-secret-token", gotAuth, "入站 Authorization 被覆盖")
	assert.Equal(t, "x", gotExtra)
	assert.Empty(t, gotTok, "助手凭据头不转发到上游")

	// 没有配置头的路由：入站 Authorization 照样丢掉；没有后续路径时上游路径不变
	gotAuth = "unset"
	tg, st := h.resolve(withTok(httptest.NewRequest("GET", "/mcp/m1", nil)))
	require.Equal(t, http.StatusOK, st)
	assert.Equal(t, up.URL+"/plain", tg.url.String())
	req, _ = http.NewRequest("GET", srv.URL+"/mcp/m1", nil)
	req.Header.Set("Authorization", "Bearer from-sandbox")
	req.Header.Set(MCPTokenHeader, assistantTok)
	resp2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_, _ = io.ReadAll(resp2.Body)
	resp2.Body.Close()
	assert.Equal(t, "/plain", gotPath)
	assert.Empty(t, gotAuth)

	// 未知路由键 404；占位符不允许出现在 MCP 路由上
	_, st = h.resolve(withTok(httptest.NewRequest("GET", "/mcp/m9/x", nil)))
	assert.Equal(t, http.StatusNotFound, st)
	req, _ = http.NewRequest("GET", srv.URL+"/mcp/m1", nil)
	req.Header.Set("Authorization", "Bearer oryn-ph-s1")
	req.Header.Set(MCPTokenHeader, assistantTok)
	resp3, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp3.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp3.StatusCode)
}

// assistantTok 是测试里全局助手持有的 MCP 凭据。
const assistantTok = "mcp-assistant-token-0123456789"

func withTok(r *http.Request) *http.Request {
	r.Header.Set(MCPTokenHeader, assistantTok)
	return r
}

// D10：没有助手凭据的请求（Space 协调者直接 GET 受保护的 SSE 端点或任意子路径）一律 403，上游收不到任何请求；
// 策略里没有凭据时连带对了头也拒绝（fail closed）。
func TestMCPRouteRequiresAssistantToken(t *testing.T) {
	hits := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: leaked\n\n")
	}))
	defer up.Close()
	routes := map[string]MCPRoute{"m0": {URL: up.URL + "/sse", Headers: map[string]string{"Authorization": "Bearer real-secret-token"}}}
	srv := httptest.NewServer(NewHandler("orb-x", fakeR{pol: Policy{NoGit: true, MCPToken: assistantTok, MCP: routes}}))
	defer srv.Close()
	for name, tc := range map[string]struct {
		method, path, tok string
	}{
		"SSE GET 无凭据":  {"GET", "/mcp/m0", ""},
		"子路径 GET 无凭据":  {"GET", "/mcp/m0/messages?sessionId=1", ""},
		"POST 无凭据":     {"POST", "/mcp/m0", ""},
		"错误凭据":         {"GET", "/mcp/m0", "mcp-assistant-token-0123456780"},
		"凭据前缀":         {"GET", "/mcp/m0", assistantTok[:10]},
		"未知路由键也不暴露存在性": {"GET", "/mcp/m9", ""},
	} {
		req, _ := http.NewRequest(tc.method, srv.URL+tc.path, nil)
		req.Header.Set("Accept", "text/event-stream")
		if tc.tok != "" {
			req.Header.Set(MCPTokenHeader, tc.tok)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err, name)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode, name)
		assert.NotContains(t, string(body), "leaked", name)
	}
	assert.Zero(t, hits, "没有任何请求到达上游")

	// 策略没有凭据（未配置）：带什么头都拒绝
	srv2 := httptest.NewServer(NewHandler("orb-x", fakeR{pol: Policy{NoGit: true, MCP: routes}}))
	defer srv2.Close()
	req, _ := http.NewRequest("GET", srv2.URL+"/mcp/m0", nil)
	req.Header.Set(MCPTokenHeader, "")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Zero(t, hits)
}

// 带凭据的请求遇到上游 302 / 307 跳到第二个服务器：代理拦成 502 并去掉 Location，即使客户端照常跟随重定向（Go 的
// http.Client 与 omp 的 fetch 一样会把自定义头带过去），第二站也收不到任何请求、拿不到凭据；同源且在路由路径下的
// 重定向改写成 /mcp/<key>/…，客户端继续走代理（凭据头在代理处去掉），上游路由外的同源路径也拦截。
func TestMCPRedirectDoesNotLeakToken(t *testing.T) {
	var mu sync.Mutex
	var evilHits int
	var evilTok string
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		evilHits++
		evilTok = r.Header.Get(MCPTokenHeader)
		mu.Unlock()
	}))
	defer evil.Close()
	var upTok, upURI []string
	var up *httptest.Server
	up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		upTok = append(upTok, r.Header.Get(MCPTokenHeader))
		upURI = append(upURI, r.URL.RequestURI())
		mu.Unlock()
		switch r.URL.Path {
		case "/base/away302":
			http.Redirect(w, r, evil.URL+"/steal", http.StatusFound)
		case "/base/away307":
			http.Redirect(w, r, evil.URL+"/steal", http.StatusTemporaryRedirect)
		case "/base/samehost":
			http.Redirect(w, r, up.URL+"/base/next?tenant=t1&page=2", http.StatusFound)
		case "/base/relative":
			w.Header().Set("Location", "next")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case "/base/outside":
			http.Redirect(w, r, up.URL+"/admin", http.StatusFound)
		default:
			fmt.Fprint(w, "ok")
		}
	}))
	defer up.Close()
	srv := httptest.NewServer(NewHandler("orb-x", fakeR{pol: Policy{NoGit: true, MCPToken: assistantTok, MCP: map[string]MCPRoute{
		"m0": {URL: up.URL + "/base/?tenant=t1", Headers: map[string]string{"Authorization": "Bearer real-secret-token"}},
	}}}))
	defer srv.Close()
	do := func(method, path string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader("{}"))
		req.Header.Set(MCPTokenHeader, assistantTok)
		resp, err := http.DefaultClient.Do(req) // 默认跟随重定向
		require.NoError(t, err)
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp
	}

	for _, p := range []string{"/mcp/m0/away302", "/mcp/m0/away307", "/mcp/m0/outside"} {
		for _, m := range []string{"GET", "POST"} {
			resp := do(m, p)
			assert.Equal(t, http.StatusBadGateway, resp.StatusCode, "%s %s", m, p)
			assert.Empty(t, resp.Header.Get("Location"), "%s %s", m, p)
		}
	}
	mu.Lock()
	assert.Zero(t, evilHits, "第二站收不到任何请求")
	assert.Empty(t, evilTok)
	mu.Unlock()

	// 同源、在路由路径下：绝对地址与相对地址都改写回代理，客户端跟随后仍经代理到达上游
	upTok, upURI = nil, nil
	assert.Equal(t, http.StatusOK, do("GET", "/mcp/m0/samehost").StatusCode)
	assert.Equal(t, http.StatusOK, do("GET", "/mcp/m0/relative").StatusCode)
	mu.Lock()
	assert.Equal(t, []string{"/base/samehost?tenant=t1", "/base/next?tenant=t1&page=2", "/base/relative?tenant=t1", "/base/next?tenant=t1"}, upURI)
	for _, tok := range upTok {
		assert.Empty(t, tok, "凭据头不到上游")
	}
	mu.Unlock()

	// 改写结果本身：不跟随时看到的是代理路径
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", srv.URL+"/mcp/m0/samehost", nil)
	req.Header.Set(MCPTokenHeader, assistantTok)
	resp, err := noFollow.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "/mcp/m0/next?page=2", resp.Header.Get("Location"))
}

func TestMCPRedact(t *testing.T) {
	got := mcpRedact(map[string]string{
		"Authorization": "Bearer abcdefghij",
		"X-Api-Key":     "k-1234567890",
		"Accept":        "application/json",
		"X-Token":       "short",
	})
	assert.ElementsMatch(t, []string{"Bearer abcdefghij", "abcdefghij", "k-1234567890"}, got)
}
