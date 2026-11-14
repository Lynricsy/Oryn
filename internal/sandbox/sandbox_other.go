//go:build !linux

// Package sandbox 在非 Linux 平台上不可用：全部函数返回 ErrUnsupported（只为让本仓库能在其它平台编译）。
package sandbox

import (
	"context"
	"errors"
)

// ErrUnsupported 表示当前平台不支持沙箱（非 Linux）。
var ErrUnsupported = errors.New("sandbox: unsupported platform")

// Policy 是沙箱策略；见 Linux 实现。
type Policy struct {
	ReadWrite     []string
	Exec          []string
	ReadOnlyDirs  []string
	ReadOnlyFiles []string
	TCPPort       uint16
}

// Report 描述实际生效的隔离；见 Linux 实现。
type Report struct {
	LandlockABI int  `json:"landlockAbi"`
	NetNS       bool `json:"netNamespace"`
	PIDNS       bool `json:"pidNamespace"`
	IPCScope    bool `json:"ipcScope"`
	UID         int  `json:"uid"`
	Seccomp     bool `json:"seccomp"`
}

// MinABI 是启动条件要求的最低 Landlock ABI。
const MinABI = 2

func Probe() (int, error)                    { return 0, ErrUnsupported }
func LoopbackUp() error                      { return ErrUnsupported }
func DropPrivileges(uid, gid int) error      { return ErrUnsupported }
func Apply(p Policy) (Report, error)         { return Report{}, ErrUnsupported }
func ExecPaths(bin string) ([]string, error) { return nil, ErrUnsupported }

// Check 是自检的一项结果；见 Linux 实现。
type Check struct {
	Name   string `json:"check"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// CheckOptions 描述自检的输入；见 Linux 实现。
type CheckOptions struct {
	Home      string
	DenyRead  []string
	DenyWrite []string
	DenyDial  []string
	DenyExec  []string
	Exec      []string
	Port      uint16
}

// Checks 在非 Linux 平台上只返回一项失败。
func Checks(_ context.Context, _ CheckOptions) []Check {
	return []Check{{Name: "sandbox", Detail: ErrUnsupported.Error()}}
}
