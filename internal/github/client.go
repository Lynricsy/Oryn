package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/version"
)

// AAD 名称（secrets.Box 的密文绑定名）。
const (
	aadPrivateKey    = "github/app/private-key"
	aadClientSecret  = "github/app/client-secret"
	aadWebhookSecret = "github/app/webhook-secret"
	aadOAuthSecret   = "github/oauth/client-secret"
)

func aadAccess(slot string) string { return "github/identity/" + slot + "/access" }

// apiError 是上游返回的非 2xx 响应。
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return fmt.Sprintf("github: HTTP %d: %s", e.Status, e.Message) }

// ghError 的类别（R2 / R4）。
const (
	kindReauth        = "reauth"         // 401：令牌失效，槽位需要重新授权
	kindOrgRestricted = "org_restricted" // 403：组织开启了 OAuth App 访问限制
	kindSSORequired   = "sso_required"   // 403 且带 X-GitHub-SSO：令牌未做 SAML SSO 授权
	kindNotFound      = "not_found"      // 404
	kindUpstream      = "upstream"       // 其余非 2xx
)

// ghError 是分类后的上游非 2xx 响应；Unwrap 出 *apiError，原有按状态码判断的调用方不受影响。
type ghError struct {
	Kind   string
	Status int
	Msg    string
	SSOURL string // sso_required 时为 X-GitHub-SSO 头里的授权链接
}

func (e *ghError) Error() string {
	return fmt.Sprintf("github: %s: HTTP %d: %s", e.Kind, e.Status, e.Msg)
}

func (e *ghError) Unwrap() error { return &apiError{Status: e.Status, Message: e.Msg} }

// classify 按状态码、message 与 X-GitHub-SSO 头给上游错误分类。
func classify(status int, h http.Header, msg string) *ghError {
	e := &ghError{Kind: kindUpstream, Status: status, Msg: msg}
	switch {
	case status == http.StatusUnauthorized:
		e.Kind = kindReauth
	case status == http.StatusForbidden && h.Get("X-GitHub-SSO") != "":
		e.Kind = kindSSORequired
		// 形如 `required; url=https://github.com/orgs/<org>/sso?authorization_request=…`
		for part := range strings.SplitSeq(h.Get("X-GitHub-SSO"), ";") {
			if u, ok := strings.CutPrefix(strings.TrimSpace(part), "url="); ok {
				e.SSOURL = u
			}
		}
	case status == http.StatusForbidden && strings.Contains(msg, "OAuth App access restrictions"):
		e.Kind = kindOrgRestricted
	case status == http.StatusNotFound:
		e.Kind = kindNotFound
	}
	return e
}

// errKind 返回 err 链上 *ghError 的类别；不是上游响应时返回空串。
func errKind(err error) string {
	var ge *ghError
	if errors.As(err, &ge) {
		return ge.Kind
	}
	return ""
}

// retryable 报告错误是否值得稍后重试：网络错误、超时、上游 5xx 与 429。
func retryable(err error) bool {
	if err == nil {
		return false
	}
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Status >= 500 || ae.Status == http.StatusTooManyRequests
	}
	var ne net.Error
	var ue *url.Error
	return errors.As(err, &ne) || errors.As(err, &ue) || errors.Is(err, context.DeadlineExceeded)
}

// IsUpstream 报告 err 是否来自 GitHub 上游（非 2xx 响应或网络故障）；REST 层据此回 502 github_upstream。
func IsUpstream(err error) bool {
	var ae *apiError
	var ne net.Error
	var ue *url.Error
	return errors.As(err, &ae) || errors.As(err, &ne) || errors.As(err, &ue)
}

func isStatus(err error, status int) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Status == status
}

// call 发一次 GitHub 请求：path 以 `/` 开头时拼到 APIURL 后，否则视为完整地址；auth 原样作为 Authorization 头；
// in 非 nil 则 JSON 编码；2xx 且 out 非 nil 则解码。非 2xx 返回 *ghError。
func (s *Service) call(ctx context.Context, method, path string, auth string, in, out any) error {
	_, err := s.request(ctx, method, path, auth, in, out)
	return err
}

// callAs 以身份槽位的令牌调用 call；上游回 401 时把槽位标为 reauth（R2）。
func (s *Service) callAs(ctx context.Context, slot, method, path string, in, out any) error {
	tok, err := s.tokenFor(ctx, slot)
	if err != nil {
		return err
	}
	err = s.call(ctx, method, path, "Bearer "+tok, in, out)
	if errKind(err) == kindReauth {
		if merr := s.markReauth(ctx, slot); merr != nil {
			return errors.Join(err, merr)
		}
	}
	return err
}

