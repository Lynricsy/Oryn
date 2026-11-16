package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/Lynricsy/Oryn/internal/version"
)

// AAD 名称（secrets.Box 的密文绑定名）。
const aadOAuthSecret = "github/oauth/client-secret"

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

// 身份槽位与令牌（R2）：带槽位令牌的请求一律以槽位名（slotUser / slotAgent）发起，令牌由 do 现取（tokenFor）并注入，
// 上游回 401 时也由 do 把该槽位标为 reauth；调用方不持有、不传递令牌，也就不必各自记得处理 401。slot 为空表示请求不带
// 任何槽位的令牌（授权码兑换、校验刚兑换的新令牌、匿名请求），Authorization 由调用方自行设置或不设。

// callAs 以槽位 slot 的令牌发一次 GitHub 请求（同 request，不要响应头）。
func (s *Service) callAs(ctx context.Context, slot, method, path string, in, out any) error {
	_, err := s.request(ctx, slot, method, path, in, out)
	return err
}

// request 以槽位 slot 的令牌发一次 GitHub 请求，另返回响应头（翻页、额度用）：path 以 `/` 开头时拼到 APIURL 后，
// 否则视为完整地址；in 非 nil 则 JSON 编码；2xx 且 out 非 nil 则解码。非 2xx 返回 *ghError。
func (s *Service) request(ctx context.Context, slot, method, path string, in, out any) (http.Header, error) {
	req, err := s.newRequest(ctx, method, path, in)
	if err != nil {
		return nil, err
	}
	return s.send(slot, req, out)
}

// newRequest 构造一个 GitHub REST 请求（路径规则同 request），设好 Accept 与 Content-Type；公共头与令牌由 do 补齐。
func (s *Service) newRequest(ctx context.Context, method, path string, in any) (*http.Request, error) {
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
	return req, nil
}

// send 经 do 发出请求并把 2xx 响应解码进 out（out 为 nil 时丢弃正文）；非 2xx 时返回响应头与 *ghError。
func (s *Service) send(slot string, req *http.Request, out any) (http.Header, error) {
	resp, err := s.do(slot, req)
	if err != nil {
		if resp != nil {
			return resp.Header, err
		}
		return nil, err
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.Header, fmt.Errorf("github: 解析响应: %w", err)
		}
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	}
	return resp.Header, nil
}

// do 是本包对 GitHub 的唯一发送点：slot 非空时注入该槽位的令牌（槽位不可用时不发请求，返回 tokenFor 的错误），
// 补齐公共头后发出并判错；上游回 401 时经 tokenRejected 把仍持有该令牌的 slot 标为 reauth（R2），之后 tokenFor
// 拒绝该槽位，请求不再发往上游。
// 边界：只有实际收到 401 的那一跳仍带着注入的令牌时才置 reauth。跟随重定向（如 job 日志 302 到另一主机的预签名地址）
// 时 net/http 会去掉 Authorization，那里回的 401 说明不了 OAuth 令牌失效：照常返回 *ghError，不改动槽位。
// 2xx 时返回响应，调用方负责关闭 Body（流式下载可直接读）；非 2xx 时 Body 已读尽关闭，返回的响应只用于读头，错误为 *ghError。
func (s *Service) do(slot string, req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	tok := ""
	if slot != "" {
		var err error
		if tok, err = s.tokenFor(ctx, slot); err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "oryn/"+version.Version)
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return resp, nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	var m struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &m)
	if m.Message == "" {
		m.Message = http.StatusText(resp.StatusCode)
	}
	ge := classify(resp.StatusCode, resp.Header, m.Message)
	if ge.Kind == kindReauth && tok != "" && resp.Request != nil && resp.Request.Header.Get("Authorization") == "Bearer "+tok {
		if merr := s.tokenRejected(ctx, tok, slot); merr != nil {
			return resp, errors.Join(ge, merr)
		}
	}
	return resp, ge
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
	_, err = s.send("", req, &out)
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

// paged 以槽位 slot 的令牌按 Link: rel="next" 翻页 GET，最多 maxPages 页；每页交给 page 解码。
func (s *Service) paged(ctx context.Context, slot, path string, maxPages int, page func(raw json.RawMessage) error) error {
	for i := 0; i < maxPages && path != ""; i++ {
		var raw json.RawMessage
		h, err := s.request(ctx, slot, http.MethodGet, path, nil, &raw)
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

// graphql 以槽位 slot 的令牌发一条 GraphQL 查询；响应带 errors 时返回错误。
func (s *Service) graphql(ctx context.Context, slot, query string, vars map[string]any, out any) error {
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := s.callAs(ctx, slot, http.MethodPost, "/graphql", map[string]any{"query": query, "variables": vars}, &resp); err != nil {
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
