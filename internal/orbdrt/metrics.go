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

// metricsReader 采集 cgroup v2 / statfs / 网卡计数（H6.1）。缺失的数据源一律记 0。
type metricsReader struct {
	cfg       Config
	prevUsage uint64
	prevAt    time.Time
}

func newMetricsReader(cfg Config) *metricsReader {
	m := &metricsReader{cfg: cfg}
	m.prevUsage, _ = m.cpuUsageUsec()
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
	if usage, ok := m.cpuUsageUsec(); ok {
		if wall := now.Sub(m.prevAt).Microseconds(); wall > 0 && usage >= m.prevUsage {
			s.CPUPct = float32(float64(usage-m.prevUsage) / float64(wall) * 100)
		}
		m.prevUsage, m.prevAt = usage, now
	}
	if cur, ok := readUint(filepath.Join(m.cfg.CgroupDir, "memory.current")); ok {
		s.MemUsedMB = float32(float64(cur) / mib)
	}
	if lim, ok := readUint(filepath.Join(m.cfg.CgroupDir, "memory.max")); ok {
		s.MemLimitMB = float32(float64(lim) / mib)
	} else if total, ok := memTotal(m.cfg.MemInfo); ok {
		s.MemLimitMB = float32(float64(total) / mib) // memory.max 为 "max"（无限制）或缺失
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(m.cfg.DiskPath, &st); err == nil {
		used := (st.Blocks - st.Bfree) * uint64(st.Bsize)
		s.DiskUsedMB = float32(float64(used) / mib)
	}
	if v, ok := readUint(filepath.Join(m.cfg.NetDir, "statistics", "rx_bytes")); ok {
		s.NetRxBytes = int64(v)
	}
	if v, ok := readUint(filepath.Join(m.cfg.NetDir, "statistics", "tx_bytes")); ok {
		s.NetTxBytes = int64(v)
	}
	return s
}

func (m *metricsReader) cpuUsageUsec() (uint64, bool) {
	f, err := os.Open(filepath.Join(m.cfg.CgroupDir, "cpu.stat"))
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

func readUint(path string) (uint64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return n, err == nil
}

func memTotal(path string) (uint64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			return kb * 1024, err == nil
		}
	}
	return 0, false
}
