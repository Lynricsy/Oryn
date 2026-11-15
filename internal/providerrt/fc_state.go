package providerrt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/config"
)

// Firecracker 驱动的状态目录（计划 8.3）：
//
//	<state_dir>/bases/<rootfs sha256 前 16 位>.ext4   只读基础盘（rootfs 的硬链接）
//	<state_dir>/vms/<orbId>/{meta.json,disk.ext4}     每台 VM 的元数据与可写盘
//	<state_dir>/snapshots/<snapId>.{ext4,json}        快照盘与其元数据
//	<state_dir>/jail/<bin 的文件名>/<orbId>/root/      jailer 的 chroot（每次启动前整目录重建；jailer 按 exec-file 的文件名分目录）
//	<state_dir>/slots.json                            槽位表（.slots.lock 上的 flock 保护）
type fcPaths struct {
	root string
	// execName 是 firecracker 可执行文件的文件名（jailer 的 chroot 在 <chroot-base>/<execName>/<id>）；空 = "firecracker"。
	execName string
}

func newFCPaths(cfg config.Firecracker) fcPaths {
	return fcPaths{root: cfg.StateDir, execName: filepath.Base(cfg.Bin)}
}

func (p fcPaths) basesDir() string           { return filepath.Join(p.root, "bases") }
func (p fcPaths) base(h string) string       { return filepath.Join(p.root, "bases", h+".ext4") }
func (p fcPaths) vmsDir() string             { return filepath.Join(p.root, "vms") }
func (p fcPaths) vmDir(orbID string) string  { return filepath.Join(p.root, "vms", orbID) }
func (p fcPaths) vmMeta(orbID string) string { return filepath.Join(p.root, "vms", orbID, "meta.json") }
func (p fcPaths) vmDisk(orbID string) string { return filepath.Join(p.root, "vms", orbID, "disk.ext4") }
func (p fcPaths) snapsDir() string           { return filepath.Join(p.root, "snapshots") }
func (p fcPaths) snapDisk(id string) string  { return filepath.Join(p.root, "snapshots", id+".ext4") }
func (p fcPaths) snapMeta(id string) string  { return filepath.Join(p.root, "snapshots", id+".json") }
func (p fcPaths) jailDir(orbID string) string {
	name := p.execName
	if name == "" {
		name = "firecracker"
	}
	return filepath.Join(p.root, "jail", name, orbID)
}
func (p fcPaths) jailRoot(orbID string) string  { return filepath.Join(p.jailDir(orbID), "root") }
func (p fcPaths) apiSock(orbID string) string   { return filepath.Join(p.jailRoot(orbID), "api.sock") }
func (p fcPaths) vsockPath(orbID string) string { return filepath.Join(p.jailRoot(orbID), "v.sock") }
func (p fcPaths) slots() string                 { return filepath.Join(p.root, "slots.json") }
func (p fcPaths) slotsLock() string             { return filepath.Join(p.root, ".slots.lock") }

// vmMeta 是一台 VM 的元数据（vms/<orbId>/meta.json）。
type vmMeta struct {
	OrbID string `json:"orbId"`
	// Base 是只读基础盘的 id（rootfs sha256 前 16 位）。
	Base string `json:"base"`
	// Snapshot 是可写盘复制自的快照 id（空 = 空盘起步）；已有 VM 的 spawn 据此上报 snapshotUsed。
	Snapshot string `json:"snapshot,omitempty"`
	Type     string `json:"type"`
	CPUs     int    `json:"cpus"`
	MemMB    int    `json:"memMB"`
	Slot     int    `json:"slot"`
	UID      int    `json:"uid"`
	Tap      string `json:"tap"`
	HostIP   string `json:"hostIP"`
	GuestIP  string `json:"guestIP"`
	MAC      string `json:"mac"`
}

// snapMeta 是快照的元数据（snapshots/<snapId>.json）。
type snapMeta struct {
	Base  string `json:"base"`
	Bytes int64  `json:"bytes"`
}

// tapName 返回 orb 的 tap 名：fc- + orb id 末 12 位（IFNAMSIZ 限 15 字符）。
func tapName(orbID string) string {
	if len(orbID) > 12 {
		orbID = orbID[len(orbID)-12:]
	}
	return "fc-" + orbID
}

// slotNet 按槽位 n 计算网络参数：宿主侧 = 网段基址 + 4n + 1，guest = + 2（/30）；MAC = 06:00 + guest 地址的 4 个字节。
func slotNet(cidr netip.Prefix, n int) (hostIP, guestIP netip.Addr, mac string) {
	b := cidr.Masked().Addr().As4()
	base := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	at := func(off uint32) netip.Addr {
		v := base + off
		return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
	}
	hostIP, guestIP = at(uint32(4*n+1)), at(uint32(4*n+2))
	g := guestIP.As4()
	mac = fmt.Sprintf("06:00:%02x:%02x:%02x:%02x", g[0], g[1], g[2], g[3])
	return hostIP, guestIP, mac
}

