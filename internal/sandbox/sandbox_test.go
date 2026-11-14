//go:build linux

package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helperEnv 携带助手进程的参数（JSON）；非空即表示当前进程是助手。
const helperEnv = "ORYN_SANDBOX_HELPER"

// helperArgs 是父测试交给助手的参数。
type helperArgs struct {
	Home         string   `json:"home"`
	Port         uint16   `json:"port"`
	DenyRead     []string `json:"denyRead"`
	DenyDial     []string `json:"denyDial"`
	HostListener string   `json:"hostListener"`
	Outside      string   `json:"outside"`
	UID          int      `json:"uid"`
	GID          int      `json:"gid"`
	// MaxABI 把 Landlock ABI 封顶（模拟旧内核）；DenyAbstract 是宿主上监听的 abstract socket 名。
	MaxABI        int      `json:"maxAbi"`
	DenyAbstract  []string `json:"denyAbstract"`
	DenyUnixDgram []string `json:"denyUnixDgram"`
	NetNS         bool     `json:"netns"` // 助手运行在独立的网络命名空间里（没有 PID 命名空间）
}

// helperResult 是助手打印到 stdout 的最后一行。
type helperResult struct {
	Report Report  `json:"report"`
	Checks []Check `json:"checks"`
	Err    string  `json:"err,omitempty"`
}

// RequireSandbox 报告环境是否要求沙箱测试必须真正运行（CI 设置 ORYN_REQUIRE_SANDBOX=1）。
func requireSandbox() bool { return os.Getenv("ORYN_REQUIRE_SANDBOX") == "1" }

// loopbackEnv 非空表示当前进程是 netnsUsable 的探测子进程。
const loopbackEnv = "ORYN_SANDBOX_LOOPBACK_HELPER"

// TestHelperLoopback 只在被 netnsUsable 以子进程方式执行时运行：在新的网络命名空间里拉起回环。
func TestHelperLoopback(t *testing.T) {
	if os.Getenv(loopbackEnv) == "" {
		t.Skip("helper process only")
	}
	if err := LoopbackUp(); err != nil {
		fmt.Println(err)
		os.Exit(3)
	}
	os.Exit(0)
}

// netnsAttr 让子进程跑在独立的网络命名空间里（非 root 时连带用户命名空间，只映射自己）。
func netnsAttr() *syscall.SysProcAttr {
	a := &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNET}
	if os.Geteuid() != 0 {
		a.Cloneflags |= syscall.CLONE_NEWUSER
		a.UidMappings = []syscall.SysProcIDMap{{ContainerID: os.Geteuid(), HostID: os.Geteuid(), Size: 1}}
		a.GidMappings = []syscall.SysProcIDMap{{ContainerID: os.Getegid(), HostID: os.Getegid(), Size: 1}}
		a.GidMappingsEnableSetgroups = false
	}
	return a
}

var (
	netnsOnce sync.Once
	netnsErr  error
)

// netnsUsable 报告本机能否建网络命名空间并拉起回环，判据与协调宿主的探测相同（Host.probe）。
// 不行的环境（例如 GitHub runner：能建命名空间，但 SIOCSIFFLAGS 被拒）里协调宿主走 Landlock 端口规则，
// 强制命名空间的测试模式没有对应的产品路径，应跳过而不是失败。
func netnsUsable() error {
	netnsOnce.Do(func() {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperLoopback$", "-test.v=false")
		cmd.Env = append(os.Environ(), loopbackEnv+"=1")
		cmd.SysProcAttr = netnsAttr()
		if out, err := cmd.CombinedOutput(); err != nil {
			netnsErr = fmt.Errorf("%w: %s", err, bytes.TrimSpace(out))
		}
	})
	return netnsErr
}

// skipUnlessSandbox 在内核不满足条件时跳过；ORYN_REQUIRE_SANDBOX=1 下改为失败。
func skipUnlessSandbox(t *testing.T) int {
	t.Helper()
	abi, err := Probe()
	if abi >= MinABI {
		return abi
	}
	if requireSandbox() {
		t.Fatalf("landlock ABI %d (%v) < %d but ORYN_REQUIRE_SANDBOX=1", abi, err, MinABI)
	}
	t.Skipf("landlock unavailable (abi %d, %v)", abi, err)
	return 0
}

