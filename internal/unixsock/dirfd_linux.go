package unixsock

import (
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// viaDirFD 以 O_PATH|O_DIRECTORY 打开 path 所在目录，返回经 /proc/self/fd 的短地址；用完调 done 关掉 fd。
func viaDirFD(path string) (addr string, done func(), err error) {
	dir, name := filepath.Split(path)
	fd, err := unix.Open(dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", nil, err
	}
	return "/proc/self/fd/" + strconv.Itoa(fd) + "/" + name, func() { _ = unix.Close(fd) }, nil
}
