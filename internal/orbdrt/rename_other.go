//go:build !linux

package orbdrt

// renameNoReplace 在没有 renameat2 的平台上以 link + unlink 实现：link 不覆盖已存在的 dst（EEXIST）。
func renameNoReplace(src, dst string) error { return linkNoReplace(src, dst) }
