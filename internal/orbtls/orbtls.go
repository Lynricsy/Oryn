// Package orbtls 管理 orb link 专用 TLS 监听的自签 CA 与服务端证书（M11a 审查修复 S-P1-3）。
//
// 文件放在 <data_dir>/orb-tls/：
//   - ca.pem / ca-key.pem：ECDSA P-256 自签 CA，有效期 10 年，首次启动生成后不再更换
//     （CA 的 PEM 经 spawn 参数下发给 orb，orbd 只信任这一张，换掉就要重建全部 orb）；
//   - server.pem / server-key.pem：由 CA 签发的服务端证书，有效期 1 年，SAN = listen 的 IP + orb_tls.hosts；
//     启动时若 SAN 变化、距到期不足 30 天、或与 CA / 私钥对不上，就用同一 CA 重签。
//
// 私钥文件权限固定为 0600（加载时发现更宽也会收紧），目录为 0700。
package orbtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	dirName    = "orb-tls"
	caCertFile = "ca.pem"
	caKeyFile  = "ca-key.pem"
	certFile   = "server.pem"
	keyFile    = "server-key.pem"

	caValidity   = 10 * 365 * 24 * time.Hour
	certValidity = 365 * 24 * time.Hour
	// renewBefore 是服务端证书提前重签的窗口。
	renewBefore = 30 * 24 * time.Hour
	// backdate 让 NotBefore 早于签发时刻，容忍 orb 与宿主之间的时钟偏差。
	backdate = time.Hour
)

// Load 加载（必要时生成 / 重签）orb link 的 CA 与服务端证书，返回 TLS 1.3 起步的服务端配置与 CA 的 PEM。
// ips 是 listen 地址的 IP，hosts 是额外的 SAN（IP 或主机名）；两者合起来不能为空。
func Load(dataDir string, ips []net.IP, hosts []string) (*tls.Config, []byte, error) {
	return load(dataDir, ips, hosts, time.Now())
}

// authority 是已加载的 CA。
type authority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func load(dataDir string, ips []net.IP, hosts []string, now time.Time) (*tls.Config, []byte, error) {
	wantIPs, wantDNS := sans(ips, hosts)
	if len(wantIPs) == 0 && len(wantDNS) == 0 {
		return nil, nil, errors.New("orbtls: 证书没有任何 SAN（listen 与 hosts 均为空）")
	}
	dir := filepath.Join(dataDir, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("orbtls: 创建目录: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("orbtls: 收紧目录权限: %w", err)
	}
	ca, err := loadOrCreateCA(dir, now)
	if err != nil {
		return nil, nil, err
	}
	leaf, key, err := loadServer(dir)
	if err != nil {
		return nil, nil, err
	}
	if reason := reissueReason(leaf, key, ca, wantIPs, wantDNS, now); reason != "" {
		if leaf, key, err = issueServer(dir, ca, wantIPs, wantDNS, now); err != nil {
			return nil, nil, err
		}
		slog.Info("orb link 服务端证书已签发", "reason", reason, "notAfter", leaf.NotAfter.UTC(),
			"ips", wantIPs, "dns", wantDNS)
	}
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{{Certificate: [][]byte{leaf.Raw}, PrivateKey: key, Leaf: leaf}},
		// link 是 WebSocket over HTTP/1.1；不协商 h2
		NextProtos: []string{"http/1.1"},
	}
	return cfg, ca.pem, nil
}

// sans 把 listen IP 与 hosts 归一成有序、去重的 IP 字符串与小写主机名。
func sans(ips []net.IP, hosts []string) (ipStrs, dns []string) {
	add := func(list []string, v string) []string {
		if v == "" || slices.Contains(list, v) {
			return list
		}
		return append(list, v)
	}
	for _, ip := range ips {
		if ip != nil {
			ipStrs = add(ipStrs, ip.String())
		}
	}
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if ip := net.ParseIP(h); ip != nil {
			ipStrs = add(ipStrs, ip.String())
			continue
		}
		dns = add(dns, strings.ToLower(strings.TrimSuffix(h, ".")))
	}
	slices.Sort(ipStrs)
	slices.Sort(dns)
	return ipStrs, dns
}

// loadOrCreateCA 读取 CA；两份文件都不存在时生成。只有私钥没有证书说明上次生成中途退出（证书从未下发过），重新生成；
// 有证书没有私钥则报错，不擅自换 CA（已下发的 orb 都钉着它）。
func loadOrCreateCA(dir string, now time.Time) (*authority, error) {
	certPath, keyPath := filepath.Join(dir, caCertFile), filepath.Join(dir, caKeyFile)
	certPEM, certErr := os.ReadFile(certPath)
	if certErr != nil && !errors.Is(certErr, fs.ErrNotExist) {
		return nil, fmt.Errorf("orbtls: 读取 CA 证书: %w", certErr)
	}
	if errors.Is(certErr, fs.ErrNotExist) {
		return createCA(certPath, keyPath, now)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("orbtls: %s 存在但缺少 %s；CA 已下发给 orb，不能自动更换，请恢复私钥或删掉整个 %s 后重建全部 orb", caCertFile, caKeyFile, dir)
	}
	if err != nil {
		return nil, fmt.Errorf("orbtls: 读取 CA 私钥: %w", err)
	}
	if err := tightenKey(keyPath); err != nil {
		return nil, err
	}
	cert, err := parseCert(certPEM)
	if err != nil {
		return nil, fmt.Errorf("orbtls: 解析 %s: %w", caCertFile, err)
	}
	key, err := parseKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("orbtls: 解析 %s: %w", caKeyFile, err)
	}
	if !cert.IsCA || !key.PublicKey.Equal(cert.PublicKey) {
		return nil, fmt.Errorf("orbtls: %s 不是 CA 或与 %s 不匹配", caCertFile, caKeyFile)
	}
	if now.Add(renewBefore).After(cert.NotAfter) {
		// CA 不自动轮换：换 CA 意味着全部 orb 要重建，交给运维决定
		slog.Error("orb link CA 即将或已经过期，需删除 orb-tls 目录后重建全部 orb", "notAfter", cert.NotAfter.UTC(), "dir", dir)
	}
	return &authority{cert: cert, key: key, pem: certPEM}, nil
}

