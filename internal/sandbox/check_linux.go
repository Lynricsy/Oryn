//go:build linux

package sandbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Check 是自检的一项结果（`orynd coordinator-host --selftest` 每项打印一行 JSON）。
type Check struct {
	Name   string `json:"check"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// ListenHost 在宿主回环上建一个临时 TCP 监听，作为自检的 HostListener（白名单外、真实在监听的端口）。尽量打开
// TCP Fast Open（失败不报错）：沙箱里的 TFO 若绕过了规则，数据会随 SYN 到达，由 HostReceived 发现。
func ListenHost() (*net.TCPListener, error) {
	lc := net.ListenConfig{Control: func(_, _ string, rc syscall.RawConn) error {
		return rc.Control(func(fd uintptr) { _ = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_FASTOPEN, 16) })
	}}
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	return l.(*net.TCPListener), nil
}

// HostReceived 在沙箱进程退出之后调用：连上过的连接留在 accept 队列里，同步 accept 一次（不与后台 accept 赛跑），
// 有连接则读出随之到达的数据。返回 nil 表示什么也没收到。
func HostReceived(l *net.TCPListener) error {
	_ = l.SetDeadline(time.Now().Add(50 * time.Millisecond))
	c, err := l.AcceptTCP()
	if err != nil {
		return nil
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 256)
	n, _ := c.Read(buf)
	return fmt.Errorf("accepted a connection from the sandbox (data %q)", buf[:n])
}

// CheckOptions 描述自检的输入；须在 Apply 之后调用 Checks（Listener 在 Apply 之前建好）。
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
	// Listener 是施加沙箱之前在 127.0.0.1:<白名单端口> 上建好的监听（模型代理的同款，M9 L3）：沙箱内自连它必须成功，
	// 而在 0.0.0.0 / [::] 上 bind 同一端口必须失败。
	Listener net.Listener
	// LandlockTCP 表示 Landlock 管辖 TCP（ABI ≥ 4）：此时 bind 必须是 Landlock 的拒绝（EACCES），端口被占用的
	// EADDRINUSE 不算数；否则（只靠网络命名空间）任何错误都算失败。
	LandlockTCP bool
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
		// TCP Fast Open 把数据放在 SYN 里：两种发起方式都必须连不上，宿主监听端什么也收不到（M6，由调用方断言）。
		mustFail("tcp fastopen sendto "+o.HostListener, fastOpenSendto(o.HostListener))
		mustFail("tcp fastopen connect "+o.HostListener, fastOpenConnect(o.HostListener))
	}
	if o.Listener != nil {
		_, port, _ := net.SplitHostPort(o.Listener.Addr().String())
		for _, host := range []string{"0.0.0.0", "::"} {
			addr := net.JoinHostPort(host, port)
			err := bindTCP(addr)
			if err != nil && o.LandlockTCP && !errors.Is(err, unix.EACCES) && !errors.Is(err, unix.EAFNOSUPPORT) && !errors.Is(err, errSetup) {
				add("bind "+addr, false, fmt.Sprintf("%v (want EACCES from landlock)", err))
				continue
			}
			mustFail("bind "+addr, err)
		}
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
	// 改其它进程的资源上限、调度与 nice 值（M5）：同一用户时只有 seccomp 挡得住。各项都先读出现值再原样写回，
	// 万一成功也不改变目标。
	mustFail("prlimit64("+strconv.Itoa(target)+")", prlimitOther(target))
	mustFail("setpriority(PRIO_PROCESS, "+strconv.Itoa(target)+")", setpriorityOther(target))
	mustFail("sched_setaffinity("+strconv.Itoa(target)+")", setaffinityOther(target))
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
	if o.Listener != nil {
		mustSucceed("dial pre-bound "+o.Listener.Addr().String(), dialListener(ctx, o.Listener))
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

// dialListener 自连预先建好的监听并 accept 到这条连接。
func dialListener(ctx context.Context, ln net.Listener) error {
	if tl, ok := ln.(*net.TCPListener); ok {
		_ = tl.SetDeadline(time.Now().Add(5 * time.Second))
		defer func() { _ = tl.SetDeadline(time.Time{}) }()
	}
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
	return <-accepted
}

// sockaddrOf 把 host:port 转成 unix.Sockaddr 与地址族。
func sockaddrOf(addr string) (unix.Sockaddr, int, error) {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return nil, 0, fmt.Errorf("%v: %w (cannot set up the check)", err, errSetup)
	}
	if ap.Addr().Is4() {
		return &unix.SockaddrInet4{Port: int(ap.Port()), Addr: ap.Addr().As4()}, unix.AF_INET, nil
	}
	return &unix.SockaddrInet6{Port: int(ap.Port()), Addr: ap.Addr().As16()}, unix.AF_INET6, nil
}

// bindTCP 建一个 TCP socket 并 bind 到 addr（不设 SO_REUSEADDR）。
func bindTCP(addr string) error {
	sa, family, err := sockaddrOf(addr)
	if err != nil {
		return err
	}
	fd, err := unix.Socket(family, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if errors.Is(err, unix.EAFNOSUPPORT) {
		return err // 内核没有该地址族（例如禁用了 IPv6）：根本绑不上
	} else if err != nil {
		return fmt.Errorf("socket: %v: %w (cannot set up the check)", err, errSetup)
	}
	defer unix.Close(fd)
	return unix.Bind(fd, sa)
}

// fastOpenSendto 以 sendto(MSG_FASTOPEN) 向 addr 发起带数据的连接（不先 connect）。
func fastOpenSendto(addr string) error {
	sa, family, err := sockaddrOf(addr)
	if err != nil {
		return err
	}
	fd, err := unix.Socket(family, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("socket: %v: %w (cannot set up the check)", err, errSetup)
	}
	defer unix.Close(fd)
	return unix.Sendto(fd, []byte("oryn-selftest-tfo"), unix.MSG_FASTOPEN, sa)
}

// fastOpenConnect 置 TCP_FASTOPEN_CONNECT 后 connect，再写数据。
func fastOpenConnect(addr string) error {
	sa, family, err := sockaddrOf(addr)
	if err != nil {
		return err
	}
	fd, err := unix.Socket(family, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("socket: %v: %w (cannot set up the check)", err, errSetup)
	}
	defer unix.Close(fd)
	if err := unix.SetsockoptInt(fd, unix.IPPROTO_TCP, unix.TCP_FASTOPEN_CONNECT, 1); errors.Is(err, unix.EOPNOTSUPP) {
		return fmt.Errorf("setsockopt TCP_FASTOPEN_CONNECT: %w (client TFO disabled by net.ipv4.tcp_fastopen)", err)
	} else if err != nil {
		return fmt.Errorf("setsockopt TCP_FASTOPEN_CONNECT: %v: %w (cannot set up the check)", err, errSetup)
	}
	if err := unix.Connect(fd, sa); err != nil {
		return err
	}
	_, err = unix.Write(fd, []byte("oryn-selftest-tfo"))
	return err
}

// prlimitOther 读目标进程的 RLIMIT_NOFILE（只读：new_limit 为 NULL）；内核对读同样做权限检查。
func prlimitOther(pid int) error {
	var old unix.Rlimit
	return unix.Prlimit(pid, unix.RLIMIT_NOFILE, nil, &old)
}

// setpriorityOther 把目标进程的 nice 值原样写回。
func setpriorityOther(pid int) error {
	prio, err := unix.Getpriority(unix.PRIO_PROCESS, pid)
	if err != nil {
		return fmt.Errorf("getpriority: %v: %w (cannot set up the check)", err, errSetup)
	}
	// 原始系统调用返回 20 - nice。
	return unix.Setpriority(unix.PRIO_PROCESS, pid, 20-prio)
}

// setaffinityOther 把目标进程的 CPU 亲和性原样写回。
func setaffinityOther(pid int) error {
	var set unix.CPUSet
	if err := unix.SchedGetaffinity(pid, &set); err != nil {
		return fmt.Errorf("sched_getaffinity: %v: %w (cannot set up the check)", err, errSetup)
	}
	return unix.SchedSetaffinity(pid, &set)
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
