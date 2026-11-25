package providerrt

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/config"
)

func TestParseDockerPSSizes(t *testing.T) {
	raw := "" +
		"orb-a\t0B (virtual 2.14GB)\n" +
		"orb-b\t128MB (virtual 2.27GB)\n" +
		"orb-c\t1.5GB (virtual 3.64GB)\n" +
		"orb-d\t34.5kB\n" +
		"orb-e\t2GiB (virtual 4GiB)\n" +
		"broken-line-without-tab\n"

	total, byOrb := parseDockerPSSizes(raw)
	assert.Equal(t, int64(0), byOrb["orb-a"])
	assert.Equal(t, int64(128_000_000), byOrb["orb-b"])
	assert.Equal(t, int64(1_500_000_000), byOrb["orb-c"])
	assert.Equal(t, int64(34_500), byOrb["orb-d"])
	assert.Equal(t, int64(2*1024*1024*1024), byOrb["orb-e"])
	assert.Equal(t, int64(128_000_000+1_500_000_000+34_500+2*1024*1024*1024), total)
}

func TestFirecrackerOrbDiskUsageSparse(t *testing.T) {
	dir := t.TempDir()
	d := NewFirecracker(config.Firecracker{StateDir: dir}, "http://127.0.0.1:8080")

	// 创建一个稀疏文件（逻辑 1 GiB，但仅写入 1 MiB 物理块），确认统计结果远小于逻辑大小
	require.NoError(t, os.MkdirAll(d.paths.vmDir("orb-fc-1"), 0o700))
	f, err := os.Create(d.paths.vmDisk("orb-fc-1"))
	require.NoError(t, err)
	require.NoError(t, f.Truncate(1<<30))
	buf := make([]byte, 1<<20)
	for i := range buf {
		buf[i] = 0x5a
	}
	_, err = f.WriteAt(buf, 0)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	total, byOrb, ok := d.OrbDiskUsage(context.Background())
	require.True(t, ok)
	assert.Contains(t, byOrb, "orb-fc-1")
	assert.GreaterOrEqual(t, byOrb["orb-fc-1"], int64(1<<20))
	assert.Less(t, byOrb["orb-fc-1"], int64(64<<20), "稀疏盘不应按 1GiB 逻辑大小虚报")
	assert.Equal(t, byOrb["orb-fc-1"], total)
}

func TestProcessOrbDiskUsage(t *testing.T) {
	dir := t.TempDir()
	p := NewProcess(dir, "orbd", config.Docker{})

	h1 := p.HomeDir("orb-p1")
	require.NoError(t, os.MkdirAll(h1, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(h1, "data.bin"), make([]byte, 64*1024), 0o600))

	total, byOrb, ok := p.OrbDiskUsage(context.Background())
	require.True(t, ok)
	assert.GreaterOrEqual(t, byOrb["orb-p1"], int64(64*1024))
	assert.Equal(t, byOrb["orb-p1"], total)
}
