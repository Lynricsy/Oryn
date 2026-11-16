package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// `oryn artifact call <op>`（M14）：orb 内直通 artifact 操作（与 agent 的 artifact 工具同名同参），经 orbd 本地控制口
// 转成 ctl artifact.call；params.files[*].source 须是 orb 内文件的绝对路径，控制面经 library.fetch 取回字节。
// 这是脚本与验收用的入口，不为每个操作单设子命令。

var artifactCmd = &cobra.Command{
	Use:   "artifact",
	Short: "Artifacts（orb 内）",
}

var artifactCallCmd = &cobra.Command{
	Use:   "call <op>",
	Short: "执行一个 artifact 操作（create_artifact、read_artifact、update_artifact 等）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		inline, _ := cmd.Flags().GetString("params")
		file, _ := cmd.Flags().GetString("params-file")
		asJSON, _ := cmd.Flags().GetBool("json")
		params, err := artifactParams(cmd.Flags().Changed("params"), inline, file)
		if err != nil {
			return err
		}
		res, err := artifactCall(cmd.Context(), args[0], params)
		if err != nil {
			return err
		}
		if asJSON {
			return printJSONTo(cmd.OutOrStdout(), res)
		}
		printArtifactText(cmd.OutOrStdout(), res)
		return nil
	},
}

// artifactParams 取 --params 或 --params-file 的 JSON 对象；两者都没给时是空对象。
func artifactParams(hasInline bool, inline, file string) (map[string]any, error) {
	var raw []byte
	switch {
	case hasInline && file != "":
		return nil, usageErr("--params 与 --params-file 只能给一个")
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, usageErr("%v", err)
		}
		raw = b
	case hasInline:
		raw = []byte(inline)
	default:
		return map[string]any{}, nil
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil || params == nil {
		return nil, usageErr("参数必须是一个 JSON 对象")
	}
	return params, nil
}

// artifactCall 以调用方线程执行 artifact 操作 op。
func artifactCall(ctx context.Context, op string, params map[string]any) (apigen.LinkCtlArtifactCallResult, error) {
	if !inOrb() {
		return apigen.LinkCtlArtifactCallResult{}, usageErr("`oryn artifact call` 只能在 Oryn orb 内使用")
	}
	if strings.TrimSpace(op) == "" {
		return apigen.LinkCtlArtifactCallResult{}, usageErr("缺少操作名")
	}
	caller, err := callerThread()
	if err != nil {
		return apigen.LinkCtlArtifactCallResult{}, err
	}
	p := apigen.LinkCtlArtifactCallParams{ThreadID: caller, Op: op, Params: params}
	var out apigen.LinkCtlArtifactCallResult
	if err := sockCall(ctx, orbsock.MethodArtifactCall, p, &out, nil); err != nil {
		return apigen.LinkCtlArtifactCallResult{}, sockErr(err)
	}
	return out, nil
}

// printArtifactText 打印结果的 text（补齐结尾换行）。
func printArtifactText(w io.Writer, res apigen.LinkCtlArtifactCallResult) {
	if res.Text == "" || strings.HasSuffix(res.Text, "\n") {
		_, _ = io.WriteString(w, res.Text)
		return
	}
	fmt.Fprintln(w, res.Text)
}

func init() {
	artifactCallCmd.Flags().String("params", "", "操作参数（JSON 对象）")
	artifactCallCmd.Flags().String("params-file", "", "从文件读取操作参数（JSON 对象）")
	artifactCallCmd.Flags().Bool("json", false, "以 JSON 输出整个结果")
	artifactCmd.AddCommand(artifactCallCmd)
	root.AddCommand(artifactCmd)
}
