package config

import (
	"errors"
	"fmt"
	"math"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Firecracker 是 firecracker 驱动配置（M11b，x86_64；见 docs/deploy.md）。
type Firecracker struct {
	// Bin / Jailer 是 firecracker 与 jailer 可执行文件。
	Bin    string `yaml:"bin"`
	Jailer string `yaml:"jailer"`
	// Kernel / Rootfs 是 guest 内核（vmlinux）与只读根盘（ext4）；须与 StateDir 在同一文件系统（驱动硬链接它们）。
	Kernel string `yaml:"kernel"`
	Rootfs string `yaml:"rootfs"`
	// StateDir 存基础盘、各 VM 的可写盘与元数据、快照、jailer 的 chroot 与槽位表。
	StateDir string `yaml:"state_dir"`
	// DiskGB 是每台 VM 可写盘（稀疏文件）的大小。
	DiskGB int `yaml:"disk_gb"`
	// UIDBase 是槽位 n 的 jailer uid / gid = UIDBase + n。
	UIDBase int                `yaml:"uid_base"`
	Network FirecrackerNetwork `yaml:"network"`
	Limits  FirecrackerLimits  `yaml:"limits"`
	// Types 是 orb 规格表；未写时取 DefaultFirecrackerTypes。
	Types       map[string]FirecrackerType `yaml:"types"`
	DefaultType string                     `yaml:"default_type"`
}

// FirecrackerNetwork 是 guest 网络：每台 VM 一个 /30（槽位 n：宿主侧 = 基址 + 4n + 1，guest = + 2），
// 网段的最后一个 /24 留给 HostAddr（dummy 接口 oryn-fc 上的地址，orynd 在它上面监听）。
type FirecrackerNetwork struct {
	CIDR     string   `yaml:"cidr"`
	HostAddr string   `yaml:"host_addr"`
	DNS      []string `yaml:"dns"`
}

// FirecrackerLimits 是驱动的容量上限；启动 VM 前核对，超限报错 capacity。
type FirecrackerLimits struct {
	MaxVMs      int `yaml:"max_vms"`
	MaxMemoryMB int `yaml:"max_memory_mb"`
}

// FirecrackerType 是一种 microVM 规格；vCPU 只能是整数。
type FirecrackerType struct {
	CPUs   int    `yaml:"cpus"`
	Memory string `yaml:"memory"`
}

// UnmarshalYAML 拒绝小数 vCPU（yaml.v3 会把 1.5 静默截断成 int 1；docker 的规格表允许小数，照抄过来容易踩到）。
// node.Decode 不继承外层的严格解码，所以这里自己拒绝未知键。
func (t *FirecrackerType) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if k := n.Content[i].Value; k != "cpus" && k != "memory" {
				return fmt.Errorf("第 %d 行：field %s not found in type config.FirecrackerType", n.Content[i].Line, k)
			}
		}
	}
	var raw struct {
		CPUs   float64 `yaml:"cpus"`
		Memory string  `yaml:"memory"`
	}
	if err := n.Decode(&raw); err != nil {
		return err
	}
	if raw.CPUs != math.Trunc(raw.CPUs) || raw.CPUs > math.MaxInt32 || raw.CPUs < math.MinInt32 {
		return fmt.Errorf("第 %d 行：cpus %v 必须是整数（Firecracker 的 vCPU 数）", n.Line, raw.CPUs)
	}
	*t = FirecrackerType{CPUs: int(raw.CPUs), Memory: raw.Memory}
	return nil
}

// MemoryMB 返回内存（MiB）；写法不合法时返回 0（加载时已校验）。
func (t FirecrackerType) MemoryMB() int {
	mb, _ := parseMemoryMB(t.Memory)
	return mb
}

// FirecrackerMinDiskGB 是 disk_gb 的下限。
const FirecrackerMinDiskGB = 8

// FirecrackerMaxSlots 是 /16 网段能容纳的槽位数：去掉留给 host_addr 的最后一个 /24 后按 /30 划分。
const FirecrackerMaxSlots = (1<<16 - 1<<8) / 4

// DefaultFirecracker 返回 firecracker 段的默认值（Types 留空，加载后再补 DefaultFirecrackerTypes，
// 以免用户写的类型表与默认类型合并）。
func DefaultFirecracker() Firecracker {
	return Firecracker{
		Bin:         "/usr/local/bin/firecracker",
		Jailer:      "/usr/local/bin/jailer",
		Kernel:      "/var/lib/oryn-fc/artifacts/vmlinux",
		Rootfs:      "/var/lib/oryn-fc/artifacts/rootfs.ext4",
		StateDir:    "/var/lib/oryn-fc",
		DiskGB:      40,
		UIDBase:     200000,
		Network:     FirecrackerNetwork{CIDR: "172.31.0.0/16", HostAddr: "172.31.255.1", DNS: []string{"1.1.1.1", "1.0.0.1"}},
		Limits:      FirecrackerLimits{MaxVMs: 8, MaxMemoryMB: 32768},
		DefaultType: "medium",
	}
}

