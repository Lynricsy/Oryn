package egress

import (
	"context"
	"errors"
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
// Headers 是由出站代理注入的请求头（鉴权头只在这里，不进沙箱）。
type MCPRoute struct {
	URL     string
	Headers map[string]string
}

// MCPTokenHeader 是 /mcp/ 路由的凭据请求头（M19 D10）：值须等于 Policy.MCPToken，只有全局助手的 agent 持有。
const MCPTokenHeader = "X-Oryn-MCP-Token"

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

// denyWeb 按拨号错误回应：非公网目标 403（记一条 Info 日志），其它 502。
func (h *Handler) denyWeb(w http.ResponseWriter, target string, err error) {
	if errors.Is(err, errNotPublic) {
		slog.Info("出站代理拒绝非公网目标", "orb", h.OrbID, "host", target, "err", errNotPublic)
		http.Error(w, errNotPublic.Error(), http.StatusForbidden)
		return
	}
	slog.Warn("出站代理连接上游失败", "orb", h.OrbID, "host", target, "err", err)
	http.Error(w, "oryn egress: 上游不可达", http.StatusBadGateway)
}

// serveConnect 处理 `CONNECT host:port`（M19 正向代理）：策略没有 Web 时 403；拨号成功后接管连接，
// 回 200 并在两端之间双向转发。不看隧道里的内容（TLS 由沙箱里的客户端自己校验）。
func (h *Handler) serveConnect(w http.ResponseWriter, r *http.Request) {
	pol := h.R.Policy().Web
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
		h.denyWeb(w, r.Host, err)
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

// serveForward 处理协调宿主的 absolute-form 请求（`GET http://host/… HTTP/1.1`，M19 正向代理）：策略没有 Web 时 403，
// 只接受 http；原样转发到请求里的地址，不做占位符替换，拨号同 CONNECT 只连公网地址。
func (h *Handler) serveForward(w http.ResponseWriter, r *http.Request, pol *WebPolicy) {
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
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) { h.denyWeb(w, target, err) },
	}
	p.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), webPolicyKey{}, pol)))
}
