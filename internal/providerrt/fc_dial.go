package providerrt

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// unixPathShortLimit 是直接拨号的路径长度上限：sun_path 只有 108 字节（含结尾 NUL），留些余量。
const unixPathShortLimit = 100

// dialUnix 拨 unix socket。路径过长（state_dir 可配置，jail 里的 api.sock / v.sock 很容易超过 sun_path）时，
// 先以 O_PATH|O_DIRECTORY 打开所在目录，再拨 /proc/self/fd/<fd>/<名字>，拨完关掉 fd。
func dialUnix(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	if len(path) < unixPathShortLimit {
		return d.DialContext(ctx, "unix", path)
	}
	dir, name := filepath.Split(path)
	fd, err := unix.Open(dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("dial unix %s: 打开所在目录: %w", path, err)
	}
	defer unix.Close(fd)
	c, err := d.DialContext(ctx, "unix", "/proc/self/fd/"+strconv.Itoa(fd)+"/"+name)
	if err != nil {
		return nil, fmt.Errorf("dial unix %s: %w", path, err)
	}
	return c, nil
}
