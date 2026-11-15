package providerrt

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/fcguest"
)

// longSockListener 在路径超过 sun_path（108 字节）的目录里放一个 unix listener：
// 先在短路径上 listen，再把 socket 文件 rename 进长目录（connect 按 inode 找到它）。
func longSockListener(t *testing.T) (net.Listener, string) {
	t.Helper()
	short, err := os.MkdirTemp("", "us")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	long := filepath.Join(short, strings.Repeat("d", 60), strings.Repeat("e", 60))
	require.NoError(t, os.MkdirAll(long, 0o700))
	ln, err := net.Listen("unix", filepath.Join(short, "s"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	path := filepath.Join(long, "api.sock")
	require.NoError(t, os.Rename(filepath.Join(short, "s"), path))
	require.Greater(t, len(path), 108)
	return ln, path
}

func TestDialUnixLongPath(t *testing.T) {
	ln, path := longSockListener(t)
	accepted := make(chan struct{})
	go func() {
		if c, err := ln.Accept(); err == nil {
			_ = c.Close()
			close(accepted)
		}
	}()
	c, err := dialUnix(context.Background(), path)
	require.NoError(t, err)
	_ = c.Close()
	<-accepted

	// 短路径照常直拨；不存在的路径报错，且不泄漏 fd。
	ln2, err := net.Listen("unix", filepath.Join(t.TempDir(), "s"))
	require.NoError(t, err)
	defer ln2.Close()
	c, err = dialUnix(context.Background(), ln2.Addr().String())
	require.NoError(t, err)
	_ = c.Close()
	_, err = dialUnix(context.Background(), filepath.Join(filepath.Dir(path), "missing.sock"))
	assert.Error(t, err)
	_, err = dialUnix(context.Background(), filepath.Join(filepath.Dir(path), "nodir", "x.sock"))
	assert.Error(t, err)
}

// api.sock 与 v.sock 的客户端都经 dialUnix：路径超过 sun_path 时照样能连上。
func TestFCLongSockClients(t *testing.T) {
	ln, path := longSockListener(t)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"state":"Running"}`)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	st, err := unixAPI{sock: func(string) string { return path }}.State(context.Background(), "orb-1")
	require.NoError(t, err)
	assert.Equal(t, "Running", st)

	vln, vpath := longSockListener(t)
	go func() {
		conn, err := vln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		if _, err := r.ReadString('\n'); err != nil {
			return
		}
		_, _ = io.WriteString(conn, "OK 1\n")
		if _, err := fcguest.ReadRequest(r); err != nil {
			return
		}
		_ = fcguest.WriteResponse(conn, fcguest.Response{OK: false, Error: "nope"})
	}()
	err = vsockGuest{sock: func(string) string { return vpath }}.Do(context.Background(), "orb-1", fcguest.Request{Op: fcguest.OpPing})
	var re *fcguest.RemoteError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, &fcguest.RemoteError{Op: fcguest.OpPing, Msg: "nope"}, re)
}

// 启动失败时把 jail 里串口日志的末尾（至多 4 KiB）带进任务输出。
func TestFCConsoleTail(t *testing.T) {
	c := newFCTest(t)
	c.d.startTimeout = 10 * time.Millisecond
	c.f.startNever = true
	root := c.d.paths.jailRoot(orbA)
	require.NoError(t, os.MkdirAll(root, 0o700))
	log := strings.Repeat("x", 10000) + "Kernel panic - not syncing: Attempted to kill init!\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, fcConsoleLog), []byte(log), 0o600))
	_, err := c.exec(fcSpawn(orbA))
	require.Error(t, err)
	assert.Contains(t, c.out.String(), "guest 串口输出末尾：\n")
	assert.Contains(t, c.out.String(), "Kernel panic - not syncing")
	assert.NotContains(t, c.out.String(), strings.Repeat("x", fcConsoleTailBytes))
}
