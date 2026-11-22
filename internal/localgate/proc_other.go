//go:build !linux

package localgate

import "syscall"

// sysProcAttr 在非 Linux 上只建独立进程组（沙箱不可用，Available 会先报错）。
func sysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
