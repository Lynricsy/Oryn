package providerrt

import (
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// hostMetricsSampler 采集 provider 宿主机的 CPU / 内存 / 磁盘 / 负载快照。
// 每次调用 Sample 仅读取内核 procfs 与 statfs（毫秒级），缺失的项保持零值或省略。
type hostMetricsSampler struct {
	procStat    string
	memInfo     string
	loadAvg     string
	procUptime  string
	mu          sync.Mutex
	prevCPUBusy uint64
	prevCPUTot  uint64
	hasPrevCPU  bool
}

func newHostMetricsSampler() *hostMetricsSampler {
	return &hostMetricsSampler{
		procStat:   "/proc/stat",
		memInfo:    "/proc/meminfo",
		loadAvg:    "/proc/loadavg",
		procUptime: "/proc/uptime",
	}
}

// Sample 采集一次宿主机快照。diskPath 为空时回退到根目录 "/"。
func (s *hostMetricsSampler) Sample(now time.Time, diskPath string, limits *apigen.ProviderHostLimits) *apigen.ProviderHostMetrics {
	if diskPath == "" {
		diskPath = "/"
	}
	m := &apigen.ProviderHostMetrics{
		SampledAt: now.UnixMilli(),
		Os:        new(runtime.GOOS),
		Arch:      new(runtime.GOARCH),
		CPUCores:  runtime.NumCPU(),
		Limits:    limits,
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		m.Hostname = &host
	}
	s.fillPlatformMetrics(m, diskPath)
	return m
}
