//go:build !unix

package providerrt

import "os"

func allocatedFileBytes(info os.FileInfo) int64 {
	return info.Size()
}
