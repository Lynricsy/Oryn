package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// orb_tls.listen 非空时另起 TLS 监听：只信任 orynd CA 的客户端能在其上建立 link；/orb/v1/* 以外一律 404；
// 明文监听照旧服务全部路由；TLS 1.2 与不信任该 CA 的客户端握手失败。
func TestOrbTLSListener(t *testing.T) {
	cfg := config.DefaultOrynd()
	cfg.DataDir = t.TempDir()
	cfg.PublicURL = "http://localhost"
	cfg.Listen = []string{"127.0.0.1:0"}
	cfg.OrbTLS = config.OrbTLS{Listen: []string{"127.0.0.1:0"}, Hosts: []string{"orynd.test"}}
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := New(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(srv.Close)
	require.NotNil(t, srv.OrbTLS)
	require.NotEmpty(t, srv.OrbCAPEM)
	assert.Equal(t, uint16(tls.VersionTLS13), srv.OrbTLS.MinVersion)

	ls, err := srv.Listen()
	require.NoError(t, err)
	require.Len(t, ls.Plain, 1)
	require.Len(t, ls.OrbTLS, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ls) }()
	t.Cleanup(func() {
		cancel()
		assert.NoError(t, <-done)
	})
	tlsURL := "https://" + ls.OrbTLS[0].Addr().String()
	plainURL := "http://" + ls.Plain[0].Addr().String()

	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(srv.OrbCAPEM))
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	t.Cleanup(hc.CloseIdleConnections)

	status := func(c *http.Client, url string) int {
		t.Helper()
		resp, err := c.Get(url)
		require.NoError(t, err, url)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	// 真实 link：持有效 orb 令牌经 TLS 建立 WebSocket + yamux，并开出 ctl 流
	orbID, tok := ids.Orb(), ids.Token("oot")
	require.NoError(t, srv.Store.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	require.NoError(t, srv.Store.Q.CreateOrb(ctx, db.CreateOrbParams{
		ID: orbID, ProviderID: "prov_1", Type: "small", State: orbs.StateConnecting, StateChangedAt: 1, TokenHash: ids.Hash(tok), CreatedAt: 1,
	}))
	sess, err := link.DialClient(ctx, link.URL(tlsURL), tok, hc)
	require.NoError(t, err)
	_, err = sess.Open(ctx, link.StreamHeader{T: link.StreamCtl})
	require.NoError(t, err)
	_ = sess.Close()

	// 未带令牌：到达 link 处理器（401），不是 404
	assert.Equal(t, http.StatusUnauthorized, status(hc, tlsURL+link.Path))
	for _, p := range []string{"/api/v1/me", "/healthz", "/", "/orb/v1", "/orb/v1/../api/v1/me", "/orb/v1/%2e%2e/api/v1/me", "/orb/v1//link", "/s/x", "/api/v1/ws"} {
		assert.Equal(t, http.StatusNotFound, status(hc, tlsURL+p), p)
	}

	// 明文监听不受影响
	assert.Equal(t, http.StatusOK, status(http.DefaultClient, plainURL+"/healthz"))
	assert.Equal(t, http.StatusUnauthorized, status(http.DefaultClient, plainURL+"/api/v1/me"))
	assert.Equal(t, http.StatusUnauthorized, status(http.DefaultClient, plainURL+link.Path))

	// TLS 1.2 客户端、不信任该 CA 的客户端都握手失败
	for name, tc := range map[string]*tls.Config{
		"tls12":   {RootCAs: pool, MaxVersion: tls.VersionTLS12},
		"no_root": {RootCAs: x509.NewCertPool()},
	} {
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: tc}}
		_, err := c.Get(tlsURL + link.Path)
		assert.Error(t, err, name)
		c.CloseIdleConnections()
	}
}

// orb_tls.listen 为空时不生成证书、不开 TLS 监听。
func TestOrbTLSDisabled(t *testing.T) {
	cfg := config.DefaultOrynd()
	cfg.DataDir = t.TempDir()
	cfg.PublicURL = "http://localhost"
	cfg.Listen = []string{"127.0.0.1:0"}
	srv, err := New(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(srv.Close)
	assert.Nil(t, srv.OrbTLS)
	assert.Empty(t, srv.OrbCAPEM)
	ls, err := srv.Listen()
	require.NoError(t, err)
	t.Cleanup(ls.Close)
	assert.Empty(t, ls.OrbTLS)
	assert.NoDirExists(t, cfg.DataDir+"/orb-tls")
}
