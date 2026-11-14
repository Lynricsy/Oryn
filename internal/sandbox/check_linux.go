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
	// HostListener 是沙箱外（宿主上）真实在监听的白名单外 TCP 地址：必须连不上（端口 1 一类没人监听的地址
	// 只能证明连接被拒，证明不了规则生效）。
	HostListener string
	DenyExec     []string // 必须执行失败的命令行（缺省 /bin/sh -c true）
	Exec         []string // 必须执行成功的命令行（为空则跳过）
	Port         uint16   // 唯一允许的 TCP 端口：在 127.0.0.1 上监听并自连
	// DenyAbstract 是沙箱外（宿主上）监听的 abstract unix socket 名（不带前导 @ 或 NUL）：沙箱里以 stream 与
	// datagram 两种方式 connect 都必须失败（AF_UNIX 一律被 seccomp 拒绝）。
	DenyAbstract []string
	// DenyUnixDgram 是沙箱外（不在 Landlock 白名单里）的路径型 unix datagram socket（宿主上建、权限 0666，像 /dev/log）：
	// 沙箱里 connect 与 sendto 都必须失败。
	DenyUnixDgram []string
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
		mustFail("tcp "+addr, dialTCP(ctx, addr))
	}
	if o.HostListener != "" {
		mustFail("tcp host listener "+o.HostListener, dialTCP(ctx, o.HostListener))
	}
	for _, s := range []struct {
		name               string
		domain, typ, proto int
	}{
		{"udp socket", unix.AF_INET, unix.SOCK_DGRAM, 0},
		{"unix socket", unix.AF_UNIX, unix.SOCK_STREAM, 0},
		{"unix dgram socket", unix.AF_UNIX, unix.SOCK_DGRAM, 0},
		{"netlink socket", unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_ROUTE},
		{"mptcp socket", unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_MPTCP},
	} {
		fd, err := unix.Socket(s.domain, s.typ|unix.SOCK_CLOEXEC, s.proto)
		if err == nil {
			_ = unix.Close(fd)
		}
		mustFail(s.name, err)
	}
	// socketpair 只放行 AF_UNIX stream（Bun 启动需要）；datagram 与 seqpacket 必须失败。
	for _, typ := range []struct {
		name string
		typ  int
	}{{"dgram", unix.SOCK_DGRAM}, {"seqpacket", unix.SOCK_SEQPACKET}} {
		mustFail("unix socketpair "+typ.name, socketpair(typ.typ))
	}
	for _, p := range o.DenyUnixDgram {
		// 放行的 stream socketpair 生来已连接：拿它 sendto 别处也必须失败。
		mustFail("sendto unix dgram "+p+" via stream socketpair", pairSendto(p))
	}
	for _, name := range o.DenyAbstract {
		for _, typ := range []struct {
			name string
			typ  int
		}{{"stream", unix.SOCK_STREAM}, {"dgram", unix.SOCK_DGRAM}} {
			mustFail("connect abstract unix "+typ.name+" @"+name, connectAbstract(typ.typ, name))
		}
	}
	for _, p := range o.DenyUnixDgram {
		mustFail("connect unix dgram "+p, dgramTo(p, false))
		mustFail("sendto unix dgram "+p, dgramTo(p, true))
	}
	mustFail("truncate(2) by path", truncateInHome(o.Home))
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
	// 父进程（orynd）可见时必须发不了信号：Requirement 保证了 ABI ≥ 6 的信号作用域或独立用户（二者之一）。
	if pp := os.Getppid(); pp > 0 {
		mustFail("kill(ppid, 0)", unix.Kill(pp, 0))
	}

	// ---- 必须成功 ----
	mustSucceed("write and rename in home", writeAndRename(o.Home))
	mustSucceed("flock and OFD lock in home", fileLocks(o.Home))
	mustSucceed("unix socketpair stream (Bun)", socketpair(unix.SOCK_STREAM))
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

// connectAbstract 以给定类型的 AF_UNIX socket connect 一个 abstract 名字。
func connectAbstract(typ int, name string) error {
	fd, err := unix.Socket(unix.AF_UNIX, typ|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return unix.Connect(fd, &unix.SockaddrUnix{Name: "@" + name})
}

// truncateInHome 在 home 里建一个文件再按路径 truncate：home 可写，但 truncate(2) 被 seccomp 拒绝
// （低于 ABI 3 的 Landlock 管不到它）。
func truncateInHome(home string) error {
	f, err := os.CreateTemp(home, "selftest-truncate-")
	if err != nil {
		return fmt.Errorf("create in %s: %w (cannot set up the check)", home, errSetup)
	}
	_ = f.Close()
	defer os.Remove(f.Name())
	return unix.Truncate(f.Name(), 0)
}

// fileLocks 在 home 里对同一文件取 flock 与 OFD 写锁，再验证 omp 原生文件锁（PI_FILE_LOCK_BACKEND=sidecar：
// 旁路文件上的 OFD 写锁）要用到的语义：另一个打开文件描述取不到写锁，且 F_OFD_GETLK 看得到持有者。
func fileLocks(home string) error {
	f, err := os.CreateTemp(home, "selftest-lock-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("flock: %w", err)
	}
	lk := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	if err := unix.FcntlFlock(f.Fd(), unix.F_OFD_SETLK, &lk); err != nil {
		return fmt.Errorf("fcntl(F_OFD_SETLK): %w", err)
	}
	g, err := os.OpenFile(f.Name(), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer g.Close()
	lk2 := unix.Flock_t{Type: unix.F_WRLCK}
	if err := unix.FcntlFlock(g.Fd(), unix.F_OFD_SETLK, &lk2); !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EACCES) {
		return fmt.Errorf("second F_OFD_SETLK: %v (want EAGAIN)", err)
	}
	q := unix.Flock_t{Type: unix.F_RDLCK}
	if err := unix.FcntlFlock(g.Fd(), unix.F_OFD_GETLK, &q); err != nil {
		return fmt.Errorf("fcntl(F_OFD_GETLK): %w", err)
	}
	if q.Type != unix.F_WRLCK {
		return fmt.Errorf("F_OFD_GETLK does not see the holder (type %d)", q.Type)
	}
	return nil
}

// socketpair 建一对给定类型的 AF_UNIX socket 并立即关闭。
func socketpair(typ int) error {
	fds, err := unix.Socketpair(unix.AF_UNIX, typ|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	_ = unix.Close(fds[0])
	_ = unix.Close(fds[1])
	return nil
}

// pairSendto 用一对 stream socketpair 的一端 sendto 路径型 socket p。
func pairSendto(p string) error {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("socketpair: %v: %w (cannot set up the check)", err, errSetup)
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	return unix.Sendto(fds[0], []byte("oryn-selftest"), 0, &unix.SockaddrUnix{Name: p})
}

func dialTCP(ctx context.Context, addr string) error {
	d := net.Dialer{Timeout: 2 * time.Second}
	c, err := d.DialContext(ctx, "tcp", addr)
	if c != nil {
		_ = c.Close()
	}
	return err
}

// dgramTo 用一个 AF_UNIX datagram socket 联系路径型 socket p：send 为假时 connect，为真时（不 connect）sendto。
func dgramTo(p string, send bool) error {
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if send {
		return unix.Sendto(fd, []byte("oryn-selftest"), 0, &unix.SockaddrUnix{Name: p})
	}
	return unix.Connect(fd, &unix.SockaddrUnix{Name: p})
}
