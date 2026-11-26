//go:build !linux

package orbdrt

import (
	"context"
	"errors"
	"net"
)

func dialVsock(context.Context, uint32) (net.Conn, error) {
	return nil, errors.New("vsock link is only supported on linux")
}