// newVMMeta 按槽位填好网络与 uid。
func newVMMeta(cfg config.Firecracker, orbID string, slot int) vmMeta {
	h, g, mac := slotNet(cfg.Network.Prefix(), slot)
	return vmMeta{
		OrbID: orbID, Slot: slot, UID: cfg.UIDBase + slot, Tap: tapName(orbID),
		HostIP: h.String(), GuestIP: g.String(), MAC: mac,
	}
}

// readJSONFile 读 JSON 文件；不存在时返回 (false, nil)。
func readJSONFile(path string, v any) (bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, fmt.Errorf("解析 %s: %w", path, err)
	}
	return true, nil
}

// writeJSONFile 原子写 JSON 文件（同目录临时文件 + rename）。
func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (p fcPaths) loadVM(orbID string) (vmMeta, bool, error) {
	var m vmMeta
	ok, err := readJSONFile(p.vmMeta(orbID), &m)
	return m, ok, err
}

// listVMs 读出所有 VM 的元数据（按目录名排序；缺 meta.json 的半成品目录跳过）。
func (p fcPaths) listVMs() ([]vmMeta, error) {
	ents, err := os.ReadDir(p.vmsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []vmMeta
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		m, ok, err := p.loadVM(e.Name())
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// slotTable 是 slots.json：orb id → 槽位。
type slotTable map[string]int

// withSlots 在 .slots.lock 的排他 flock 下读出槽位表、交给 fn 修改并原子写回（fn 返回 false 时不写）。
func (p fcPaths) withSlots(fn func(slotTable) (bool, error)) error {
	if err := os.MkdirAll(p.root, 0o700); err != nil {
		return err
	}
	lf, err := os.OpenFile(p.slotsLock(), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("锁定槽位表: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) }()
	t := slotTable{}
	if _, err := readJSONFile(p.slots(), &t); err != nil {
		return err
	}
	write, err := fn(t)
	if err != nil || !write {
		return err
	}
	return writeJSONFile(p.slots(), t)
}

// allocSlot 返回 orb 的槽位：已有则复用（幂等），否则取最小的空闲槽位。
func (p fcPaths) allocSlot(orbID string) (int, error) {
	slot := -1
	err := p.withSlots(func(t slotTable) (bool, error) {
		if n, ok := t[orbID]; ok {
			slot = n
			return false, nil
		}
		used := make(map[int]bool, len(t))
		for _, n := range t {
			used[n] = true
		}
		for n := range config.FirecrackerMaxSlots {
			if !used[n] {
				slot, t[orbID] = n, n
				return true, nil
			}
		}
		return false, fmt.Errorf("capacity: %d 个槽位已全部占用", config.FirecrackerMaxSlots)
	})
	return slot, err
}

// releaseSlot 释放 orb 的槽位（没有则无操作）。
func (p fcPaths) releaseSlot(orbID string) error {
	return p.withSlots(func(t slotTable) (bool, error) {
		if _, ok := t[orbID]; !ok {
			return false, nil
		}
		delete(t, orbID)
		return true, nil
	})
}

// fileDigest 是按 (大小, mtime) 缓存的文件 sha256。
type fileDigest struct {
	size  int64
	mtime time.Time
	sum   string
}

// digestCache 缓存大文件（rootfs、vmlinux）的 sha256，文件大小或 mtime 变化时重算。
type digestCache struct {
	mu sync.Mutex
	m  map[string]fileDigest
}

// sum 返回 path 的 sha256（十六进制）。
func (c *digestCache) sum(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if d, ok := c.m[path]; ok && d.size == st.Size() && d.mtime.Equal(st.ModTime()) {
		return d.sum, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	if c.m == nil {
		c.m = map[string]fileDigest{}
	}
	s := hex.EncodeToString(h.Sum(nil))
	c.m[path] = fileDigest{size: st.Size(), mtime: st.ModTime(), sum: s}
	return s, nil
}

// baseRefs 返回被 VM 或快照引用的基础盘 id。
func (p fcPaths) baseRefs() (map[string]bool, error) {
	refs := map[string]bool{}
	vms, err := p.listVMs()
	if err != nil {
		return nil, err
	}
	for _, m := range vms {
		refs[m.Base] = true
	}
	ents, err := os.ReadDir(p.snapsDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || strings.HasSuffix(id, ".tmp") {
			continue
		}
		var sm snapMeta
		if _, err := readJSONFile(p.snapMeta(id), &sm); err != nil {
			return nil, err
		}
		refs[sm.Base] = true
	}
	return refs, nil
}
