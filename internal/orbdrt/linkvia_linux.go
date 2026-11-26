package orbdrt

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// dialVsock 连宿主（CID 2）的 vsock 端口。socket 以非阻塞方式创建并交给 Go 的 poller（os.NewFile 识别非阻塞 fd），
// connect 的完成经 SyscallConn 等可写再查 SO_ERROR；ctx 结束时把写截止时间拨到过去以解除等待。
// 这样 ctx 取消时不会有别的 goroutine 仍在用这个 fd。
func dialVsock(ctx context.Context, port uint32) (net.Conn, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	f := os.NewFile(uintptr(fd), fmt.Sprintf("vsock:%d", port))
	sa := &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: port}
	rc, err := f.SyscallConn()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("vsock connect: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = f.SetWriteDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = f.SetWriteDeadline(time.Unix(1, 0)) })
	var (
		started bool
		cerr    error
	)
	werr := rc.Write(func(s uintptr) bool {
		if !started {
			started = true
			cerr = unix.Connect(int(s), sa)
			if errors.Is(cerr, unix.EINPROGRESS) || errors.Is(cerr, unix.EAGAIN) || errors.Is(cerr, unix.EALREADY) {
				cerr = nil
				return false // 等可写
			}
			return true
		}
		n, err := unix.GetsockoptInt(int(s), unix.SOL_SOCKET, unix.SO_ERROR)
		switch {
		case err != nil:
			cerr = err
		case n != 0:
			cerr = unix.Errno(n)
		default:
			if _, err := unix.Getpeername(int(s)); errors.Is(err, unix.ENOTCONN) {
				return false // 虚假唤醒，继续等
			}
		}
		return true
	})
	if !stop() {
		_ = f.Close()
		return nil, fmt.Errorf("vsock connect: %w", ctx.Err())
	}
	if werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = f.Close()
		if ctx.Err() != nil {
			werr = ctx.Err()
		}
		return nil, fmt.Errorf("vsock connect cid %d port %d: %w", unix.VMADDR_CID_HOST, port, werr)
	}
	_ = f.SetWriteDeadline(time.Time{})
	local := vsockAddr{port: unix.VMADDR_PORT_ANY, cid: unix.VMADDR_CID_ANY}
	if sa, err := unix.Getsockname(fd); err == nil {
		if vm, ok := sa.(*unix.SockaddrVM); ok {
			local = vsockAddr{cid: vm.CID, port: vm.Port}
		}
	}
	return &vsockConn{File: f, local: local, remote: vsockAddr{cid: unix.VMADDR_CID_HOST, port: port}}, nil
}

// vsockConn 把已连上的 vsock fd（*os.File，可轮询，支持截止时间）包成 net.Conn。
type vsockConn struct {
	*os.File
	local, remote vsockAddr
}

func (c *vsockConn) LocalAddr() net.Addr  { return c.local }
func (c *vsockConn) RemoteAddr() net.Addr { return c.remote }

type vsockAddr struct{ cid, port uint32 }

func (vsockAddr) Network() string  { return "vsock" }
func (a vsockAddr) String() string { return fmt.Sprintf("vm(%d):%d", a.cid, a.port) }
