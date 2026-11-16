//go:build !linux

package render

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// ExecMain 在非 Linux 平台上不可用：渲染依赖 Linux 的降权、Landlock 与进程组管理。
func ExecMain(_ []string, stderr io.Writer) int {
	fmt.Fprintln(stderr, "render-exec: 只支持 Linux")
	return ExecExitCode
}

func procAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

func killGroup(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	_ = p.Kill()
	return nil
}
