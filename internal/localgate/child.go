package localgate

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/Lynricsy/Oryn/internal/sandbox"
)

// 闸门沙箱里可见的系统路径。Exec 目录可执行且可读（目录规则覆盖其下全部文件，含动态链接器与共享库），并同时列入只读目录
// 以便列目录（解释器按目录找模块）；只读目录与文件不可执行。不包括 /etc 整体（orynd 的配置在那里）、数据目录、/home、/root、/var、/run。
var (
	execDirs = []string{
		"/usr/bin", "/bin", "/usr/sbin", "/sbin", "/usr/local/bin",
		"/usr/lib", "/usr/lib64", "/lib", "/lib64", "/usr/libexec", "/usr/local/lib",
	}
	readOnlyDirs = []string{
		"/usr/share", "/usr/local/share", "/etc/ssl", "/etc/ca-certificates", "/etc/pki", "/etc/alternatives",
		"/proc", "/sys/devices/system/cpu",
	}
	readOnlyFiles = []string{
		"/etc/ld.so.cache", "/etc/localtime", "/etc/passwd", "/etc/group", "/etc/nsswitch.conf", "/etc/hosts",
		"/etc/os-release", "/dev/urandom", "/dev/random", "/dev/zero",
	}
)

// RunChild 是 `orynd schedule-gate --dir <d> --port <p> [--uid u --gid g] [--pidns] -- <命令>` 的主体（不读配置文件）：
// 降权 → 施加沙箱 → 在状态管道写 ok → 以 <d>/gates 为工作目录 exec `/bin/sh -c <命令>`。exec 之前任何一步失败都以
// ExitSandbox 退出，原因写 stderr。
func RunChild(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet(Command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "一次性的可写目录（含 gates/ 与 tmp/）")
	port := fs.Int("port", 0, "回环上正向代理的端口（唯一可连的 TCP 端口）")
	uid := fs.Int("uid", -1, "降权到的 uid（-1 = 不切换用户）")
	gid := fs.Int("gid", -1, "降权到的 gid")
	pidns := fs.Bool("pidns", false, "运行在独立的 PID 命名空间里")
	maxABI := fs.Int("max-landlock-abi", 0, "仅供测试：把使用的 Landlock ABI 封顶")
	if err := fs.Parse(args); err != nil {
		return ExitSandbox
	}
	fail := func(format string, a ...any) int {
		_, _ = fmt.Fprintf(stderr, Command+": "+format+"\n", a...)
		return ExitSandbox
	}
	if fs.NArg() != 1 || *dir == "" || *port < 1 || *port > 65535 {
		return fail("usage: %s --dir <d> --port <p> [--uid u --gid g] [--pidns] -- <command>", Command)
	}
	command := fs.Arg(0)
	proxy := os.Getenv(proxyEnv)
	status := os.NewFile(statusFD, "status")
	if status == nil {
		return fail("fd %d (status) is missing", statusFD)
	}
	if err := sandbox.DropPrivileges(*uid, *gid); err != nil {
		return fail("%v", err)
	}
	gates := filepath.Join(*dir, "gates")
	_, err := sandbox.Apply(sandbox.Policy{
		ReadWrite:     []string{*dir},
		Exec:          append(existing(execDirs), gates),
		ReadOnlyDirs:  append(append([]string{}, readOnlyDirs...), execDirs...),
		ReadOnlyFiles: readOnlyFiles,
		TCPPort:       uint16(*port),
		PIDNS:         *pidns,
		SeparateUID:   *uid >= 0,
		MaxABI:        *maxABI,
	})
	if err != nil {
		return fail("sandbox: %v", err)
	}
	if err := os.Chdir(gates); err != nil {
		return fail("chdir: %v", err)
	}
	if _, err := status.WriteString("ok\n"); err != nil {
		return fail("status: %v", err)
	}
	_ = status.Close()
	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "HOME=" + *dir, "TMPDIR=" + filepath.Join(*dir, "tmp"),
		"http_proxy=" + proxy, "https_proxy=" + proxy, "HTTP_PROXY=" + proxy, "HTTPS_PROXY=" + proxy,
		"ALL_PROXY=" + proxy, "all_proxy=" + proxy,
	}
	err = syscall.Exec("/bin/sh", []string{"sh", "-c", command}, env)
	// exec 失败时状态已报告成功：按命令执行失败退出（127 同 shell 的「找不到命令」）
	_, _ = fmt.Fprintf(stderr, "%s: exec /bin/sh: %v\n", Command, err)
	return 127
}

// existing 返回 paths 里存在的路径（Landlock 的执行规则要求路径存在）。
func existing(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil || !errors.Is(err, os.ErrNotExist) {
			out = append(out, p)
		}
	}
	return out
}
