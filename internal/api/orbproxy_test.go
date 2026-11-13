package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawOrbd 是只会照脚本写原始字节的恶意 orbd：每条连接读一个请求，记下它，再交给 respond 处理连接。
type rawOrbd struct {
	ln   net.Listener
	reqs chan *http.Request
}

func newRawOrbd(t *testing.T, respond func(c net.Conn)) *rawOrbd {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	o := &rawOrbd{ln: ln, reqs: make(chan *http.Request, 16)}
	done := make(chan struct{})
	t.Cleanup(func() { close(done); _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				req, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil {
					return
				}
				o.reqs <- req
				respond(c)
				// 保持连接直到测试结束（「永不应答」的场景靠截止时间结束请求）
				<-done
			}()
		}
	}()
	return o
}

// transport 返回把任意主机名都拨到该 orbd 的 Transport（取代 orb link 的 fs 流）。
func (o *rawOrbd) transport() http.RoundTripper {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", o.ln.Addr().String())
		},
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 64 << 10,
	}
}

// proxyServer 起一个只做 serveOrbProxy 的前端服务。
func proxyServer(t *testing.T, o *rawOrbd, upstream string, opt orbProxyOpt) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveOrbProxy(w, r, o.transport(), "orb_test", upstream, r.URL.RawQuery, opt)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeRaw(c net.Conn, s string) { _, _ = io.WriteString(c, strings.ReplaceAll(s, "\n", "\r\n")) }

func errCode(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(body, &e), string(body))
	return e.Code
}

func doGet(t *testing.T, url string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, body
}

// orbd 返回白名单外的状态码（401 会让前端登出、3xx、未经请求的 101）：一律 502 orbd_bad_response，
// 原状态码与响应头都不到达客户端；101 不得把客户端连接劫持成直通 orb 的隧道。
func TestOrbProxyRejectsUnexpectedStatus(t *testing.T) {
	for name, raw := range map[string]string{
		"401": "HTTP/1.1 401 Unauthorized\nContent-Type: application/json\nContent-Length: 23\n\n{\"code\":\"unauthorized\"}",
		"302": "HTTP/1.1 302 Found\nLocation: https://evil.example/\nContent-Length: 0\n\n",
		"101": "HTTP/1.1 101 Switching Protocols\nConnection: Upgrade\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			o := newRawOrbd(t, func(c net.Conn) {
				writeRaw(c, raw)
				if name == "101" {
					_, _ = io.WriteString(c, "tunnel-bytes")
				}
			})
			srv := proxyServer(t, o, "/git/status", orbProxyOpt{})
			res, body := doGet(t, srv.URL+"/x", nil)
			assert.Equal(t, http.StatusBadGateway, res.StatusCode, string(body))
			assert.Equal(t, "orbd_bad_response", errCode(t, body))
			assert.Empty(t, res.Header.Get("Location"))
			assert.NotContains(t, string(body), "tunnel-bytes")
		})
	}
}

// 1xx 中间响应（如带 Set-Cookie 的 103）不得绕过响应头白名单到达客户端。
func TestOrbProxyDrops1xx(t *testing.T) {
	o := newRawOrbd(t, func(c net.Conn) {
		writeRaw(c, "HTTP/1.1 103 Early Hints\nSet-Cookie: oryn_session=evil\nLink: <https://evil/>; rel=preconnect\n\n"+
			"HTTP/1.1 200 OK\nContent-Type: application/json\nSet-Cookie: oryn_session=evil2\nContent-Length: 2\n\n{}")
	})
	srv := proxyServer(t, o, "/fs/list", orbProxyOpt{})
	var got1xx []textproto.MIMEHeader
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		Got1xxResponse: func(_ int, h textproto.MIMEHeader) error { got1xx = append(got1xx, h); return nil },
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/x", nil)
	require.NoError(t, err)
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "{}", string(body))
	assert.Empty(t, got1xx, "1xx 中间响应不得转发给客户端")
	assert.Empty(t, res.Header.Values("Set-Cookie"))
	assert.Empty(t, res.Header.Values("Link"))
}