// TestHelperSandbox 只在被 TestSandboxApply 以子进程方式重新执行时运行：降权、施加沙箱、自检，打印结果。
func TestHelperSandbox(t *testing.T) {
	raw := os.Getenv(helperEnv)
	if raw == "" {
		t.Skip("helper process only")
	}
	var a helperArgs
	res := helperResult{}
	emit := func() {
		b, _ := json.Marshal(res)
		_, _ = os.Stdout.Write(append(append([]byte("\nRESULT "), b...), '\n'))
		os.Exit(0)
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		res.Err = err.Error()
		emit()
	}
	execs, err := ExecPaths("/usr/bin/true")
	if err != nil {
		res.Err = err.Error()
		emit()
	}
	if a.NetNS {
		if err := LoopbackUp(); err != nil {
			res.Err = err.Error()
			emit()
		}
	}
	// 模型代理的监听在施加沙箱之前建好（沙箱内不能 bind 任何 TCP 端口，M9 L3）。
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(a.Port))))
	if err != nil {
		res.Err = err.Error()
		emit()
	}
	if err := DropPrivileges(a.UID, a.GID); err != nil {
		res.Err = err.Error()
		emit()
	}
	rep, err := Apply(Policy{
		ReadWrite:     []string{a.Home},
		Exec:          execs,
		ReadOnlyDirs:  []string{"/usr/lib", "/usr/lib64", "/lib", "/lib64", "/proc"},
		ReadOnlyFiles: []string{"/etc/ld.so.cache"},
		TCPPort:       a.Port,
		MaxABI:        a.MaxABI,
		NetNS:         a.NetNS,
		SeparateUID:   a.UID >= 0,
	})
	if err != nil {
		res.Err = err.Error()
		emit()
	}
	res.Report = rep
	res.Report.NetNS = a.NetNS
	res.Checks = Checks(context.Background(), CheckOptions{
		Home:          a.Home,
		DenyRead:      a.DenyRead,
		DenyWrite:     []string{filepath.Join(a.Outside, "written"), filepath.Join(filepath.Dir(a.Home), "escape")},
		DenyDial:      a.DenyDial,
		HostListener:  a.HostListener,
		Exec:          []string{"/usr/bin/true"},
		Listener:      ln,
		LandlockTCP:   rep.LandlockABI >= TCPABI,
		DenyAbstract:  a.DenyAbstract,
		DenyUnixDgram: a.DenyUnixDgram,
	})
	emit()
}

func freePort(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return uint16(port)
}

// sbxMode 是助手进程的隔离条件：maxABI 把 Landlock ABI 封顶（0 = 本机真实 ABI），netns 为真时助手在独立的网络命名空间里，
// sameUID 为真时以 root 运行也不降权（沙箱与父进程同一用户）。wantErr 非空表示 Apply 必须拒绝施加，且原因含该文本。
type sbxMode struct {
	maxABI  int
	netns   bool
	sameUID bool
	wantErr string
}

// TestRequirement 覆盖启动条件的每条门槛：低于 ABI 4 时 TCP 只能靠网络命名空间、低于 ABI 6 时信号只能靠
// PID 命名空间或独立用户。
func TestRequirement(t *testing.T) {
	for _, c := range []struct {
		abi                  int
		netns, pidns, sepUID bool
		want                 string
	}{
		{1, true, true, true, "< 2"},
		{2, true, true, false, ""},
		{3, true, true, false, ""},
		{2, true, false, true, ""},
		{2, true, false, false, "< 6、没有 PID 命名空间"},
		{2, false, false, true, "< 4 且没有网络命名空间"},
		{3, false, true, true, "< 4 且没有网络命名空间"},
		{4, false, false, true, ""},
		{5, false, false, false, "< 6、没有 PID 命名空间"},
		{6, false, false, false, ""},
		{11, false, false, false, ""},
	} {
		err := Requirement(c.abi, c.netns, c.pidns, c.sepUID)
		name := fmt.Sprintf("abi=%d netns=%v pidns=%v separateUID=%v", c.abi, c.netns, c.pidns, c.sepUID)
		if c.want == "" {
			assert.NoError(t, err, name)
		} else if assert.Error(t, err, name) {
			assert.Contains(t, err.Error(), c.want, name)
		}
	}
}

