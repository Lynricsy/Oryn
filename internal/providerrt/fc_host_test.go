package providerrt

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/config"
)

// osHost 的磁盘部分不需要 root：mke2fs 建稀疏 ext4、cp 稀疏复制并按实际占用计字节。
func TestFCOSHostDisks(t *testing.T) {
	for _, c := range []string{"mke2fs", "cp", "dumpe2fs"} {
		if _, err := exec.LookPath(c); err != nil {
			t.Skipf("PATH 中没有 %s", c)
		}
	}
	dir := t.TempDir()
	h := newOSHost(config.Firecracker{StateDir: dir})
	uid := os.Getuid()
	disk := filepath.Join(dir, "disk.ext4")
	require.NoError(t, h.MakeDisk(disk, 8, uid))
	st, err := os.Stat(disk)
	require.NoError(t, err)
	assert.Equal(t, int64(8)<<30, st.Size())
	used, err := diskUsage(disk)
	require.NoError(t, err)
	assert.Less(t, used, int64(1)<<30, "空盘应是稀疏文件")
	out, err := exec.Command("dumpe2fs", "-h", disk).CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "Filesystem volume name:   oryn-data")

	cp := filepath.Join(dir, "copy.ext4")
	n, err := h.CopySparse(disk, cp, uid)
	require.NoError(t, err)
	assert.Positive(t, n)
	assert.Less(t, n, int64(1)<<30)
	cst, err := os.Stat(cp)
	require.NoError(t, err)
	assert.Equal(t, st.Size(), cst.Size())
	assert.Equal(t, os.FileMode(0o600), cst.Mode().Perm())
}
