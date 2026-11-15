package orbtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testIPs = []net.IP{net.ParseIP("172.18.0.1"), net.ParseIP("127.0.0.1")}

// t0 是固定的「当前时刻」，各测试以它为假时钟推进。
var t0 = time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC)

func leafOf(t *testing.T, cfg *tls.Config) *x509.Certificate {
	t.Helper()
	require.Len(t, cfg.Certificates, 1)
	leaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	require.NoError(t, err)
	return leaf
}

func caOf(t *testing.T, caPEM []byte) *x509.Certificate {
	t.Helper()
	ca, err := parseCert(caPEM)
	require.NoError(t, err)
	return ca
}

// verifyAt 以 caPEM 为唯一根、在 now 时刻校验 leaf 对 host 有效。
func verifyAt(t *testing.T, caPEM []byte, leaf *x509.Certificate, host string, now time.Time) error {
	t.Helper()
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(caPEM))
	_, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: host, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

func perm(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	return fi.Mode().Perm()
}

func TestLoadFirstRun(t *testing.T) {
	dir := t.TempDir()
	cfg, caPEM, err := load(dir, testIPs, []string{"orynd.internal", "10.0.0.5"}, t0)
	require.NoError(t, err)

	assert.Equal(t, uint16(tls.VersionTLS13), cfg.MinVersion)
	assert.Equal(t, []string{"http/1.1"}, cfg.NextProtos)

	ca := caOf(t, caPEM)
	assert.True(t, ca.IsCA)
	assert.Equal(t, elliptic.P256(), ca.PublicKey.(*ecdsa.PublicKey).Curve)
	assert.Equal(t, t0.Add(caValidity), ca.NotAfter)

	leaf := leafOf(t, cfg)
	assert.Equal(t, elliptic.P256(), leaf.PublicKey.(*ecdsa.PublicKey).Curve)
	assert.Equal(t, t0.Add(certValidity), leaf.NotAfter)
	assert.False(t, leaf.IsCA)
	for _, h := range []string{"172.18.0.1", "127.0.0.1", "10.0.0.5", "orynd.internal"} {
		assert.NoError(t, verifyAt(t, caPEM, leaf, h, t0), h)
	}
	assert.Error(t, verifyAt(t, caPEM, leaf, "evil.example", t0))

	tlsDir := filepath.Join(dir, dirName)
	assert.Equal(t, os.FileMode(0o700), perm(t, tlsDir))
	assert.Equal(t, os.FileMode(0o600), perm(t, filepath.Join(tlsDir, caKeyFile)))
	assert.Equal(t, os.FileMode(0o600), perm(t, filepath.Join(tlsDir, keyFile)))
	assert.Equal(t, os.FileMode(0o644), perm(t, filepath.Join(tlsDir, caCertFile)))
	onDisk, err := os.ReadFile(filepath.Join(tlsDir, caCertFile))
	require.NoError(t, err)
	assert.Equal(t, caPEM, onDisk)
}

