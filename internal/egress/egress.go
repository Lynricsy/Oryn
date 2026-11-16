// Package egress 是控制面侧的出站代理：orb 内 agent 的模型 / git / GitHub API 请求经 link 的 egress 流到达这里，
// 在此把占位令牌替换为真实密钥后转发到上游。真实密钥从不进入 orb。
//
// 安全约束（orb 内代码可任意构造请求，并能看到全部占位符）：
//   - 占位符只在固定的凭据位置（若干请求头与 query 参数 key）被替换，出现在其它任何位置一律拒绝；
//   - 每个模型路由只接受绑定到它的密钥 id：路由自己的 apiKey 密钥（hosts 为空时只能用在它自己的路由上），
//     以及 hosts 非空且与路由上游主机匹配的其它密钥；git 路由不接受任何占位符；
//   - git 路由只放行 smart-HTTP 端点；有项目仓库时只放行该仓库（读写），否则任意仓库只读；
//     路径含 `.`、`..`、空段或不是规范形式的一律 400；
//   - GitHub API 路由（/gh/api.github.com/…，M14）只在策略开放时可用，注入按项目仓库收窄的令牌，
//     并给 JSON 写请求的正文追加 AgentMarker；
//   - 响应头与响应体中出现的真实值会被换回占位符，防止上游回显泄露（101 升级后的字节流只脱敏响应头，
//     改写升级后的协议帧会破坏其分帧）。
package egress

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// AgentMarker 由出站代理追加到 agent 发往 GitHub 的正文末尾（GitHub 渲染时不可见），控制面据此识别 agent 自己的评论（M14）。
const AgentMarker = "<!-- oryn-agent -->"

// markBodyMax 是追加 AgentMarker 时读入的请求正文上限；更大的请求原样透传。
const markBodyMax = 1 << 20

// Route 是一条模型路由：上游前缀与允许在该路由上使用的密钥 id。
type Route struct {
	Origin string `json:"origin"`
	// SecretIDs 是路由所属 provider 自己的 apiKey 密钥：hosts 为空时不限主机（只绑定在它自己的路由上），
	// 非空时须与上游主机匹配。
	SecretIDs []string `json:"secretIds"`
	// HostBoundIDs 是 provider 子树里引用的其它密钥：只有 hosts 非空且与上游主机匹配时才可注入。
	HostBoundIDs []string `json:"hostBoundIds,omitempty"`
}

// Policy 是一台 orb 的出站策略。
type Policy struct {
	// Routes 是模型路由键 → 路由。
	Routes map[string]Route
	// GitRepos 是 git 主机 → 允许读写的仓库路径（如 "owner/repo"）。非空时只放行其中列出的仓库
	// （其它主机、同主机其它仓库一律拒绝）；为空时（无项目的 orb）任意有令牌主机上的任意仓库只读，
	// 拒绝 git-receive-pack。
	GitRepos map[string][]string
	// NoGit 为真时 /git/… 一律 403（协调宿主没有任何 git 访问）。
	NoGit bool
	// GitHub 非 nil 时放行 /gh/api.github.com/… 与 /gh/uploads.github.com/…（M14/M15），并对 github.com 的 git 不按仓库限制（M15）。
	GitHub *GitHubAccess
}

// GitHubAccess 是 orb 的 GitHub 访问：非 nil 时放行 `/gh/api.github.com/…`、`/gh/uploads.github.com/…`；
// GitOrigin 是 `github.com` 的 git 上游，默认 https://github.com；UploadsOrigin 是上传主机的上游，空 = https://uploads.github.com。
// 两者只有测试会指向别处。
type GitHubAccess struct {
	APIOrigin, GitOrigin, UploadsOrigin string
}

// defaultUploadsOrigin 是 GitHub 上传 API（release 附件）的上游。
const defaultUploadsOrigin = "https://uploads.github.com"

