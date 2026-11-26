package orbdrt

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Lynricsy/Oryn/internal/unixsock"
)

// ORYN_LINK_VIA（M30 沙箱，docs/plan-m30.md C4）：orbd 拨 ORYN_URL 时底层连接的走法。沙箱可能没有网卡，
// 控制通道经 provider 中继：docker 是挂进容器的 unix socket，Firecracker 是 vsock（连宿主 CID 2）。

const (
	linkViaUnix  = "unix"
	linkViaVsock = "vsock"
)

// parseLinkVia 只接受 `unix:<绝对路径>` 与 `vsock:<1..4294967295>`。
func parseLinkVia(v string) (kind, arg string, err error) {
	kind, arg, ok := strings.Cut(v, ":")
	if !ok {
		return "", "", fmt.Errorf("invalid link via %q: want unix:<absolute path> or vsock:<port>", v)
	}
	switch kind {
	case linkViaUnix:
		if !filepath.IsAbs(arg) || strings.ContainsRune(arg, 0) {
			return "", "", fmt.Errorf("invalid link via %q: unix socket path must be absolute", v)
		}
	case linkViaVsock:
		if _, err := parseVsockPort(arg); err != nil {
			return "", "", fmt.Errorf("invalid link via %q: %w", v, err)
		}
	default:
		return "", "", fmt.Errorf("invalid link via %q: want unix:<absolute path> or vsock:<port>", v)
	}
	return kind, arg, nil
}

func parseVsockPort(s string) (uint32, error) {
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("vsock port must be 1-4294967295")
	}
	return uint32(n), nil
}

// dialVia 按 via 建立到控制面的底层连接。
func dialVia(ctx context.Context, via string) (net.Conn, error) {
	kind, arg, err := parseLinkVia(via)
	if err != nil {
		return nil, err
	}
	if kind == linkViaUnix {
		return unixsock.Dial(ctx, arg)
	}
	port, _ := parseVsockPort(arg)
	return dialVsock(ctx, port)
}
