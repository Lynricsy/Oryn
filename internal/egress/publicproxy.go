package egress

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// PublicProxy 是控制面自己的 git 连未担保主机时用的回环 CONNECT 代理（M24 阶段 ②）：只放行 443 端口，
// 拨号沿用 dialWeb（空 WebPolicy = 一律 publicOnly），在连接时检查实际 IP，DNS 重绑定无效。
// git 子进程只能经 http.proxy 交出连接，出站代理的 Handler 绑定 orb 策略，不适合控制面，所以另开监听。
type PublicProxy struct {
	ln  net.Listener
	srv *http.Server
}

// ListenPublicProxy 在 127.0.0.1 的随机端口上起守卫代理；调用方用完 Close。
func ListenPublicProxy() (*PublicProxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &PublicProxy{ln: ln}
	p.srv = &http.Server{Handler: http.HandlerFunc(servePublicConnect), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := p.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("公网守卫代理退出", "err", err)
		}
	}()
	return p, nil
}

// URL 是给 git `http.proxy` 用的地址，形如 http://127.0.0.1:<port>。
func (p *PublicProxy) URL() string { return "http://" + p.ln.Addr().String() }

// Close 停止监听；已建立的隧道随两端关闭而结束。
func (p *PublicProxy) Close() error { return p.srv.Close() }

// servePublicConnect 只接受 CONNECT host:443，只连公网地址；成功后接管连接并双向转发。
func servePublicConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		w.Header().Set("Allow", http.MethodConnect)
		http.Error(w, "oryn repos: only CONNECT", http.StatusMethodNotAllowed)
		return
	}
	host, port, ok := webTarget(r.Host)
	if !ok {
		http.Error(w, "oryn repos: CONNECT target must be host:port", http.StatusBadRequest)
		return
	}
	if port != "443" {
		http.Error(w, "oryn repos: only port 443", http.StatusForbidden)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "oryn repos: connection cannot be hijacked", http.StatusInternalServerError)
		return
	}
	up, err := dialWeb(r.Context(), &WebPolicy{}, host, port)
	if err != nil {
		if errors.Is(err, errNotPublic) {
			slog.Info("公网守卫拒绝非公网目标", "host", r.Host)
			http.Error(w, errNotPublic.Error(), http.StatusForbidden)
			return
		}
		slog.Warn("公网守卫连接上游失败", "host", r.Host, "err", err)
		http.Error(w, "oryn repos: upstream unreachable", http.StatusBadGateway)
		return
	}
	down, buf, err := hj.Hijack()
	if err != nil {
		_ = up.Close()
		return
	}
	_ = down.SetDeadline(time.Time{})
	if _, err := down.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		_ = up.Close()
		_ = down.Close()
		return
	}
	go tunnel(down, buf.Reader, up)
}
