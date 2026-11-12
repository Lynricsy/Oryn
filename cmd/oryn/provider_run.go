package main

import (
	"context"
	"errors"
	"fmt"
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

func init() {
	providerRunCmd.Flags().String("config", "/etc/oryn/provider.yaml", "provider 配置文件")
	providerRunCmd.Flags().Bool("allow-multiple", false, "允许与同一 provider 的其它实例并存")
	providerRunCmd.Flags().Bool("debug", false, "输出调试日志")
	providerCmd.AddCommand(providerRunCmd)
}
