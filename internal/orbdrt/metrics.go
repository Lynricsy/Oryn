package orbdrt

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

const mib = 1 << 20

// procUserHZ 是 /proc/stat 的时钟节拍（USER_HZ，Linux 用户态 ABI 固定为 100）。
const procUserHZ = 100

// metricsSource 是 CPU 与内存的数据源：容器内读 cgroup v2，runner 与 Firecracker guest 读 /proc（M11b）。
type metricsSource interface {
	// cpuUsageUsec 返回累计 CPU 时间（微秒）；读不到时 ok=false。
	cpuUsageUsec() (usec uint64, ok bool)
	// memory 返回已用与上限（字节），读不到的项为 0。
	memory() (used, limit uint64)
}

// metricsReader 采集 CPU / 内存（数据源见 metricsSource）、statfs 与网卡计数（H6.1）。缺失的数据一律记 0。
type metricsReader struct {
	src       metricsSource
	diskPath  string
	netDir    string
	prevUsage uint64
	prevAt    time.Time
}

// newMetricsReader 选择数据源：runner 形态或 <CgroupDir>/memory.current 不存在（例如 Firecracker guest 的根
// cgroup）时用 /proc，否则用 cgroup v2。
func newMetricsReader(cfg Config) *metricsReader {
	var src metricsSource = cgroupSource{dir: cfg.CgroupDir, memInfo: cfg.MemInfo}
	if _, err := os.Stat(filepath.Join(cfg.CgroupDir, "memory.current")); cfg.Runner != nil || err != nil {
		src = procSource{stat: cfg.ProcStat, memInfo: cfg.MemInfo}
	}
	m := &metricsReader{src: src, diskPath: cfg.DiskPath, netDir: cfg.NetDir}
	m.prevUsage, _ = m.src.cpuUsageUsec()
	m.prevAt = time.Now()
	return m
}

func (r *Runtime) metricsLoop(ctx context.Context) {
	tick := time.NewTicker(r.cfg.MetricsInterval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			s := r.metrics.sample(time.Now())
			r.notify("metrics.sample", s)
		case <-ctx.Done():
			return
		}
	}
}

// sample 生成一条样本。cpuPct 以单核为 100%（与 docker stats 一致，多核可超过 100）。
func (m *metricsReader) sample(now time.Time) apigen.OrbMetricSample {
	s := apigen.OrbMetricSample{TS: apigen.TimestampMs(now.UnixMilli())}
	if usage, ok := m.src.cpuUsageUsec(); ok {
		if wall := now.Sub(m.prevAt).Microseconds(); wall > 0 && usage >= m.prevUsage {
			s.CPUPct = float32(float64(usage-m.prevUsage) / float64(wall) * 100)
		}
		m.prevUsage, m.prevAt = usage, now
	}
	used, limit := m.src.memory()
	s.MemUsedMB = float32(float64(used) / mib)
	s.MemLimitMB = float32(float64(limit) / mib)
	var st syscall.Statfs_t
	if err := syscall.Statfs(m.diskPath, &st); err == nil {
		used := (st.Blocks - st.Bfree) * uint64(st.Bsize)
		s.DiskUsedMB = float32(float64(used) / mib)
	}
	if v, ok := readUint(filepath.Join(m.netDir, "statistics", "rx_bytes")); ok {
		s.NetRxBytes = int64(v)
	}
	if v, ok := readUint(filepath.Join(m.netDir, "statistics", "tx_bytes")); ok {
		s.NetTxBytes = int64(v)
	}
	return s
}

// cgroupSource 读 cgroup v2：cpu.stat 的 usage_usec、memory.current、memory.max（"max" 或缺失时取 MemTotal）。
type cgroupSource struct{ dir, memInfo string }

func (c cgroupSource) cpuUsageUsec() (uint64, bool) {
	f, err := os.Open(filepath.Join(c.dir, "cpu.stat"))
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), " ")
		if ok && k == "usage_usec" {
			n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

func (c cgroupSource) memory() (used, limit uint64) {
	used, _ = readUint(filepath.Join(c.dir, "memory.current"))
	if lim, ok := readUint(filepath.Join(c.dir, "memory.max")); ok {
		limit = lim
	} else {
		limit = meminfo(c.memInfo)["MemTotal"]
	}
	return used, limit
}

// procSource 读整机计数：/proc/stat 首行（user + nice + system + irq + softirq + steal 的节拍数，guest 已含在
// user 里）与 /proc/meminfo（已用 = MemTotal − MemAvailable，上限 = MemTotal）。
type procSource struct{ stat, memInfo string }

func (p procSource) cpuUsageUsec() (uint64, bool) {
	f, err := os.Open(p.stat)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, false
	}
	fields := strings.Fields(sc.Text())
	// cpu user nice system idle iowait irq softirq steal …
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, false
	}
	var busy uint64
	for _, idx := range []int{1, 2, 3, 6, 7, 8} {
		if idx >= len(fields) {
			break // 极老的内核没有 irq / softirq / steal 列
		}
		n, err := strconv.ParseUint(fields[idx], 10, 64)
		if err != nil {
			return 0, false
		}
		busy += n
	}
	return busy * (1_000_000 / procUserHZ), true
}

func (p procSource) memory() (used, limit uint64) {
	mi := meminfo(p.memInfo)
	total, avail := mi["MemTotal"], mi["MemAvailable"]
	if total >= avail && avail > 0 {
		used = total - avail
	}
	return used, total
}

func readUint(path string) (uint64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return n, err == nil
}

// meminfo 读 /proc/meminfo 格式的文件，返回各项的字节数（kB 换算）；读不到时为空表。
func meminfo(path string) map[string]uint64 {
	out := map[string]uint64{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		n, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		if len(fields) >= 3 && fields[2] == "kB" {
			n *= 1024
		}
		out[strings.TrimSuffix(fields[0], ":")] = n
	}
	return out
}
