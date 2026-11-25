//go:build unix

package providerrt

import (
	"os"
	"syscall"
)

// allocatedFileBytes 返回文件在宿主机磁盘上实际占用的物理字节数（稀疏文件按已分配块统计，不虚报逻辑大小）。
func allocatedFileBytes(info os.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Blocks >= 0 {
		return st.Blocks * 512
	}
	return info.Size()
}
