// Package apnstest 提供假的 APNs 端点（HTTP/2 TLS）与 .p8 key 生成器，供 apns 客户端及其使用方的测试共用。
package apnstest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// KeyID 与 TeamID 是测试用的 provider token 身份。
const (
	KeyID  = "ABC123DEFG"
	TeamID = "XDPWT9ABRD"
)

// Request 是一次推送请求。
type Request struct {
	// Env 是根地址的环境段（URL(env) 里的 env）。
	Env, Token string
	// Proto 是 HTTP 主版本号（APNs 只接受 2）。
	Proto  int
	Header http.Header
	// Raw 是请求体原文：用来断言其中没有任何明文。
	Raw  string
	Body map[string]any
	// JWT 是 Authorization 里的 provider token 原文，Claims 是其已验证的声明。
	JWT    string
	Claims map[string]any
}

// Reply 是对某个设备令牌预设的错误响应。
type Reply struct {
	Status int
	Reason string
}

// Server 是假的 APNs：校验 ES256 provider token、记录请求、按令牌返回预设错误。
// 根地址为 URL(env)：路径 /<env>/3/device/<token>，测试据此区分发往哪个环境。
type Server struct {
	*httptest.Server

	t   *testing.T
	pub *ecdsa.PublicKey

	mu       sync.Mutex
	reqs     []Request
	errFor   map[string]Reply
	rejectN  int
	rejectAs string
}

// New 启动 HTTP/2 TLS 的假 APNs；pub 是签发 provider token 的 key 的公钥。
func New(t *testing.T, pub *ecdsa.PublicKey) *Server {
	t.Helper()
	s := &Server{t: t, pub: pub, errFor: map[string]Reply{}}
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(s.serve))
	s.EnableHTTP2 = true
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

// URL 是 env 环境的根地址。
func (s *Server) URL(env string) string { return s.Server.URL + "/" + env }

// SetErr 让发给 token 的通知返回 r。
func (s *Server) SetErr(token string, r Reply) {
	s.mu.Lock()
	s.errFor[token] = r
	s.mu.Unlock()
}

// RejectProviderToken 让接下来 n 个请求以 403 reason（ExpiredProviderToken / InvalidProviderToken）拒绝。
func (s *Server) RejectProviderToken(n int, reason string) {
	s.mu.Lock()
	s.rejectN, s.rejectAs = n, reason
	s.mu.Unlock()
}

// Requests 返回已收到的请求（副本）。
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	env, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	token, ok := strings.CutPrefix(rest, "3/device/")
	if r.Method != http.MethodPost || !ok {
		http.NotFound(w, r)
		return
	}
	b, _ := io.ReadAll(r.Body)
	req := Request{Env: env, Token: token, Proto: r.ProtoMajor, Header: r.Header.Clone(), Raw: string(b)}
	if err := json.Unmarshal(b, &req.Body); err != nil {
		s.t.Errorf("apnstest: payload is not JSON: %v", err)
	}
	req.JWT, _ = strings.CutPrefix(r.Header.Get("Authorization"), "bearer ")
	claims, err := s.verify(req.JWT)
	if err != nil {
		s.t.Errorf("apnstest: provider token does not verify: %v", err)
	}
	req.Claims = claims
	s.reqs = append(s.reqs, req)
	w.Header().Set("apns-id", "00000000-0000-0000-0000-000000000000")
	if s.rejectN > 0 {
		s.rejectN--
		reply(w, http.StatusForbidden, s.rejectAs)
		return
	}
	if e, ok := s.errFor[token]; ok {
		reply(w, e.Status, e.Reason)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func reply(w http.ResponseWriter, status int, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"reason": reason})
}

// verify 校验 ES256 签名（r ‖ s 各 32 字节）与头部 {alg: ES256, kid: KeyID} 并返回声明。
func (s *Server) verify(jwt string) (map[string]any, error) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a JWT")
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	var h map[string]string
	if err := json.Unmarshal(hb, &h); err != nil {
		return nil, err
	}
	if h["alg"] != "ES256" || h["kid"] != KeyID || len(h) != 2 {
		return nil, errors.New("unexpected JWT header")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return nil, errors.New("signature is not 64 bytes")
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(s.pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return nil, errors.New("bad signature")
	}
	cb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var c map[string]any
	if err := json.Unmarshal(cb, &c); err != nil {
		return nil, err
	}
	return c, nil
}

// Key 生成一把 P-256 key，返回 AuthKey_<KeyID>.p8 的内容（PKCS#8 PEM，与 Apple 下发的格式一致）与私钥。
func Key(t *testing.T) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), k
}
