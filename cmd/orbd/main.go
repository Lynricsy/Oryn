// orbd 是 orb 内的守护进程（计划 C2 / C4 / H3.3）：连接控制面、监督 omp、复制会话条目。
//
//	orbd run         启动守护进程（读取 ORYN_URL、ORYN_ORB_ID、ORYN_ORB_TOKEN；可选 ORYN_ORB_CA_PEM、ORYN_AGENT_BIN、ORYN_LOG_LEVEL）
//	orbd run --runner --dir <目录> … [--terminal] [--token-file <文件>] [--allow-root]
//	                 runner 形态（M11b）：用户自己的机器主动接入。读取 ORYN_URL 与 ORYN_RUNNER_TOKEN（缺省读
//	                 --token-file，再缺省 $HOME/.config/oryn/runner.token；必须以 ort_ 开头），经 whoami 得知 orb id。
//	                 启动时下载并校验钉住的 Oryn Core 到 ~/.oryn/core/<tag>/omp（ORYN_CORE_BASE_URL 指向镜像），
//	                 常驻监听出站通道 127.0.0.1:7811（ORYN_EGRESS_ADDR 可改）与 SSH 网关 127.0.0.1:7822（M21）
//	orbd --version   打印版本
//
// Firecracker guest（M11b）：以 PID 1 且无参数运行时进入 guest-init（内核 init=/usr/local/bin/orbd），
// 换根后以 `orbd guest-init-stage2` 继续。
//
// 信号：SIGTERM / SIGINT 优雅停止全部 agent 后退出 0；SIGHUP 立即丢弃并重建 link。
package main

import (
	"context"
	"errors"
	"flag"
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

func main() {
	maybeGuestInit()
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// geteuid 可在测试中替换（runner 以 root 运行时拒绝）。
var geteuid = os.Geteuid

// runOptions 是 `orbd run` 的命令行参数。
type runOptions struct {
	runner    bool
	allowRoot bool
	opts      orbdrt.RunnerOptions
}

// parseRun 解析 `orbd run` 之后的参数；不带 --runner 时任何参数都是错误（行为与 M11a 相同）。
func parseRun(args []string, stderr io.Writer) (runOptions, error) {
	var o runOptions
	fs := flag.NewFlagSet("orbd run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&o.runner, "runner", false, "run as a runner on this machine")
	fs.Func("dir", "directory served to threads (repeatable; runner only)", func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New("empty directory")
		}
		o.opts.Dirs = append(o.opts.Dirs, s)
		return nil
	})
	fs.BoolVar(&o.opts.Terminal, "terminal", false, "enable the terminal panel (runner only)")
	fs.StringVar(&o.opts.TokenFile, "token-file", "", "runner token file when ORYN_RUNNER_TOKEN is unset (default $HOME/.config/oryn/runner.token)")
	fs.BoolVar(&o.allowRoot, "allow-root", false, "allow a runner to run as root (runner only)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if !o.runner {
		var set []string
		fs.Visit(func(f *flag.Flag) { set = append(set, "--"+f.Name) })
		if len(set) > 0 {
			return o, fmt.Errorf("%s requires --runner", strings.Join(set, ", "))
		}
		return o, nil
	}
	if len(o.opts.Dirs) == 0 {
		return o, errors.New("--runner requires at least one --dir")
	}
	return o, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: orbd run [--runner --dir <path> … [--terminal] [--token-file <path>] [--allow-root]] | orbd --version")
		return 2
	}
	switch args[0] {
	case "--version", "-version", "version":
		fmt.Fprintln(stdout, "orbd", version.Version)
		return 0
	case guestStage2Cmd:
		return guestStage2(stderr)
	case "run":
	default:
		fmt.Fprintf(stderr, "orbd: unknown command %q\n", args[0])
		return 2
	}
	ro, err := parseRun(args[1:], stderr)
	if err != nil {
		fmt.Fprintln(stderr, "orbd:", err)
		return 2
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: logLevel(os.Getenv("ORYN_LOG_LEVEL"))})))

	var cfg orbdrt.Config
	if ro.runner {
		// runner 完全信任控制面：谁控制了 orynd 就能以本用户身份执行任意命令，因此默认不以 root 运行。
		if geteuid() == 0 && !ro.allowRoot {
			fmt.Fprintln(stderr, "orbd: refusing to run a runner as root (the control plane could run anything as root); pass --allow-root to override")
			return 2
		}
		cfg, err = orbdrt.RunnerConfigFromEnv(version.Version, ro.opts)
	} else {
		cfg, err = orbdrt.ConfigFromEnv(version.Version)
	}
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
