// orbd 是 orb 内的守护进程（计划 C2 / C4 / H3.3）：连接控制面、监督 omp、复制会话条目。
//
//	orbd run         启动守护进程（读取 ORYN_URL、ORYN_ORB_ID、ORYN_ORB_TOKEN；可选 ORYN_AGENT_BIN、ORYN_LOG_LEVEL）
//	orbd --version   打印版本
//
// 信号：SIGTERM / SIGINT 优雅停止全部 agent 后退出 0；SIGHUP 立即丢弃并重建 link。
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Lynricsy/Oryn/internal/orbdrt"
	"github.com/Lynricsy/Oryn/internal/version"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: orbd run | orbd --version")
		return 2
	}
	switch args[0] {
	case "--version", "-version", "version":
		fmt.Fprintln(stdout, "orbd", version.Version)
		return 0
	case "run":
	default:
		fmt.Fprintf(stderr, "orbd: unknown command %q\n", args[0])
		return 2
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: logLevel(os.Getenv("ORYN_LOG_LEVEL"))})))

	cfg, err := orbdrt.ConfigFromEnv(version.Version)
	if err != nil {
		fmt.Fprintln(stderr, "orbd:", err)
		return 2
	}
	rt, err := orbdrt.New(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "orbd:", err)
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		for sig := range sigs {
			if sig == syscall.SIGHUP {
				slog.Info("orbd: SIGHUP; redialing link")
				rt.Redial()
				continue
			}
			slog.Info("orbd: stopping", "signal", sig.String())
			go func() {
				rt.Shutdown(orbdrt.DefaultStopGrace)
				cancel()
			}()
			// 第二个终止信号：不再等待。
			for sig := range sigs {
				if sig != syscall.SIGHUP {
					os.Exit(1)
				}
			}
		}
	}()
	if err := rt.Run(ctx); err != nil {
		fmt.Fprintln(stderr, "orbd:", err)
		return 1
	}
	return 0
}

func logLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
