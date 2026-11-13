package orbdrt

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace 以 renameat2(RENAME_NOREPLACE) 原子地移动 src 到 dst，dst 已存在时失败（EEXIST）。
// 文件系统不支持该标志（EINVAL）时退回 link + unlink。
func renameNoReplace(src, dst string) error {
	err := unix.Renameat2(unix.AT_FDCWD, src, unix.AT_FDCWD, dst, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) {
		return linkNoReplace(src, dst)
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
	return nil
}
