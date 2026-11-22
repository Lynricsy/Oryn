package egress

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// webDialTimeout 是正向代理（CONNECT 隧道与 absolute-form 转发）连接上游的拨号超时。
const webDialTimeout = 10 * time.Second

// WebPolicy 是协调宿主（全局助手的沙箱，M19）的公网访问：非 nil 时正向代理放行 CONNECT 与 http 的 absolute-form 请求，
// 但只连接公网地址。AllowHosts 是例外清单：元素是小写的 `host` 或 `host:port`，命中的目标不做公网判定
// （主人明确允许的内网服务）。只对域名有意义：omp 的代理包装层对字面的回环与内网 IP 不走代理。
type WebPolicy struct {
	AllowHosts []string
}

// MCPRoute 是一条 MCP 路由（`/mcp/<key>/…`，M19）：上游是主人在全局 mcp.json 里配置的 HTTP / SSE 服务器地址，
// Headers 是由出站代理注入的请求头（鉴权头只在这里，不进沙箱）。ResearchTools 是主人勾选的后台研究可用工具（M26）：
// nil = 这个服务器不开放给后台研究（持研究凭据的请求 403）；非 nil 时研究凭据的 tools/call 只放行其中的工具。
type MCPRoute struct {
	URL           string
	Headers       map[string]string
	ResearchTools []string
}

// MCPTokenHeader 是 /mcp/ 路由的凭据请求头（M19 D10）：值须等于 Policy.MCPToken，只有全局助手的 agent 持有。
const MCPTokenHeader = "X-Oryn-MCP-Token"

// mcpFilterBodyMax 是持后台研究凭据的 /mcp/ 请求体上限（M26）：过滤要读完整个 JSON-RPC 对象才能判断。
const mcpFilterBodyMax = 1 << 20

// mcpOpenMethods 是研究凭据不受工具白名单限制的 JSON-RPC 方法（握手、心跳、列出工具）；`notifications/` 前缀另判。
var mcpOpenMethods = map[string]bool{"initialize": true, "ping": true, "tools/list": true, "server/discover": true}

// mcpResearchFilter 过滤持后台研究凭据的 /mcp/ 请求（M26）：GET（SSE 流）与 DELETE（结束会话）放行；POST 的体
// 必须是单个 JSON-RPC 对象：应答（没有 method）、握手与列工具放行，tools/call 只放行 allow 里的工具，其余方法一律
// 不转发、直接回 JSON-RPC 错误。handled 为真时已写出响应；为假时请求体已复原，照常转发。
//
// 上游可能用大小写不敏感的解码（Go 的 encoding/json 按 Unicode 折叠匹配字段名），所以 method、params 与 params.name
// 只认精确的键：同一层里按大小写折叠相同的键、或与这几个名字折叠相同而写法不同的键一律 400，免得过滤看到的
// 工具名与上游执行的不是同一个。
func mcpResearchFilter(w http.ResponseWriter, r *http.Request, allow []string) (handled bool) {
	switch r.Method {
	case http.MethodGet, http.MethodDelete:
		return false
	case http.MethodPost:
	default:
		http.Error(w, "oryn: 后台研究的 MCP 请求只接受 GET、POST 与 DELETE", http.StatusMethodNotAllowed)
		return true
	}
	if !identityEncoding(r.Header) {
		http.Error(w, "oryn: 后台研究的 MCP 请求体不能压缩", http.StatusUnsupportedMediaType)
		return true
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, mcpFilterBodyMax+1))
	if err != nil {
		http.Error(w, "oryn: 读取请求体失败", http.StatusBadRequest)
		return true
	}
	if len(buf) > mcpFilterBodyMax {
		http.Error(w, "oryn: 后台研究的 MCP 请求体过大", http.StatusRequestEntityTooLarge)
		return true
	}
	r.Body = readCloser{bytes.NewReader(buf), r.Body}
	msg, err := jsonObject(buf, "method", "params")
	if err != nil {
		http.Error(w, "oryn: 后台研究的 MCP 请求体必须是单个 JSON-RPC 对象", http.StatusBadRequest)
		return true
	}
	method, hasMethod := "", false
	if raw, ok := msg["method"]; ok {
		var s *string
		if json.Unmarshal(raw, &s) != nil || s == nil {
			http.Error(w, "oryn: JSON-RPC 的 method 必须是字符串", http.StatusBadRequest)
			return true
		}
		method, hasMethod = *s, true
	}
	if hv := r.Header.Values("Mcp-Method"); len(hv) > 1 || (len(hv) == 1 && hv[0] != method) {
		http.Error(w, "oryn: Mcp-Method 请求头与请求体不符", http.StatusBadRequest)
		return true
	}
	nameHdr := r.Header.Values("Mcp-Name")
	if len(nameHdr) > 1 {
		http.Error(w, "oryn: Mcp-Name 请求头与请求体不符", http.StatusBadRequest)
		return true
	}
	switch {
	case !hasMethod:
		// 对服务器请求的应答：不触发任何操作
		if len(nameHdr) > 0 {
			http.Error(w, "oryn: Mcp-Name 请求头与请求体不符", http.StatusBadRequest)
			return true
		}
		return false
	case mcpOpenMethods[method] || strings.HasPrefix(method, "notifications/"):
		if len(nameHdr) > 0 {
			http.Error(w, "oryn: Mcp-Name 请求头与请求体不符", http.StatusBadRequest)
			return true
		}
		return false
	case method == "tools/call":
		name, ok := "", false
		if raw := msg["params"]; len(raw) > 0 && raw[0] == '{' {
			params, err := jsonObject(raw, "name")
			if err != nil {
				http.Error(w, "oryn: tools/call 的 params 无效", http.StatusBadRequest)
				return true
			}
			var s *string
			if json.Unmarshal(params["name"], &s) == nil && s != nil {
				name, ok = *s, true
			}
		}
		if len(nameHdr) == 1 && (!ok || nameHdr[0] != name) {
			http.Error(w, "oryn: Mcp-Name 请求头与请求体不符", http.StatusBadRequest)
			return true
		}
		if ok && slices.Contains(allow, name) {
			return false
		}
		slog.Info("egress: 后台研究调用了白名单外的 MCP 工具", "tool", name)
		writeJSONRPCError(w, msg["id"], -32602, fmt.Sprintf("tool %q is not available to background research", name))
		return true
	default:
		writeJSONRPCError(w, msg["id"], -32601, fmt.Sprintf("method %q is not available to background research", method))
		return true
	}
}

