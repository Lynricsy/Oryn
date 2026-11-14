package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func caPool(t *testing.T, dir string) *x509.CertPool {
	t.Helper()
	ca, err := readCert(filepath.Join(dir, caCertFile))
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return pool
}

func TestEnsureCertsChainAndReuse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)

	c1, err := ensureCerts(dir, now)
	require.NoError(t, err)
	require.Len(t, c1.Certificate, 2, "链 = 叶子 + CA")

	for _, f := range []string{caKeyFile, leafKeyFile} {
		st, err := os.Stat(filepath.Join(dir, f))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), st.Mode().Perm(), f)
	}
	pool := caPool(t, dir)
	for _, name := range []string{"localhost", "127.0.0.1", "::1"} {
		_, err := c1.Leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: name, CurrentTime: now})
		assert.NoError(t, err, name)
	}
	_, err = c1.Leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "example.com", CurrentTime: now})
	assert.Error(t, err)

	// 再次调用：CA 与叶子都复用
	caBefore, err := os.ReadFile(filepath.Join(dir, caCertFile))
	require.NoError(t, err)
	c2, err := ensureCerts(dir, now.Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, c1.Certificate[0], c2.Certificate[0])

	// 叶子临近过期：只重签叶子，CA 文件不变（系统信任库里的锚点继续有效）
	later := now.Add(leafValidity - renewBefore/2)
	c3, err := ensureCerts(dir, later)
	require.NoError(t, err)
	assert.NotEqual(t, c1.Certificate[0], c3.Certificate[0])
	caAfter, err := os.ReadFile(filepath.Join(dir, caCertFile))
	require.NoError(t, err)
	assert.Equal(t, caBefore, caAfter)
	_, err = c3.Leaf.Verify(x509.VerifyOptions{Roots: caPool(t, dir), DNSName: "localhost", CurrentTime: later})
	assert.NoError(t, err)
}

func TestEnsureCertsRefusesHalfCA(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, caCertFile), []byte("x"), 0o644))
	_, err := ensureCerts(dir, time.Now())
	assert.ErrorContains(t, err, "同时存在")
}

// TestCARefusesForeignNames：CA 带 Critical 名称约束，即使用它的私钥为 example.com 签发叶子证书，
// Go 的 x509 校验也不接受（CA 私钥泄露时无法用来冒充其它域名）。
func TestCARefusesForeignNames(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	_, err := ensureCerts(dir, now)
	require.NoError(t, err)
	ca, err := readCert(filepath.Join(dir, caCertFile))
	require.NoError(t, err)
	caKey, err := readKey(filepath.Join(dir, caKeyFile))
	require.NoError(t, err)
	assert.True(t, ca.PermittedDNSDomainsCritical)
	assert.Equal(t, []string{"localhost"}, ca.PermittedDNSDomains)

	sign := func(dns []string, ips []net.IP) *x509.Certificate {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(42),
			Subject:      pkix.Name{CommonName: "x"},
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			DNSNames:     dns,
			IPAddresses:  ips,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
		require.NoError(t, err)
		leaf, err := x509.ParseCertificate(der)
		require.NoError(t, err)
		return leaf
	}
	pool := caPool(t, dir)
	verify := func(leaf *x509.Certificate, name string) error {
		_, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: name, CurrentTime: now})
		return err
	}

	var cie x509.CertificateInvalidError
	err = verify(sign([]string{"example.com"}, nil), "example.com")
	require.ErrorAs(t, err, &cie)
	assert.Equal(t, x509.CANotAuthorizedForThisName, cie.Reason)
	// 夹带一个合法名字也不行：约束对叶子的全部 SAN 生效
	err = verify(sign([]string{"localhost", "example.com"}, nil), "localhost")
	require.ErrorAs(t, err, &cie)
	assert.Equal(t, x509.CANotAuthorizedForThisName, cie.Reason)
	err = verify(sign(nil, []net.IP{net.IPv4(10, 0, 0, 1)}), "10.0.0.1")
	require.ErrorAs(t, err, &cie)
	assert.Equal(t, x509.CANotAuthorizedForThisName, cie.Reason)
	// 约束内的名字照常通过
	assert.NoError(t, verify(sign([]string{"localhost"}, []net.IP{net.IPv4(127, 0, 0, 2), net.IPv6loopback}), "127.0.0.2"))
}

