package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/providerrt"
	"github.com/Lynricsy/Oryn/internal/version"
)

var providerRunCmd = &cobra.Command{
	Use:   "run",
	Short: "运行 orb provider（向控制面拉取任务并在本机执行）",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		path, _ := cmd.Flags().GetString("config")
		allowMultiple, _ := cmd.Flags().GetBool("allow-multiple")
		debug, _ := cmd.Flags().GetBool("debug")
		level := slog.LevelInfo
		if debug {
			level = slog.LevelDebug
		}
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

		cfg, err := config.LoadProvider(path)
		if err != nil {
			return err
		}
		tokenFile := cfg.TokenFile
		if !filepath.IsAbs(tokenFile) {
			// 相对路径按当前工作目录解析（与 orynd 的 data_dir 一致）。
			if tokenFile, err = filepath.Abs(tokenFile); err != nil {
				return err
			}
		}
		raw, err := os.ReadFile(tokenFile)
		if err != nil {
			return fmt.Errorf("读取 token_file: %w", err)
		}
		token := strings.TrimSpace(string(raw))
		if !strings.HasPrefix(token, "opv_") {
			return fmt.Errorf("%s 中不是 provider 令牌（应以 opv_ 开头）", tokenFile)
		}
		drv, err := providerrt.New(cfg)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, os.Interrupt)
		defer stop()
		if pf, ok := drv.(providerrt.Preflighter); ok {
			if err := pf.Preflight(ctx); err != nil {
				return err
			}
		}
		if hk, ok := drv.(providerrt.Housekeeper); ok {
			go hk.Housekeep(ctx)
		}
		r := &providerrt.Runner{
			URL: cfg.URL, Token: token, OrbURL: cfg.OrbURL, Driver: drv,
			Version: version.Version, AllowMultiple: allowMultiple,
		}
		err = r.Run(ctx)
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	},
}

// providerPreflightCmd 只跑驱动的启动前检查并逐项打印结果（不连控制面、不读令牌）。
var providerPreflightCmd = &cobra.Command{
	Use:   "preflight",
	Short: "只运行 provider 驱动的启动前检查并逐项打印结果",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		path, _ := cmd.Flags().GetString("config")
		cfg, err := config.LoadProvider(path)
		if err != nil {
			return err
		}
		drv, err := providerrt.New(cfg)
		if err != nil {
			return err
		}
		return runPreflight(cmd.Context(), cmd.OutOrStdout(), drv)
	},
}

// runPreflight 打印各项预检结果；有失败项时返回错误（退出码非零）。
func runPreflight(ctx context.Context, w io.Writer, drv providerrt.Driver) error {
	var checks []providerrt.PreflightCheck
	switch d := drv.(type) {
	case providerrt.PreflightReporter:
		checks = d.PreflightChecks(ctx)
	case providerrt.Preflighter:
		checks = []providerrt.PreflightCheck{{Name: "预检", Err: d.Preflight(ctx)}}
	default:
		fmt.Fprintln(w, "该驱动没有启动前检查")
		return nil
	}
	failed := 0
	for _, c := range checks {
		switch {
		case c.Err != nil:
			failed++
			fmt.Fprintf(w, "✗ %s: %v\n", c.Name, c.Err)
		case c.Warn != "":
			fmt.Fprintf(w, "! %s: %s\n", c.Name, c.Warn)
		default:
			fmt.Fprintf(w, "✓ %s\n", c.Name)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d 项预检失败", failed)
	}
	return nil
}

func init() {
	providerRunCmd.Flags().String("config", "/etc/oryn/provider.yaml", "provider 配置文件")
	providerRunCmd.Flags().Bool("allow-multiple", false, "允许与同一 provider 的其它实例并存")
	providerRunCmd.Flags().Bool("debug", false, "输出调试日志")
	providerCmd.AddCommand(providerRunCmd)
	providerPreflightCmd.Flags().String("config", "/etc/oryn/provider.yaml", "provider 配置文件")
	providerCmd.AddCommand(providerPreflightCmd)
}