// writeJSONRPCError 以 HTTP 200 回一个 JSON-RPC 错误应答；id 为空时（通知）回 null。
func writeJSONRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// errJSONObject 是 jsonObject 的拒绝：不是单个对象、有重复（按大小写折叠）的键、或 exact 里某个名字的别样写法。
var errJSONObject = errors.New("not a single JSON object with unambiguous keys")

// jsonObject 把 b 解析成单个 JSON 对象的键 → 原始值。同一层里按 Unicode 大小写折叠相同的键（含完全重复的键），
// 或与 exact 中某个名字折叠相同而写法不同的键，都报 errJSONObject；对象之后不能再有内容。
func jsonObject(b []byte, exact ...string) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errJSONObject
	}
	out := map[string]json.RawMessage{}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, errJSONObject
		}
		k, _ := tok.(string)
		f := foldKey(k)
		if seen[f] {
			return nil, errJSONObject
		}
		seen[f] = true
		for _, e := range exact {
			if k != e && f == foldKey(e) {
				return nil, errJSONObject
			}
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, errJSONObject
		}
		out[k] = v
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, errJSONObject
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errJSONObject
	}
	return out, nil
}

// foldKey 把 s 的每个字符换成它 Unicode 简单大小写折叠轨道里码点最小的那个（K、k 与开尔文符号同归一处，
// s 与长 s 同归一处），用作大小写不敏感的比较键。
func foldKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		m := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			m = min(m, f)
		}
		b.WriteRune(m)
	}
	return b.String()
}

// mcpRedactMin 是 MCP 凭据值参与响应脱敏的最短长度：更短的值几乎必然误伤正文，也不算有效凭据。
const mcpRedactMin = 8

// credentialHeaderWords 是 MCP 请求头名里表示凭据的词（小写子串匹配，覆盖 Authorization、X-Api-Key、Cookie 等）。
var credentialHeaderWords = []string{"auth", "token", "key", "secret", "cookie"}

// mcpRedact 返回 MCP 路由要从响应里抹掉的值：凭据类请求头（名字含 credentialHeaderWords 之一）的完整值，
// 以及 `<scheme> <凭据>` 形式里的凭据部分。
func mcpRedact(headers map[string]string) []string {
	var out []string
	for k, v := range headers {
		lk := strings.ToLower(k)
		if !slices.ContainsFunc(credentialHeaderWords, func(w string) bool { return strings.Contains(lk, w) }) {
			continue
		}
		if len(v) >= mcpRedactMin {
			out = append(out, v)
		}
		if _, cred, ok := strings.Cut(strings.TrimSpace(v), " "); ok {
			if cred = strings.TrimSpace(cred); len(cred) >= mcpRedactMin {
				out = append(out, cred)
			}
		}
	}
	return out
}

