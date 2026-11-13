// tlsproxy 是本机开发 / 验收用的 HTTPS 反代（仅标准库）：WebKitGTK 拒收经 http:// 下发的 Secure Cookie
// （没有 localhost 例外），而 orynd 的会话 Cookie 是 `__Host-` + `Secure`，桌面壳在本机必须走 HTTPS。
//
// 用法：
//
//	go run ./dev/tlsproxy -init -dir .dev/tls        # 生成（已有则复用）本机 CA 与 localhost 证书后退出
//	sudo trust anchor --store .dev/tls/ca.crt        # 让系统（含 WebKitGTK）信任该 CA
//	go run ./dev/tlsproxy -listen 127.0.0.1:7643 -target http://127.0.0.1:7610 -dir .dev/tls [-static <目录>]
//
// 证书：ECDSA P-256 CA（10 年）+ SAN 为 localhost、127.0.0.1、::1 的叶子证书；文件为 -dir 下的
// ca.crt / ca.key / localhost.crt / localhost.key（私钥 0600）。-static 目录挂在 /updates/。
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr, nil); err != nil {
		fmt.Fprintln(os.Stderr, "tlsproxy:", err)
		os.Exit(1)
	}
}

// run 解析参数并运行；ready 非空时在开始监听后收到实际监听地址（测试用）。
func run(ctx context.Context, args []string, stderr io.Writer, ready chan<- net.Addr) error {
	fs := flag.NewFlagSet("tlsproxy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "127.0.0.1:7643", "HTTPS 监听地址")
	target := fs.String("target", "http://127.0.0.1:7610", "上游 orynd 地址")
	dir := fs.String("dir", ".dev/tls", "证书目录（不存在则创建）")
	static := fs.String("static", "", "挂到 /updates/ 的静态文件目录（可选）")
	initOnly := fs.Bool("init", false, "只生成证书后退出")
	if err := fs.Parse(args); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))

	cert, err := ensureCerts(*dir, time.Now())
	if err != nil {
		return err
	}
	if *initOnly {
		ca, _ := filepath.Abs(filepath.Join(*dir, caCertFile))
		log.Info("证书就绪", "ca", ca, "notAfter", cert.Leaf.NotAfter.Format(time.DateOnly))
		return nil
	}
	u, err := url.Parse(*target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("-target %q 必须是 http(s):// 绝对地址", *target)
	}
	if *static != "" {
		if st, err := os.Stat(*static); err != nil || !st.IsDir() {
			return fmt.Errorf("-static %q 不是目录", *static)
		}
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           newHandler(u, *static),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	log.Info("监听", "addr", "https://"+ln.Addr().String(), "target", u.String(), "static", *static)
	if ready != nil {
		ready <- ln.Addr()
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ServeTLS(ln, "", "") }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		// WebSocket 等被劫持的连接不受 Shutdown 管理，超时后直接关闭
		_ = srv.Close()
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
