// orynd 是 Oryn 控制面。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/localgate"
	"github.com/Lynricsy/Oryn/internal/server"
	"github.com/Lynricsy/Oryn/internal/version"
)

func main() {
	// 协调宿主子进程（M9）：在解析全局 flag 与读配置文件之前分派，退出码由它自己决定。
	if len(os.Args) > 1 && os.Args[1] == "coordinator-host" {
		os.Exit(coordinatorHost(os.Args[2:]))
	}
	// 渲染子进程（M14）：同样在读配置之前分派，见 render_exec.go
	if len(os.Args) > 1 && os.Args[1] == "render-exec" {
		os.Exit(renderExec(os.Args[2:]))
	}
	// 定时本机闸门的沙箱子进程：同样在读配置之前分派，见 localgate.RunChild
	if len(os.Args) > 1 && os.Args[1] == localgate.Command {
		os.Exit(localgate.RunChild(os.Args[2:], os.Stderr))
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
	ls, err := srv.Listen()
	if err != nil {
		return err
	}
	return srv.Serve(ctx, ls)
}

// subcommands 是 `orynd --config <file> <子命令> …` 的本机管理命令，由各文件在 init() 中注册。
var subcommands = map[string]func(ctx context.Context, cfg config.Orynd, args []string) error{}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	_ = lv.UnmarshalText([]byte(level))
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv}))
}

// configPath 是本次运行的 --config（子命令用，例如自检把它列入「必须读不到」）。
var configPath string
