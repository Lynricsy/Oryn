//go:build linux

package providerrt

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

const hostMiB = 1 << 20

func (s *hostMetricsSampler) fillPlatformMetrics(m *apigen.ProviderHostMetrics, diskPath string) {
	if busy, total, ok := readProcStatTotals(s.procStat); ok {
		s.mu.Lock()
		if s.hasPrevCPU && total > s.prevCPUTot && busy >= s.prevCPUBusy {
			pct := float32(float64(busy-s.prevCPUBusy) / float64(total-s.prevCPUTot) * 100)
			if pct < 0 {
				pct = 0
			} else if pct > 100 {
				pct = 100
			}
			m.CPUPct = &pct
		}
		s.prevCPUBusy, s.prevCPUTot, s.hasPrevCPU = busy, total, true
		s.mu.Unlock()
	}

	if l1, l5, l15, ok := readProcLoadAvg(s.loadAvg); ok {
		m.LoadAvg1 = &l1
		m.LoadAvg5 = &l5
		m.LoadAvg15 = &l15
	}

	if up, ok := readProcUptime(s.procUptime); ok {
		m.UptimeSeconds = &up
	}

	if totalBytes, usedBytes, availBytes, ok := readProcMemInfo(s.memInfo); ok {
		m.MemTotalMB = int64(totalBytes / hostMiB)
		m.MemUsedMB = int64(usedBytes / hostMiB)
		availMB := int64(availBytes / hostMiB)
		m.MemAvailableMB = &availMB
	}

	if path, total, used, avail, ok := statDisk(diskPath); ok {
		m.DiskPath = &path
		m.DiskTotalBytes = total
		m.DiskUsedBytes = used
		m.DiskAvailableBytes = &avail
	}
}

// readProcStatTotals 读取 /proc/stat 首行，返回 (busyTicks, totalTicks, ok)。
func readProcStatTotals(path string) (busy, total uint64, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, 0, false
	}
	fields := strings.Fields(sc.Text())
	// cpu user nice system idle iowait irq softirq steal …
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	var vals [8]uint64
	n := min(len(fields)-1, len(vals))
	for i := range n {
		v, err := strconv.ParseUint(fields[i+1], 10, 64)
		if err != nil {
			return 0, 0, false
		}
		vals[i] = v
		total += v
	}
	// idle = vals[3], iowait = vals[4]
	idle := vals[3] + vals[4]
	if total >= idle {
		busy = total - idle
	}
	return busy, total, total > 0
}

func readProcLoadAvg(path string) (l1, l5, l15 float32, ok bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) < 3 {
		return 0, 0, 0, false
	}
	v1, err1 := strconv.ParseFloat(fields[0], 32)
	v5, err2 := strconv.ParseFloat(fields[1], 32)
	v15, err3 := strconv.ParseFloat(fields[2], 32)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, false
	}
	return float32(v1), float32(v5), float32(v15), true
}

func readProcUptime(path string) (int64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) < 1 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return int64(v), true
}

func readProcMemInfo(path string) (total, used, avail uint64, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0, false
	}
	defer f.Close()
	vals := map[string]uint64{}
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
		vals[strings.TrimSuffix(fields[0], ":")] = n
	}
	total = vals["MemTotal"]
	if total == 0 {
		return 0, 0, 0, false
	}
	if a, has := vals["MemAvailable"]; has {
		avail = a
	} else {
		avail = vals["MemFree"] + vals["Buffers"] + vals["Cached"]
	}
	if avail > total {
		avail = total
	}
	used = total - avail
	return total, used, avail, true
}

func statDisk(path string) (resolved string, total, used, avail int64, ok bool) {
	candidates := []string{path}
	if path != "/" {
		candidates = append(candidates, "/")
	}
	for _, p := range candidates {
		var st syscall.Statfs_t
		if err := syscall.Statfs(p, &st); err != nil {
			continue
		}
		bsize := uint64(st.Bsize)
		t := st.Blocks * bsize
		bfree := st.Bfree * bsize
		bavail := st.Bavail * bsize
		var u uint64
		if t >= bfree {
			u = t - bfree
		}
		return p, int64(t), int64(u), int64(bavail), true
	}
	return "", 0, 0, 0, false
}
