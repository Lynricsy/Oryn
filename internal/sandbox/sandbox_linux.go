//go:build linux

// Package sandbox 在进程内对自身施加不可逆的操作系统沙箱（M9 协调宿主 H2）：
// Landlock（文件读写 / 执行白名单、TCP 端口、IPC 作用域）+ seccomp（前置架构过滤与系统调用黑名单）
// + no_new_privs + 清空能力，必要时先降权到独立的非特权用户。
// 网络 / PID 命名空间由调用方在创建子进程时决定（见 internal/coordhost），本包只负责把回环网卡拉起。
package sandbox

import (
	"debug/elf"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
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
	TCPPort       uint16   // 唯一允许 bind / connect 的 TCP 端口
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

// MinABI 是启动条件要求的最低 Landlock ABI（跨目录改名需要 refer 权限）。
const MinABI = 2

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
	if err != nil || abi < MinABI {
		return Report{}, fmt.Errorf("landlock unavailable (abi %d): kernel >= 5.19 with the landlock LSM enabled is required", abi)
	}
	if p.TCPPort == 0 {
		return Report{}, errors.New("sandbox: TCPPort is required")
	}
	rules := []landlock.Rule{
		landlock.PathAccess(readWriteRights, p.ReadWrite...),
		landlock.PathAccess(ll.AccessFSExecute|ll.AccessFSReadFile, p.Exec...),
		landlock.PathAccess(ll.AccessFSReadFile|ll.AccessFSReadDir, p.ReadOnlyDirs...).IgnoreIfMissing(),
		landlock.PathAccess(ll.AccessFSReadFile, p.ReadOnlyFiles...).IgnoreIfMissing(),
		landlock.PathAccess(ll.AccessFSReadFile|ll.AccessFSWriteFile|ll.AccessFSTruncate, "/dev/null"),
		landlock.BindTCP(p.TCPPort),
		landlock.ConnectTCP(p.TCPPort),
	}
	if err := landlock.V11.BestEffort().Restrict(rules...); err != nil {
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

// denied 是无条件拒绝（EPERM）的系统调用；本机架构没有的跳过。
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
}

// requiredSyscalls 在本机架构上必须存在，否则拒绝施加（过滤器会形同虚设）。
var requiredSyscalls = []string{"ptrace", "mount", "unshare", "setns", "bpf", "socket", "clone"}

// cloneNewFlags 是 clone 的全部 CLONE_NEW* 标志位。
const cloneNewFlags = unix.CLONE_NEWNS | unix.CLONE_NEWCGROUP | unix.CLONE_NEWUTS | unix.CLONE_NEWIPC |
	unix.CLONE_NEWUSER | unix.CLONE_NEWPID | unix.CLONE_NEWNET

// loadDenyFilter 装入主过滤器：默认放行，黑名单调用、非 TCP 套接字与建命名空间的 clone 回 EPERM。
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
		Names: names,
		NamesWithCondtions: []seccomp.NameWithConditions{
			// 不是 AF_INET / AF_INET6。
			{Name: "socket", Conditions: seccomp.ArgumentConditions{
				{Argument: 0, Operation: seccomp.NotEqual, Value: unix.AF_INET},
				{Argument: 0, Operation: seccomp.NotEqual, Value: unix.AF_INET6},
			}},
			// 不是 SOCK_STREAM（类型位 0..3 中除最低位外有置位）。
			{Name: "socket", Conditions: seccomp.ArgumentConditions{
				{Argument: 1, Operation: seccomp.BitsSet, Value: 0x0E},
			}},
			// 协议不是 0 / IPPROTO_TCP（挡 MPTCP 等）。
			{Name: "socket", Conditions: seccomp.ArgumentConditions{
				{Argument: 2, Operation: seccomp.NotEqual, Value: 0},
				{Argument: 2, Operation: seccomp.NotEqual, Value: unix.IPPROTO_TCP},
			}},
			{Name: "clone", Conditions: seccomp.ArgumentConditions{
				{Argument: 0, Operation: seccomp.BitsSet, Value: cloneNewFlags},
			}},
		},
		Action: seccomp.ActionErrno,
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
