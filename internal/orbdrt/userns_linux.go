package orbdrt

import (
	"os/exec"
	"syscall"
)

// userNamespacesUsable 在新的 user namespace 里跑一次 `true`，成功即说明 Chromium 的命名空间沙箱可用。
// Docker 默认的 seccomp 配置会以 EPERM 拒绝（docker 驱动给 orb 换了放行的配置，见 providerrt/orb-seccomp.json）。
func userNamespacesUsable() bool {
	bin, err := exec.LookPath("true")
	if err != nil {
		return false
	}
	cmd := exec.Command(bin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER}
	return cmd.Run() == nil
}