func TestLoadReusesCAAndCert(t *testing.T) {
	dir := t.TempDir()
	cfg1, ca1, err := load(dir, testIPs, []string{"orynd.internal"}, t0)
	require.NoError(t, err)
	// 一周后、hosts 换了顺序与大小写：既不换 CA 也不重签
	cfg2, ca2, err := load(dir, []net.IP{testIPs[1], testIPs[0]}, []string{"ORYND.internal."}, t0.Add(7*24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, ca1, ca2)
	assert.Equal(t, leafOf(t, cfg1).SerialNumber, leafOf(t, cfg2).SerialNumber)
}

func TestLoadReissuesOnSANChange(t *testing.T) {
	dir := t.TempDir()
	cfg1, ca1, err := load(dir, testIPs, nil, t0)
	require.NoError(t, err)
	old := leafOf(t, cfg1)

	cfg2, ca2, err := load(dir, []net.IP{net.ParseIP("172.18.0.1")}, []string{"orynd.internal"}, t0.Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, ca1, ca2, "CA 不变")
	leaf := leafOf(t, cfg2)
	assert.NotEqual(t, old.SerialNumber, leaf.SerialNumber)
	assert.NoError(t, verifyAt(t, ca2, leaf, "orynd.internal", t0.Add(time.Hour)))
	assert.Error(t, verifyAt(t, ca2, leaf, "127.0.0.1", t0.Add(time.Hour)), "去掉的 IP 不再在证书里")

	// 磁盘上已是新证书：再加载不再重签
	cfg3, _, err := load(dir, []net.IP{net.ParseIP("172.18.0.1")}, []string{"orynd.internal"}, t0.Add(2*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, leaf.SerialNumber, leafOf(t, cfg3).SerialNumber)
}

func TestLoadReissuesBeforeExpiry(t *testing.T) {
	dir := t.TempDir()
	cfg1, ca1, err := load(dir, testIPs, nil, t0)
	require.NoError(t, err)
	old := leafOf(t, cfg1)

	// 距到期还有 31 天：不重签
	early := old.NotAfter.Add(-renewBefore - 24*time.Hour)
	cfg2, _, err := load(dir, testIPs, nil, early)
	require.NoError(t, err)
	assert.Equal(t, old.SerialNumber, leafOf(t, cfg2).SerialNumber)

	// 距到期 29 天：重签，CA 不变，新证书从此刻起一年
	late := old.NotAfter.Add(-renewBefore + 24*time.Hour)
	cfg3, ca3, err := load(dir, testIPs, nil, late)
	require.NoError(t, err)
	assert.Equal(t, ca1, ca3)
	leaf := leafOf(t, cfg3)
	assert.NotEqual(t, old.SerialNumber, leaf.SerialNumber)
	assert.Equal(t, late.Add(certValidity), leaf.NotAfter)
	assert.NoError(t, verifyAt(t, ca3, leaf, "172.18.0.1", late.Add(200*24*time.Hour)))
}

func TestLoadTightensKeyPermissions(t *testing.T) {
	dir := t.TempDir()
	_, _, err := load(dir, testIPs, nil, t0)
	require.NoError(t, err)
	tlsDir := filepath.Join(dir, dirName)
	for _, f := range []string{caKeyFile, keyFile} {
		require.NoError(t, os.Chmod(filepath.Join(tlsDir, f), 0o644))
	}
	require.NoError(t, os.Chmod(tlsDir, 0o755))
	_, _, err = load(dir, testIPs, nil, t0)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), perm(t, filepath.Join(tlsDir, caKeyFile)))
	assert.Equal(t, os.FileMode(0o600), perm(t, filepath.Join(tlsDir, keyFile)))
	assert.Equal(t, os.FileMode(0o700), perm(t, tlsDir))
}

func TestLoadCAFiles(t *testing.T) {
	t.Run("证书在而私钥丢失：报错，不换 CA", func(t *testing.T) {
		dir := t.TempDir()
		_, _, err := load(dir, testIPs, nil, t0)
		require.NoError(t, err)
		require.NoError(t, os.Remove(filepath.Join(dir, dirName, caKeyFile)))
		_, _, err = load(dir, testIPs, nil, t0)
		assert.ErrorContains(t, err, caKeyFile)
	})
	t.Run("只有私钥（上次生成中途退出）：重新生成", func(t *testing.T) {
		dir := t.TempDir()
		_, ca1, err := load(dir, testIPs, nil, t0)
		require.NoError(t, err)
		require.NoError(t, os.Remove(filepath.Join(dir, dirName, caCertFile)))
		cfg, ca2, err := load(dir, testIPs, nil, t0)
		require.NoError(t, err)
		assert.NotEqual(t, ca1, ca2)
		assert.NoError(t, verifyAt(t, ca2, leafOf(t, cfg), "127.0.0.1", t0), "服务端证书随新 CA 重签")
	})
	t.Run("服务端私钥损坏：重签", func(t *testing.T) {
		dir := t.TempDir()
		cfg1, _, err := load(dir, testIPs, nil, t0)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, dirName, keyFile), []byte("garbage"), 0o600))
		cfg2, _, err := load(dir, testIPs, nil, t0)
		require.NoError(t, err)
		assert.NotEqual(t, leafOf(t, cfg1).SerialNumber, leafOf(t, cfg2).SerialNumber)
	})
	t.Run("没有任何 SAN：报错", func(t *testing.T) {
		_, _, err := load(t.TempDir(), nil, nil, t0)
		assert.Error(t, err)
	})
}

// 真实握手：只信任这张 CA 的 TLS 1.3 客户端能连上，TLS 1.2 客户端被拒。
func TestHandshake(t *testing.T) {
	cfg, caPEM, err := Load(t.TempDir(), []net.IP{net.ParseIP("127.0.0.1")}, nil)
	require.NoError(t, err)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = c.(*tls.Conn).Handshake()
				_ = c.Close()
			}()
		}
	}()
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(caPEM))

	c, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{RootCAs: pool})
	require.NoError(t, err)
	assert.Equal(t, uint16(tls.VersionTLS13), c.ConnectionState().Version)
	_ = c.Close()

	_, err = tls.Dial("tcp", ln.Addr().String(), &tls.Config{RootCAs: pool, MaxVersion: tls.VersionTLS12})
	assert.Error(t, err, "TLS 1.2 被拒")

	_, err = tls.Dial("tcp", ln.Addr().String(), &tls.Config{RootCAs: x509.NewCertPool()})
	assert.Error(t, err, "不信任该 CA 的客户端握手失败")
}