// Resolver 提供策略与密钥查询。
type Resolver interface {
	// Policy 返回该 orb 当前的出站策略。
	Policy() Policy
	// SecretByID 按密钥 id 返回明文与允许的上游主机列表（空 = 未绑定主机）。
	SecretByID(id string) (value string, hosts []string, ok bool)
	// GitToken 返回 git 主机的访问令牌（密钥 git/<host>/token）。
	GitToken(host string) (token string, ok bool)
	// GitHubToken 返回这台 orb 的 GitHub API 令牌（M14；按仓库收窄）；不归 GitHub App 管或拿不到时 ok 为假。
	GitHubToken() (token string, ok bool)
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
	url    *url.URL
	auth   string   // 非空时覆盖 Authorization（git、gh）
	redact []string // 要从响应里抹掉的真实值（换成 oryn-redacted）
	mark   bool     // 给请求正文追加 AgentMarker（gh）
	// stripEncoding 为真时去掉客户端的 Accept-Encoding，让 Transport 自己协商并解压（M15：上游 gzip 时响应脱敏与写操作识别读到的是明文）。
	stripEncoding bool
	own           []string // 路由自己的 apiKey 密钥 id
	hostBound     []string // 须 hosts 匹配的其它密钥 id
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t, status := h.resolve(r)
	if status != http.StatusOK {
		msg := "oryn egress: 未知或不允许的路由"
		switch status {
		case http.StatusBadRequest:
			msg = "oryn egress: 路径不是规范形式"
		case http.StatusBadGateway:
			msg = "oryn egress: GitHub 令牌不可用（身份未连接或签发失败）"
		}
		http.Error(w, msg, status)
		return
	}
	used, ok := h.substitute(r, t)
	if !ok {
		http.Error(w, "oryn egress: 占位符位置无效或不允许用于该路由", http.StatusForbidden)
		return
	}
	secretsUsed := make([][2]string, 0, len(used)+len(t.redact)) // 真实值 → 占位符，用于响应脱敏
	for id, v := range used {
		secretsUsed = append(secretsUsed, [2]string{v, "oryn-ph-" + id})
	}
	for _, v := range t.redact {
		secretsUsed = append(secretsUsed, [2]string{v, "oryn-redacted"})
	}
	if t.mark {
		if err := markRequest(r); err != nil {
			http.Error(w, "oryn egress: 读取请求正文失败", http.StatusBadRequest)
			return
		}
	}
	p := &httputil.ReverseProxy{
		FlushInterval: -1, // SSE 逐块立即 flush
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = t.url.Scheme
			pr.Out.URL.Host = t.url.Host
			pr.Out.URL.Path = t.url.Path
			pr.Out.URL.RawPath = t.url.RawPath
			pr.Out.Host = t.url.Host
			pr.Out.Header.Del("X-Forwarded-For")
			if t.auth != "" {
				pr.Out.Header.Set("Authorization", t.auth)
			}
			if t.stripEncoding {
				pr.Out.Header.Del("Accept-Encoding")
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
			// 101：ReverseProxy 需要原样的双向 body（io.ReadWriteCloser）来桥接升级后的连接；
			// 替换成只读的脱敏读取器会让升级失败（502）。升级后的协议有自己的分帧，不做按字节替换。
			if resp.StatusCode == http.StatusSwitchingProtocols {
				return nil
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

// resolve 把 /llm/<key>/…、/git/<host>/… 或 /gh/api.github.com/… 映射为上游；返回 HTTP 状态（200 = 放行）。
func (h *Handler) resolve(r *http.Request) (target, int) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(parts) < 2 {
		return target{}, http.StatusNotFound
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
			return target{}, http.StatusNotFound
		}
		u, err := url.Parse(rt.Origin)
		if err != nil || u.Host == "" {
			return target{}, http.StatusNotFound
		}
		u.Path = rest
		return target{url: u, own: rt.SecretIDs, hostBound: rt.HostBoundIDs}, http.StatusOK
	case "git":
		if pol.NoGit {
			return target{}, http.StatusForbidden
		}
		host := strings.ToLower(parts[1])
		// 解码后的路径（%2e%2e 已还原为 ..）原样转发，因此在这里拒绝一切非规范形式。
		if !canonicalPath(rest) {
			return target{}, http.StatusBadRequest
		}
		service := r.URL.Query().Get("service")
		repo, write, ok := gitEndpoint(rest, service)
		if !ok {
			return target{}, http.StatusNotFound
		}
		// M15：github.com 改由 OAuth 用户令牌注入，能做什么取决于账号自身权限，不再按仓库限制；
		// 其它主机（PAT）维持 M2 规则：有项目时只放行项目仓库，无项目时任意仓库只读。
		githubOpen := host == "github.com" && pol.GitHub != nil
		if !githubOpen {
			if len(pol.GitRepos) > 0 {
				if !slices.ContainsFunc(pol.GitRepos[host], func(a string) bool { return NormalizeRepo(a) == repo }) {
					return target{}, http.StatusForbidden
				}
			} else if write {
				return target{}, http.StatusForbidden
			}
		}
		tok, ok := h.R.GitToken(host)
		if !ok {
			return target{}, http.StatusNotFound
		}
		auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tok))
		up := &url.URL{Scheme: "https", Host: host, Path: rest}
		if host == "github.com" && pol.GitHub != nil && pol.GitHub.GitOrigin != "" {
			o, err := url.Parse(pol.GitHub.GitOrigin)
			if err != nil || o.Host == "" {
				return target{}, http.StatusNotFound
			}
			up.Scheme, up.Host = o.Scheme, o.Host
		}
		return target{url: up, auth: auth, redact: []string{strings.TrimPrefix(auth, "Basic "), tok}}, http.StatusOK
	case "gh":
		// GitHub API（M14/M15）：令牌由出站代理注入，这里不做路径白名单；保留原始转义（如 contents 路径里的 %2F）。
		if pol.GitHub == nil {
			return target{}, http.StatusForbidden
		}
		origin := pol.GitHub.APIOrigin
		switch strings.ToLower(parts[1]) {
		case "api.github.com":
		case "uploads.github.com":
			origin = cmp.Or(pol.GitHub.UploadsOrigin, defaultUploadsOrigin)
		default:
			return target{}, http.StatusNotFound
		}
		o, err := url.Parse(origin)
		if err != nil || o.Host == "" {
			return target{}, http.StatusNotFound
		}
		tok, ok := h.R.GitHubToken()
		if !ok {
			return target{}, http.StatusBadGateway
		}
		up := &url.URL{
			Scheme:  o.Scheme,
			Host:    o.Host,
			Path:    o.Path + rest,
			RawPath: o.Path + strings.TrimPrefix(r.URL.EscapedPath(), "/gh/"+parts[1]),
		}
		return target{url: up, auth: "Bearer " + tok, redact: []string{tok}, mark: true, stripEncoding: true}, http.StatusOK
	}
	return target{}, http.StatusNotFound
}

