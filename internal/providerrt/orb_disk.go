package providerrt

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// orbDiskCacheTTL 是 Docker 驱动缓存 `docker ps -a --size` 与 Runner 后台采样 orb 磁盘的默认周期（避免每次 15s 心跳都扫磁盘）。
const orbDiskCacheTTL = 60 * time.Second

// orbDiskSampleTimeout 限制单次后台 orb 磁盘采样的最长耗时，防止慢盘或超大目录长期占住采样器。
const orbDiskSampleTimeout = 30 * time.Second

// orbDiskMaxEntries 限制随心跳上报的 orbDiskByOrb 明细上限（总计 orbDiskBytes 始终完整）。
const orbDiskMaxEntries = 500

// OrbDiskUsage 实现 OrbDiskSizer：统计所有带 label oryn-orb=1 的容器可写层大小（Docker SizeRw）。
// 注意：Moby overlay2 的 SizeRw 按 FileInfo.Size() 统计逻辑字节数（稀疏文件按逻辑大小计），
// 故仅作独立参考读数，不得用于与宿主机 statfs 物理磁盘做堆叠扣减；结果按 orbDiskCacheTTL 缓存并在 5s 超时内执行。
func (d *Docker) OrbDiskUsage(ctx context.Context) (int64, map[string]int64, bool) {
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	t := now()

	d.orbDiskMu.Lock()
	if d.orbDiskValid && t.Sub(d.orbDiskAt) < orbDiskCacheTTL {
		total, cp := d.orbDiskTotal, maps.Clone(d.orbDiskByOrb)
		d.orbDiskMu.Unlock()
		return total, cp, true
	}
	d.orbDiskMu.Unlock()

	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var stdout bytes.Buffer
	err := d.exec(qctx, &stdout, io.Discard, nil,
		"ps", "-a", "--filter", "label=oryn-orb=1", "--size", "--format", "{{.Names}}\t{{.Size}}",
	)

	d.orbDiskMu.Lock()
	defer d.orbDiskMu.Unlock()
	if err != nil {
		if d.orbDiskValid {
			return d.orbDiskTotal, maps.Clone(d.orbDiskByOrb), true
		}
		return 0, nil, false
	}

	total, byOrb := parseDockerPSSizes(stdout.String())
	d.orbDiskTotal = total
	d.orbDiskByOrb = byOrb
	d.orbDiskAt = t
	d.orbDiskValid = true
	return total, maps.Clone(byOrb), true
}

// parseDockerPSSizes 解析 `docker ps -a --size --format '{{.Names}}\t{{.Size}}'` 输出。
// 每行格式如 `orb_01abc\t128MB (virtual 2.14GB)`；仅取括号前的可写层 SizeRw，避免重复计入共享基础镜像。
func parseDockerPSSizes(out string) (int64, map[string]int64) {
	byOrb := map[string]int64{}
	var total int64
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, sizeField, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		rwPart, _, _ := strings.Cut(sizeField, "(")
		n, parsed := parseHumanBytes(strings.TrimSpace(rwPart))
		if !parsed {
			continue
		}
		byOrb[name] = n
		total += n
	}
	return total, byOrb
}

// parseHumanBytes 解析 Docker units.HumanSize 格式（SI 1000 进制与 IEC 1024 进制均兼容）。
func parseHumanBytes(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	idx := 0
	for idx < len(s) && ((s[idx] >= '0' && s[idx] <= '9') || s[idx] == '.') {
		idx++
	}
	if idx == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(s[:idx], 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	unit := strings.TrimSpace(s[idx:])
	var mult float64
	switch strings.ToLower(unit) {
	case "", "b":
		mult = 1
	case "kb":
		mult = 1e3
	case "mb":
		mult = 1e6
	case "gb":
		mult = 1e9
	case "tb":
		mult = 1e12
	case "pb":
		mult = 1e15
	case "kib":
		mult = 1024
	case "mib":
		mult = 1024 * 1024
	case "gib":
		mult = 1024 * 1024 * 1024
	case "tib":
		mult = 1024 * 1024 * 1024 * 1024
	default:
		return 0, false
	}
	return int64(math.Round(v * mult)), true
}

// OrbDiskUsage 实现 OrbDiskSizer：统计 Firecracker 每台 VM 可写盘（vms/<orbId>/disk.ext4）的已分配块字节数（st_blocks*512）。
// 注意：由于 CopySparse 使用 --reflink=auto，支持 CoW 的文件系统上 VM 盘与快照可能共享物理块，此值仅作独立参考读数，不得与宿主机 statfs 扣减。
func (d *FirecrackerDriver) OrbDiskUsage(ctx context.Context) (int64, map[string]int64, bool) {
	if ctx.Err() != nil {
		return 0, nil, false
	}
	entries, err := os.ReadDir(d.paths.vmsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return 0, map[string]int64{}, true
		}
		return 0, nil, false
	}
	byOrb := make(map[string]int64, len(entries))
	var total int64
	for _, ent := range entries {
		if ctx.Err() != nil {
			return 0, nil, false
		}
		if !ent.IsDir() {
			continue
		}
		orbID := ent.Name()
		info, err := os.Stat(d.paths.vmDisk(orbID))
		if err != nil {
			continue
		}
		n := allocatedFileBytes(info)
		byOrb[orbID] = n
		total += n
	}
	return total, byOrb, true
}

// OrbDiskUsage 实现 OrbDiskSizer：统计 Process 驱动下各 orb 目录（<Root>/orbs/<orbId>）的实际物理磁盘占用。
func (p *Process) OrbDiskUsage(ctx context.Context) (int64, map[string]int64, bool) {
	if ctx.Err() != nil {
		return 0, nil, false
	}
	orbsDir := filepath.Join(p.Root, "orbs")
	entries, err := os.ReadDir(orbsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, map[string]int64{}, true
		}
		return 0, nil, false
	}
	byOrb := make(map[string]int64, len(entries))
	var total int64
	for _, ent := range entries {
		if ctx.Err() != nil {
			return 0, nil, false
		}
		if !ent.IsDir() {
			continue
		}
		orbID := ent.Name()
		var orbBytes int64
		walkErr := filepath.WalkDir(filepath.Join(orbsDir, orbID), func(_ string, d fs.DirEntry, err error) error {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			if info, statErr := d.Info(); statErr == nil {
				orbBytes += allocatedFileBytes(info)
			}
			return nil
		})
		if walkErr != nil && ctx.Err() != nil {
			return 0, nil, false
		}
		byOrb[orbID] = orbBytes
		total += orbBytes
	}
	return total, byOrb, true
}

// trimOrbDiskMap 在 orb 数量超过 orbDiskMaxEntries 时截断 map，防止心跳 JSON 过大。
func trimOrbDiskMap(byOrb map[string]int64) map[string]int64 {
	if len(byOrb) == 0 {
		return nil
	}
	if len(byOrb) <= orbDiskMaxEntries {
		return byOrb
	}
	out := make(map[string]int64, orbDiskMaxEntries)
	for k, v := range byOrb {
		out[k] = v
		if len(out) >= orbDiskMaxEntries {
			break
		}
	}
	return out
}
