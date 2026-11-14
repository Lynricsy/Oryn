// oryn 是 Oryn 的命令行客户端（含 `oryn provider run`）。
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/version"
)

// root 是根命令；各子命令在自己的文件里用 init() 挂载。
var root = &cobra.Command{
	Use:           "oryn",
	Short:         "Oryn 命令行客户端",
	Version:       version.Version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func main() {
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "oryn:", err)
		var ec *exitCodeError
		if errors.As(err, &ec) {
			os.Exit(ec.code)
		}
		os.Exit(1)
	}
}
