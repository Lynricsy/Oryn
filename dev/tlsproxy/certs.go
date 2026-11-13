package main

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
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// 证书文件名（都在 -dir 目录下）。CA 一旦生成就不再改动：用户已把 ca.crt 装进系统信任库，
// 重新生成会让已信任的锚点失效。叶子证书过期或与 CA 不匹配时只重签叶子。
const (
	caCertFile   = "ca.crt"
	caKeyFile    = "ca.key"
	leafCertFile = "localhost.crt"
	leafKeyFile  = "localhost.key"
)

const (
	caValidity   = 10 * 365 * 24 * time.Hour
	leafValidity = 397 * 24 * time.Hour
	// renewBefore 内即将过期的叶子证书提前重签。
	renewBefore = 7 * 24 * time.Hour
)

// leafDNSNames / leafIPs 是叶子证书的 SAN。
var (
	leafDNSNames = []string{"localhost"}
	leafIPs      = []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
)

// ensureCerts 在 dir 中准备（已有则复用）本机 CA 与 localhost 叶子证书，返回可直接用于 TLS 监听的证书链。
func ensureCerts(dir string, now time.Time) (tls.Certificate, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, err
	}
	caCert, caKey, err := loadOrCreateCA(dir, now)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("CA：%w", err)
	}
	if c, err := loadLeaf(dir, caCert, now); err == nil {
		return c, nil
	}
	return createLeaf(dir, caCert, caKey, now)
}

func loadOrCreateCA(dir string, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPath, keyPath := filepath.Join(dir, caCertFile), filepath.Join(dir, caKeyFile)
	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	switch {
	case certErr == nil && keyErr == nil:
		cert, err := readCert(certPath)
		if err != nil {
			return nil, nil, err
		}
		key, err := readKey(keyPath)
		if err != nil {
			return nil, nil, err
		}
		if !cert.IsCA {
			return nil, nil, fmt.Errorf("%s 不是 CA 证书", certPath)
		}
		return cert, key, nil
	case errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist):
	default:
		// 只剩一半时不擅自覆盖：另一半可能已装进系统信任库
		return nil, nil, fmt.Errorf("%s 与 %s 必须同时存在或同时不存在", certPath, keyPath)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Oryn dev local CA", Organization: []string{"Oryn dev"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	if err := writeKey(keyPath, key); err != nil {
		return nil, nil, err
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return cert, key, err
}

// loadLeaf 读取已有叶子证书；不存在、不由该 CA 签发、SAN 不全或临近过期都返回错误（调用方随后重签）。
func loadLeaf(dir string, ca *x509.Certificate, now time.Time) (tls.Certificate, error) {
	c, err := tls.LoadX509KeyPair(filepath.Join(dir, leafCertFile), filepath.Join(dir, leafKeyFile))
	if err != nil {
		return c, err
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return c, err
	}
	if now.Add(renewBefore).After(leaf.NotAfter) {
		return c, errors.New("叶子证书即将过期")
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	for _, name := range append(append([]string{}, leafDNSNames...), "127.0.0.1", "::1") {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: name, CurrentTime: now}); err != nil {
			return c, err
		}
	}
	c.Leaf = leaf
	c.Certificate = append(c.Certificate[:1], ca.Raw)
	return c, nil
}

func createLeaf(dir string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, now time.Time) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := randSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost", Organization: []string{"Oryn dev"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     leafDNSNames,
		IPAddresses:  leafIPs,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := writeKey(filepath.Join(dir, leafKeyFile), key); err != nil {
		return tls.Certificate{}, err
	}
	if err := writePEM(filepath.Join(dir, leafCertFile), "CERTIFICATE", der, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der, ca.Raw}, PrivateKey: key, Leaf: leaf}, nil
}

func randSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

func readCert(path string) (*x509.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s 不是 PEM 证书", path)
	}
	return x509.ParseCertificate(blk.Bytes)
}

func readKey(path string) (*ecdsa.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, fmt.Errorf("%s 不是 PEM 私钥", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s：%w", path, err)
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s 不是 ECDSA 私钥", path)
	}
	return ec, nil
}

func writeKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	return writePEM(path, "PRIVATE KEY", der, 0o600)
}

// writePEM 先写同目录临时文件再改名，避免留下写了一半的证书。
func writePEM(path, typ string, der []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := pem.Encode(tmp, &pem.Block{Type: typ, Bytes: der}); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
