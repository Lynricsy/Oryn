// Package fcmtest 提供假的 Google OAuth 令牌端点 + FCM HTTP v1 端点与服务账号生成器，供 fcm 客户端及其使用方的测试共用。
package fcmtest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// ProjectID 与 ClientEmail 是 ServiceAccount 生成的服务账号字段。
const (
	ProjectID   = "oryn-test"
	ClientEmail = "fcm-sender@oryn-test.iam.gserviceaccount.com"
	KeyID       = "kid-1"
)

// Err 是对某个注册令牌预设的错误响应。
type Err struct {
	Status int
	Body   string
}

// Send 是一次 messages:send 请求。
type Send struct {
	Path, Auth string
	// Raw 是请求体原文：用来断言其中没有任何明文。
	Raw  string
	Body map[string]any
}

// Google 是假的令牌端点（/token）+ FCM 端点（/v1/projects/…）：校验 JWT 断言签名、记录请求、按令牌返回预设错误。
// 签发的 access token 依次为 ya29.test-1、ya29.test-2 …
type Google struct {
	Server *httptest.Server

	t   *testing.T
	pub *rsa.PublicKey

	mu     sync.Mutex
	tokens []map[string]any // 已验证的 JWT 声明
	sends  []Send
	issued int
	// ErrFor 是注册令牌 → 预设错误；RejectAccess 中的 access token 一律 401。修改前持锁：用 Set* 方法。
	errFor       map[string]Err
	rejectAccess map[string]bool
}

// NewGoogle 启动假端点，pub 是签发断言的服务账号公钥（不匹配时令牌端点回 400 invalid_grant）。
func NewGoogle(t *testing.T, pub *rsa.PublicKey) *Google {
	t.Helper()
	g := &Google{t: t, pub: pub, errFor: map[string]Err{}, rejectAccess: map[string]bool{}}
	g.Server = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.Server.Close)
	return g
}

// TokenURL 是假令牌端点地址。
func (g *Google) TokenURL() string { return g.Server.URL + "/token" }

// SetErr 让发给 token 的消息返回 e。
func (g *Google) SetErr(token string, e Err) {
	g.mu.Lock()
	g.errFor[token] = e
	g.mu.Unlock()
}

// RejectAccess 让带此 access token 的发送请求一律 401。
func (g *Google) RejectAccess(access string) {
	g.mu.Lock()
	g.rejectAccess[access] = true
	g.mu.Unlock()
}

// Snapshot 返回已验证的令牌请求声明与发送请求（副本）。
func (g *Google) Snapshot() (tokens []map[string]any, sends []Send) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]map[string]any(nil), g.tokens...), append([]Send(nil), g.sends...)
}

func (g *Google) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case r.URL.Path == "/token":
		if err := r.ParseForm(); err != nil {
			g.t.Errorf("fcmtest: parse token form: %v", err)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			g.t.Errorf("fcmtest: token Content-Type = %q", ct)
		}
		if gt := r.PostForm.Get("grant_type"); gt != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			g.t.Errorf("fcmtest: grant_type = %q", gt)
		}
		claims, err := g.verify(r.PostForm.Get("assertion"))
		if err != nil {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		g.tokens = append(g.tokens, claims)
		g.issued++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "ya29.test-" + strconv.Itoa(g.issued), "expires_in": 3599, "token_type": "Bearer"})
	case strings.HasPrefix(r.URL.Path, "/v1/projects/"):
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			g.t.Errorf("fcmtest: send body is not JSON: %v", err)
		}
		auth := r.Header.Get("Authorization")
		g.sends = append(g.sends, Send{r.URL.Path, auth, string(b), body})
		if g.rejectAccess[strings.TrimPrefix(auth, "Bearer ")] {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"code":401,"status":"UNAUTHENTICATED"}}`)
			return
		}
		msg, _ := body["message"].(map[string]any)
		tok, _ := msg["token"].(string)
		if e, ok := g.errFor[tok]; ok {
			w.WriteHeader(e.Status)
			_, _ = io.WriteString(w, e.Body)
			return
		}
		_, _ = io.WriteString(w, `{"name":"projects/`+ProjectID+`/messages/0:1"}`)
	default:
		http.NotFound(w, r)
	}
}

// verify 校验 RS256 签名与头部并返回声明。
func (g *Google) verify(jwt string) (map[string]any, error) {
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
	if h["alg"] != "RS256" || h["typ"] != "JWT" || h["kid"] != KeyID || len(h) != 3 {
		g.t.Errorf("fcmtest: unexpected JWT header %v", h)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(g.pub, crypto.SHA256, sum[:], sig); err != nil {
		return nil, err
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

// ServiceAccount 生成一份服务账号 JSON（PKCS#8 私钥，与 Google 下发的格式一致）与其公钥。
func ServiceAccount(t *testing.T) (string, *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]string{
		"type": "service_account", "project_id": ProjectID, "private_key_id": KeyID,
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": ClientEmail, "token_uri": "https://oauth2.googleapis.com/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b), &key.PublicKey
}
