package render

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func testProxy(t *testing.T) *proxy {
	t.Helper()
	site := &fakeSite{pages: map[string]string{"/index.html": "<p>hello</p>"}}
	p, err := newProxy(site, []string{"https://cdn.jsdelivr.net", "https://unpkg.com"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	return p
}

// proxyGet 经代理发一个绝对 URI 的 GET。
func proxyGet(t *testing.T, p *proxy, target string) (int, string) {
	t.Helper()
	pu, _ := url.Parse("http://" + p.ln.Addr().String())
	c := &http.Client{
		Transport:     &http.Transport{Proxy: http.ProxyURL(pu)},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := c.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// proxyConnect 向代理发 CONNECT，返回状态码与（放行时）隧道连接。
func proxyConnect(t *testing.T, p *proxy, hostport string) (int, net.Conn) {
	t.Helper()
	c, err := net.Dial("tcp", p.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := io.WriteString(c, "CONNECT "+hostport+" HTTP/1.1\r\nHost: "+hostport+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, c
}

func TestProxyContent(t *testing.T) {
	p := testProxy(t)
	p.add("tok", &proxyJob{target: Target{ArtifactID: "a", Version: 1, Self: "http://" + Host + "/tok/"}})
	if code, body := proxyGet(t, p, "http://"+Host+"/tok/"); code != 200 || body != "<p>hello</p>" {
		t.Fatalf("job content = %d %q", code, body)
	}
	if code, _ := proxyGet(t, p, "http://"+Host+"/tok"); code != http.StatusFound {
		t.Fatalf("job root without slash = %d, want 302", code)
	}
	if code, _ := proxyGet(t, p, "http://"+Host+"/unknown/"); code != http.StatusForbidden {
		t.Fatalf("unknown token = %d, want 403", code)
	}
	if code, _ := proxyGet(t, p, "http://127.0.0.1:1/"); code != http.StatusForbidden {
		t.Fatalf("other host = %d, want 403", code)
	}
	if code, _ := proxyGet(t, p, "http://"+Host+".evil.example/tok/"); code != http.StatusForbidden {
		t.Fatalf("lookalike host = %d, want 403", code)
	}
	p.remove("tok")
	if code, _ := proxyGet(t, p, "http://"+Host+"/tok/"); code != http.StatusForbidden {
		t.Fatalf("finished job = %d, want 403", code)
	}
	// 直接对代理发相对路径的请求（不是代理请求）
	resp, err := http.Get("http://" + p.ln.Addr().String() + "/tok/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("origin-form request = %d, want 403", resp.StatusCode)
	}
}

func TestProxyFixedJob(t *testing.T) {
	p := testProxy(t)
	p.add("pf", &proxyJob{fixed: preflightPage})
	if code, body := proxyGet(t, p, "http://"+Host+"/pf/"); code != 200 || body != string(preflightPage) {
		t.Fatalf("preflight page = %d %q", code, body)
	}
	if code, _ := proxyGet(t, p, "http://"+Host+"/pf/other.html"); code != http.StatusNotFound {
		t.Fatalf("preflight other path = %d, want 404", code)
	}
	// 自检作业不算非严格作业：不放行 CDN
	if code, _ := proxyConnect(t, p, "cdn.jsdelivr.net:443"); code != http.StatusForbidden {
		t.Fatalf("CONNECT during preflight only = %d, want 403", code)
	}
}

func TestProxyConnect(t *testing.T) {
	p := testProxy(t)
	// 上游换成本地的回显服务
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	var dialed []string
	p.dial = func(ctx context.Context, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		return net.Dial("tcp", echo.Addr().String())
	}

	if code, _ := proxyConnect(t, p, "cdn.jsdelivr.net:443"); code != http.StatusForbidden {
		t.Fatalf("CONNECT with no job = %d, want 403", code)
	}
	p.add("strict", &proxyJob{target: Target{ArtifactID: "a", Version: 1, Strict: true}})
	if code, _ := proxyConnect(t, p, "cdn.jsdelivr.net:443"); code != http.StatusForbidden {
		t.Fatalf("CONNECT with only a strict job = %d, want 403", code)
	}
	p.add("loose", &proxyJob{target: Target{ArtifactID: "b", Version: 1}})
	for _, hp := range []string{"example.com:443", "cdn.jsdelivr.net:80", "127.0.0.1:443", "CDN.jsdelivr.net.evil.example:443", "cdn.jsdelivr.net"} {
		if code, _ := proxyConnect(t, p, hp); code != http.StatusForbidden {
			t.Errorf("CONNECT %s = %d, want 403", hp, code)
		}
	}
	if len(dialed) != 0 {
		t.Fatalf("denied CONNECTs dialed %q", dialed)
	}
	code, c := proxyConnect(t, p, "CDN.jsdelivr.net:443")
	if code != http.StatusOK {
		t.Fatalf("CONNECT whitelisted CDN with a loose job = %d, want 200", code)
	}
	if _, err := io.WriteString(c, "ping\n"); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || line != "ping\n" {
		t.Fatalf("tunnel echo = %q, %v", line, err)
	}
	if len(dialed) != 1 || !strings.EqualFold(dialed[0], "cdn.jsdelivr.net:443") {
		t.Fatalf("dialed %q", dialed)
	}
}