// mcpRedirect 处理 MCP 上游的重定向（3xx 带 Location）。客户端（omp 的 fetch）会跟随重定向且保留自定义请求头：
// 若原样转交 Location，跨源跳转会把 X-Oryn-MCP-Token 带到别的服务器，同源的绝对地址也会让客户端绕过代理直连上游。
// 因此：解析后与路由同源（scheme + host）且路径在路由路径之下的，改写成 /mcp/<key>/… 继续走代理；其余一律改成 502、
// 去掉 Location。
func mcpRedirect(resp *http.Response, key string, base *url.URL) {
	if resp.StatusCode < 300 || resp.StatusCode > 399 {
		return
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return
	}
	ref := base
	if resp.Request != nil && resp.Request.URL != nil {
		ref = resp.Request.URL
	}
	basePath := strings.TrimSuffix(base.Path, "/")
	u, err := ref.Parse(loc)
	if err == nil && u.User == nil && strings.EqualFold(u.Scheme, base.Scheme) && strings.EqualFold(u.Host, base.Host) &&
		(u.Path == basePath || strings.HasPrefix(u.Path, basePath+"/")) {
		out := "/mcp/" + key + strings.TrimPrefix(u.Path, basePath)
		// 代理转发时会把路由自带的查询串拼在前面：这里去掉重复的那一段
		q := u.RawQuery
		if base.RawQuery != "" {
			q = strings.TrimPrefix(strings.TrimPrefix(q, base.RawQuery), "&")
		}
		if q != "" {
			out += "?" + q
		}
		resp.Header.Set("Location", out)
		return
	}
	slog.Warn("egress: 拦截 MCP 上游的路由外重定向", "route", key, "location", loc)
	const msg = "oryn egress: MCP 上游重定向到路由之外，已拦截\n"
	_ = resp.Body.Close()
	resp.Header.Del("Location")
	resp.StatusCode, resp.Status = http.StatusBadGateway, "502 Bad Gateway"
	resp.Body = io.NopCloser(strings.NewReader(msg))
	resp.ContentLength = int64(len(msg))
	resp.Header.Set("Content-Length", strconv.Itoa(len(msg)))
	resp.Header.Set("Content-Type", "text/plain; charset=utf-8")
}

// errNotPublic 是正向代理拒绝非公网目标的错误；文案也是回给沙箱的 403 正文。
var errNotPublic = errors.New("destination is not a public address")

// 公网判定之外额外拒绝的网段：本网络、CGNAT 共享地址、基准测试网段。
var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("198.18.0.0/15"),
}

// publicAddr 报告 a 是否是可经正向代理连接的公网地址。
func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	return !slices.ContainsFunc(nonPublicPrefixes, func(p netip.Prefix) bool { return p.Contains(a) })
}

// publicOnly 是 net.Dialer.Control：在连接建立前检查实际要连的 IP（域名解析之后），不是公网地址就中止。
func publicOnly(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !publicAddr(ap.Addr()) {
		return errNotPublic
	}
	return nil
}

// webAllowed 报告 host（不带端口）或 host:port 在 AllowHosts 里。
func webAllowed(pol *WebPolicy, host, port string) bool {
	h := strings.ToLower(host)
	hp := net.JoinHostPort(h, port)
	return slices.ContainsFunc(pol.AllowHosts, func(a string) bool { return a == h || a == hp })
}

// dialWeb 为正向代理连接 host:port：命中 AllowHosts 时普通拨号，否则只连公网地址（errNotPublic）。
func dialWeb(ctx context.Context, pol *WebPolicy, host, port string) (net.Conn, error) {
	d := net.Dialer{Timeout: webDialTimeout}
	if !webAllowed(pol, host, port) {
		d.Control = publicOnly
	}
	return d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
}

// webPolicyKey 是 absolute-form 转发请求的 context 里携带 *WebPolicy 的键（webTransport 拨号时取）。
type webPolicyKey struct{}

// webTransport 是 absolute-form 转发的上游传输：按请求 context 里的 WebPolicy 拨号；不复用连接，
// 免得策略收紧之后旧连接还被别的请求沿用。不读环境变量里的代理。
var webTransport = &http.Transport{
	DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
		pol, _ := ctx.Value(webPolicyKey{}).(*WebPolicy)
		if pol == nil {
			return nil, errNotPublic
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		return dialWeb(ctx, pol, host, port)
	},
	DisableKeepAlives: true,
}

// webTarget 解析 CONNECT 的目标 `host:port`；端口必须是 1–65535。
func webTarget(hostport string) (host, port string, ok bool) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil || host == "" {
		return "", "", false
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", "", false
	}
	return host, port, true
}

// denyWeb 按拨号错误回应：非公网目标 403（记一条 Info 日志），其它 502。who 是日志里的调用方（orb id 或闸门）。
func denyWeb(w http.ResponseWriter, who, target string, err error) {
	if errors.Is(err, errNotPublic) {
		slog.Info("出站代理拒绝非公网目标", "caller", who, "host", target, "err", errNotPublic)
		http.Error(w, errNotPublic.Error(), http.StatusForbidden)
		return
	}
	slog.Warn("出站代理连接上游失败", "caller", who, "host", target, "err", err)
	http.Error(w, "oryn egress: 上游不可达", http.StatusBadGateway)
}

