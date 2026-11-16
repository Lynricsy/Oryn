package render

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Host 是渲染作业在代理上的虚拟主机：`http://render.oryn.invalid/<作业令牌>/<路径>`（.invalid 永远解析不到，
// 只有经代理才有意义）。
const Host = "render.oryn.invalid"

// Target 是渲染作业要提供的 artifact 版本。
type Target struct {
	ArtifactID string
	// Version 是作业开始时解析好的版本（不是 0）。
	Version int
	// Strict 为真时页面不能从 CDN 加载任何东西（最新版本声明了 files 或 sample 能力）。
	Strict bool
	// Self 是作业根的 URL（`http://render.oryn.invalid/<令牌>/`），写进 CSP 代替 'self'。
	Self string
}

// Site 提供渲染作业的内容（由 artifacts.Host 实现）。
type Site interface {
	// RenderTarget 解析作业要渲染的版本（0 = 最新）与是否严格；artifact 不存在、在回收站里或版本不存在时回错误。
	RenderTarget(ctx context.Context, artifactID string, version int) (resolved int, strict bool, err error)
	// ServeRender 以渲染模式提供作业根之内的路径 p（以 / 开头）：不注入运行时、不标源码，响应头同帧但没有 frame-ancestors。
	ServeRender(w http.ResponseWriter, r *http.Request, t Target, p string)
}

// proxyJob 是代理上登记的一个进行中的作业：要么是 artifact 的某个版本，要么（自检）是一段固定的 HTML。
type proxyJob struct {
	target Target
	fixed  []byte
}

// proxy 是 Chromium 唯一能连的网络出口：只放行渲染作业的内容与（有非严格作业时）白名单 CDN 的 443 隧道，其余一律 403。
type proxy struct {
	site Site
	cdns map[string]bool // CDN 的主机名（不带协议与端口）

	mu   sync.Mutex
	jobs map[string]*proxyJob

	// observe 仅供测试：记录每个请求是否被放行。
	observe func(method, target string, allowed bool)
	// dial 连接隧道的上游（测试里换成本地监听）。
	dial func(ctx context.Context, addr string) (net.Conn, error)

	ln  net.Listener
	srv *http.Server
}

// newProxy 在 127.0.0.1 的随机端口上开始监听；端口在进程生命周期内不变。
func newProxy(site Site, cdns []string) (*proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: 10 * time.Second}
	p := &proxy{site: site, cdns: map[string]bool{}, jobs: map[string]*proxyJob{}, ln: ln,
		dial: func(ctx context.Context, addr string) (net.Conn, error) { return d.DialContext(ctx, "tcp", addr) }}
	for _, c := range cdns {
		if h, ok := strings.CutPrefix(c, "https://"); ok && h != "" {
			p.cdns[strings.ToLower(h)] = true
		}
	}
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// port 返回代理监听的端口。
func (p *proxy) port() int { return p.ln.Addr().(*net.TCPAddr).Port }

func (p *proxy) close() { _ = p.srv.Close() }

func (p *proxy) add(token string, j *proxyJob) {
	p.mu.Lock()
	p.jobs[token] = j
	p.mu.Unlock()
}

func (p *proxy) remove(token string) {
	p.mu.Lock()
	delete(p.jobs, token)
	p.mu.Unlock()
}

func (p *proxy) job(token string) (*proxyJob, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	j, ok := p.jobs[token]
	return j, ok
}

// anyLoose 报告当前是否至少有一个非严格的 artifact 作业在进行（自检作业不算）。
func (p *proxy) anyLoose() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, j := range p.jobs {
		if j.fixed == nil && !j.target.Strict {
			return true
		}
	}
	return false
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodConnect:
		p.connect(w, r)
	case r.URL.IsAbs() && r.URL.Scheme == "http" && strings.EqualFold(r.URL.Host, Host):
		p.content(w, r)
	default:
		p.deny(w, r, "不在白名单里")
	}
}

func (p *proxy) record(r *http.Request, allowed bool) {
	if p.observe != nil {
		target := r.Host
		if r.Method != http.MethodConnect {
			target = r.URL.String()
		}
		p.observe(r.Method, target, allowed)
	}
}

func (p *proxy) deny(w http.ResponseWriter, r *http.Request, why string) {
	p.record(r, false)
	slog.Debug("artifacts: 渲染代理拒绝请求", "method", r.Method, "host", r.Host, "why", why)
	http.Error(w, "forbidden", http.StatusForbidden)
}

// content 提供 `http://render.oryn.invalid/<令牌>/<路径>`：令牌必须对应一个进行中的作业。
func (p *proxy) content(w http.ResponseWriter, r *http.Request) {
	token, rest, hasSlash := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	j, ok := p.job(token)
	if !ok {
		p.deny(w, r, "未知的作业令牌")
		return
	}
	p.record(r, true)
	if !hasSlash {
		// 作业根必须以 / 结尾，页面里的相对路径才落在作业根之下
		http.Redirect(w, r, "/"+token+"/", http.StatusFound)
		return
	}
	if j.fixed != nil {
		serveFixed(w, r, rest, j.fixed)
		return
	}
	p.site.ServeRender(w, r, j.target, "/"+rest)
}

// serveFixed 提供自检作业的那一段固定 HTML（只有作业根）。
func serveFixed(w http.ResponseWriter, r *http.Request, rest string, body []byte) {
	h := w.Header()
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	if rest != "" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		http.NotFound(w, r)
		return
	}
	h.Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

// connect 处理 `CONNECT <主机>:443`：主机必须是白名单 CDN，且当前至少有一个非严格作业在进行；放行则直连隧道。
func (p *proxy) connect(w http.ResponseWriter, r *http.Request) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != "443" || !p.cdns[strings.ToLower(host)] {
		p.deny(w, r, "不是白名单 CDN 的 443 端口")
		return
	}
	if !p.anyLoose() {
		p.deny(w, r, "没有非严格的作业在进行")
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		p.deny(w, r, "连接不支持隧道")
		return
	}
	up, err := p.dial(r.Context(), net.JoinHostPort(host, "443"))
	if err != nil {
		p.record(r, false)
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	p.record(r, true)
	down, buf, err := hj.Hijack()
	if err != nil {
		_ = up.Close()
		return
	}
	if _, err := down.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		_ = up.Close()
		_ = down.Close()
		return
	}
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