// TestSandboxApply 在真实内核上验证沙箱：白名单外读写、执行、联网、建套接字（AF_UNIX 一律拒绝，含 socketpair）、
// 命名空间、挂载、ptrace、clone3、按路径 truncate、向父进程发信号、提权全部失败；home 内读写与跨目录改名、flock / OFD 锁、
// 白名单程序、白名单端口成功；进程状态符合预期。以 root 运行时先降权到 nobody（65534）。按下面的隔离条件各跑一遍：
// 宿主上的 abstract socket、白名单外路径上的 0666 unix datagram socket 与宿主上监听的白名单外 TCP 端口在每种条件下
// 都必须联系不上（什么也收不到）；Landlock 管不到 TCP（ABI < 4）又没有网络命名空间、或管不到信号（ABI < 6）又没有
// PID 命名空间与独立用户时，Apply 拒绝施加。
func TestSandboxApply(t *testing.T) {
	abi := skipUnlessSandbox(t)
	root := os.Geteuid() == 0
	// 非 root 时助手与父进程同一用户、也没有 PID 命名空间：ABI < 6 一律拒绝。
	signalErr := ""
	if !root {
		signalErr = "< 6、没有 PID 命名空间"
	}
	const tcpErr = "< 4 且没有网络命名空间"
	for _, m := range []struct {
		name string
		mode sbxMode
	}{
		{"real", sbxMode{}},
		{"abi2", sbxMode{maxABI: 2, wantErr: tcpErr}},
		{"abi3", sbxMode{maxABI: 3, wantErr: tcpErr}},
		{"abi4", sbxMode{maxABI: 4, wantErr: signalErr}},
		{"abi5", sbxMode{maxABI: 5, wantErr: signalErr}},
		{"abi6", sbxMode{maxABI: 6}},
		{"abi8", sbxMode{maxABI: 8}},
		{"abi2-netns", sbxMode{maxABI: 2, netns: true, wantErr: signalErr}},
		{"abi3-netns", sbxMode{maxABI: 3, netns: true, wantErr: signalErr}},
		{"abi6-netns", sbxMode{maxABI: 6, netns: true}},
		{"abi5-same-uid", sbxMode{maxABI: 5, sameUID: true, wantErr: "< 6、没有 PID 命名空间"}},
		{"abi6-same-uid", sbxMode{maxABI: 6, sameUID: true}},
	} {
		t.Run(m.name, func(t *testing.T) {
			if m.mode.maxABI > abi {
				t.Skipf("landlock abi %d < %d", abi, m.mode.maxABI)
			}
			if m.mode.sameUID && !root {
				t.Skip("非 root 时总是同一用户（由其它模式覆盖）")
			}
			if m.mode.netns {
				if err := netnsUsable(); err != nil {
					t.Skipf("本机建不了可用的网络命名空间（协调宿主在此会走端口规则）：%v", err)
				}
			}
			res := sandboxApply(t, m.mode)
			if m.mode.wantErr == "" && m.mode.maxABI > 0 {
				assert.Equal(t, m.mode.maxABI, res.Report.LandlockABI)
				assert.Equal(t, m.mode.maxABI >= 6, res.Report.IPCScope)
			}
		})
	}
}

