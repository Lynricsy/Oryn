package unixsock

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// longDir 返回一个路径长度使其中的 socket 超过 sun_path（108 字节）的目录。
func longDir(t *testing.T) string {
	t.Helper()
	short, err := os.MkdirTemp("", "us")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	long := filepath.Join(short, strings.Repeat("d", 60), strings.Repeat("e", 60))
	require.NoError(t, os.MkdirAll(long, 0o700))
	return long
}

// longSockListener 在长目录里放一个 unix listener：先在短路径上 listen，再把 socket 文件 rename 进去（connect 按 inode 找到它）。
func longSockListener(t *testing.T) (net.Listener, string) {
	t.Helper()
	long := longDir(t)
	short := filepath.Dir(filepath.Dir(long))
	ln, err := net.Listen("unix", filepath.Join(short, "s"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	path := filepath.Join(long, "api.sock")
	require.NoError(t, os.Rename(filepath.Join(short, "s"), path))
	require.Greater(t, len(path), 108)
	return ln, path
}

func TestDialLongPath(t *testing.T) {
	ln, path := longSockListener(t)
	accepted := make(chan struct{})
	go func() {
		if c, err := ln.Accept(); err == nil {
			_ = c.Close()
			close(accepted)
		}
	}()
	c, err := Dial(context.Background(), path)
	require.NoError(t, err)
	_ = c.Close()
	<-accepted

	// 短路径照常直拨；不存在的路径报错，且不泄漏 fd。
	ln2, err := net.Listen("unix", filepath.Join(t.TempDir(), "s"))
	require.NoError(t, err)
	defer ln2.Close()
	c, err = Dial(context.Background(), ln2.Addr().String())
	require.NoError(t, err)
	_ = c.Close()
	_, err = Dial(context.Background(), filepath.Join(filepath.Dir(path), "missing.sock"))
	assert.Error(t, err)
	_, err = Dial(context.Background(), filepath.Join(filepath.Dir(path), "nodir", "x.sock"))
	assert.Error(t, err)
}

func TestListen(t *testing.T) {
	for name, dir := range map[string]string{"short": t.TempDir(), "long": longDir(t)} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, "link.sock")
			require.NoError(t, os.WriteFile(path, []byte("stale"), 0o600), "同名旧文件会被替换")
			ln, err := Listen(path, 0o666, -1)
			require.NoError(t, err)
			st, err := os.Stat(path)
			require.NoError(t, err)
			assert.Equal(t, os.ModeSocket|0o666, st.Mode()&(os.ModeSocket|os.ModePerm))

			go func() {
				if c, err := ln.Accept(); err == nil {
					_, _ = c.Write([]byte("hi"))
					_ = c.Close()
				}
			}()
			c, err := Dial(context.Background(), path)
			require.NoError(t, err)
			buf := make([]byte, 2)
			_, err = c.Read(buf)
			require.NoError(t, err)
			assert.Equal(t, "hi", string(buf))
			_ = c.Close()

			require.NoError(t, ln.Close())
			_, err = os.Stat(path)
			assert.NoError(t, err, "关闭不删除 socket 文件：同一路径可能已被新 listener 接管")
		})
	}
}
