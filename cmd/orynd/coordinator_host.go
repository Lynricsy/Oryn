package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/coordhost"
)

// M9 协调宿主（协调宿主分册 H4）：
//   - `orynd coordinator-host …`：协调宿主子进程（由 orynd 自己拉起；不读配置文件，见 coordhost.RunChild）；
//   - `orynd --config <f> coordinator check`：可用性检查 + 沙箱自检。

func init() {
	subcommands["coordinator"] = cmdCoordinator
}

// coordinatorHost 是 `orynd coordinator-host` 的入口，返回进程退出码。
func coordinatorHost(args []string) int {
	return coordhost.RunChild(args, os.Stdout, os.Stderr)
}

// cmdCoordinator 是 `orynd --config <f> coordinator check`：把探测结论、沙箱报告与自检各行（JSON）打到 stdout，
// 全部通过退出 0。
func cmdCoordinator(ctx context.Context, cfg config.Orynd, args []string) error {
	if len(args) != 1 || args[0] != "check" {
		return errors.New("用法：orynd --config <file> coordinator check")
	}
	if err := coordhost.SelfTest(ctx, coordhost.ConfigFrom(cfg, configPath), os.Stdout); err != nil {
		return fmt.Errorf("coordinator check: %w", err)
	}
	fmt.Fprintln(os.Stderr, "coordinator check: ok")
	return nil
}
