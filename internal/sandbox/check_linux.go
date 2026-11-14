//go:build linux

package sandbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Check 是自检的一项结果（`orynd coordinator-host --selftest` 每项打印一行 JSON）。
type Check struct {
	Name   string `json:"check"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// CheckOptions 描述自检的输入；须在 Apply 之后调用 Checks。
type CheckOptions struct {
	Home      string   // 沙箱内唯一可写的目录
	DenyRead  []string // 必须读不到的文件
	DenyWrite []string // 必须建不出的文件（home 之外）
	DenyDial  []string // 必须连不上的 TCP 地址
	DenyExec  []string // 必须执行失败的命令行（缺省 /bin/sh -c true）
	Exec      []string // 必须执行成功的命令行（为空则跳过）
	Port      uint16   // 唯一允许的 TCP 端口：在 127.0.0.1 上监听并自连
}

// Checks 在已施加沙箱的进程里逐项验证隔离是否生效：「必须失败」的操作失败、「必须成功」的操作成功，
// 且 /proc/self/status 显示 no_new_privs、seccomp 过滤模式、能力集与边界集为空。
func Checks(ctx context.Context, o CheckOptions) []Check {
	var out []Check
	add := func(name string, ok bool, detail string) {
		out = append(out, Check{Name: name, OK: ok, Detail: detail})
	}
	// mustFail 把操作的错误当作「通过」。
	mustFail := func(name string, err error) {
		if err == nil {
			add(name, false, "unexpectedly succeeded")
			return
		}
		add(name, !errors.Is(err, errSetup), err.Error())
	}
	mustSucceed := func(name string, err error) {
		if err != nil {
			add(name, false, err.Error())
			return
		}
		add(name, true, "")
	}

	// ---- 必须失败 ----
	for _, p := range o.DenyRead {
		_, err := os.ReadFile(p)
		mustFail("read "+p, err)
	}
	_, err := os.ReadDir("/")
	mustFail("readdir /", err)
	for _, p := range o.DenyWrite {
		err := os.WriteFile(p, []byte("x"), 0o600)
		if err == nil {
			_ = os.Remove(p)
		}
		mustFail("write "+p, err)
	}
	denyExec := o.DenyExec
	if len(denyExec) == 0 {
		denyExec = []string{"/bin/sh", "-c", "true"}
	}
	mustFail("exec "+denyExec[0], runQuiet(ctx, denyExec))
	for _, addr := range o.DenyDial {
		d := net.Dialer{Timeout: 2 * time.Second}
		c, err := d.DialContext(ctx, "tcp", addr)
		if c != nil {
			_ = c.Close()
		}
		mustFail("tcp "+addr, err)
	}
	for _, s := range []struct {
		name               string
		domain, typ, proto int
	}{
		{"udp socket", unix.AF_INET, unix.SOCK_DGRAM, 0},
		{"unix socket", unix.AF_UNIX, unix.SOCK_STREAM, 0},
		{"netlink socket", unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_ROUTE},
		{"mptcp socket", unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_MPTCP},
	} {
		fd, err := unix.Socket(s.domain, s.typ|unix.SOCK_CLOEXEC, s.proto)
		if err == nil {
			_ = unix.Close(fd)
		}
		mustFail(s.name, err)
	}
	mustFail("unshare(CLONE_NEWUSER)", unix.Unshare(unix.CLONE_NEWUSER))
	mustFail("mount", unix.Mount("none", o.Home, "tmpfs", 0, ""))
	if len(o.Exec) > 0 {
		mustFail("exec copy in home", execCopy(ctx, o.Home, o.Exec[0]))
	}
	target := os.Getppid()
	if target <= 0 {
		target = 1 // PID 命名空间里父进程不可见
	}
	mustFail("ptrace attach "+strconv.Itoa(target), ptraceAttach(target))
	_, _, e := unix.Syscall(unix.SYS_CLONE3, 0, 0, 0)
	add("clone3", e == unix.ENOSYS, fmt.Sprintf("errno %v (want ENOSYS)", e))
	if abi, _ := Probe(); abi >= 6 {
		if pp := os.Getppid(); pp > 0 {
			mustFail("kill(ppid, 0)", unix.Kill(pp, 0))
		}
	}

	// ---- 必须成功 ----
	mustSucceed("write and rename in home", writeAndRename(o.Home))
	if len(o.Exec) > 0 {
		mustSucceed("exec "+strings.Join(o.Exec, " "), runQuiet(ctx, o.Exec))
	}
	if o.Port != 0 {
		mustSucceed("listen and dial 127.0.0.1:"+strconv.Itoa(int(o.Port)), selfDial(ctx, o.Port))
	}

	// ---- 进程状态 ----
	st, err := readStatus()
	if err != nil {
		add("/proc/self/status", false, err.Error())
		return out
	}
	for _, want := range []struct{ key, val string }{
		{"NoNewPrivs", "1"},
		{"Seccomp", "2"},
		{"CapEff", "0000000000000000"},
		{"CapBnd", "0000000000000000"},
		{"CapPrm", "0000000000000000"},
		{"CapAmb", "0000000000000000"},
	} {
		got := st[want.key]
		if want.key == "CapBnd" && got != want.val && !boundingDropped.Load() && st["NoNewPrivs"] == "1" && os.Getuid() != 0 {
			add("status CapBnd", true, fmt.Sprintf("CapBnd=%s: no CAP_SETPCAP to drop it; harmless with uid %d, empty capability sets and no_new_privs", got, os.Getuid()))
			continue
		}
		add("status "+want.key, got == want.val, fmt.Sprintf("%s=%s (want %s)", want.key, got, want.val))
	}
	return out
}

func runQuiet(ctx context.Context, argv []string) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	if out, err := cmd.CombinedOutput(); err != nil {
		if len(out) > 0 {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out[:min(len(out), 300)])))
		}
		return err
	}
	return nil
}

func ptraceAttach(pid int) error {
	_, _, e := unix.Syscall6(unix.SYS_PTRACE, unix.PTRACE_ATTACH, uintptr(pid), 0, 0, 0, 0)
	if e != 0 {
		return e
	}
	// 意外成功时立即脱离，免得目标停住。
	_, _, _ = unix.Syscall6(unix.SYS_PTRACE, unix.PTRACE_DETACH, uintptr(pid), 0, 0, 0, 0)
	return nil
}

// writeAndRename 在 home 里建两个子目录、写文件并跨目录改名（需要 Landlock 的 refer 权限）。
func writeAndRename(home string) error {
	dir, err := os.MkdirTemp(home, "selftest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	for _, d := range []string{a, b} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(a, "f"), []byte("ok"), 0o600); err != nil {
		return err
	}
	return os.Rename(filepath.Join(a, "f"), filepath.Join(b, "f"))
}

func selfDial(ctx context.Context, port uint16) error {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	if err != nil {
		return err
	}
	defer ln.Close()
	accepted := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			_ = c.Close()
		}
		accepted <- err
	}()
	d := net.Dialer{Timeout: 2 * time.Second}
	c, err := d.DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		return err
	}
	_ = c.Close()
	select {
	case err := <-accepted:
		return err
	case <-time.After(2 * time.Second):
		return errors.New("accept timed out")
	}
}

func readStatus() (map[string]string, error) {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if ok {
			m[k] = strings.TrimSpace(v)
		}
	}
	return m, sc.Err()
}

// execCopy 把一个允许执行的程序复制进 home 再执行：home 可写但不可执行，必须失败。
func execCopy(ctx context.Context, home, bin string) error {
	b, err := os.ReadFile(bin)
	if err != nil {
		return fmt.Errorf("read %s: %w (cannot set up the check)", bin, errSetup)
	}
	p := filepath.Join(home, ".selftest-exec")
	defer os.Remove(p)
	if err := os.WriteFile(p, b, 0o700); err != nil {
		return fmt.Errorf("write %s: %w (cannot set up the check)", p, errSetup)
	}
	return runQuiet(ctx, []string{p, "--version"})
}

// errSetup 标记自检项本身没能搭起来（这种失败不代表沙箱生效，按不通过处理）。
var errSetup = errors.New("setup failed")
