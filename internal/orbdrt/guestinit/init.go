//go:build linux

package guestinit

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Lynricsy/Oryn/internal/fcguest"
)

// Stage2Arg 是第一阶段换根后 exec 自己时的子命令（`orbd guest-init-stage2`）。
const Stage2Arg = "guest-init-stage2"

const (
	// fifreeze / fithaw 是 FIFREEZE / FITHAW（_IOWR('X', 119/120, int)），x/sys/unix 未导出。
	fifreeze = 0xC0045877
	fithaw   = 0xC0045878
	// connTimeout 是每条 vsock 连接的收发超时（SO_RCVTIMEO / SO_SNDTIMEO）。
	connTimeout = 10 * time.Second
)

func setupLogging() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

// fatal 记日志后退出：PID 1 退出使内核 panic（启动参数 panic=1）→ 复位，Firecracker 随之退出，宿主看得到。
func fatal(err error) {
	slog.Error("guest-init: fatal", "err", err)
	os.Exit(1)
}

// Main 是第一阶段（根是只读的 /dev/vda）：在 /mnt 的 tmpfs 上把 /dev/vda（只读）与 /dev/vdb（ext4，读写）叠成
// overlay，挂到 /mnt/root；可写盘移到新根的 /run/oryn-data；/proc、/sys、/dev 移进新根；换根后 exec 第二阶段。不返回。
func Main() {
	setupLogging()
	if err := stage1(); err != nil {
		fatal(err)
	}
}

type mountSpec struct {
	src, dst, fstype string
	flags            uintptr
	data             string
}

func mountAll(ms []mountSpec) error {
	for _, m := range ms {
		if err := os.MkdirAll(m.dst, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", m.dst, err)
		}
		if err := unix.Mount(m.src, m.dst, m.fstype, m.flags, m.data); err != nil {
			// 内核可能已自动挂了 devtmpfs 等。
			if errors.Is(err, unix.EBUSY) && m.flags&unix.MS_MOVE == 0 {
				continue
			}
			return fmt.Errorf("mount %s on %s (%s): %w", m.src, m.dst, m.fstype, err)
		}
	}
	return nil
}

