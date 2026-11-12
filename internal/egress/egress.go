// Package egress 是控制面侧的出站代理：orb 内 agent 的模型 / git 请求经 link 的 egress 流到达这里，
// 在此把占位令牌替换为真实密钥后转发到上游。真实密钥从不进入 orb。
//
// 安全约束（orb 内代码可任意构造请求，并能看到全部占位符）：
//   - 占位符只在固定的凭据位置（若干请求头与 query 参数 key）被替换，出现在其它任何位置一律拒绝；
//   - 每个模型路由只接受绑定到它的密钥 id，git 路由不接受任何占位符；
//   - git 路由只放行 smart-HTTP 端点，且可限定仓库；
//   - 响应头与响应体中出现的真实值会被换回占位符，防止上游回显泄露。
package egress

import (
	"bytes"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Route 是一条模型路由：上游前缀与允许在该路由上使用的密钥 id。
type Route struct {
	Origin    string   `json:"origin"`
	SecretIDs []string `json:"secretIds"`
}

// Policy 是一台 orb 的出站策略。
type Policy struct {
	// Routes 是模型路由键 → 路由。
	Routes map[string]Route
	// GitRepos 是 git 主机 → 允许访问的仓库路径（如 "owner/repo"）；主机不在表中时该主机全部仓库可用。
	GitRepos map[string][]string
}

// Resolver 提供策略与密钥查询。
type Resolver interface {
	// Policy 返回该 orb 当前的出站策略。
	Policy() Policy
	// SecretByID 按密钥 id 返回明文与允许的上游主机列表（空 = 不额外限制主机）。
	SecretByID(id string) (value string, hosts []string, ok bool)
	// GitToken 返回 git 主机的访问令牌（密钥 git/<host>/token）。
	GitToken(host string) (token string, ok bool)
}

var placeholderRe = regexp.MustCompile(`oryn-ph-([A-Za-z0-9_]+)`)

// credentialHeaders 是允许出现占位符的请求头（规范化名）。
var credentialHeaders = map[string]bool{
	"Authorization":  true,
	"X-Api-Key":      true,
	"X-Goog-Api-Key": true,
	"Api-Key":        true,
}

// credentialQuery 是允许出现占位符的 query 参数。
const credentialQuery = "key"

// Handler 为一台 orb 的 egress 流提供代理服务。
type Handler struct {
	OrbID string
	R     Resolver
}

// NewHandler 构造代理。
func NewHandler(orbID string, r Resolver) *Handler { return &Handler{OrbID: orbID, R: r} }

type target struct {
	url     *url.URL
	auth    string   // 非空时覆盖 Authorization（git）
	allowed []string // 允许的密钥 id
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t, ok := h.resolve(r)
	if !ok {
		http.Error(w, "oryn egress: 未知或不允许的路由", http.StatusNotFound)
		return
	}
	used, ok := h.substitute(r, t)
	if !ok {
		http.Error(w, "oryn egress: 占位符位置无效或不允许用于该路由", http.StatusForbidden)
		return
	}
	secretsUsed := make([][2]string, 0, len(used)+1) // 真实值 → 占位符，用于响应脱敏
	for id, v := range used {
		secretsUsed = append(secretsUsed, [2]string{v, "oryn-ph-" + id})
	}
	if t.auth != "" {
		tok := strings.TrimPrefix(t.auth, "Basic ")
		secretsUsed = append(secretsUsed, [2]string{tok, "oryn-redacted"})
		if raw, err := base64.StdEncoding.DecodeString(tok); err == nil {
			if _, pw, ok := strings.Cut(string(raw), ":"); ok {
				secretsUsed = append(secretsUsed, [2]string{pw, "oryn-redacted"})
			}
		}
	}
	p := &httputil.ReverseProxy{
		FlushInterval: -1, // SSE 逐块立即 flush
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = t.url.Scheme
			pr.Out.URL.Host = t.url.Host
			pr.Out.URL.Path = t.url.Path
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			pr.Out.Host = t.url.Host
			pr.Out.Header.Del("X-Forwarded-For")
			if t.auth != "" {
				pr.Out.Header.Set("Authorization", t.auth)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			if len(secretsUsed) == 0 {
				return nil
			}
			for k, vs := range resp.Header {
				for i, v := range vs {
					resp.Header[k][i] = redact(v, secretsUsed)
				}
			}
			resp.Body = newRedactReader(resp.Body, secretsUsed)
			resp.ContentLength = -1
			resp.Header.Del("Content-Length")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Warn("egress 上游错误", "orb", h.OrbID, "host", t.url.Host, "err", err)
			http.Error(w, "oryn egress: 上游不可达", http.StatusBadGateway)
		},
	}
	p.ServeHTTP(w, r)
}

// resolve 把 /llm/<key>/… 或 /git/<host>/… 映射为上游。
func (h *Handler) resolve(r *http.Request) (target, bool) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(parts) < 2 {
		return target{}, false
	}
	rest := "/"
	if len(parts) == 3 {
		rest += parts[2]
	}
	pol := h.R.Policy()
	switch parts[0] {
	case "llm":
		rt, ok := pol.Routes[parts[1]]
		if !ok {
			return target{}, false
		}
		u, err := url.Parse(rt.Origin)
		if err != nil || u.Host == "" {
			return target{}, false
		}
		u.Path = rest
		return target{url: u, allowed: rt.SecretIDs}, true
	case "git":
		host := strings.ToLower(parts[1])
		if !gitEndpointAllowed(rest, r.URL.Query().Get("service")) {
			return target{}, false
		}
		if repos, limited := pol.GitRepos[host]; limited && !repoAllowed(rest, repos) {
			return target{}, false
		}
		tok, ok := h.R.GitToken(host)
		if !ok {
			return target{}, false
		}
		auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tok))
		return target{url: &url.URL{Scheme: "https", Host: host, Path: rest}, auth: auth}, true
	}
	return target{}, false
}

