//go:build linux

// Package sandbox 在进程内对自身施加不可逆的操作系统沙箱（M9 协调宿主 H2）：
// Landlock（文件读写 / 执行白名单、TCP 端口、IPC 作用域）+ seccomp（前置架构过滤与系统调用黑名单，AF_UNIX 只留已连接的 stream socketpair）
// + no_new_privs + 清空能力，必要时先降权到独立的非特权用户。
// 网络 / PID 命名空间由调用方在创建子进程时决定（见 internal/coordhost），本包只负责把回环网卡拉起；
// 低版本内核上 Landlock 管不到的部分由命名空间或独立用户补上，补不上就拒绝施加（见 Requirement）。
package sandbox

import (
	"debug/elf"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"unsafe"

	seccomp "github.com/elastic/go-seccomp-bpf"
	"github.com/elastic/go-seccomp-bpf/arch"
	"github.com/landlock-lsm/go-landlock/landlock"
	ll "github.com/landlock-lsm/go-landlock/landlock/syscall"
	"golang.org/x/sys/unix"
	"kernel.org/pub/linux/libs/security/libcap/psx"
)

// ErrUnsupported 表示当前平台不支持沙箱（非 Linux）。
var ErrUnsupported = errors.New("sandbox: unsupported platform")

// Policy 是沙箱策略；路径一律为绝对路径。
type Policy struct {
	ReadWrite     []string // 目录：读写、创建、删除、同目录与跨目录改名（不可执行）
	Exec          []string // 文件：可 execve + 可读
	ReadOnlyDirs  []string // 目录：只读（不可执行）；不存在的忽略
	ReadOnlyFiles []string // 文件：只读；不存在的忽略
	// TCPPort 是唯一允许 connect 的 TCP 端口。沙箱内不能 bind 任何 TCP 端口：需要监听的（模型代理）由调用方在施加沙箱
	// 之前建好再交进来（M9 L3），否则沙箱内可以在非回环地址上监听该端口。
	TCPPort uint16
	// NetNS / PIDNS 表示调用方已把进程放进独立的网络 / PID 命名空间（Landlock 管不到 TCP 或信号时由它们隔离）。
	NetNS bool
	PIDNS bool
	// SeparateUID 表示进程已降权到与控制面（orynd）不同的用户：同用户进程收不到它的信号。
	SeparateUID bool
	// MaxABI 仅供测试：把实际使用的 Landlock ABI 封顶（模拟旧内核）；0 = 不封顶。
	MaxABI int
}

// Report 描述实际生效的隔离（协调宿主写到状态管道，控制面经 GET /api/v1/coordinator 展示）。
type Report struct {
	LandlockABI int  `json:"landlockAbi"`
	NetNS       bool `json:"netNamespace"` // 由调用方填
	PIDNS       bool `json:"pidNamespace"` // 由调用方填
	IPCScope    bool `json:"ipcScope"`     // ABI ≥ 6
	UID         int  `json:"uid"`
	Seccomp     bool `json:"seccomp"`
}

// Probe 返回内核支持的 Landlock ABI 版本；内核不支持或 LSM 未启用时返回 0 与原因。
func Probe() (int, error) {
	v, _, e := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if e != 0 {
		return 0, fmt.Errorf("landlock_create_ruleset: %w", e)
	}
	return int(v), nil
}

// LoopbackUp 把当前网络命名空间的 lo 网卡置为 UP（新建的网络命名空间里它默认是 DOWN）。
// 需要该命名空间里的 CAP_NET_ADMIN，因此必须在 DropPrivileges 之前调用。
func LoopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("loopback: socket: %w", err)
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq("lo")
	if err != nil {
		return fmt.Errorf("loopback: %w", err)
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return fmt.Errorf("loopback: SIOCGIFFLAGS: %w", err)
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP | unix.IFF_RUNNING)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("loopback: SIOCSIFFLAGS: %w", err)
	}
	return nil
}

