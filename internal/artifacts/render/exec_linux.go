//go:build linux

package render

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/Lynricsy/Oryn/internal/sandbox"
)

// renderRlimits 是 Chromium 进程树的资源上限（RLIMIT_CORE 与 RLIMIT_NOFILE 由 sandbox.DropPrivileges 设置）。
var renderRlimits = []struct {
	name     string
	resource int
	value    uint64
}{
	{"RLIMIT_FSIZE", unix.RLIMIT_FSIZE, 256 << 20},
	{"RLIMIT_CPU", unix.RLIMIT_CPU, 120},
}

// ExecMain 是 `orynd render-exec` 的实现，返回进程退出码（成功时不返回：进程被 Chromium 替换）。
// 顺序：降权（仅 root）→ rlimit → no_new_privs → Landlock 只放行连代理端口 → execve。
// 任何一步失败都以 ExecExitCode 退出并在 stderr 写一行原因。
func ExecMain(args []string, stderr io.Writer) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "render-exec: "+format+"\n", a...)
		return ExecExitCode
	}
	a, err := ParseExecArgs(args)
	if err != nil {
		return fail("%v", err)
	}
	uid := -1 // 非 root：不切换用户，只清能力
	switch euid := os.Geteuid(); {
	case euid == 0:
		uid = a.UID
	case euid != a.UID:
		return fail("以 uid %d 运行，不能切换到 uid %d", euid, a.UID)
	}
	if err := sandbox.DropPrivileges(uid, a.GID); err != nil {
		return fail("降权失败：%v", err)
	}
	for _, l := range renderRlimits {
		if err := unix.Setrlimit(l.resource, &unix.Rlimit{Cur: l.value, Max: l.value}); err != nil {
			return fail("%s：%v", l.name, err)
		}
	}
	// 换 uid 会清掉父进程设的 PR_SET_PDEATHSIG，重新设上；设之前父进程已经退出就不再继续
	if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(syscall.SIGKILL), 0, 0, 0); err != nil {
		return fail("PR_SET_PDEATHSIG：%v", err)
	}
	if os.Getppid() == 1 {
		return fail("orynd 已退出")
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fail("PR_SET_NO_NEW_PRIVS：%v", err)
	}
	// 作业目录由 orynd 建好并交给渲染账号；它的上级目录不允许该账号进入时 Chromium 只会报含糊的错，这里先查
	if home := os.Getenv("HOME"); home != "" {
		if err := unix.Access(home, unix.R_OK|unix.W_OK|unix.X_OK); err != nil {
			return fail("渲染账号（uid %d）不能读写作业目录 %s：%v。把 artifacts.render.dir 设到该账号能进入的位置", a.UID, home, err)
		}
	}
	if _, err := sandbox.RestrictConnect(a.Port); err != nil {
		return fail("Landlock 网络规则：%v", err)
	}
	err = syscall.Exec(a.Argv[0], a.Argv, os.Environ())
	return fail("执行 %s：%v", a.Argv[0], err)
}

// procAttr 返回 render-exec 子进程的属性：独立进程组（超时与取消时整组杀掉），orynd 退出时它被 SIGKILL。
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

// killGroup 杀掉以 pid 为组长的整个进程组；组已经不存在时不算错误。
func killGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
