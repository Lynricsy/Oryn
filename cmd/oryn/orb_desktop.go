package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// `oryn orb desktop …`：orb 内经 orbd 本地控制口查看桌面状态、启停桌面上的浏览器（M8）。
// 退出码：0 成功；1 未就绪、桌面未启用或启动失败；2 调用错误（不在 orb 内、orbd 不可达等）。

const (
	exitDesktopOK     = 0
	exitDesktopFailed = 1
	exitDesktopUsage  = 2
)

var orbDesktopCmd = &cobra.Command{
	Use:   "desktop",
	Short: "orb 内：查看桌面状态、启停桌面上的浏览器",
}

var orbDesktopStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "显示桌面状态（显示、浏览器、CDP 地址、Profile）",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, _ []string) {
		asJSON, _ := cmd.Flags().GetBool("json")
		exitIfNot0(desktopStatus(cmd.Context(), asJSON, os.Stdout, os.Stderr))
	},
}

var orbDesktopBrowserCmd = &cobra.Command{
	Use:   "browser",
	Short: "启停桌面上的浏览器",
}

var orbDesktopBrowserStartCmd = &cobra.Command{
	Use:   "start",
	Short: "启动浏览器并等到 DevTools 就绪（至多 30 秒）",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, _ []string) {
		exitIfNot0(desktopBrowser(cmd.Context(), true, os.Stdout, os.Stderr))
	},
}

var orbDesktopBrowserStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "关闭浏览器",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, _ []string) {
		exitIfNot0(desktopBrowser(cmd.Context(), false, os.Stdout, os.Stderr))
	},
}

// desktopCallErr 打印控制口错误并给出退出码：orbd 明确拒绝（未就绪、未启用、启动失败）为 1，其余为 2。
func desktopCallErr(err error, stderr io.Writer) int {
	fmt.Fprintln(stderr, "oryn:", err)
	var e *orbsock.Error
	if errors.As(err, &e) && e.Code != orbsock.CodeInvalidParams && e.Code != orbsock.CodeMethodNotFound {
		return exitDesktopFailed
	}
	return exitDesktopUsage
}

func desktopStatus(ctx context.Context, asJSON bool, stdout, stderr io.Writer) int {
	var res orbsock.DesktopResult
	if err := sockCall(ctx, orbsock.MethodDesktopStatus, nil, &res, nil); err != nil {
		return desktopCallErr(err, stderr)
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			fmt.Fprintln(stderr, "oryn:", err)
			return exitDesktopUsage
		}
		return exitDesktopOK
	}
	ready := "not ready"
	if res.State.DisplayReady {
		ready = "ready"
	}
	profile := "none"
	if res.ProfileName != "" {
		profile = res.ProfileName
	}
	fmt.Fprintf(stdout, "display %s %s\nbrowser %s\ncdp %s\nprofile %s\n", res.Display, ready, res.State.Browser, res.CDPURL, profile)
	return exitDesktopOK
}

func desktopBrowser(ctx context.Context, start bool, stdout, stderr io.Writer) int {
	method := orbsock.MethodDesktopBrowserStop
	if start {
		method = orbsock.MethodDesktopBrowserStart
	}
	var res orbsock.DesktopResult
	if err := sockCall(ctx, method, nil, &res, nil); err != nil {
		return desktopCallErr(err, stderr)
	}
	fmt.Fprintf(stdout, "browser %s\n", res.State.Browser)
	if start && res.State.Browser != "running" {
		return exitDesktopFailed
	}
	return exitDesktopOK
}

func init() {
	orbDesktopStatusCmd.Flags().Bool("json", false, "输出 JSON")
	orbDesktopBrowserCmd.AddCommand(orbDesktopBrowserStartCmd, orbDesktopBrowserStopCmd)
	orbDesktopCmd.AddCommand(orbDesktopStatusCmd, orbDesktopBrowserCmd)
}
