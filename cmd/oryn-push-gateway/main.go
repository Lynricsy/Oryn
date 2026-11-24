// oryn-push-gateway 是官方推送网关：把自部署 orynd 的 RFC 8291 密文转给 APNs / FCM（docs/push-gateway.md）。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Lynricsy/Oryn/internal/pushgw"
	"github.com/Lynricsy/Oryn/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "oryn-push-gateway:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fsFlags := flag.NewFlagSet("oryn-push-gateway", flag.ContinueOnError)
	cfgPath := fsFlags.String("config", pushgw.DefaultConfigPath, "配置文件路径")
	showVersion := fsFlags.Bool("version", false, "打印版本号并退出")
	if err := fsFlags.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Println(version.Version)
		return nil
	}
	if rest := fsFlags.Args(); len(rest) > 0 {
		return fmt.Errorf("多余的参数 %q", rest)
	}
	cfg, err := pushgw.Load(*cfgPath)
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := pushgw.New(ctx, cfg, pushgw.Options{Log: log})
	if err != nil {
		return err
	}
	defer srv.Close()
	return srv.Run(ctx)
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	_ = lv.UnmarshalText([]byte(level))
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv}))
}