// markRequest 给 JSON 写请求的正文追加 AgentMarker（见 markAgentBodies），并同步 ContentLength 与 Content-Length 头。
// 非 POST / PATCH / PUT、非 JSON、没有正文或超过 markBodyMax 的请求原样透传。读正文失败返回错误。
func markRequest(r *http.Request) error {
	switch r.Method {
	case http.MethodPost, http.MethodPatch, http.MethodPut:
	default:
		return nil
	}
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" || r.ContentLength <= 0 || r.ContentLength > markBodyMax {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, r.ContentLength))
	_ = r.Body.Close()
	if err != nil {
		return err
	}
	nb := markAgentBodies(b)
	r.Body = io.NopCloser(bytes.NewReader(nb))
	r.ContentLength = int64(len(nb))
	r.Header.Set("Content-Length", strconv.Itoa(len(nb)))
	return nil
}

// markAgentBodies 给 JSON 请求里的正文字段追加 AgentMarker：顶层 "body"、顶层 "comments" 数组各元素的 "body"、
// "variables"."input"."body"。只处理字符串且尚未含标记的值；解析失败或没有可改的字段时原样返回。
func markAgentBodies(b []byte) []byte {
	var top map[string]json.RawMessage
	if json.Unmarshal(b, &top) != nil || top == nil {
		return b
	}
	changed := markField(top, "body")
	if raw, ok := top["comments"]; ok {
		var cs []map[string]json.RawMessage
		if json.Unmarshal(raw, &cs) == nil {
			hit := false
			for _, c := range cs {
				if c != nil && markField(c, "body") {
					hit = true
				}
			}
			if hit && setRaw(top, "comments", cs) {
				changed = true
			}
		}
	}
	if raw, ok := top["variables"]; ok {
		var vars map[string]json.RawMessage
		if json.Unmarshal(raw, &vars) == nil && vars != nil {
			var input map[string]json.RawMessage
			if in, ok := vars["input"]; ok && json.Unmarshal(in, &input) == nil && input != nil && markField(input, "body") &&
				setRaw(vars, "input", input) && setRaw(top, "variables", vars) {
				changed = true
			}
		}
	}
	if !changed {
		return b
	}
	out, err := encodeJSON(top)
	if err != nil {
		return b
	}
	return out
}

