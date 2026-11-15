package providerrt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/Lynricsy/Oryn/internal/config"
)

// ownConsole 让槽位 0 的 uid 等于测试进程的 uid（openConsole 要求串口日志归槽位 uid 所有）。
func ownConsole(cfg *config.Firecracker) { cfg.UIDBase = os.Getuid() }

// usage 返回文件实际占用的字节数（先 fsync，免得延迟分配影响 st_blocks）。
func usage(t *testing.T, path string) int64 {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	require.NoError(t, f.Sync())
	require.NoError(t, f.Close())
	n, err := diskUsage(path)
	require.NoError(t, err)
	return n
}

// logData 返回约 n 字节的串口日志，最后一行是 marker。
func logData(n int, marker string) []byte {
	line := []byte("[    1.234567] guest kernel log line padding padding padding\n")
	var b bytes.Buffer
	for b.Len() < n-len(marker)-1 {
		b.Write(line)
	}
	b.WriteString(marker + "\n")
	return b.Bytes()
}

// consoleFile 建好 jail 里的串口日志目录，返回串口日志路径。
func (c *fcTest) consoleFile(orbID string) string {
	c.t.Helper()
	p := c.d.consolePath(orbID)
	require.NoError(c.t, os.MkdirAll(filepath.Dir(p), 0o700))
	return p
}

func newConsoleTest(t *testing.T) *fcTest {
	c := newFCTest(t, ownConsole)
	c.d.consoleMax, c.d.consoleKeep = 256<<10, 64<<10
	c.must(fcSpawn(orbA))
	return c
}

// S3 补充层：provider 运行时，guest 写超上限的串口日志后实际占用回到上限以内；文件大小与 firecracker 的写偏移
// （它不以 O_APPEND 打开）不受影响，末尾保留，consoleTail 跳过空洞读到最新输出；没超上限时不动。
func TestFCConsoleTrim(t *testing.T) {
	c := newConsoleTest(t)
	const limit, keep = 256 << 10, 64 << 10
	path := c.consoleFile(orbA)
	w, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	require.NoError(t, err)
	defer w.Close()

	for i, n := range []int{4 << 20, 1 << 20} {
		marker := fmt.Sprintf("tail-marker-%d", i)
		_, err := w.Write(logData(n, marker))
		require.NoError(t, err)
		st, err := os.Stat(path)
		require.NoError(t, err)
		require.Greater(t, usage(t, path), int64(limit))
		before, err := os.ReadFile(path)
		require.NoError(t, err)

		c.d.trimConsoles()

		assert.LessOrEqual(t, usage(t, path), int64(limit))
		st2, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, st.Size(), st2.Size(), "打孔不改文件大小")
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, before[len(before)-keep:], after[len(after)-keep:], "末尾 keep 字节原样保留")

		var out bytes.Buffer
		c.d.consoleTail(c.meta(orbA), &out)
		assert.Contains(t, out.String(), marker)
		assert.NotContains(t, out.String(), "\x00")
	}

	u := usage(t, path)
	c.d.trimConsoles()
	assert.Equal(t, u, usage(t, path))
}

// 升级前启动的 VM 串口日志还在 jail 根下：同样打孔。
func TestFCConsoleTrimLegacy(t *testing.T) {
	c := newConsoleTest(t)
	require.NoError(t, os.MkdirAll(c.d.paths.jailRoot(orbA), 0o700))
	legacy := filepath.Join(c.d.paths.jailRoot(orbA), fcConsoleLog)
	require.NoError(t, os.WriteFile(legacy, logData(2<<20, "end"), 0o600))
	c.d.trimConsoles()
	assert.LessOrEqual(t, usage(t, legacy), int64(256<<10))
}

// Housekeep 立即检查一次，ctx 结束即返回。
func TestFCHousekeep(t *testing.T) {
	c := newConsoleTest(t)
	path := c.consoleFile(orbA)
	require.NoError(t, os.WriteFile(path, logData(2<<20, "end"), 0o600))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.d.Housekeep(ctx)
	assert.LessOrEqual(t, usage(t, path), int64(256<<10))
	var _ Housekeeper = c.d
}