func stage1() error {
	if err := mountAll([]mountSpec{
		{"proc", "/proc", "proc", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"sysfs", "/sys", "sysfs", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"devtmpfs", "/dev", "devtmpfs", unix.MS_NOSUID, "mode=0755"},
	}); err != nil {
		return err
	}
	// /mnt 在只读根上，挂载点之下的目录只能在 tmpfs 里建。
	if err := unix.Mount("tmpfs", "/mnt", "tmpfs", 0, "mode=0755"); err != nil {
		return fmt.Errorf("mount tmpfs on /mnt: %w", err)
	}
	if err := mountAll([]mountSpec{
		{"/dev/vda", "/mnt/lower", "ext4", unix.MS_RDONLY, ""},
		{"/dev/vdb", "/mnt/data", "ext4", 0, ""},
	}); err != nil {
		return err
	}
	for _, d := range []string{"/mnt/data/upper", "/mnt/data/work", "/mnt/root"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if err := unix.Mount("overlay", "/mnt/root", "overlay", 0,
		"lowerdir=/mnt/lower,upperdir=/mnt/data/upper,workdir=/mnt/data/work"); err != nil {
		return fmt.Errorf("mount overlay: %w", err)
	}
	if err := unix.Mount("tmpfs", "/mnt/root/run", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0755"); err != nil {
		return fmt.Errorf("mount tmpfs on /run: %w", err)
	}
	moves := []mountSpec{{"/mnt/data", "/mnt/root" + dataMount, "", unix.MS_MOVE, ""}}
	for _, p := range []string{"/proc", "/sys", "/dev"} {
		moves = append(moves, mountSpec{p, "/mnt/root" + p, "", unix.MS_MOVE, ""})
	}
	if err := mountAll(moves); err != nil {
		return err
	}
	if err := os.Chdir("/mnt/root"); err != nil {
		return err
	}
	if err := unix.Mount(".", "/", "", unix.MS_MOVE, ""); err != nil {
		return fmt.Errorf("move new root: %w", err)
	}
	if err := unix.Chroot("."); err != nil {
		return fmt.Errorf("chroot: %w", err)
	}
	if err := os.Chdir("/"); err != nil {
		return err
	}
	return unix.Exec(orbdBin, []string{"orbd", Stage2Arg}, os.Environ())
}

// Stage2 是第二阶段（已在新根里，仍是 PID 1）：挂 devpts、/dev/shm、/tmp、cgroup2，拉起 lo，关掉内核的
// Ctrl-Alt-Del 直接复位（改为给 PID 1 发 SIGINT），然后在 vsock 上服务宿主的控制协议。不返回。
func Stage2() {
	setupLogging()
	if err := stage2Mounts(); err != nil {
		fatal(err)
	}
	if err := loopbackUp(); err != nil {
		slog.Warn("guest-init: bring up lo failed", "err", err)
	}
	if err := unix.Reboot(unix.LINUX_REBOOT_CMD_CAD_OFF); err != nil {
		slog.Warn("guest-init: disable ctrl-alt-del failed", "err", err)
	}
	sup := newSupervisor(readCredential("/"))
	g := newGuest(realSys(), sup, readImageConfig(imageEnvPath))

	sigs := make(chan os.Signal, 16)
	signal.Notify(sigs, syscall.SIGCHLD, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for sig := range sigs {
			switch sig {
			case syscall.SIGCHLD:
				sup.reap()
			default:
				slog.Info("guest-init: shutdown signal", "signal", sig.String())
				go g.shutdownSignal()
			}
		}
	}()

	fd, err := listenVsock(fcguest.Port)
	if err != nil {
		fatal(err)
	}
	slog.Info("guest-init: listening", "vsockPort", fcguest.Port)
	for {
		nfd, _, err := unix.Accept4(fd, unix.SOCK_CLOEXEC)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.ECONNABORTED) {
				continue
			}
			slog.Warn("guest-init: accept failed", "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		tv := unix.NsecToTimeval(connTimeout.Nanoseconds())
		_ = unix.SetsockoptTimeval(nfd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
		_ = unix.SetsockoptTimeval(nfd, unix.SOL_SOCKET, unix.SO_SNDTIMEO, &tv)
		go func() {
			f := os.NewFile(uintptr(nfd), "vsock")
			defer f.Close()
			g.serveConn(f)
		}()
	}
}

func stage2Mounts() error {
	if err := mountAll([]mountSpec{
		{"devpts", "/dev/pts", "devpts", unix.MS_NOSUID | unix.MS_NOEXEC, "gid=5,mode=620,ptmxmode=666"},
		{"shm", "/dev/shm", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=1777,size=1g"},
		{"tmpfs", "/tmp", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=1777"},
		{"cgroup2", "/sys/fs/cgroup", "cgroup2", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
	}); err != nil {
		return err
	}
	// devtmpfs 里的 /dev/ptmx 是旧式设备节点；指向本 devpts 实例的 ptmx 更稳妥。
	if fi, err := os.Lstat("/dev/ptmx"); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		_ = os.Remove("/dev/ptmx")
		_ = os.Symlink("pts/ptmx", "/dev/ptmx")
	}
	return nil
}

// loopbackUp 用 SIOCSIFFLAGS 拉起 lo（eth0 由内核的 ip= 启动参数配置）。
func loopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return err
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr)
}

// listenVsock 在 AF_VSOCK 的 (VMADDR_CID_ANY, port) 上监听（阻塞 fd）。
func listenVsock(port uint32) (int, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("vsock socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("vsock bind %d: %w", port, err)
	}
	if err := unix.Listen(fd, 16); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("vsock listen: %w", err)
	}
	return fd, nil
}

// realSys 是 sysOps 的真实现。
func realSys() sysOps {
	return sysOps{
		SetClock: func(t time.Time) error {
			ts := unix.NsecToTimespec(t.UnixNano())
			return unix.ClockSettime(unix.CLOCK_REALTIME, &ts)
		},
		SetHostname: func(name string) error { return unix.Sethostname([]byte(name)) },
		WriteFile:   writeFileAs,
		Symlink:     symlinkAs,
		Sync:        unix.Sync,
		Freeze:      func(path string) error { return freezeIoctl(path, fifreeze, unix.EBUSY) },
		Thaw:        func(path string) error { return freezeIoctl(path, fithaw, unix.EINVAL) },
		KillAll:     func() { _ = unix.Kill(-1, unix.SIGKILL) },
		Reboot: func() {
			if err := unix.Reboot(unix.LINUX_REBOOT_CMD_RESTART); err != nil {
				fatal(fmt.Errorf("reboot: %w", err))
			}
		},
		AfterFunc: func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop },
	}
}

// freezeIoctl 对 path 所在文件系统做 FIFREEZE / FITHAW；benign 是「已冻结 / 未冻结」对应的 errno，视为成功。
func freezeIoctl(path string, req uint, benign unix.Errno) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err := unix.IoctlSetInt(fd, req, 0); err != nil && !errors.Is(err, benign) {
		return err
	}
	return nil
}

// ensureDir 在目录不存在时创建它并设属主 uid:gid。
func ensureDir(dir string, uid, gid int) error {
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.Chown(dir, uid, gid)
}

// symlinkAs 让 path 成为指向 target 的符号链接：已是时不动，否则在同目录建临时链接再 rename 覆盖（原子替换
// 原有的普通文件或旧链接），链接属主 uid:gid。
func symlinkAs(target, path string, uid, gid int) error {
	if cur, err := os.Readlink(path); err == nil && cur == target {
		return nil
	}
	dir := filepath.Dir(path)
	if err := ensureDir(dir, uid, gid); err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".guest-init-link")
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	err := os.Lchown(tmp, uid, gid)
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// writeFileAs 原子写文件并设属主与权限；父目录不存在时创建并设同一属主。
func writeFileAs(path string, data []byte, mode os.FileMode, uid, gid int) error {
	dir := filepath.Dir(path)
	if err := ensureDir(dir, uid, gid); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".guest-init-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Chown(uid, gid)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}
