//go:build linux

package localgate

import (
	"os"
	"syscall"
)

// sysProcAttr 返回子进程属性：独立进程组、父进程死亡即 SIGKILL，并另建 PID / IPC / UTS 命名空间（非 root 时同时建
// user namespace，把自己映射为其中的 root）。子进程是命名空间的 init：它退出或被杀时内核杀掉命名空间里的全部进程，
// 包括 setsid / 换了进程组的后台进程。不建网络命名空间：网络由 Landlock 的 TCP 规则限制，子进程要连回环上的代理。
func sysProcAttr() *syscall.SysProcAttr {
	a := &syscall.SysProcAttr{
		Setpgid: true, Pdeathsig: syscall.SIGKILL,
		Cloneflags: syscall.CLONE_NEWPID | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS,
	}
	if os.Geteuid() != 0 {
		a.Cloneflags |= syscall.CLONE_NEWUSER
		a.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Geteuid(), Size: 1}}
		a.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getegid(), Size: 1}}
		a.GidMappingsEnableSetgroups = false
	}
	return a
}