// TestEnsureCertsRefusesUnconstrainedCA：旧版生成的无名称约束 CA 不再复用，报错提示撤销信任并重建，且不改动任何文件。
func TestEnsureCertsRefusesUnconstrainedCA(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Oryn dev local CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	require.NoError(t, writeKey(filepath.Join(dir, caKeyFile), key))
	require.NoError(t, writePEM(filepath.Join(dir, caCertFile), "CERTIFICATE", der, 0o644))
	before, err := os.ReadFile(filepath.Join(dir, caCertFile))
	require.NoError(t, err)

	_, err = ensureCerts(dir, now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "没有名称约束")
	assert.Contains(t, err.Error(), "trust anchor --remove")
	after, err := os.ReadFile(filepath.Join(dir, caCertFile))
	require.NoError(t, err)
	assert.Equal(t, before, after)
	_, err = os.Stat(filepath.Join(dir, leafCertFile))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

type seen struct {
	host, xff, proto, path string
}

// startProxy 以 run 启动代理（-listen 127.0.0.1:0），返回监听地址与信任生成 CA 的客户端 TLS 配置。
func startProxy(t *testing.T, target, static string) (string, *tls.Config) {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan net.Addr, 1)
	done := make(chan error, 1)
	args := []string{"-listen", "127.0.0.1:0", "-target", target, "-dir", dir}
	if static != "" {
		args = append(args, "-static", static)
	}
	go func() { done <- run(ctx, args, io.Discard, ready) }()
	var addr net.Addr
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("代理提前退出：%v", err)
	}
	t.Cleanup(func() {
		cancel()
		assert.NoError(t, <-done)
	})
	return addr.String(), &tls.Config{RootCAs: caPool(t, dir), ServerName: "localhost"}
}

func TestProxyForwardsHostAndStatic(t *testing.T) {
	got := make(chan seen, 4)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- seen{host: r.Host, xff: r.Header.Get("X-Forwarded-For"), proto: r.Header.Get("X-Forwarded-Proto"), path: r.URL.Path}
		_, _ = io.WriteString(w, "backend")
	}))
	defer backend.Close()
	static := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(static, "latest.json"), []byte(`{"version":"0.0.2"}`), 0o644))

	addr, tc := startProxy(t, backend.URL, static)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tc}}

	req, err := http.NewRequest(http.MethodGet, "https://"+addr+"/api/v1/me", nil)
	require.NoError(t, err)
	req.Host = "localhost:7643"
	res, err := client.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	assert.Equal(t, "backend", string(body))
	s := <-got
	assert.Equal(t, "localhost:7643", s.host, "Host 原样透传")
	assert.Equal(t, "127.0.0.1", s.xff)
	assert.Equal(t, "https", s.proto)
	assert.Equal(t, "/api/v1/me", s.path)

	// 不设 ServerName 时按 IP SAN 校验
	ipClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: tc.RootCAs}}}
	res, err = ipClient.Get("https://" + addr + "/updates/latest.json")
	require.NoError(t, err)
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, `{"version":"0.0.2"}`, string(body))
	assert.Equal(t, "no-store", res.Header.Get("Cache-Control"))
	assert.Empty(t, got, "静态目录不经过上游")

	// 不信任生成 CA 的客户端被拒
	_, err = (&http.Client{}).Get("https://" + addr + "/")
	assert.Error(t, err)
}

func TestProxyUpgrade(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "echo" {
			http.Error(w, "want upgrade", http.StatusBadRequest)
			return
		}
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: echo\r\nConnection: Upgrade\r\n\r\n")
		_ = brw.Flush()
		line, err := brw.ReadString('\n')
		if err != nil {
			return
		}
		_, _ = brw.WriteString("echo:" + line)
		_ = brw.Flush()
	}))
	defer backend.Close()

	addr, tc := startProxy(t, backend.URL, "")
	conn, err := tls.Dial("tcp", addr, tc)
	require.NoError(t, err)
	defer conn.Close()
	_, err = io.WriteString(conn, "GET /api/v1/ws HTTP/1.1\r\nHost: localhost:7643\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
	require.NoError(t, err)
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusSwitchingProtocols, res.StatusCode)
	_, err = io.WriteString(conn, "ping\n")
	require.NoError(t, err)
	line, err := br.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "echo:ping\n", line)
}
