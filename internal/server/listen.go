package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/version"
)

// orbTLSPrefix 是 orb 专用 TLS 监听唯一放行的路径前缀。
const orbTLSPrefix = "/orb/v1/"

// Listeners 是 orynd 的全部监听：Plain 服务全部路由；OrbTLS 只服务 /orb/v1/*（M11a 审查修复 S-P1-3）。
type Listeners struct {
	Plain  []net.Listener
	OrbTLS []net.Listener
}

// Close 关闭尚未交给 Serve 的监听。
func (l *Listeners) Close() {
	for _, ln := range append(append([]net.Listener(nil), l.Plain...), l.OrbTLS...) {
		_ = ln.Close()
	}
}

// Listen 打开 cfg.Listen 与 cfg.OrbTLS.Listen 上的全部 TCP 监听；任一失败时关闭已打开的并返回错误。
func (s *Server) Listen() (*Listeners, error) {
	ls := &Listeners{}
	open := func(addr string) (net.Listener, error) {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			ls.Close()
			return nil, fmt.Errorf("监听 %s: %w", addr, err)
		}
		return ln, nil
	}
	for _, addr := range s.Config.Listen {
		ln, err := open(addr)
		if err != nil {
			return nil, err
		}
		ls.Plain = append(ls.Plain, ln)
	}
	if len(s.Config.OrbTLS.Listen) > 0 && s.OrbTLS == nil {
		ls.Close()
		return nil, errors.New("orb_tls.listen 已配置但 TLS 证书未加载")
	}
	for _, addr := range s.Config.OrbTLS.Listen {
		ln, err := open(addr)
		if err != nil {
			return nil, err
		}
		ls.OrbTLS = append(ls.OrbTLS, ln)
	}
	return ls, nil
}

// Serve 在 ls 上服务直到 ctx 取消或某个监听出错，然后优雅关闭（10s）。
// 明文监听用 s.Handler；TLS 监听用同一 handler，但只放行 /orb/v1/*，TLS 最低 1.3。
func (s *Server) Serve(ctx context.Context, ls *Listeners) error {
	plain := &http.Server{Handler: s.Handler, ReadHeaderTimeout: 15 * time.Second}
	servers := []*http.Server{plain}
	errc := make(chan error, len(ls.Plain)+len(ls.OrbTLS))
	for _, ln := range ls.Plain {
		slog.Info("orynd 监听", "addr", ln.Addr().String(), "version", version.Version)
		go func() { errc <- plain.Serve(ln) }()
	}
	if len(ls.OrbTLS) > 0 {
		orb := &http.Server{
			Handler:           orbOnly(s.Handler),
			ReadHeaderTimeout: 15 * time.Second,
			TLSConfig:         s.OrbTLS,
			ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
		}
		servers = append(servers, orb)
		for _, ln := range ls.OrbTLS {
			slog.Info("orynd orb link TLS 监听", "addr", ln.Addr().String())
			go func() { errc <- orb.Serve(tls.NewListener(ln, s.OrbTLS)) }()
		}
	}
	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, srv := range servers {
		if err := srv.Shutdown(shutdownCtx); err != nil && serveErr == nil {
			serveErr = err
		}
	}
	return serveErr
}

// orbOnly 只放行规范形式的 /orb/v1/* 路径，其余（含带 . / .. 段、重复斜杠或百分号编码的路径）一律 404。
func orbOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if !strings.HasPrefix(p, orbTLSPrefix) || path.Clean(p) != p || r.URL.RawPath != "" {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}
