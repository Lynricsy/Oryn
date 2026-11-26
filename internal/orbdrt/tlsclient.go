package orbdrt

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// orbHTTPClient 构造 orbd 访问控制面（ORYN_URL）所用的 HTTP client（契约 TLS 客户端）：
//   - caPEM 只含空白（未给，或快照镜像里被清空成 ""）且 via 为空 → nil：调用方用 http.DefaultClient，HTTPS 走系统根（反代场景）。
//   - caPEM 非空时只信任 caPEM 里的证书：自建 x509.CertPool，不合并系统根。PEM 里只能有 CERTIFICATE 块且至少一张，
//     任何无法解析的内容都是错误（orbd 因此启动失败，而不是悄悄退回系统根）。
//   - via 非空（ORYN_LINK_VIA，M30 沙箱）时底层连接一律经 via 拨出（忽略 URL 里的地址、不走代理）；TLS 与
//     WebSocket 仍在其上端到端进行。via 无法解析是错误。
func orbHTTPClient(caPEM, via string) (*http.Client, error) {
	if via != "" {
		if _, _, err := parseLinkVia(via); err != nil {
			return nil, err
		}
	}
	var pool *x509.CertPool
	if strings.TrimSpace(caPEM) != "" {
		var err error
		if pool, err = parseCAPEM(caPEM); err != nil {
			return nil, err
		}
	}
	if pool == nil && via == "" {
		return nil, nil
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if pool != nil {
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	if via != "" {
		tr.Proxy = nil
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) { return dialVia(ctx, via) }
	}
	return &http.Client{Transport: tr}, nil
}

// parseCAPEM 解析只含 CERTIFICATE 块的 PEM（至少一张）。
func parseCAPEM(caPEM string) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	rest := []byte(caPEM)
	n := 0
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("unexpected PEM block %q", block.Type)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse certificate: %w", err)
		}
		pool.AddCert(cert)
		n++
	}
	if len(bytes.TrimSpace(rest)) > 0 {
		return nil, errors.New("trailing data that is not a PEM certificate")
	}
	if n == 0 {
		return nil, errors.New("no certificate found")
	}
	return pool, nil
}