// DropPrivileges 清空能力边界集、（uid ≥ 0 时）切换到 uid/gid、清空环境能力与全部能力集，
// 并收紧 core 与文件描述符上限。uid < 0 表示不切换用户，只清能力。
// 能力边界集与能力集是线程属性，经 psx 作用于全部线程。
func DropPrivileges(uid, gid int) error {
	// 1. 能力边界集：逐个丢弃（无 CAP_SETPCAP 时 EPERM，忽略）。
	last := 63
	if b, err := os.ReadFile("/proc/sys/kernel/cap_last_cap"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			last = n
		}
	}
	dropped := true
	for c := 0; c <= last; c++ {
		_, _, e := psx.Syscall3(unix.SYS_PRCTL, unix.PR_CAPBSET_DROP, uintptr(c), 0)
		switch e {
		case 0, unix.EINVAL:
		case unix.EPERM:
			dropped = false
		default:
			return fmt.Errorf("drop privileges: PR_CAPBSET_DROP %d: %w", c, e)
		}
	}
	boundingDropped.Store(dropped)
	// 2. 切换用户（syscall.Set* 在 Linux 上作用于全部线程）。
	if uid >= 0 {
		if gid < 0 {
			return errors.New("drop privileges: gid is required with uid")
		}
		if err := syscall.Setgroups(nil); err != nil {
			return fmt.Errorf("drop privileges: setgroups: %w", err)
		}
		if err := syscall.Setresgid(gid, gid, gid); err != nil {
			return fmt.Errorf("drop privileges: setresgid %d: %w", gid, err)
		}
		if err := syscall.Setresuid(uid, uid, uid); err != nil {
			return fmt.Errorf("drop privileges: setresuid %d: %w", uid, err)
		}
		r, e, s := unix.Getresuid()
		if r != uid || e != uid || s != uid {
			return fmt.Errorf("drop privileges: uid is %d/%d/%d after setresuid %d", r, e, s, uid)
		}
	}
	// 3. 环境能力与能力集全部清零。
	if _, _, e := psx.Syscall6(unix.SYS_PRCTL, unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0, 0); e != 0 && e != unix.EINVAL {
		return fmt.Errorf("drop privileges: PR_CAP_AMBIENT_CLEAR_ALL: %w", e)
	}
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if _, _, e := psx.Syscall3(unix.SYS_CAPSET, uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data[0])), 0); e != 0 {
		return fmt.Errorf("drop privileges: capset: %w", e)
	}
	runtime.KeepAlive(&hdr)
	runtime.KeepAlive(&data)
	// 4. 资源上限。
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{}); err != nil {
		return fmt.Errorf("drop privileges: RLIMIT_CORE: %w", err)
	}
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &unix.Rlimit{Cur: 4096, Max: 4096}); err != nil {
		return fmt.Errorf("drop privileges: RLIMIT_NOFILE: %w", err)
	}
	return nil
}

// boundingDropped 记录 DropPrivileges 是否清空了能力边界集。非 root 且没有 user namespace 时没有 CAP_SETPCAP、
// 清不掉；那时边界集无害（uid 非 0、能力集为空、no_new_privs 下 execve 无法获得任何能力），自检据此放宽。
var boundingDropped atomic.Bool

// ExecPaths 返回执行 bin 所需的可执行文件：bin 的真实路径与其 ELF 解释器（PT_INTERP，静态二进制没有）。
func ExecPaths(bin string) ([]string, error) {
	real, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return nil, fmt.Errorf("exec paths: %w", err)
	}
	if real, err = filepath.Abs(real); err != nil {
		return nil, fmt.Errorf("exec paths: %w", err)
	}
	f, err := elf.Open(real)
	if err != nil {
		return nil, fmt.Errorf("exec paths: %s: %w", real, err)
	}
	defer f.Close()
	out := []string{real}
	for _, p := range f.Progs {
		if p.Type != elf.PT_INTERP {
			continue
		}
		b := make([]byte, p.Filesz)
		if _, err := p.ReadAt(b, 0); err != nil {
			return nil, fmt.Errorf("exec paths: read PT_INTERP of %s: %w", real, err)
		}
		interp, err := filepath.EvalSymlinks(strings.TrimRight(string(b), "\x00"))
		if err != nil {
			return nil, fmt.Errorf("exec paths: interpreter of %s: %w", real, err)
		}
		out = append(out, interp)
	}
	return out, nil
}

// readWriteRights 是 ReadWrite 目录得到的权限：常规读写 + 跨目录改名，不含执行。
const readWriteRights = landlock.AccessFSSet(ll.AccessFSWriteFile | ll.AccessFSReadFile | ll.AccessFSReadDir |
	ll.AccessFSRemoveDir | ll.AccessFSRemoveFile | ll.AccessFSMakeChar | ll.AccessFSMakeDir | ll.AccessFSMakeReg |
	ll.AccessFSMakeSock | ll.AccessFSMakeFifo | ll.AccessFSMakeBlock | ll.AccessFSMakeSym | ll.AccessFSTruncate |
	ll.AccessFSRefer)