func createCA(certPath, keyPath string, now time.Time) (*authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("orbtls: 生成 CA 私钥: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Oryn orb link CA"},
		NotBefore:             now.Add(-backdate),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("orbtls: 签发 CA: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("orbtls: 解析 CA: %w", err)
	}
	keyPEM, err := encodeKey(key)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	// 先写私钥再写证书：证书文件存在即表示 CA 完整
	if err := writeFileAtomic(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(certPath, certPEM, 0o644); err != nil {
		return nil, err
	}
	slog.Info("orb link CA 已生成", "path", certPath, "notAfter", cert.NotAfter.UTC())
	return &authority{cert: cert, key: key, pem: certPEM}, nil
}

// loadServer 读取服务端证书与私钥；缺失或解析失败都返回 nil（由调用方重签），只有读文件的 I/O 错误才报错。
func loadServer(dir string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPath, keyPath := filepath.Join(dir, certFile), filepath.Join(dir, keyFile)
	certPEM, err := os.ReadFile(certPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("orbtls: 读取服务端证书: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("orbtls: 读取服务端私钥: %w", err)
	}
	if err := tightenKey(keyPath); err != nil {
		return nil, nil, err
	}
	cert, err := parseCert(certPEM)
	if err != nil {
		slog.Warn("orb link 服务端证书无法解析，将重签", "err", err)
		return nil, nil, nil
	}
	key, err := parseKey(keyPEM)
	if err != nil {
		slog.Warn("orb link 服务端私钥无法解析，将重签", "err", err)
		return nil, nil, nil
	}
	return cert, key, nil
}

// reissueReason 返回需要重签的原因；空串 = 现有证书可用。
func reissueReason(leaf *x509.Certificate, key *ecdsa.PrivateKey, ca *authority, wantIPs, wantDNS []string, now time.Time) string {
	switch {
	case leaf == nil || key == nil:
		return "missing"
	case !key.PublicKey.Equal(leaf.PublicKey):
		return "key_mismatch"
	case leaf.CheckSignatureFrom(ca.cert) != nil:
		return "ca_mismatch"
	case now.Before(leaf.NotBefore):
		return "not_yet_valid"
	case now.Add(renewBefore).After(leaf.NotAfter):
		return "expiring"
	}
	gotIPs, gotDNS := sans(leaf.IPAddresses, leaf.DNSNames)
	if !slices.Equal(gotIPs, wantIPs) || !slices.Equal(gotDNS, wantDNS) {
		return "san_changed"
	}
	return ""
}

func issueServer(dir string, ca *authority, ipStrs, dns []string, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("orbtls: 生成服务端私钥: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, nil, err
	}
	ips := make([]net.IP, 0, len(ipStrs))
	for _, s := range ipStrs {
		ips = append(ips, net.ParseIP(s))
	}
	notAfter := now.Add(certValidity)
	if notAfter.After(ca.cert.NotAfter) {
		notAfter = ca.cert.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Oryn orb link"},
		NotBefore:    now.Add(-backdate),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  ips,
		DNSNames:     dns,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, nil, fmt.Errorf("orbtls: 签发服务端证书: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("orbtls: 解析服务端证书: %w", err)
	}
	keyPEM, err := encodeKey(key)
	if err != nil {
		return nil, nil, err
	}
	// 先私钥后证书：中途退出时两者对不上，下次启动按 key_mismatch 重签
	if err := writeFileAtomic(filepath.Join(dir, keyFile), keyPEM, 0o600); err != nil {
		return nil, nil, err
	}
	if err := writeFileAtomic(filepath.Join(dir, certFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

func newSerial() (*big.Int, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("orbtls: 生成序列号: %w", err)
	}
	return n, nil
}

func parseCert(data []byte) (*x509.Certificate, error) {
	b, _ := pem.Decode(data)
	if b == nil || b.Type != "CERTIFICATE" {
		return nil, errors.New("不是 PEM 证书")
	}
	return x509.ParseCertificate(b.Bytes)
}

func parseKey(data []byte) (*ecdsa.PrivateKey, error) {
	b, _ := pem.Decode(data)
	if b == nil || b.Type != "PRIVATE KEY" {
		return nil, errors.New("不是 PKCS#8 PEM 私钥")
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("私钥类型 %T 不是 ECDSA", k)
	}
	return ek, nil
}

func encodeKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("orbtls: 编码私钥: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// tightenKey 把私钥文件权限收紧到 0600。
func tightenKey(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("orbtls: %w", err)
	}
	if fi.Mode().Perm() == 0o600 {
		return nil
	}
	slog.Warn("orb link 私钥权限过宽，已收紧为 0600", "path", path, "mode", fi.Mode().Perm())
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("orbtls: 收紧私钥权限: %w", err)
	}
	return nil
}

// writeFileAtomic 先写同目录临时文件（权限一开始就是 perm）再 rename 覆盖。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("orbtls: %w", err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return fmt.Errorf("orbtls: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("orbtls: 写 %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("orbtls: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("orbtls: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("orbtls: %w", err)
	}
	return nil
}
