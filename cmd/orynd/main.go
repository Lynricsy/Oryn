// orynd 是 Oryn 控制面。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/server"
	"github.com/Lynricsy/Oryn/internal/version"
)

func main() {
	// 协调宿主子进程（M9）：在解析全局 flag 与读配置文件之前分派，退出码由它自己决定。
	if len(os.Args) > 1 && os.Args[1] == "coordinator-host" {
		os.Exit(coordinatorHost(os.Args[2:]))
	}
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "orynd:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fsFlags := flag.NewFlagSet("orynd", flag.ContinueOnError)
	cfgPath := fsFlags.String("config", "/etc/oryn/orynd.yaml", "配置文件路径")
	showVersion := fsFlags.Bool("version", false, "打印版本号并退出")
	if err := fsFlags.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Println(version.Version)
		return nil
	}
	configPath = *cfgPath
	cfg, err := config.LoadOrynd(*cfgPath)
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if rest := fsFlags.Args(); len(rest) > 0 {
		sub, ok := subcommands[rest[0]]
		if !ok {
			return fmt.Errorf("未知子命令 %q", rest[0])
		}
		return sub(ctx, cfg, rest[1:])
	}

	srv, err := server.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer srv.Close()
	if err := srv.AnnounceSetup(ctx); err != nil {
		return err
	}
	return serve(ctx, cfg, srv.Handler)
}

// subcommands 是 `orynd --config <file> <子命令> …` 的本机管理命令，由各文件在 init() 中注册。
var subcommands = map[string]func(ctx context.Context, cfg config.Orynd, args []string) error{}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	_ = lv.UnmarshalText([]byte(level))
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv}))
}

// serve 在全部 listen 地址上启动同一个 http.Server，ctx 取消后优雅关闭。
func serve(ctx context.Context, cfg config.Orynd, h http.Handler) error {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 15 * time.Second}
	errc := make(chan error, len(cfg.Listen))
	for _, addr := range cfg.Listen {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("监听 %s: %w", addr, err)
		}
		slog.Info("orynd 监听", "addr", ln.Addr().String(), "version", version.Version)
		go func() { errc <- srv.Serve(ln) }()
	}
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// configPath 是本次运行的 --config（子命令用，例如自检把它列入「必须读不到」）。
var configPath string
