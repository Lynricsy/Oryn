package orbdrt

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// orbHTTPClient 构造 orbd 访问控制面（ORYN_URL）所用的 HTTP client（契约 TLS 客户端）：
//   - caPEM 只含空白（未给，或快照镜像里被清空成 ""）→ nil：调用方用 http.DefaultClient，HTTPS 走系统根（反代场景）。
//   - 否则只信任 caPEM 里的证书：自建 x509.CertPool，不合并系统根。PEM 里只能有 CERTIFICATE 块且至少一张，
//     任何无法解析的内容都是错误（orbd 因此启动失败，而不是悄悄退回系统根）。
func orbHTTPClient(caPEM string) (*http.Client, error) {
	if strings.TrimSpace(caPEM) == "" {
		return nil, nil
	}
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
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &http.Client{Transport: tr}, nil
}
