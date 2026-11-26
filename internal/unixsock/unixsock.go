// Package unixsock 拨号与监听 unix socket，绕过 sun_path（108 字节）的路径长度上限。
package unixsock

import (
	"context"
	"fmt"
	"net"
	"os"
)

// shortLimit 是直接使用路径的长度上限：sun_path 只有 108 字节（含结尾 NUL），留些余量。
const shortLimit = 100

// Dial 拨 unix socket。路径过长（state_dir 可配置，jail 里的 api.sock / v.sock 很容易超过 sun_path）时，
// 先以 O_PATH|O_DIRECTORY 打开所在目录，再拨 /proc/self/fd/<fd>/<名字>，拨完关掉 fd（只在 Linux 上）。
func Dial(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	if len(path) < shortLimit {
		return d.DialContext(ctx, "unix", path)
	}
	addr, done, err := viaDirFD(path)
	if err != nil {
		return nil, fmt.Errorf("dial unix %s: 打开所在目录: %w", path, err)
	}
	defer done()
	c, err := d.DialContext(ctx, "unix", addr)
	if err != nil {
		return nil, fmt.Errorf("dial unix %s: %w", path, err)
	}
	return c, nil
}

// Listen 在 path 上监听 unix socket：先删掉同名旧文件，绑定后 chmod mode，uid >= 0 时 chown uid:uid。
// 路径过长时与 Dial 同一手法，经 /proc/self/fd/<fd>/<名字> 绑定。
// 返回的 listener 关闭时不删除 socket 文件：同一路径可能已被新的 listener 接管（中继重建），删除由调用方负责。
func Listen(path string, mode os.FileMode, uid int) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("listen unix %s: 删除旧文件: %w", path, err)
	}
	addr := path
	if len(path) >= shortLimit {
		a, done, err := viaDirFD(path)
		if err != nil {
			return nil, fmt.Errorf("listen unix %s: 打开所在目录: %w", path, err)
		}
		defer done()
		addr = a
	}
	ln, err := net.Listen("unix", addr)
	if err != nil {
		return nil, fmt.Errorf("listen unix %s: %w", path, err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := os.Chmod(path, mode); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("listen unix %s: chmod: %w", path, err)
	}
	if uid >= 0 {
		if err := os.Chown(path, uid, uid); err != nil {
			_ = ln.Close()
			return nil, fmt.Errorf("listen unix %s: chown: %w", path, err)
		}
	}
	return ln, nil
}
