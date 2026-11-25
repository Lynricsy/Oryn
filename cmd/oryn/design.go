package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// `oryn design eject <artifact-id> --to <dir>`（M29 D3）：在 orb 内经 orbd.sock 导出设计画布的工程 zip
// 并安全解压到目标空目录，便于初始化 git 仓库后经 `oryn boxes deploy` 部署到 box。

var designCmd = &cobra.Command{
	Use:   "design",
	Short: "设计画布（orb 内）",
}

var designEjectCmd = &cobra.Command{
	Use:   "eject <artifact-id>",
	Short: "把设计画布导出为可部署到 box 的独立工程",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		to, _ := cmd.Flags().GetString("to")
		version, _ := cmd.Flags().GetInt("version")
		asJSON, _ := cmd.Flags().GetBool("json")
		res, err := designEject(cmd.Context(), args[0], to, version)
		if err != nil {
			return err
		}
		if asJSON {
			return printJSONTo(cmd.OutOrStdout(), res)
		}
		printDesignEject(cmd.OutOrStdout(), res)
		return nil
	},
}

// designEject 校验参数、把 --to 转为绝对路径并经 orbd.sock 调 design.eject。
func designEject(ctx context.Context, id, to string, version int) (orbsock.DesignEjectResult, error) {
	if !inOrb() {
		return orbsock.DesignEjectResult{}, usageErr("`oryn design eject` 只能在 Oryn orb 内使用")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return orbsock.DesignEjectResult{}, usageErr("缺少 artifact id")
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return orbsock.DesignEjectResult{}, usageErr("必须用 --to 指定导出目录")
	}
	if version < 0 {
		return orbsock.DesignEjectResult{}, usageErr("--version 不能是负数")
	}
	absTo, err := filepath.Abs(to)
	if err != nil {
		return orbsock.DesignEjectResult{}, usageErr("解析导出目录 %q 失败：%v", to, err)
	}
	caller, err := callerThread()
	if err != nil {
		return orbsock.DesignEjectResult{}, err
	}
	p := orbsock.DesignEjectParams{
		ThreadID: caller,
		ID:       id,
		Version:  version,
		To:       absTo,
	}
	var out orbsock.DesignEjectResult
	if err := sockCall(ctx, orbsock.MethodDesignEject, p, &out, nil); err != nil {
		return orbsock.DesignEjectResult{}, sockErr(err)
	}
	return out, nil
}

// printDesignEject 打印导出摘要与下一步部署提示。
func printDesignEject(w io.Writer, res orbsock.DesignEjectResult) {
	fmt.Fprintf(w, "已导出 %s（版本 %d，%d 个文件）到 %s\n", res.ID, res.Version, res.Files, res.Dir)
	fmt.Fprintf(w, "下一步：\n  cd %s && git init && git add -A && git commit -m \"init\" && oryn boxes deploy --on <box> --name <app> --create\n", designShellArg(res.Dir))
}

// designShellArg 仅在路径含空白或常见 shell 元字符时加单引号，普通路径保持原样可读。
func designShellArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!&|;()<>*?[]{}") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func init() {
	designEjectCmd.Flags().String("to", "", "导出目标目录（空目录或尚不存在）")
	designEjectCmd.Flags().Int("version", 0, "导出指定版本（默认最新）")
	designEjectCmd.Flags().Bool("json", false, "以 JSON 输出结果")
	designCmd.AddCommand(designEjectCmd)
	root.AddCommand(designCmd)
}
