package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// `oryn library put <文件>`（M13）：orb 内的 agent 把文件存进全局助手文件库（库内共享、来源记为本线程），
// 经 orbd 本地控制口转成 ctl library.put，控制面再经 library.fetch 从本 orb 取回字节。

var libraryCmd = &cobra.Command{
	Use:   "library",
	Short: "全局助手文件库（orb 内）",
}

var libraryPutCmd = &cobra.Command{
	Use:   "put <文件>",
	Short: "把 orb 里的文件存进全局助手文件库（库内共享）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		asJSON, _ := cmd.Flags().GetBool("json")
		f, err := libraryPut(cmd.Context(), args[0], name)
		if err != nil {
			return err
		}
		if asJSON {
			return printJSON(f)
		}
		printLibraryFile(cmd.OutOrStdout(), f)
		return nil
	},
}

// libraryPut 把 file 存进全局助手文件库；name 为空时取文件名。
func libraryPut(ctx context.Context, file, name string) (apigen.AssistantFile, error) {
	if !inOrb() {
		return apigen.AssistantFile{}, usageErr("`oryn library put` 只能在 Oryn orb 内使用")
	}
	caller, err := callerThread()
	if err != nil {
		return apigen.AssistantFile{}, err
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return apigen.AssistantFile{}, usageErr("%v", err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return apigen.AssistantFile{}, usageErr("%v", err)
	}
	if !fi.Mode().IsRegular() {
		return apigen.AssistantFile{}, usageErr("%s 不是普通文件（目录请先打包）", file)
	}
	p := apigen.LinkCtlLibraryPutParams{ThreadID: caller, Path: abs}
	if name != "" {
		p.Name = &name
	}
	var out apigen.AssistantFile
	if err := sockCall(ctx, orbsock.MethodLibraryPut, p, &out, nil); err != nil {
		return apigen.AssistantFile{}, sockErr(err)
	}
	return out, nil
}

func printLibraryFile(w io.Writer, f apigen.AssistantFile) {
	fmt.Fprintf(w, "%s\t%s\t%d 字节\t库内共享\n", f.ID, f.Name, f.Size)
}

func init() {
	libraryPutCmd.Flags().String("name", "", "库内文件名（缺省取文件名）")
	libraryPutCmd.Flags().Bool("json", false, "以 JSON 输出文件记录")
	libraryCmd.AddCommand(libraryPutCmd)
	root.AddCommand(libraryCmd)
}
