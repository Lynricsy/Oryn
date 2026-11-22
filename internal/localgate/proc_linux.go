//go:build linux

package localgate

import (
	"os"
	"syscall"
)

// sysProcAttr 返回子进程属性：独立进程组、父进程死亡即 SIGKILL；pidns 为真时另建 PID / IPC / UTS 命名空间
// （非 root 时同时建 user namespace，把自己映射为其中的 root）。不建网络命名空间：网络由 Landlock 的 TCP 规则限制，
// 子进程要连回环上的代理。
func sysProcAttr(pidns bool) *syscall.SysProcAttr {
	a := &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if !pidns {
		return a
	}
	a.Cloneflags = syscall.CLONE_NEWPID | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS
	if os.Geteuid() != 0 {
		a.Cloneflags |= syscall.CLONE_NEWUSER
		a.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Geteuid(), Size: 1}}
		a.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getegid(), Size: 1}}
		a.GidMappingsEnableSetgroups = false
	}
	return a
}
