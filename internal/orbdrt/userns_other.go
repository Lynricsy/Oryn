//go:build !linux

package orbdrt

// userNamespacesUsable：非 Linux 没有 user namespace。
func userNamespacesUsable() bool { return false }
