package render

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
)

// chromiumNames 是 artifacts.render.chromium 为空时依次在 PATH 里找的可执行文件名。
var chromiumNames = []string{"chromium", "chromium-browser", "google-chrome-stable", "google-chrome"}

// findChromium 返回 Chromium 可执行文件的绝对路径：configured 非空时只认它，否则依次在 PATH 里找。
func findChromium(configured string) (string, error) {
	if configured != "" {
		p, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("找不到配置的 Chromium %s：%w", configured, err)
		}
		return filepath.Abs(p)
	}
	for _, n := range chromiumNames {
		if p, err := exec.LookPath(n); err == nil {
			return filepath.Abs(p)
		}
	}
	return "", errors.New("找不到 Chromium：安装 chromium，或在 artifacts.render.chromium 里写明可执行文件路径")
}

// chromiumJob 是一次 Chromium 运行的参数。
type chromiumJob struct {
	Dir           string // 作业目录（HOME、profile 与输出都在这里）
	Port          int    // 强制代理的端口
	URL           string
	Width, Height int
	Scale         float64
	PDF           bool
	// DumpDOM 为真时把渲染后的 DOM 序列化到 stdout（--dump-dom），不写输出文件。
	DumpDOM   bool
	NoSandbox bool
}

// output 返回 Chromium 写出结果的文件。
func (j chromiumJob) output() string {
	if j.PDF {
		return filepath.Join(j.Dir, "out.pdf")
	}
	return filepath.Join(j.Dir, "out.png")
}

// chromiumArgs 返回 Chromium 的参数（不含 argv[0]），逐项固定：全部网络经强制代理（含回环，
// `<-loopback>` 去掉了缺省的回环直连），WebRTC 不走代理之外的 UDP。除非配置了 no_sandbox，绝不传 --no-sandbox。
func chromiumArgs(j chromiumJob) []string {
	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--hide-scrollbars",
		"--mute-audio",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-sync",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-dev-shm-usage",
		"--dns-prefetch-disable",
		// 旧版 Chromium 认前一个名字，新版（探查时的 156）只认后一个；不认识的开关会被忽略，两个都传
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--proxy-server=http://127.0.0.1:" + strconv.Itoa(j.Port),
		"--proxy-bypass-list=<-loopback>",
		"--user-data-dir=" + filepath.Join(j.Dir, "profile"),
		"--window-size=" + strconv.Itoa(j.Width) + "," + strconv.Itoa(j.Height),
		"--force-device-scale-factor=" + strconv.FormatFloat(j.Scale, 'f', -1, 64),
		"--virtual-time-budget=5000",
		"--run-all-compositor-stages-before-draw",
	}
	if j.NoSandbox {
		args = append(args, "--no-sandbox")
	}
	switch {
	case j.DumpDOM:
		// 与截图同一套 --virtual-time-budget：页面脚本（含 React 首次渲染与探针的空闲回调）跑完再序列化（plan-m29 P5）
		args = append(args, "--dump-dom")
	case j.PDF:
		args = append(args, "--print-to-pdf="+j.output(), "--no-pdf-header-footer")
	default:
		args = append(args, "--screenshot="+j.output())
	}
	return append(args, j.URL)
}

// chromiumEnv 返回从零构造的环境变量：HOME 与 XDG 目录都指向作业目录。
func chromiumEnv(dir string) []string {
	return []string{
		"HOME=" + dir,
		"XDG_CONFIG_HOME=" + dir,
		"XDG_CACHE_HOME=" + dir,
		"PATH=/usr/bin:/bin",
		"LANG=C.UTF-8",
	}
}

// ExecCommand 是 orynd 里 render-exec 子命令的名字（`orynd render-exec …`）。
const ExecCommand = "render-exec"

// ExecExitCode 是 render-exec 在执行 Chromium 之前任何一步失败时的退出码（stderr 另有一行原因）。
const ExecExitCode = 70

// ExecArgs 是 `orynd render-exec --uid <n> --gid <n> --port <n> -- <argv…>` 的参数。
type ExecArgs struct {
	UID, GID int
	Port     uint16
	// Argv 是要执行的程序（argv[0] 是绝对路径）与它的参数。
	Argv []string
}

// Args 返回 orynd 的命令行参数（不含 orynd 自己的 argv[0]）。
func (a ExecArgs) Args() []string {
	out := []string{ExecCommand,
		"--uid", strconv.Itoa(a.UID),
		"--gid", strconv.Itoa(a.GID),
		"--port", strconv.Itoa(int(a.Port)),
		"--"}
	return append(out, a.Argv...)
}

// ParseExecArgs 解析 render-exec 之后的参数。uid 必须非 0（不以 root 跑 Chromium），三个参数都必须给，
// `--` 之后的 argv[0] 必须是绝对路径。
func ParseExecArgs(args []string) (ExecArgs, error) {
	fs := flag.NewFlagSet(ExecCommand, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	uid := fs.Int("uid", -1, "")
	gid := fs.Int("gid", -1, "")
	port := fs.Int("port", 0, "")
	if err := fs.Parse(args); err != nil {
		return ExecArgs{}, err
	}
	switch {
	case *uid < 1:
		return ExecArgs{}, errors.New("--uid 必须是非 root 的 uid")
	case *gid < 0:
		return ExecArgs{}, errors.New("--gid 必须给出")
	case *port < 1 || *port > 65535:
		return ExecArgs{}, errors.New("--port 必须在 1–65535 之间")
	}
	// flag 在 `--` 处停下并吞掉它；没有 `--` 时剩余参数从第一个非 flag 开始，这里要求必须有 `--`
	if n := len(args) - fs.NArg(); n == 0 || args[n-1] != "--" {
		return ExecArgs{}, errors.New("要执行的程序必须放在 -- 之后")
	}
	argv := fs.Args()
	if len(argv) == 0 || !filepath.IsAbs(argv[0]) {
		return ExecArgs{}, errors.New("-- 之后的程序必须是绝对路径")
	}
	return ExecArgs{UID: *uid, GID: *gid, Port: uint16(*port), Argv: argv}, nil
}