// DefaultFirecrackerTypes 是 firecracker 的默认规格（与 docker 的默认规格同名同量）。
func DefaultFirecrackerTypes() map[string]FirecrackerType {
	return map[string]FirecrackerType{
		"small":  {CPUs: 2, Memory: "4g"},
		"medium": {CPUs: 4, Memory: "8g"},
		"large":  {CPUs: 8, Memory: "16g"},
	}
}

// Prefix 返回已校验的 network.cidr。
func (n FirecrackerNetwork) Prefix() netip.Prefix {
	p, _ := netip.ParsePrefix(n.CIDR)
	return p
}

// validate 校验 driver=firecracker 时的配置；错误前缀均为 firecracker.。
func (f Firecracker) validate() []error {
	var errs []error
	for _, p := range []struct{ key, v string }{
		{"bin", f.Bin}, {"jailer", f.Jailer}, {"kernel", f.Kernel}, {"rootfs", f.Rootfs}, {"state_dir", f.StateDir},
	} {
		if p.v == "" || !filepath.IsAbs(p.v) {
			errs = append(errs, fmt.Errorf("firecracker.%s %q 必须是非空的绝对路径", p.key, p.v))
		}
	}
	if f.DiskGB < FirecrackerMinDiskGB {
		errs = append(errs, fmt.Errorf("firecracker.disk_gb %d 至少为 %d", f.DiskGB, FirecrackerMinDiskGB))
	}
	if f.UIDBase < 1 || f.UIDBase > math.MaxInt32-FirecrackerMaxSlots {
		errs = append(errs, fmt.Errorf("firecracker.uid_base %d 必须在 1 与 %d 之间", f.UIDBase, math.MaxInt32-FirecrackerMaxSlots))
	}
	errs = append(errs, f.Network.validate()...)
	if n := f.Limits.MaxVMs; n < 1 || n > FirecrackerMaxSlots {
		errs = append(errs, fmt.Errorf("firecracker.limits.max_vms %d 必须在 1 与 %d 之间", n, FirecrackerMaxSlots))
	}
	if f.Limits.MaxMemoryMB < 1 {
		errs = append(errs, fmt.Errorf("firecracker.limits.max_memory_mb %d 至少为 1", f.Limits.MaxMemoryMB))
	}
	names := make([]string, 0, len(f.Types))
	for name := range f.Types {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		t := f.Types[name]
		if t.CPUs < 1 {
			errs = append(errs, fmt.Errorf("firecracker.types.%s.cpus %d 至少为 1（只能是整数）", name, t.CPUs))
		}
		if _, err := parseMemoryMB(t.Memory); err != nil {
			errs = append(errs, fmt.Errorf("firecracker.types.%s.memory: %w", name, err))
		}
	}
	if _, ok := f.Types[f.DefaultType]; !ok {
		errs = append(errs, fmt.Errorf("firecracker.default_type %q 不在 types 中", f.DefaultType))
	}
	return errs
}

func (n FirecrackerNetwork) validate() []error {
	var errs []error
	p, err := netip.ParsePrefix(n.CIDR)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("firecracker.network.cidr %q 无效：%w", n.CIDR, err))
	case !p.Addr().Is4() || p.Bits() != 16 || p != p.Masked():
		errs = append(errs, fmt.Errorf("firecracker.network.cidr %q 必须是 IPv4 /16 网段（如 172.31.0.0/16）", n.CIDR))
	default:
		b := p.Addr().As4()
		last := netip.PrefixFrom(netip.AddrFrom4([4]byte{b[0], b[1], 255, 0}), 24)
		a, err := netip.ParseAddr(n.HostAddr)
		if err != nil || !last.Contains(a) {
			errs = append(errs, fmt.Errorf("firecracker.network.host_addr %q 必须是 %s 内的 IPv4 地址（网段的最后一个 /24）", n.HostAddr, last))
		}
	}
	if len(n.DNS) == 0 {
		errs = append(errs, errors.New("firecracker.network.dns 至少给一个地址"))
	}
	for _, s := range n.DNS {
		if _, err := netip.ParseAddr(s); err != nil {
			errs = append(errs, fmt.Errorf("firecracker.network.dns %q 不是 IP 地址", s))
		}
	}
	return errs
}

// parseMemoryMB 解析 "<整数>m|g|t"（不区分大小写）为 MiB；必须为正。
func parseMemoryMB(s string) (int, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "" {
		return 0, errors.New("不能为空（写法如 4g、512m）")
	}
	mult := 0
	switch v[len(v)-1] {
	case 'm':
		mult = 1
	case 'g':
		mult = 1 << 10
	case 't':
		mult = 1 << 20
	default:
		return 0, fmt.Errorf("%q 须带单位 m / g / t（如 4g、512m）", s)
	}
	n, err := strconv.Atoi(v[:len(v)-1])
	if err != nil || n < 1 || n > math.MaxInt32/mult {
		return 0, fmt.Errorf("%q 不是正整数大小（如 4g、512m）", s)
	}
	return n * mult, nil
}
