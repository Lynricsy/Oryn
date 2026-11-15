//go:build linux

package main

import (
	"fmt"
	"io"
	"os"

	"github.com/Lynricsy/Oryn/internal/orbdrt/guestinit"
)

// guestStage2Cmd 是 guest-init 第一阶段换根后 exec 自己时用的子命令。
const guestStage2Cmd = guestinit.Stage2Arg

// maybeGuestInit 在 Firecracker guest 里以 PID 1、无参数运行（内核 init=/usr/local/bin/orbd）时进入
// guest-init 第一阶段；它换根后 exec `orbd guest-init-stage2`，不返回。
func maybeGuestInit() {
	if os.Getpid() == 1 && len(os.Args) == 1 {
		guestinit.Main()
	}
}

// guestStage2 是 guest-init 的第二阶段（只在 PID 1 下有意义）。
func guestStage2(stderr io.Writer) int {
	if os.Getpid() != 1 {
		fmt.Fprintln(stderr, "orbd: guest-init-stage2 must run as PID 1")
		return 2
	}
	guestinit.Stage2()
	return 1
}
