package providerrt

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

func TestHostMetricsSampler(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("procfs tests only run on linux")
	}
	dir := t.TempDir()
	statPath := filepath.Join(dir, "stat")
	memPath := filepath.Join(dir, "meminfo")
	loadPath := filepath.Join(dir, "loadavg")
	upPath := filepath.Join(dir, "uptime")

	require.NoError(t, os.WriteFile(statPath, []byte("cpu  100 0 100 800 0 0 0 0 0 0\n"), 0o600))
	require.NoError(t, os.WriteFile(memPath, []byte(
		"MemTotal:       16777216 kB\n"+
			"MemFree:         2097152 kB\n"+
			"MemAvailable:    6291456 kB\n",
	), 0o600))
	require.NoError(t, os.WriteFile(loadPath, []byte("1.25 0.85 0.50 2/300 1234\n"), 0o600))
	require.NoError(t, os.WriteFile(upPath, []byte("86400.75 12345.00\n"), 0o600))

	s := &hostMetricsSampler{
		procStat:   statPath,
		memInfo:    memPath,
		loadAvg:    loadPath,
		procUptime: upPath,
	}
	lim := &apigen.ProviderHostLimits{MaxVMs: new(8), MaxMemoryMB: new(32768)}
	now := time.UnixMilli(1_700_000_000_000)

	// 第一次采样记录基线，无 cpuPct 差分
	m1 := s.Sample(now, dir, lim)
	require.NotNil(t, m1)
	assert.Equal(t, now.UnixMilli(), m1.SampledAt)
	assert.Nil(t, m1.CPUPct)
	assert.Greater(t, m1.CPUCores, 0)
	assert.Equal(t, int64(16384), m1.MemTotalMB)
	assert.Equal(t, int64(10240), m1.MemUsedMB)
	require.NotNil(t, m1.MemAvailableMB)
	assert.Equal(t, int64(6144), *m1.MemAvailableMB)
	require.NotNil(t, m1.LoadAvg1)
	assert.InDelta(t, 1.25, float64(*m1.LoadAvg1), 0.01)
	require.NotNil(t, m1.UptimeSeconds)
	assert.Equal(t, int64(86400), *m1.UptimeSeconds)
	require.NotNil(t, m1.DiskPath)
	assert.Equal(t, dir, *m1.DiskPath)
	assert.Greater(t, m1.DiskTotalBytes, int64(0))
	require.NotNil(t, m1.Limits)
	assert.Equal(t, 8, *m1.Limits.MaxVMs)

	// 第二次采样：总 ticks 增加 1000（busy +350，idle +650）→ 35%
	require.NoError(t, os.WriteFile(statPath, []byte("cpu  300 0 250 1450 0 0 0 0 0 0\n"), 0o600))
	m2 := s.Sample(now.Add(15*time.Second), filepath.Join(dir, "nonexistent"), nil)
	require.NotNil(t, m2.CPUPct)
	assert.InDelta(t, 35.0, float64(*m2.CPUPct), 0.1)
	require.NotNil(t, m2.DiskPath)
	assert.Equal(t, "/", *m2.DiskPath, "不存在的目录应回退到根目录 /")
}