// 请求头只转发白名单：Cookie、Authorization、前置代理加的身份头、浏览器指纹头都不进 orb。
func TestOrbProxyRequestHeaderAllowlist(t *testing.T) {
	o := newRawOrbd(t, func(c net.Conn) {
		writeRaw(c, "HTTP/1.1 206 Partial Content\nContent-Length: 2\n\nok")
	})
	srv := proxyServer(t, o, "/fs/file", orbProxyOpt{raw: true, filename: "a.txt"})
	res, _ := doGet(t, srv.URL+"/x?path=a", map[string]string{
		"Cookie": "oryn_session=s", "Authorization": "Bearer x", "Cf-Access-Jwt-Assertion": "jwt", "Cf-Connecting-Ip": "1.2.3.4",
		"True-Client-Ip": "1.2.3.4", "User-Agent": "Mozilla/5.0", "Accept-Language": "zh", "Sec-Fetch-Site": "same-origin",
		"X-Oryn-Csrf": "c", "X-Forwarded-For": "1.2.3.4", "Range": "bytes=0-1", "If-Range": `"e"`, "Accept": "*/*",
	})
	assert.Equal(t, http.StatusPartialContent, res.StatusCode)
	req := <-o.reqs
	var names []string
	for k := range req.Header {
		if k != "Connection" { // Transport 自己加的 Connection: close（DisableKeepAlives）
			names = append(names, k)
		}
	}
	assert.ElementsMatch(t, []string{"Range", "If-Range", "Accept"}, names, "转发到 orbd 的请求头：%v", req.Header)
}

// 不应答的 orbd 不得无限占住请求：JSON 路由与 GET fs/file 等响应头超时后 504 orbd_timeout。
func TestOrbProxyDeadlines(t *testing.T) {
	oldCall, oldHdr := orbCallTimeout, orbReadHeaderTimeout
	orbCallTimeout, orbReadHeaderTimeout = 200*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { orbCallTimeout, orbReadHeaderTimeout = oldCall, oldHdr })

	silent := newRawOrbd(t, func(net.Conn) {})
	for name, opt := range map[string]orbProxyOpt{"json": {}, "raw": {raw: true, filename: "a"}} {
		t.Run(name, func(t *testing.T) {
			srv := proxyServer(t, silent, "/git/status", opt)
			start := time.Now()
			res, body := doGet(t, srv.URL+"/x", nil)
			assert.Equal(t, http.StatusGatewayTimeout, res.StatusCode, string(body))
			assert.Equal(t, "orbd_timeout", errCode(t, body))
			assert.Less(t, time.Since(start), 5*time.Second)
		})
	}

	// GET fs/file：响应头按时到达后，下载本身不受响应头截止时间限制
	slow := newRawOrbd(t, func(c net.Conn) {
		writeRaw(c, "HTTP/1.1 200 OK\nContent-Length: 4\n\n")
		_, _ = io.WriteString(c, "ab")
		time.Sleep(3 * orbReadHeaderTimeout)
		_, _ = io.WriteString(c, "cd")
	})
	srv := proxyServer(t, slow, "/fs/file", orbProxyOpt{raw: true, filename: "a"})
	res, body := doGet(t, srv.URL+"/x", nil)
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "abcd", string(body))
	assert.Equal(t, "application/octet-stream", res.Header.Get("Content-Type"))
}

// 声明长度超过上限的 JSON 响应：不转发、502 orbd_bad_response。
func TestOrbProxyJSONResponseCap(t *testing.T) {
	o := newRawOrbd(t, func(c net.Conn) {
		writeRaw(c, "HTTP/1.1 200 OK\nContent-Type: application/json\nContent-Length: 40000000\n\n[")
	})
	srv := proxyServer(t, o, "/fs/list", orbProxyOpt{})
	res, body := doGet(t, srv.URL+"/x", nil)
	assert.Equal(t, http.StatusBadGateway, res.StatusCode)
	assert.Equal(t, "orbd_bad_response", errCode(t, body))

	// 未声明长度（chunked）的超长 JSON：读到上限即中止，客户端拿不到完整响应
	cb := &cappedBody{ReadCloser: io.NopCloser(strings.NewReader(strings.Repeat("x", 10))), left: 4}
	got, err := io.ReadAll(cb)
	assert.ErrorIs(t, err, errOrbBadResponse)
	assert.LessOrEqual(t, len(got), 4)
}
