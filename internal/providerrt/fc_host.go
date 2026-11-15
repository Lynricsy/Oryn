package providerrt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/config"
)

// fcCgroupParent 是所有 VM 的父 cgroup（jailer 的 --parent-cgroup），位于 cgroup v2 根下。
const fcCgroupParent = "oryn-fc"

// fcMemOverheadMB 是 cgroup memory.max 在 guest 内存之外给 firecracker 进程本身留的余量。
const fcMemOverheadMB = 512

// osHost 是 fcHost 的真实现：jailer / ip / mke2fs / cp 走 os/exec，cgroup 与 jail 直接操作文件系统。需要 root。
type osHost struct {
	cfg        config.Firecracker
	paths      fcPaths
	cgroupRoot string // /sys/fs/cgroup
}

func newOSHost(cfg config.Firecracker) *osHost {
	return &osHost{cfg: cfg, paths: newFCPaths(cfg), cgroupRoot: "/sys/fs/cgroup"}
}

func (h *osHost) cgroupDir(orbID string) string {
	return filepath.Join(h.cgroupRoot, fcCgroupParent, orbID)
}

// run 执行命令，失败时错误里带合并输出（命令行不含敏感信息）。
func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	// jailer --daemonize 的子进程会继承管道直到改向 /dev/null；不让它拖住 Wait。
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(buf.String()))
	}
	return nil
}

// jailerArgs 是 jailer 的 argv（计划 8.3）。
func jailerArgs(cfg config.Firecracker, vm vmMeta) []string {
	u := strconv.Itoa(vm.UID)
	return []string{
		"--id", vm.OrbID, "--exec-file", cfg.Bin, "--uid", u, "--gid", u,
		"--chroot-base-dir", filepath.Join(cfg.StateDir, "jail"),
		"--cgroup-version", "2", "--parent-cgroup", fcCgroupParent,
		"--cgroup", fmt.Sprintf("cpu.max=%d 100000", vm.CPUs*100000),
		"--cgroup", fmt.Sprintf("memory.max=%d", int64(vm.MemMB+fcMemOverheadMB)<<20),
		"--daemonize", "--new-pid-ns",
		"--", "--api-sock", "/api.sock",
	}
}

// ensureCgroupParent 建好 /sys/fs/cgroup/oryn-fc，并向根与它的 cgroup.subtree_control 写 +cpu +memory。
func (h *osHost) ensureCgroupParent() error {
	parent := filepath.Join(h.cgroupRoot, fcCgroupParent)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	for _, dir := range []string{h.cgroupRoot, parent} {
		if err := os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte("+cpu +memory"), 0); err != nil {
			return fmt.Errorf("启用 %s 的 cpu / memory 控制器: %w", dir, err)
		}
	}
	return nil
}

