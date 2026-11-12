// Package version 保存由 ldflags 注入的构建版本号。
package version

// Version 在构建时通过 -ldflags "-X github.com/Lynricsy/Oryn/internal/version.Version=..." 注入。
var Version = "dev"