// markField 在 m[key] 是不含 AgentMarker 的字符串时追加标记；返回是否改动。
func markField(m map[string]json.RawMessage, key string) bool {
	raw, ok := m[key]
	if !ok {
		return false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || strings.Contains(s, AgentMarker) {
		return false
	}
	return setRaw(m, key, s+"\n\n"+AgentMarker)
}

// setRaw 把 v 编码后写回 m[key]；编码失败返回 false。
func setRaw(m map[string]json.RawMessage, key string, v any) bool {
	b, err := encodeJSON(v)
	if err != nil {
		return false
	}
	m[key] = b
	return true
}

// encodeJSON 编码 v，不转义 HTML 字符（标记里的 `<` `>` 原样保留），去掉结尾换行。
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// canonicalPath 报告 p（以 / 开头）不含 `.`、`..` 与空段，且与 path.Clean 的结果一致。
func canonicalPath(p string) bool {
	if p == "/" || path.Clean(p) != p {
		return false
	}
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// gitEndpoint 只认 git smart-HTTP 端点，返回规范化的仓库路径与是否为写操作（receive-pack）。
func gitEndpoint(p, service string) (repo string, write, ok bool) {
	var base string
	switch {
	case strings.HasSuffix(p, "/info/refs"):
		if service != "git-upload-pack" && service != "git-receive-pack" {
			return "", false, false
		}
		base, write = strings.TrimSuffix(p, "/info/refs"), service == "git-receive-pack"
	case strings.HasSuffix(p, "/git-upload-pack"):
		base = strings.TrimSuffix(p, "/git-upload-pack")
	case strings.HasSuffix(p, "/git-receive-pack"):
		base, write = strings.TrimSuffix(p, "/git-receive-pack"), true
	default:
		return "", false, false
	}
	repo = NormalizeRepo(base)
	if strings.Count(repo, "/") < 1 {
		return "", false, false
	}
	return repo, write, true
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
	host := t.url.Hostname()
	repl := func(s string) string {
		return placeholderRe.ReplaceAllStringFunc(s, func(m string) string {
			id := placeholderRe.FindStringSubmatch(m)[1]
			own, bound := slices.Contains(t.own, id), slices.Contains(t.hostBound, id)
			if !own && !bound {
				valid = false
				return m
			}
			v, hosts, ok := h.R.SecretByID(id)
			// hosts 为空的密钥只能用作它自己 provider 路由的 apiKey；其它引用必须绑定了匹配的主机。
			if !ok || (len(hosts) == 0 && !own) || (len(hosts) > 0 && !hostAllowed(hosts, host)) {
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

// HostAllowed 报告 host 是否在 hosts 之列（支持 `*.example.com` 通配）；hosts 为空时为 false。
func HostAllowed(hosts []string, host string) bool { return hostAllowed(hosts, host) }

func hostAllowed(hosts []string, host string) bool {
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