// Apply 对当前进程（全部线程）施加沙箱；不可逆。返回错误时进程处于未知的半受限状态，调用方必须退出。
func Apply(p Policy) (Report, error) {
	abi, err := Probe()
	if p.MaxABI > 0 && abi > p.MaxABI {
		abi = p.MaxABI
	}
	if err != nil {
		return Report{}, fmt.Errorf("landlock unavailable: %w", err)
	}
	if err := Requirement(abi, p.NetNS, p.PIDNS, p.SeparateUID); err != nil {
		return Report{}, err
	}
	if p.TCPPort == 0 {
		return Report{}, errors.New("sandbox: TCPPort is required")
	}
	// 规则只能用预设处理的权限（预设低于 V3 / V4 时没有 truncate / TCP 规则，缺口由 Requirement 把关）。
	cfg := landlockPreset(abi)
	fs := cfg.HandledAccessFS
	rules := []landlock.Rule{
		landlock.PathAccess(readWriteRights&fs, p.ReadWrite...),
		landlock.PathAccess(ll.AccessFSExecute|ll.AccessFSReadFile, p.Exec...),
		landlock.PathAccess(ll.AccessFSReadFile|ll.AccessFSReadDir, p.ReadOnlyDirs...).IgnoreIfMissing(),
		landlock.PathAccess(ll.AccessFSReadFile, p.ReadOnlyFiles...).IgnoreIfMissing(),
		landlock.PathAccess((ll.AccessFSReadFile|ll.AccessFSWriteFile|ll.AccessFSTruncate)&fs, "/dev/null"),
	}
	if cfg.HandledAccessNet != 0 {
		rules = append(rules, landlock.ConnectTCP(p.TCPPort))
	}
	if err := cfg.BestEffort().Restrict(rules...); err != nil {
		return Report{}, fmt.Errorf("landlock: %w", err)
	}
	if _, err := os.ReadDir("/"); err == nil {
		return Report{}, errors.New("landlock not enforced")
	}
	if err := loadPrefilter(); err != nil {
		return Report{}, err
	}
	if err := loadDenyFilter(); err != nil {
		return Report{}, err
	}
	return Report{LandlockABI: abi, IPCScope: abi >= 6, UID: os.Getuid(), Seccomp: true}, nil
}

// landlockPreset 返回不高于 abi 的 go-landlock 预设（BestEffort 再按内核实际能力降级）。
func landlockPreset(abi int) landlock.Config {
	presets := []landlock.Config{landlock.V1, landlock.V1, landlock.V2, landlock.V3, landlock.V4, landlock.V5,
		landlock.V6, landlock.V7, landlock.V8, landlock.V9, landlock.V10, landlock.V11}
	return presets[min(max(abi, 0), len(presets)-1)]
}

// auditArch 返回本机的 seccomp 架构常量。
func auditArch() (uint32, error) {
	switch runtime.GOARCH {
	case "amd64":
		return unix.AUDIT_ARCH_X86_64, nil
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64, nil
	}
	return 0, fmt.Errorf("sandbox: unsupported architecture %s", runtime.GOARCH)
}

// loadPrefilter 装入手写的前置过滤器：非本机架构（含 x86-64 上的 32 位 int 0x80）与 x32 调用直接杀进程，
// clone3 回 ENOSYS（libc 会退回 clone，主过滤器才能检查其标志位），其余放行。
func loadPrefilter() error {
	a, err := auditArch()
	if err != nil {
		return err
	}
	const (
		ldAbs = unix.BPF_LD | unix.BPF_W | unix.BPF_ABS
		jeq   = unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K
		jge   = unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K
		ret   = unix.BPF_RET | unix.BPF_K
		// struct seccomp_data 的偏移：nr 在 0，arch 在 4。
		offNr   = 0
		offArch = 4
	)
	kill := uint32(unix.SECCOMP_RET_KILL_PROCESS)
	prog := []unix.SockFilter{
		{Code: ldAbs, K: offArch},
		{Code: jeq, Jt: 1, Jf: 0, K: a},
		{Code: ret, K: kill},
		{Code: ldAbs, K: offNr},
	}
	if a == unix.AUDIT_ARCH_X86_64 {
		// x32 调用（nr 带 __X32_SYSCALL_BIT）→ 跳过后面三条，落到末尾的 kill。
		prog = append(prog, unix.SockFilter{Code: jge, Jt: 3, Jf: 0, K: 0x40000000})
	}
	prog = append(prog,
		unix.SockFilter{Code: jeq, Jt: 1, Jf: 0, K: uint32(unix.SYS_CLONE3)},
		unix.SockFilter{Code: ret, K: unix.SECCOMP_RET_ALLOW},
		unix.SockFilter{Code: ret, K: unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)},
		unix.SockFilter{Code: ret, K: kill},
	)
	fprog := unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// Landlock 已对全部线程置 no_new_privs；这里再对当前线程置一次，不依赖 Landlock 的实现细节。
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("seccomp prefilter: no_new_privs: %w", err)
	}
	r, _, e := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&fprog)))
	runtime.KeepAlive(prog)
	if e != 0 {
		return fmt.Errorf("seccomp prefilter: %w", e)
	}
	if r != 0 {
		return fmt.Errorf("seccomp prefilter: thread %d could not be synchronized", r)
	}
	return nil
}

