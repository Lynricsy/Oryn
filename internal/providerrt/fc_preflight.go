package providerrt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// PreflightCheck 是一项预检的结果：Err 非空为失败，Warn 非空为只警告，都空为通过。
type PreflightCheck struct {
	Name string
	Err  error
	Warn string
}

// PreflightReporter 是能逐项报告预检结果的驱动（`oryn provider preflight` 逐项打印）。
type PreflightReporter interface {
	PreflightChecks(ctx context.Context) []PreflightCheck
}

// FCNetIface 是 deploy/firecracker/oryn-fc-net.sh 建的 dummy 接口（带 host_addr）。
const FCNetIface = "oryn-fc"

// fcSupportedKernels 是 Firecracker 支持的宿主内核版本（其它版本只警告）。
var fcSupportedKernels = []string{"5.10", "6.1", "6.18"}

// Preflight 实现 Preflighter：任一项失败即返回错误（各项失败合并），警告只记日志。
func (d *FirecrackerDriver) Preflight(ctx context.Context) error {
	var errs []error
	for _, c := range d.PreflightChecks(ctx) {
		switch {
		case c.Err != nil:
			errs = append(errs, fmt.Errorf("firecracker 预检 %s: %w", c.Name, c.Err))
		case c.Warn != "":
			slog.Warn("firecracker 预检", "check", c.Name, "warn", c.Warn)
		}
	}
	return errors.Join(errs...)
}

// PreflightChecks 实现 PreflightReporter（计划 8.3 的预检清单，外加 orb_url 端口须在 nft 放行列表里）。
func (d *FirecrackerDriver) PreflightChecks(ctx context.Context) []PreflightCheck {
	cfg := d.Cfg
	var out []PreflightCheck
	add := func(name string, err error) { out = append(out, PreflightCheck{Name: name, Err: err}) }

	add("以 root 运行", func() error {
		if os.Geteuid() != 0 {
			return fmt.Errorf("euid 为 %d：jailer、tap、cgroup 都需要 root", os.Geteuid())
		}
		return nil
	}())
	for _, dev := range []string{"/dev/kvm", "/dev/net/tun"} {
		add(dev+" 可读写", func() error {
			f, err := os.OpenFile(dev, os.O_RDWR, 0)
			if err != nil {
				return err
			}
			return f.Close()
		}())
	}
	add("cgroup v2", func() error {
		var st unix.Statfs_t
		if err := unix.Statfs("/sys/fs/cgroup", &st); err != nil {
			return err
		}
		if st.Type != unix.CGROUP2_SUPER_MAGIC {
			return errors.New("/sys/fs/cgroup 不是 cgroup2（需要统一层级）")
		}
		return nil
	}())
	for _, cmd := range []string{"ip", "nft", "mke2fs", "cp"} {
		_, err := exec.LookPath(cmd)
		add("PATH 中有 "+cmd, err)
	}
	for _, f := range []struct {
		key, path string
		exec      bool
	}{{"bin", cfg.Bin, true}, {"jailer", cfg.Jailer, true}, {"kernel", cfg.Kernel, false}, {"rootfs", cfg.Rootfs, false}} {
		add("firecracker."+f.key+" "+f.path, checkArtifact(f.path, f.exec))
	}
	add("state_dir 与产物在同一文件系统", d.checkSameFS())
	add("接口 "+FCNetIface+" 带 "+cfg.Network.HostAddr, checkIfaceAddr(FCNetIface, cfg.Network.HostAddr))
	nftOut, nftErr := nftListTable(ctx)
	add("nft 表 inet oryn_fc", nftErr)
	if nftErr == nil {
		out = append(out, checkOrbURLPort(d.OrbURL, cfg.Network.HostAddr, nftOut))
	}
	add("网段 "+cfg.Network.CIDR+" 不与其它接口重叠", checkCIDROverlap(cfg.Network.Prefix()))
	var uts unix.Utsname
	rel := ""
	if unix.Uname(&uts) == nil {
		rel = unix.ByteSliceToString(uts.Release[:])
	}
	out = append(out, PreflightCheck{Name: "宿主内核 " + rel, Warn: kernelWarning(rel)})
	return out
}

// checkArtifact 要求产物是普通文件；可执行文件要有执行位，内核与根盘要对其他用户可读（jail 里以槽位 uid 打开硬链接）。
func checkArtifact(path string, executable bool) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("不是普通文件")
	}
	if executable && st.Mode().Perm()&0o111 == 0 {
		return errors.New("没有执行权限")
	}
	if !executable && st.Mode().Perm()&0o004 == 0 {
		return fmt.Errorf("权限 %v：须对其他用户可读（firecracker 以槽位 uid 打开它的硬链接）", st.Mode().Perm())
	}
	return nil
}

// checkSameFS 在 state_dir 里试建 kernel 与 rootfs 的硬链接。
func (d *FirecrackerDriver) checkSameFS() error {
	if err := os.MkdirAll(d.Cfg.StateDir, 0o700); err != nil {
		return err
	}
	for _, src := range []string{d.Cfg.Kernel, d.Cfg.Rootfs} {
		dst := filepath.Join(d.Cfg.StateDir, ".preflight-link")
		_ = os.Remove(dst)
		if err := os.Link(src, dst); err != nil {
			return fmt.Errorf("硬链接 %s 进 state_dir 失败（须在同一文件系统）: %w", src, err)
		}
		_ = os.Remove(dst)
	}
	return nil
}