func sandboxApply(t *testing.T, mode sbxMode) helperResult {
	t.Helper()

	uid, gid := -1, -1
	if os.Geteuid() == 0 && !mode.sameUID {
		uid, gid = 65534, 65534
	}
	// home 与「秘密」都放在 /tmp 下自建的目录里：降权后的 nobody 要能进入（t.TempDir 的父目录是 0700）。
	home, err := os.MkdirTemp("", "oryn-sbx-home-")
	require.NoError(t, err)
	outside, err := os.MkdirTemp("", "oryn-sbx-outside-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(home); _ = os.RemoveAll(outside) })
	require.NoError(t, os.Chmod(outside, 0o777)) // DAC 放行，只有 Landlock 能挡住
	secret := filepath.Join(outside, "secret")
	require.NoError(t, os.WriteFile(secret, []byte("s3cret"), 0o644))
	if uid >= 0 {
		require.NoError(t, os.Chown(home, uid, gid))
	}

	// 宿主上一个真实在监听的白名单外 TCP 端口（开了 TFO）：沙箱内必须连不上（只有 Port 放行），且它接不到任何连接与数据。
	other, err := ListenHost()
	require.NoError(t, err)
	t.Cleanup(func() { _ = other.Close() })

	// 宿主上监听的 abstract unix socket（stream 与 datagram）：沙箱内两种类型的 connect 都必须失败。
	abstract := fmt.Sprintf("oryn-sbx-test-%d-%d-%v-%v", os.Getpid(), mode.maxABI, mode.netns, mode.sameUID)
	sl, err := net.Listen("unix", "@"+abstract)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sl.Close() })
	dl, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: "@" + abstract + "-dgram", Net: "unixgram"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dl.Close() })
	// 白名单外路径上的 0666 unix datagram socket（像 /dev/log）：沙箱内 connect / sendto 都必须失败，且它收不到任何东西。
	dgramPath := filepath.Join(outside, "dgram.sock")
	pl, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: dgramPath, Net: "unixgram"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pl.Close() })
	require.NoError(t, os.Chmod(dgramPath, 0o666))

	port := freePort(t)
	args, err := json.Marshal(helperArgs{
		Home: home, Port: port, Outside: outside, UID: uid, GID: gid,
		MaxABI: mode.maxABI, NetNS: mode.netns,
		DenyAbstract: []string{abstract, abstract + "-dgram"}, DenyUnixDgram: []string{dgramPath},
		DenyRead:     []string{secret, "/etc/hostname"},
		DenyDial:     []string{"1.1.1.1:443", "127.0.0.1:1"},
		HostListener: other.Addr().String(),
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperSandbox$", "-test.v=false")
	cmd.Env = append(os.Environ(), helperEnv+"="+string(args))
	if mode.netns {
		cmd.SysProcAttr = netnsAttr()
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "helper failed: %s\n%s", out, stderr.String())

	var res helperResult
	found := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if b, ok := bytes.CutPrefix(sc.Bytes(), []byte("RESULT ")); ok {
			require.NoError(t, json.Unmarshal(b, &res))
			found = true
		}
	}
	require.True(t, found, "no RESULT line in helper output: %s", out)
	if mode.wantErr != "" {
		assert.Contains(t, res.Err, mode.wantErr, "Apply 必须拒绝施加")
		assert.Empty(t, res.Checks)
		return res
	}
	require.Empty(t, res.Err)
	assert.True(t, res.Report.Seccomp)
	assert.GreaterOrEqual(t, res.Report.LandlockABI, MinABI)
	if uid >= 0 {
		assert.Equal(t, uid, res.Report.UID)
	}
	names := map[string]bool{}
	for _, c := range res.Checks {
		names[c.Name] = true
		assert.True(t, c.OK, "check %q failed: %s", c.Name, c.Detail)
		t.Logf("%-40s ok=%v %s", c.Name, c.OK, c.Detail)
	}
	for _, want := range []string{
		"read " + secret, "readdir /", "exec /bin/sh", "exec copy in home", "tcp 1.1.1.1:443", "tcp 127.0.0.1:1",
		"tcp host listener " + other.Addr().String(), "udp socket", "unix socket", "unix dgram socket",
		"unix socketpair stream (Bun)", "unix socketpair dgram", "unix socketpair seqpacket", "netlink socket", "truncate(2) by path",
		"unshare(CLONE_NEWUSER)", "mount", "clone3", "kill(ppid, 0)",
		"write and rename in home", "flock and OFD lock in home", "exec /usr/bin/true",
		"dial pre-bound 127.0.0.1:" + strconv.Itoa(int(port)),
		"bind 0.0.0.0:" + strconv.Itoa(int(port)), "bind [::]:" + strconv.Itoa(int(port)),
		"tcp fastopen sendto " + other.Addr().String(), "tcp fastopen connect " + other.Addr().String(),
		fmt.Sprintf("prlimit64(%d)", os.Getpid()), fmt.Sprintf("setpriority(PRIO_PROCESS, %d)", os.Getpid()),
		fmt.Sprintf("sched_setaffinity(%d)", os.Getpid()),
		"status NoNewPrivs", "status Seccomp", "status CapEff", "status CapBnd",
		"connect abstract unix stream @" + abstract, "connect abstract unix dgram @" + abstract,
		"connect abstract unix stream @" + abstract + "-dgram", "connect abstract unix dgram @" + abstract + "-dgram",
		"connect unix dgram " + dgramPath, "sendto unix dgram " + dgramPath, "sendto unix dgram " + dgramPath + " via stream socketpair",
	} {
		assert.True(t, names[want], "missing check %q", want)
	}
	// 宿主上的监听端什么都没收到。
	buf := make([]byte, 64)
	for _, l := range []*net.UnixConn{pl, dl} {
		_ = l.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, _, rerr := l.ReadFrom(buf)
		assert.Error(t, rerr, "沙箱外的 datagram socket %s 收到了 %q", l.LocalAddr(), buf[:n])
	}
	// 连上过的连接留在 accept 队列里：助手退出后同步 accept 一次（含 TFO 随 SYN 带来的数据）。
	assert.NoError(t, HostReceived(other), "宿主上白名单外的 TCP 端口接到了沙箱的连接")
	// 秘密文件未被改动、home 之外没有写出文件。
	_, err = os.Stat(filepath.Join(outside, "written"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	t.Logf("sandbox report: %+v; %d checks passed", res.Report, len(res.Checks))
	return res
}

func TestExecPaths(t *testing.T) {
	ps, err := ExecPaths("/usr/bin/true")
	if err != nil {
		t.Skipf("no /usr/bin/true: %v", err)
	}
	require.NotEmpty(t, ps)
	for _, p := range ps {
		assert.True(t, filepath.IsAbs(p), p)
		_, err := os.Stat(p)
		assert.NoError(t, err)
	}
	// 当前测试二进制自身：Go 二进制可能是静态的（只有一项）或带解释器（两项）。
	self, err := ExecPaths(os.Args[0])
	require.NoError(t, err)
	assert.NotEmpty(t, self)
	assert.LessOrEqual(t, len(self), 2, strconv.Itoa(len(self)))
}

func TestProbe(t *testing.T) {
	abi := skipUnlessSandbox(t)
	assert.GreaterOrEqual(t, abi, MinABI)
}