// denied 是无条件拒绝（EPERM）的系统调用；本机架构没有的跳过。truncate 按路径截断不受低于 ABI 3 的 Landlock 管辖。
var denied = []string{
	"ptrace", "process_vm_readv", "process_vm_writev", "kcmp", "pidfd_getfd", "process_madvise",
	"unshare", "setns", "mount", "umount2", "pivot_root", "chroot", "open_tree", "move_mount", "fsopen",
	"fsconfig", "fsmount", "fspick", "mount_setattr", "open_by_handle_at", "name_to_handle_at",
	"kexec_load", "kexec_file_load", "init_module", "finit_module", "delete_module", "reboot", "swapon",
	"swapoff", "syslog", "acct", "settimeofday", "clock_settime", "clock_adjtime", "adjtimex", "sethostname",
	"setdomainname", "quotactl", "iopl", "ioperm",
	"add_key", "request_key", "keyctl", "bpf", "perf_event_open", "userfaultfd", "lookup_dcookie",
	"io_uring_setup", "io_uring_enter", "io_uring_register",
	"shmget", "shmat", "shmctl", "shmdt", "semget", "semop", "semctl", "semtimedop", "msgget", "msgsnd",
	"msgrcv", "msgctl", "mq_open", "mq_unlink",
	"truncate",
}

// socketRules 是 socket(2)、socketpair(2) 与 clone(2) 的拒绝条件（同一项内各条件为「且」，项与项之间为「或」）。
// socket 只放行 TCP：AF_INET / AF_INET6 的 SOCK_STREAM，协议 0 或 IPPROTO_TCP；AF_UNIX 等其它域一律拒绝。
// socketpair 只放行 AF_UNIX 的 SOCK_STREAM（协议 0）：Bun 启动时要建一对（拒绝会 abort），这对 socket 生来
// 已连接，connect / sendto 别处都会失败；datagram 一端却能 sendto 任意路径型 socket，seqpacket 也一并拒绝。
func socketRules() []seccomp.NameWithConditions {
	cond := func(cs ...seccomp.Condition) seccomp.NameWithConditions {
		return seccomp.NameWithConditions{Name: "socket", Conditions: cs}
	}
	pairCond := func(cs ...seccomp.Condition) seccomp.NameWithConditions {
		return seccomp.NameWithConditions{Name: "socketpair", Conditions: cs}
	}
	domainIs := func(d int) seccomp.Condition {
		return seccomp.Condition{Argument: 0, Operation: seccomp.Equal, Value: uint64(d)}
	}
	domainNot := func(d int) seccomp.Condition {
		return seccomp.Condition{Argument: 0, Operation: seccomp.NotEqual, Value: uint64(d)}
	}
	// 类型低 4 位：SOCK_STREAM = 1；高位是 SOCK_CLOEXEC / SOCK_NONBLOCK。
	notStream := seccomp.Condition{Argument: 1, Operation: seccomp.BitsSet, Value: 0x0E}
	protoNot := func(p int) seccomp.Condition {
		return seccomp.Condition{Argument: 2, Operation: seccomp.NotEqual, Value: uint64(p)}
	}
	rules := []seccomp.NameWithConditions{
		cond(domainNot(unix.AF_INET), domainNot(unix.AF_INET6)),
		pairCond(domainNot(unix.AF_UNIX)),
		pairCond(notStream),
		pairCond(protoNot(0)),
	}
	for _, d := range []int{unix.AF_INET, unix.AF_INET6} {
		rules = append(rules,
			cond(domainIs(d), notStream),
			// 协议不是 0 / IPPROTO_TCP（挡 MPTCP 等）。
			cond(domainIs(d), protoNot(0), protoNot(unix.IPPROTO_TCP)),
		)
	}
	return append(rules, seccomp.NameWithConditions{Name: "clone", Conditions: seccomp.ArgumentConditions{
		{Argument: 0, Operation: seccomp.BitsSet, Value: cloneNewFlags},
	}})
}