// request 同 call，另返回响应头（翻页用）。
func (s *Service) request(ctx context.Context, method, path string, auth string, in, out any) (http.Header, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	target := path
	if strings.HasPrefix(path, "/") {
		target = s.cfg.APIURL + path
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return s.send(req, auth, out)
}

// send 补齐公共头后发出请求并解码响应。
func (s *Service) send(req *http.Request, auth string, out any) (http.Header, error) {
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "oryn/"+version.Version)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var m struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &m)
		if m.Message == "" {
			m.Message = http.StatusText(resp.StatusCode)
		}
		return resp.Header, classify(resp.StatusCode, resp.Header, m.Message)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.Header, fmt.Errorf("github: 解析响应: %w", err)
		}
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	}
	return resp.Header, nil
}

// oauthTokenResponse 是 `<WebURL>/login/oauth/access_token` 的响应（失败时 GitHub 也回 200，带 error 字段）。
// OAuth App 的令牌不过期、没有刷新令牌。
type oauthTokenResponse struct {
	AccessToken      string `json:"access_token"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// oauthToken 以表单参数调用令牌端点（授权码兑换）。
func (s *Service) oauthToken(ctx context.Context, form url.Values) (oauthTokenResponse, error) {
	var out oauthTokenResponse
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.WebURL+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return out, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, err = s.send(req, "", &out)
	return out, err
}

var nextLinkRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// nextLink 取 Link 头里 rel="next" 的地址；没有返回空串。
func nextLink(h http.Header) string {
	for _, v := range h.Values("Link") {
		if m := nextLinkRe.FindStringSubmatch(v); m != nil {
			return m[1]
		}
	}
	return ""
}

// paged 按 Link: rel="next" 翻页 GET，最多 maxPages 页；每页交给 page 解码。
func (s *Service) paged(ctx context.Context, path, auth string, maxPages int, page func(raw json.RawMessage) error) error {
	for i := 0; i < maxPages && path != ""; i++ {
		var raw json.RawMessage
		h, err := s.request(ctx, http.MethodGet, path, auth, nil, &raw)
		if err != nil {
			return err
		}
		if err := page(raw); err != nil {
			return err
		}
		path = nextLink(h)
	}
	return nil
}

// app 读 App 行；没有 App → github_not_connected。
func (s *Service) app(ctx context.Context) (db.GithubApp, error) {
	app, err := s.st.Q.GetGithubApp(ctx)
	if err != nil {
		if isNoRows(err) {
			return app, errf("github_not_connected", "GitHub App not created")
		}
		return app, err
	}
	return app, nil
}

// appJWT 签发 App JWT（RS256，iat = now − 60s，exp = now + 9m）。
func (s *Service) appJWT(app db.GithubApp) (string, error) {
	keyPEM, err := s.cfg.Box.Open(aadPrivateKey, app.PrivateKeyEnc)
	if err != nil {
		return "", err
	}
	key, err := parseRSAKey(keyPEM)
	if err != nil {
		return "", err
	}
	now := s.now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": strconv.FormatInt(app.AppID, 10),
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// parseRSAKey 解析 PEM 私钥（PKCS#1 或 PKCS#8）。
func parseRSAKey(b []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("github: App 私钥不是 PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github: 解析 App 私钥: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("github: App 私钥不是 RSA")
	}
	return rk, nil
}

// appAuth 返回 App JWT 的 Authorization 头。
func (s *Service) appAuth(ctx context.Context) (string, error) {
	app, err := s.app(ctx)
	if err != nil {
		return "", err
	}
	jwt, err := s.appJWT(app)
	if err != nil {
		return "", err
	}
	return "Bearer " + jwt, nil
}

// installToken 取安装令牌，内存缓存 installTokenTTL。
func (s *Service) installToken(ctx context.Context, installationID int64) (string, error) {
	s.mu.Lock()
	c, ok := s.instTokens[installationID]
	s.mu.Unlock()
	if ok && s.now().Before(c.exp) {
		return c.token, nil
	}
	auth, err := s.appAuth(ctx)
	if err != nil {
		return "", err
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := s.call(ctx, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", installationID), auth, nil, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", errors.New("github: 安装令牌响应缺少 token")
	}
	s.mu.Lock()
	s.instTokens[installationID] = cachedToken{token: out.Token, exp: s.now().Add(installTokenTTL)}
	s.mu.Unlock()
	return out.Token, nil
}

// graphql 发一条 GraphQL 查询；响应带 errors 时返回错误。
func (s *Service) graphql(ctx context.Context, auth, query string, vars map[string]any, out any) error {
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := s.call(ctx, http.MethodPost, "/graphql", auth, map[string]any{"query": query, "variables": vars}, &resp); err != nil {
		return err
	}
	if len(resp.Errors) > 0 {
		msgs := make([]string, len(resp.Errors))
		for i, e := range resp.Errors {
			msgs[i] = e.Message
		}
		return fmt.Errorf("github: graphql: %s", strings.Join(msgs, "; "))
	}
	if out != nil && len(resp.Data) > 0 {
		return json.Unmarshal(resp.Data, out)
	}
	return nil
}
