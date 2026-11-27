package orbdrt

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// dialVsock 连 CID 2：宿主上装了 vsock_loopback 且本机 CID 是 2（vhost_vsock）时，连接落到本机的 vsock 监听，
// 可以不起 VM 验证拨号、读写、截止时间与 ctx 取消。条件不满足时跳过（真机由 TestRealFirecrackerSandbox 覆盖）。
func TestLinkViaVsockLoopback(t *testing.T) {
	lfd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Skipf("no AF_VSOCK: %v", err)
	}
	ln := os.NewFile(uintptr(lfd), "vsock-listen")
	t.Cleanup(func() { _ = ln.Close() })
	if err := unix.Bind(lfd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: unix.VMADDR_PORT_ANY}); err != nil {
		t.Skipf("vsock bind: %v", err)
	}
	require.NoError(t, unix.Listen(lfd, 4))
	sa, err := unix.Getsockname(lfd)
	require.NoError(t, err)
	port := sa.(*unix.SockaddrVM).Port

	accepted := make(chan *os.File, 1)
	go func() {
		nfd, _, err := unix.Accept(lfd)
		if err != nil {
			close(accepted)
			return
		}
		accepted <- os.NewFile(uintptr(nfd), "vsock-peer")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dialVia(ctx, "vsock:"+strconv.FormatUint(uint64(port), 10))
	if err != nil {
		// ETIMEDOUT：本机有 vsock 但 CID 2 不是自己（如 GitHub runner 的 VM），连接无人应答
		if errors.Is(err, unix.ENODEV) || errors.Is(err, unix.EHOSTUNREACH) || errors.Is(err, unix.ECONNRESET) ||
			errors.Is(err, unix.ENETUNREACH) || errors.Is(err, unix.EAFNOSUPPORT) || errors.Is(err, unix.ETIMEDOUT) {
			t.Skipf("CID 2 is not this host: %v", err)
		}
		require.NoError(t, err)
	}
	defer c.Close()
	assert.Equal(t, "vsock", c.RemoteAddr().Network())
	peer := <-accepted
	require.NotNil(t, peer)
	defer peer.Close()

	_, err = c.Write([]byte("ping"))
	require.NoError(t, err)
	buf := make([]byte, 4)
	_, err = io.ReadFull(peer, buf)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(buf))

	require.NoError(t, c.SetReadDeadline(time.Now().Add(50*time.Millisecond)))
	_, err = c.Read(buf)
	assert.ErrorIs(t, err, os.ErrDeadlineExceeded)

	// 没有监听的端口：拨号失败而不是挂住。
	_, err = dialVsock(ctx, port+1)
	assert.Error(t, err)
}