// gitEndpointAllowed 只放行 git smart-HTTP 端点。
func gitEndpointAllowed(p, service string) bool {
	switch {
	case strings.HasSuffix(p, "/info/refs"):
		return service == "git-upload-pack" || service == "git-receive-pack"
	case strings.HasSuffix(p, "/git-upload-pack"), strings.HasSuffix(p, "/git-receive-pack"):
		return true
	}
	return false
}

// repoAllowed 判断 /owner/repo(.git)/… 是否属于允许的仓库。
func repoAllowed(p string, repos []string) bool {
	segs := strings.SplitN(strings.TrimPrefix(p, "/"), "/", 3)
	if len(segs) < 2 {
		return false
	}
	got := strings.ToLower(segs[0] + "/" + strings.TrimSuffix(segs[1], ".git"))
	return slices.ContainsFunc(repos, func(r string) bool { return NormalizeRepo(r) == got })
}

// NormalizeRepo 把 "Owner/Repo.git" 规范为 "owner/repo"。
func NormalizeRepo(r string) string {
	return strings.ToLower(strings.TrimSuffix(strings.Trim(r, "/"), ".git"))
}

// substitute 把凭据位置上的占位符替换为真实值；返回用到的 id → 值。
// 任一占位符出现在非凭据位置、不属于该路由或主机不被允许，返回 false。
func (h *Handler) substitute(r *http.Request, t target) (map[string]string, bool) {
	used := map[string]string{}
	valid := true
	repl := func(s string) string {
		return placeholderRe.ReplaceAllStringFunc(s, func(m string) string {
			id := placeholderRe.FindStringSubmatch(m)[1]
			if !slices.Contains(t.allowed, id) {
				valid = false
				return m
			}
			v, hosts, ok := h.R.SecretByID(id)
			if !ok || !hostAllowed(hosts, t.url.Hostname()) {
				valid = false
				return m
			}
			used[id] = v
			return v
		})
	}
	if strings.Contains(r.URL.Path, "oryn-ph-") || strings.Contains(r.URL.RawPath, "oryn-ph-") {
		return nil, false
	}
	for k, vs := range r.Header {
		if strings.Contains(k, "oryn-ph-") {
			return nil, false
		}
		for i, v := range vs {
			if !strings.Contains(v, "oryn-ph-") {
				continue
			}
			if !credentialHeaders[http.CanonicalHeaderKey(k)] {
				return nil, false
			}
			r.Header[k][i] = repl(v)
		}
	}
	if strings.Contains(r.URL.RawQuery, "oryn-ph-") {
		vals, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			return nil, false
		}
		for k, vs := range vals {
			for i, v := range vs {
				if !strings.Contains(v, "oryn-ph-") && !strings.Contains(k, "oryn-ph-") {
					continue
				}
				if k != credentialQuery {
					return nil, false
				}
				vals[k][i] = repl(v)
			}
		}
		r.URL.RawQuery = vals.Encode()
	}
	return used, valid
}