// 被攻破的 firecracker 可以把 jail 根下的 console.log 换成符号链接或别的文件的硬链接：不打孔、不读出来。
func TestFCConsoleHostile(t *testing.T) {
	big := bytes.Repeat([]byte("SECRET-DATA\n"), 100<<10)
	t.Run("symlink", func(t *testing.T) {
		c := newConsoleTest(t)
		path := c.consoleFile(orbA)
		secret := filepath.Join(c.root, "host-secret")
		require.NoError(t, os.WriteFile(secret, big, 0o600))
		require.NoError(t, os.Symlink(secret, path))
		u := usage(t, secret)
		c.d.trimConsoles()
		assert.Equal(t, u, usage(t, secret))
		var out bytes.Buffer
		c.d.consoleTail(c.meta(orbA), &out)
		assert.Empty(t, out.String())
	})
	t.Run("hardlink", func(t *testing.T) {
		c := newConsoleTest(t)
		path := c.consoleFile(orbA)
		disk := c.d.paths.vmDisk(orbA)
		require.NoError(t, os.WriteFile(disk, big, 0o600))
		require.NoError(t, os.Link(disk, path))
		c.d.trimConsoles()
		b, err := os.ReadFile(disk)
		require.NoError(t, err)
		assert.Equal(t, big, b, "可写盘不能被打孔")
		var out bytes.Buffer
		c.d.consoleTail(c.meta(orbA), &out)
		assert.NotContains(t, out.String(), "SECRET")
	})
	t.Run("other owner", func(t *testing.T) {
		if os.Getuid() != 0 {
			t.Skip("chown 需要 root")
		}
		c := newConsoleTest(t)
		path := c.consoleFile(orbA)
		require.NoError(t, os.WriteFile(path, big, 0o600))
		require.NoError(t, os.Chown(path, 4242, 4242))
		u := usage(t, path)
		c.d.trimConsoles()
		assert.Equal(t, u, usage(t, path))
		var st syscall.Stat_t
		require.NoError(t, syscall.Stat(path, &st))
		assert.Equal(t, uint32(4242), st.Uid)
	})
}

// S3 硬上限：串口日志所在的 tmpfs 写满后返回 ENOSPC（firecracker 丢字节继续运行），实际占用不超过容量，
// 与 provider 是否在跑无关；Kill 的卸载让 jail 目录能整个删掉。需要 root（mount）。
func TestFCConsoleTmpfs(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("挂载 tmpfs 需要 root")
	}
	const size = 256 << 10
	root := t.TempDir()
	dir := filepath.Join(root, fcConsoleDir)
	require.NoError(t, mountConsole(dir, size, 4242))
	t.Cleanup(func() { _ = unmountConsole(dir) })

	path := filepath.Join(dir, fcConsoleLog)
	var st syscall.Stat_t
	require.NoError(t, syscall.Stat(path, &st))
	assert.Equal(t, uint32(4242), st.Uid)
	assert.Equal(t, uint32(0o600), st.Mode&0o777)
	require.NoError(t, syscall.Stat(dir, &st))
	assert.Equal(t, uint32(0), st.Uid, "目录归 root：槽位 uid 不能在里面改名或另建文件")
	assert.Equal(t, uint32(0o755), st.Mode&0o777)

	// 像 firecracker 那样经一个 fd 持续写，直到 ENOSPC。
	f, err := os.OpenFile(path, os.O_RDWR|unix.O_NONBLOCK, 0)
	require.NoError(t, err)
	chunk := bytes.Repeat([]byte("x"), 4<<10)
	var werr error
	for range (4 * size) / len(chunk) {
		if _, werr = f.Write(chunk); werr != nil {
			break
		}
	}
	require.ErrorIs(t, werr, unix.ENOSPC)
	require.NoError(t, f.Close())
	assert.LessOrEqual(t, usage(t, path), int64(size))

	// 卸载后目录可删（Kill 的顺序）；没挂载时卸载不算错。
	require.NoError(t, unmountConsole(dir))
	require.NoError(t, unmountConsole(dir))
	require.NoError(t, os.RemoveAll(root))
	require.NoError(t, unmountConsole(dir))
	assert.False(t, errors.Is(unmountConsole(filepath.Join(root, "missing")), unix.ENOENT))
}
