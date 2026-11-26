//go:build !linux

package unixsock

import "errors"

// viaDirFD 只在 Linux 上可用（依赖 /proc/self/fd）。
func viaDirFD(string) (string, func(), error) {
	return "", nil, errors.New("unix socket path exceeds sun_path and /proc/self/fd is only available on linux")
}
