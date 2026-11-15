//go:build !linux

package main

import (
	"fmt"
	"io"
)

const guestStage2Cmd = "guest-init-stage2"

// maybeGuestInit：Firecracker guest 只有 Linux。
func maybeGuestInit() {}

func guestStage2(stderr io.Writer) int {
	fmt.Fprintln(stderr, "orbd: guest-init-stage2 is only available on Linux")
	return 2
}