// requiredSyscalls 在本机架构上必须存在，否则拒绝施加（过滤器会形同虚设）。
var requiredSyscalls = []string{"ptrace", "mount", "unshare", "setns", "bpf", "socket", "socketpair", "truncate", "clone",
	"prlimit64", "setpriority", "sched_setaffinity", "sched_setscheduler", "sched_setparam", "ioprio_set",
	"sendto", "sendmsg", "sendmmsg"}

// fastOpenRules 拒绝带 MSG_FASTOPEN 的发送：TFO 客户端经 sendto / sendmsg / sendmmsg 直接发起连接，
// 不走 security_socket_connect，较旧内核上 Landlock 的 TCP 端口规则因此管不到它（CI 的 ABI 8 内核实测
// 能连到白名单外的宿主端口）。flags 参数位置：sendto 第 3 个、sendmsg 第 2 个、sendmmsg 第 3 个（从 0 计）。
func fastOpenRules() []seccomp.NameWithConditions {
	flag := func(name string, arg uint32) seccomp.NameWithConditions {
		return seccomp.NameWithConditions{Name: name, Conditions: seccomp.ArgumentConditions{
			{Argument: arg, Operation: seccomp.BitsSet, Value: unix.MSG_FASTOPEN},
		}}
	}
	return []seccomp.NameWithConditions{flag("sendto", 3), flag("sendmsg", 2), flag("sendmmsg", 3)}
}

// targetRules 是改动其它进程资源上限、调度与 I/O 优先级的系统调用的拒绝条件（M5）：Landlock 的信号作用域与独立用户
// 都管不到它们，同一用户、没有 PID 命名空间时沙箱能改 orynd 等进程的 RLIMIT 与调度。只放行作用于自身的调用
// （pid / who 为 0，glibc 的 setrlimit / getrlimit 即 prlimit64(0, …)）；setpriority 与 ioprio_set 另要求
// which 为「单个进程」（PRIO_PROCESS = 0、IOPRIO_WHO_PROCESS = 1），按进程组 / 用户的一律拒绝。
func targetRules() []seccomp.NameWithConditions {
	argNot := func(name string, arg uint32, v uint64) seccomp.NameWithConditions {
		return seccomp.NameWithConditions{Name: name, Conditions: seccomp.ArgumentConditions{
			{Argument: arg, Operation: seccomp.NotEqual, Value: v},
		}}
	}
	var rules []seccomp.NameWithConditions
	for _, n := range []string{"prlimit64", "sched_setaffinity", "sched_setscheduler", "sched_setparam", "sched_setattr"} {
		rules = append(rules, argNot(n, 0, 0))
	}
	return append(rules,
		argNot("setpriority", 0, 0), argNot("setpriority", 1, 0),
		argNot("ioprio_set", 0, 1), argNot("ioprio_set", 1, 0),
	)
}

// cloneNewFlags 是 clone 的全部 CLONE_NEW* 标志位。
const cloneNewFlags = unix.CLONE_NEWNS | unix.CLONE_NEWCGROUP | unix.CLONE_NEWUTS | unix.CLONE_NEWIPC |
	unix.CLONE_NEWUSER | unix.CLONE_NEWPID | unix.CLONE_NEWNET

// loadDenyFilter 装入主过滤器：默认放行，黑名单调用、建命名空间的 clone 与 TCP 以外的套接字回 EPERM。
func loadDenyFilter() error {
	info, err := arch.GetInfo("")
	if err != nil {
		return fmt.Errorf("seccomp: %w", err)
	}
	for _, n := range requiredSyscalls {
		if _, ok := info.SyscallNames[n]; !ok {
			return fmt.Errorf("seccomp: syscall %q unknown on %s", n, info.Name)
		}
	}
	names := make([]string, 0, len(denied))
	for _, n := range denied {
		if _, ok := info.SyscallNames[n]; ok {
			names = append(names, n)
		} else {
			slog.Debug("seccomp: syscall not on this architecture; skipped", "syscall", n)
		}
	}
	group := seccomp.SyscallGroup{
		Names:              names,
		NamesWithCondtions: slices.Concat(socketRules(), targetRules(), fastOpenRules()),
		Action:             seccomp.ActionErrno,
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err = seccomp.LoadFilter(seccomp.Filter{
		NoNewPrivs: true,
		Flag:       seccomp.FilterFlagTSync,
		Policy:     seccomp.Policy{DefaultAction: seccomp.ActionAllow, Syscalls: []seccomp.SyscallGroup{group}},
	})
	if err != nil {
		return fmt.Errorf("seccomp: %w", err)
	}
	return nil
}