// serveConnect 处理 `CONNECT host:port`（M19 正向代理）：策略没有 Web 时 403。
func (h *Handler) serveConnect(w http.ResponseWriter, r *http.Request) {
	serveWebConnect(w, r, h.R.Policy().Web, h.OrbID)
}

// serveForward 处理协调宿主的 absolute-form 请求（M19 正向代理），见 serveWebForward。
func (h *Handler) serveForward(w http.ResponseWriter, r *http.Request, pol *WebPolicy) {
	serveWebForward(w, r, pol, h.OrbID)
}

// serveWebConnect 处理 `CONNECT host:port`：pol 为 nil 时 403；拨号成功后接管连接，回 200 并在两端之间双向转发。
// 不看隧道里的内容（TLS 由沙箱里的客户端自己校验）。
func serveWebConnect(w http.ResponseWriter, r *http.Request, pol *WebPolicy, who string) {
	if pol == nil {
		http.Error(w, "web access is turned off", http.StatusForbidden)
		return
	}
	host, port, ok := webTarget(r.Host)
	if !ok {
		http.Error(w, "oryn egress: CONNECT 的目标必须是 host:port", http.StatusBadRequest)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "oryn egress: 连接不支持隧道", http.StatusInternalServerError)
		return
	}
	up, err := dialWeb(r.Context(), pol, host, port)
	if err != nil {
		denyWeb(w, who, r.Host, err)
		return
	}
	down, buf, err := hj.Hijack()
	if err != nil {
		_ = up.Close()
		return
	}
	// 服务端为读请求头设过的期限不能带进隧道
	_ = down.SetDeadline(time.Time{})
	if _, err := down.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		_ = up.Close()
		_ = down.Close()
		return
	}
	// 从 buf.Reader 读：客户端紧跟 CONNECT 发来、已被服务端预读进缓冲的字节（如 TLS ClientHello）也会转发出去
	go tunnel(down, buf.Reader, up)
}

// tunnel 在两端之间双向转发，任一方向结束就关掉两端。
func tunnel(down net.Conn, downR io.Reader, up net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, downR); done <- struct{}{} }()
	go func() { _, _ = io.Copy(down, up); done <- struct{}{} }()
	<-done
	_ = up.Close()
	_ = down.Close()
	<-done
}

// serveWebForward 处理 absolute-form 请求（`GET http://host/… HTTP/1.1`）：pol 为 nil 时 403，只接受 http；
// 原样转发到请求里的地址，不做占位符替换，拨号同 CONNECT 只连公网地址。
func serveWebForward(w http.ResponseWriter, r *http.Request, pol *WebPolicy, who string) {
	if pol == nil {
		http.Error(w, "web access is turned off", http.StatusForbidden)
		return
	}
	if r.URL.Scheme != "http" {
		http.Error(w, "oryn egress: 代理形式的请求只接受 http 绝对地址（https 请用 CONNECT）", http.StatusBadRequest)
		return
	}
	target := r.URL.Host
	p := &httputil.ReverseProxy{
		FlushInterval: -1,
		Transport:     webTransport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.Host = pr.In.URL.Host
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) { denyWeb(w, who, target, err) },
	}
	p.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), webPolicyKey{}, pol)))
}

// WebProxy 返回只做正向代理的处理器（定时的本机闸门用，不经 link）：CONNECT 隧道与 http absolute-form 转发，
// 按 pol 只连公网地址（AllowHosts 例外）。token 非空时要求 Proxy-Authorization 为 Basic、密码等于 token（用户名任意），
// 否则 407；其它形式的请求 400。who 是日志里的调用方。
func WebProxy(pol *WebPolicy, token, who string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			_, pass, ok := proxyBasicAuth(r)
			if !ok || subtle.ConstantTimeCompare([]byte(pass), []byte(token)) != 1 {
				w.Header().Set("Proxy-Authenticate", `Basic realm="oryn"`)
				http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
				return
			}
		}
		switch {
		case r.Method == http.MethodConnect:
			serveWebConnect(w, r, pol, who)
		case r.URL.Host != "":
			serveWebForward(w, r, pol, who)
		default:
			http.Error(w, "oryn egress: 只接受代理形式的请求", http.StatusBadRequest)
		}
	})
}

// proxyBasicAuth 解析 Proxy-Authorization 的 Basic 凭据（同 Request.BasicAuth，换了头名）。
func proxyBasicAuth(r *http.Request) (user, pass string, ok bool) {
	auth := r.Header.Get("Proxy-Authorization")
	if auth == "" {
		return "", "", false
	}
	rr := &http.Request{Header: http.Header{"Authorization": {auth}}}
	return rr.BasicAuth()
}