func checkIfaceAddr(name, addr string) error {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return fmt.Errorf("%w（先运行 deploy/firecracker/oryn-fc-net.sh up）", err)
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return err
	}
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr().String() == addr {
			return nil
		}
	}
	return fmt.Errorf("接口 %s 上没有地址 %s", name, addr)
}

func nftListTable(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "nft", "list", "table", "inet", "oryn_fc")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		first, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		return "", fmt.Errorf("nft list table inet oryn_fc: %w: %s（先运行 deploy/firecracker/oryn-fc-net.sh up）", err, first)
	}
	return stdout.String(), nil
}

// nftDportRe 匹配放行规则里的 `tcp dport 7643` 或 `tcp dport { 7600, 7643 }`。
var nftDportRe = regexp.MustCompile(`tcp dport (\{[^}]*\}|\S+)`)

// portRange 是闭区间端口段。
type portRange struct{ lo, hi int }

// nftAcceptPorts 从 `nft list table inet oryn_fc` 的输出里解析带 accept 的 tcp dport 端口；解析不出来返回错误。
func nftAcceptPorts(listing string) ([]portRange, error) {
	var out []portRange
	for line := range strings.SplitSeq(listing, "\n") {
		if !strings.Contains(line, "accept") {
			continue
		}
		for _, m := range nftDportRe.FindAllStringSubmatch(line, -1) {
			for el := range strings.SplitSeq(strings.Trim(m[1], "{}"), ",") {
				el = strings.TrimSpace(el)
				lo, hi, isRange := strings.Cut(el, "-")
				a, err := strconv.Atoi(lo)
				if err != nil {
					return nil, fmt.Errorf("无法解析端口 %q", el)
				}
				b := a
				if isRange {
					if b, err = strconv.Atoi(hi); err != nil {
						return nil, fmt.Errorf("无法解析端口 %q", el)
					}
				}
				out = append(out, portRange{a, b})
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("没找到带 accept 的 tcp dport 规则")
	}
	return out, nil
}

// orbURLPort 返回 orb_url 的主机与端口（缺省端口按 scheme）。
func orbURLPort(orbURL string) (string, int, error) {
	u, err := url.Parse(orbURL)
	if err != nil {
		return "", 0, err
	}
	p := u.Port()
	switch {
	case p != "":
	case u.Scheme == "https":
		p = "443"
	case u.Scheme == "http":
		p = "80"
	default:
		return "", 0, fmt.Errorf("orb_url %q 没有端口", orbURL)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return "", 0, err
	}
	return u.Hostname(), n, nil
}

// checkOrbURLPort：orb_url 的端口不在 nft 放行端口里 → 失败（guest 连不上控制面）；任一边解析不出来 → 只警告。
// orb_url 的主机不是 host_addr 时也只警告（guest 只能经 host_addr 进宿主）。
func checkOrbURLPort(orbURL, hostAddr, listing string) PreflightCheck {
	c := PreflightCheck{Name: "orb_url 端口在 nft 放行列表（ORYN_FC_LINK_PORTS）里"}
	host, port, err := orbURLPort(orbURL)
	if err != nil {
		c.Warn = fmt.Sprintf("解析 orb_url %q: %v", orbURL, err)
		return c
	}
	ports, err := nftAcceptPorts(listing)
	if err != nil {
		c.Warn = fmt.Sprintf("解析 nft 规则: %v", err)
		return c
	}
	if !slices.ContainsFunc(ports, func(r portRange) bool { return r.lo <= port && port <= r.hi }) {
		c.Err = fmt.Errorf("orb_url %s 的端口 %d 不在 inet oryn_fc 放行的端口里；用 ORYN_FC_LINK_PORTS 重新运行 oryn-fc-net.sh up", orbURL, port)
		return c
	}
	if host != hostAddr {
		c.Warn = fmt.Sprintf("orb_url 的主机 %s 不是 network.host_addr %s（guest 只能经 host_addr 访问宿主）", host, hostAddr)
	}
	return c
}

// checkCIDROverlap 要求除 oryn-fc 与 fc-* 之外的接口地址都不与 cidr 重叠。
func checkCIDROverlap(cidr netip.Prefix) error {
	ifs, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, ifc := range ifs {
		if ifc.Name == FCNetIface || strings.HasPrefix(ifc.Name, "fc-") {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			return err
		}
		if err := cidrOverlap(cidr, ifc.Name, addrs); err != nil {
			return err
		}
	}
	return nil
}

func cidrOverlap(cidr netip.Prefix, name string, addrs []net.Addr) error {
	for _, a := range addrs {
		p, err := netip.ParsePrefix(a.String())
		if err == nil && p.Overlaps(cidr) {
			return fmt.Errorf("接口 %s 的 %s 与 %s 重叠", name, p, cidr)
		}
	}
	return nil
}

// kernelWarning 在宿主内核不是 Firecracker 支持的版本时返回警告。
func kernelWarning(release string) string {
	parts := strings.SplitN(release, ".", 3)
	if len(parts) >= 2 && slices.Contains(fcSupportedKernels, parts[0]+"."+parts[1]) {
		return ""
	}
	return fmt.Sprintf("不在 Firecracker 支持的宿主内核 %v 之列，可以运行但未经上游验证", fcSupportedKernels)
}