func hostAllowed(hosts []string, host string) bool {
	if len(hosts) == 0 {
		return true
	}
	h := strings.ToLower(host)
	for _, a := range hosts {
		a = strings.ToLower(a)
		if a == h || (strings.HasPrefix(a, "*.") && strings.HasSuffix(h, a[1:])) {
			return true
		}
	}
	return false
}

func redact(s string, pairs [][2]string) string {
	for _, p := range pairs {
		if p[0] != "" {
			s = strings.ReplaceAll(s, p[0], p[1])
		}
	}
	return s
}

// redactReader 流式地把真实值替换为占位符。只扣留「可能是某个真实值前缀」的尾部字节，
// 因此普通流式文本（SSE 事件以 \n\n 结尾）不会被延迟。
type redactReader struct {
	src   io.ReadCloser
	pairs [][2]string
	buf   []byte // 已读未输出
	out   []byte // 待交付
	chunk []byte
	eof   bool
}

func newRedactReader(src io.ReadCloser, pairs [][2]string) *redactReader {
	ps := make([][2]string, 0, len(pairs))
	for _, p := range pairs {
		if p[0] != "" {
			ps = append(ps, p)
		}
	}
	return &redactReader{src: src, pairs: ps, chunk: make([]byte, 32<<10)}
}

func (r *redactReader) Read(p []byte) (int, error) {
	for len(r.out) == 0 {
		if r.eof {
			if len(r.buf) == 0 {
				return 0, io.EOF
			}
			r.out, r.buf = []byte(redact(string(r.buf), r.pairs)), nil
			break
		}
		n, err := r.src.Read(r.chunk)
		r.buf = append(r.buf, r.chunk[:n]...)
		if err == io.EOF {
			r.eof = true
		} else if err != nil {
			return 0, err
		}
		for _, pr := range r.pairs {
			r.buf = bytes.ReplaceAll(r.buf, []byte(pr[0]), []byte(pr[1]))
		}
		hold := r.holdback()
		r.out = append(r.out, r.buf[:len(r.buf)-hold]...)
		r.buf = append(r.buf[:0:0], r.buf[len(r.buf)-hold:]...)
	}
	n := copy(p, r.out)
	r.out = r.out[n:]
	return n, nil
}

// holdback 返回 buf 末尾中是某个真实值真前缀的最长后缀长度。
func (r *redactReader) holdback() int {
	best := 0
	for _, pr := range r.pairs {
		s := pr[0]
		for l := min(len(s)-1, len(r.buf)); l > best; l-- {
			if bytes.HasSuffix(r.buf, []byte(s[:l])) {
				best = l
				break
			}
		}
	}
	return best
}

func (r *redactReader) Close() error { return r.src.Close() }