// Launch 实现 fcHost：清掉残留进程与 jail，重建 chroot（内核、基础盘、可写盘的硬链接），jailer 启动，等 api.sock。
func (h *osHost) Launch(ctx context.Context, vm vmMeta) error {
	if err := h.Kill(vm.OrbID); err != nil {
		return err
	}
	if err := h.ensureCgroupParent(); err != nil {
		return err
	}
	root := h.paths.jailRoot(vm.OrbID)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	disk := h.paths.vmDisk(vm.OrbID)
	if err := os.Chown(disk, vm.UID, vm.UID); err != nil {
		return err
	}
	for _, l := range []struct{ src, name string }{
		{h.cfg.Kernel, "vmlinux"}, {h.paths.base(vm.Base), "base.ext4"}, {disk, "disk.ext4"},
	} {
		if err := os.Link(l.src, filepath.Join(root, l.name)); err != nil {
			return fmt.Errorf("硬链接 %s 进 jail: %w", l.src, err)
		}
	}
	console := filepath.Join(root, fcConsoleLog)
	if err := os.WriteFile(console, nil, 0o600); err != nil {
		return err
	}
	// firecracker 降权后要在 chroot 根里建 api.sock 与 v.sock，并写串口日志。
	for _, p := range []string{root, console} {
		if err := os.Chown(p, vm.UID, vm.UID); err != nil {
			return err
		}
	}
	if err := run(ctx, h.cfg.Jailer, jailerArgs(h.cfg, vm)...); err != nil {
		return err
	}
	sock := h.paths.apiSock(vm.OrbID)
	deadline := time.Now().Add(10 * time.Second)
	for {
		dctx, cancel := context.WithTimeout(ctx, time.Second)
		c, err := dialUnix(dctx, sock)
		cancel()
		if err == nil {
			_ = c.Close()
			return nil
		}
		if time.Now().After(deadline) || !h.Alive(vm.OrbID) {
			return fmt.Errorf("firecracker %s 的 api.sock 未就绪: %w", vm.OrbID, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Alive 实现 fcHost：该 VM 的 cgroup 里还有进程。
func (h *osHost) Alive(orbID string) bool {
	b, err := os.ReadFile(filepath.Join(h.cgroupDir(orbID), "cgroup.procs"))
	return err == nil && len(bytes.TrimSpace(b)) > 0
}

// Kill 实现 fcHost：cgroup.kill → 等 populated 0（至多 10 秒）→ rmdir cgroup → 删 jail 目录。
func (h *osHost) Kill(orbID string) error {
	cg := h.cgroupDir(orbID)
	if _, err := os.Stat(cg); err == nil {
		if h.Alive(orbID) {
			if err := os.WriteFile(filepath.Join(cg, "cgroup.kill"), []byte("1"), 0); err != nil {
				return fmt.Errorf("cgroup.kill %s: %w", orbID, err)
			}
			deadline := time.Now().Add(10 * time.Second)
			for h.Alive(orbID) {
				if time.Now().After(deadline) {
					return fmt.Errorf("cgroup.kill 后 %s 仍有进程", orbID)
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		if err := syscall.Rmdir(cg); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("删除 cgroup %s: %w", cg, err)
		}
	}
	return os.RemoveAll(h.paths.jailDir(orbID))
}

// TapUp 实现 fcHost（计划 8.4）：tap 归槽位 uid 所有，宿主侧地址 /30，严格反向路径过滤。
func (h *osHost) TapUp(vm vmMeta) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := h.TapDown(vm); err != nil {
		return err
	}
	u := strconv.Itoa(vm.UID)
	for _, args := range [][]string{
		{"tuntap", "add", "dev", vm.Tap, "mode", "tap", "user", u, "group", u},
		{"addr", "add", vm.HostIP + "/30", "dev", vm.Tap},
		{"link", "set", vm.Tap, "up"},
	} {
		if err := run(ctx, "ip", args...); err != nil {
			return err
		}
	}
	return os.WriteFile("/proc/sys/net/ipv4/conf/"+vm.Tap+"/rp_filter", []byte("1"), 0)
}

// TapDown 实现 fcHost。
func (h *osHost) TapDown(vm vmMeta) error {
	if _, err := net.InterfaceByName(vm.Tap); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return run(ctx, "ip", "link", "del", vm.Tap)
}

// MakeDisk 实现 fcHost：truncate 出稀疏文件，mke2fs 成 ext4（卷标 oryn-data）。
func (h *osHost) MakeDisk(path string, gb int, uid int) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	err = f.Truncate(int64(gb) << 30)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := run(ctx, "mke2fs", "-q", "-t", "ext4", "-F", "-L", "oryn-data", path); err != nil {
		return err
	}
	return os.Chown(path, uid, uid)
}

// CopySparse 实现 fcHost：cp --sparse=always --reflink=auto，返回 dst 实际占用的字节数（st_blocks × 512）。
func (h *osHost) CopySparse(src, dst string, uid int) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := run(ctx, "cp", "--sparse=always", "--reflink=auto", src, dst); err != nil {
		return 0, err
	}
	if err := os.Chown(dst, uid, uid); err != nil {
		return 0, err
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		return 0, err
	}
	return diskUsage(dst)
}

// diskUsage 返回文件实际占用的字节数。
func diskUsage(path string) (int64, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, err
	}
	return st.Blocks * 512, nil
}
