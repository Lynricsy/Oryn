package orbdrt

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/link"
)

func TestParseLinkVia(t *testing.T) {
	for _, ok := range []struct{ v, kind, arg string }{
		{"unix:/run/oryn-link/link.sock", "unix", "/run/oryn-link/link.sock"},
		{"vsock:5006", "vsock", "5006"},
		{"vsock:4294967295", "vsock", "4294967295"},
	} {
		kind, arg, err := parseLinkVia(ok.v)
		require.NoError(t, err, ok.v)
		assert.Equal(t, ok.kind, kind)
		assert.Equal(t, ok.arg, arg)
	}
	for _, bad := range []string{
		"", "unix", "unix:", "unix:relative/link.sock", "unix:/a\x00b", "vsock:", "vsock:0", "vsock:-1",
		"vsock:4294967296", "vsock:abc", "tcp:127.0.0.1:7600", "UNIX:/x",
	} {
		_, _, err := parseLinkVia(bad)
		assert.Error(t, err, "%q", bad)
	}
	_, err := orbHTTPClient("", "vsock:0")
	assert.Error(t, err)
	_, err = New(Config{URL: "http://cp", OrbID: "orb-x", Token: testToken, Home: t.TempDir(), LinkVia: "tcp:1"})
	assert.ErrorContains(t, err, "ORYN_LINK_VIA")
}

// ORYN_LINK_VIA=unix:…：底层连接经 unix socket（这里由一个转发到 TLS 服务端的中继接住），TLS、WebSocket 与
// Bearer 令牌仍是端到端。URL 里的主机名不可达，能连上只能是走了 via。
func TestLinkViaUnix(t *testing.T) {
	tokens := make(chan string, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := link.Accept(w, r, func(_ *http.Request, tok string) error {
			tokens <- tok
			return nil
		})
		if err != nil {
			return
		}
		<-sess.CloseChan()
	}))
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
	})

	sock := filepath.Join(t.TempDir(), "link.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	var relayed atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			relayed.Add(1)
			go func() {
				defer c.Close()
				up, err := net.Dial("tcp", srv.Listener.Addr().String())
				if err != nil {
					return
				}
				defer up.Close()
				go func() { _, _ = io.Copy(up, c) }()
				_, _ = io.Copy(c, up)
			}()
		}
	}()

	hc, err := orbHTTPClient(certPEM(srv.Certificate().Raw), "unix:"+sock)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// httptest 的证书含 example.com：TLS 按 URL 的主机名校验，连接本身走 unix socket。
	sess, err := link.DialClient(ctx, link.URL("https://example.com"), "oot_via", hc)
	require.NoError(t, err)
	defer sess.Close()
	assert.Equal(t, "oot_via", <-tokens)
	assert.EqualValues(t, 1, relayed.Load())

	// 没给 CA 也构造 client（系统根 + via 拨号，不走代理）。
	hc, err = orbHTTPClient("", "unix:"+sock)
	require.NoError(t, err)
	require.NotNil(t, hc)
	tr := hc.Transport.(*http.Transport)
	assert.Nil(t, tr.Proxy)
}
