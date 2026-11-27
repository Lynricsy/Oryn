package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// `oryn artifact call <op>`（M14）：orb 内直通 artifact 操作（与 agent 的 artifact 工具同名同参），经 orbd 本地控制口
// 转成 ctl artifact.call；params.files[*].source 须是 orb 内文件的绝对路径，控制面经 library.fetch 取回字节。
// 这是脚本与验收用的入口；只有 pull（M31：按页循环 pull_artifact 把文件写到本机）单设子命令。

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

var artifactPullCmd = &cobra.Command{
	Use:   "pull <artifact-id>",
	Short: "把 artifact 的一个文件（缺省入口文件）取回到本机",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		to, _ := cmd.Flags().GetString("to")
		version, _ := cmd.Flags().GetInt("version")
		path, _ := cmd.Flags().GetString("path")
		overwrite, _ := cmd.Flags().GetBool("overwrite")
		asJSON, _ := cmd.Flags().GetBool("json")
		res, err := artifactPull(cmd.Context(), args[0], to, path, version, overwrite)
		if err != nil {
			return err
		}
		if asJSON {
			return printJSONTo(cmd.OutOrStdout(), res)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "已取回 %s 版本 %d 的 %s → %s（%d 字节，sha256 %s）\n", res.ID, res.Version, res.Path, res.To, res.Size, res.Sha256)
		return nil
	},
}

// artifactPullResult 是 `oryn artifact pull` 的结果（--json 原样输出）。
type artifactPullResult struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Path    string `json:"path"`
	To      string `json:"to"`
	Size    int64  `json:"size"`
	Sha256  string `json:"sha256"`
}

// pullPage 是 pull_artifact 一页结果的 text（JSON）。
type pullPage struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Sha256  string `json:"sha256"`
	Offset  int64  `json:"offset"`
	Data    string `json:"data"`
	Next    string `json:"next"`
}

// artifactPull 循环 pull_artifact 把文件写到 <to>.oryn-part，核对大小与 sha256 后改名为 to。第一页之后的页都钉在
// 第一页的版本与路径上，取回中途有新版本也不会拼出混合的文件。to 已存在且没给 overwrite 时报调用错误。
func artifactPull(ctx context.Context, id, to, path string, version int, overwrite bool) (res artifactPullResult, err error) {
	if !inOrb() {
		return res, usageErr("`oryn artifact pull` 只能在 Oryn orb 内使用")
	}
	if id = strings.TrimSpace(id); id == "" {
		return res, usageErr("缺少 artifact id")
	}
	if strings.TrimSpace(to) == "" {
		return res, usageErr("必须用 --to 指定保存路径")
	}
	if version < 0 {
		return res, usageErr("--version 不能是负数")
	}
	absTo, err := filepath.Abs(to)
	if err != nil {
		return res, usageErr("解析保存路径 %q 失败：%v", to, err)
	}
	if _, err := os.Lstat(absTo); err == nil && !overwrite {
		return res, usageErr("%s 已存在；要覆盖请加 --overwrite", absTo)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, err
	}
	// 独占创建临时文件：已存在说明另一次取回正在写同一目标，或上次中断留下了残片；两种情况都不覆盖、不删除。
	// 下面的清理只针对这次创建成功的这个文件。
	part := absTo + ".oryn-part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return res, fmt.Errorf("临时文件 %s 已存在：另一次取回正在写同一目标，或上次取回中断留下了残片；确认没有取回在进行后删掉它再重试", part)
	}
	if err != nil {
		return res, err
	}
	defer func() {
		if f != nil {
			_ = f.Close()
		}
		if err != nil {
			_ = os.Remove(part)
		}
	}()
	params := map[string]any{"id": id}
	if version > 0 {
		params["version"] = version
	}
	if path != "" {
		params["path"] = path
	}
	h := sha256.New()
	var first pullPage
	var written int64
	for page := 0; ; page++ {
		out, err := artifactCall(ctx, "pull_artifact", params)
		if err != nil {
			return res, err
		}
		var pg pullPage
		if err := json.Unmarshal([]byte(out.Text), &pg); err != nil {
			return res, fmt.Errorf("pull_artifact 的结果无法解析：%w", err)
		}
		if page == 0 {
			first = pg
			params["version"], params["path"] = pg.Version, pg.Path
		}
		if pg.Offset != written || pg.Version != first.Version || pg.Sha256 != first.Sha256 {
			return res, fmt.Errorf("pull_artifact 第 %d 页与前面的页对不上（偏移 %d，已写 %d 字节）", page+1, pg.Offset, written)
		}
		data, err := base64.StdEncoding.DecodeString(pg.Data)
		if err != nil {
			return res, fmt.Errorf("pull_artifact 第 %d 页的 data 不是合法的 base64：%w", page+1, err)
		}
		if _, err := f.Write(data); err != nil {
			return res, err
		}
		h.Write(data)
		written += int64(len(data))
		if pg.Next == "" {
			break
		}
		params["cursor"] = pg.Next
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if written != first.Size || sum != first.Sha256 {
		return res, fmt.Errorf("取回的文件与 artifact 不一致（%d 字节、sha256 %s；应为 %d 字节、%s）", written, sum, first.Size, first.Sha256)
	}
	err = f.Close()
	f = nil
	if err != nil {
		return res, err
	}
	if err = os.Rename(part, absTo); err != nil {
		return res, err
	}
	return artifactPullResult{ID: first.ID, Version: first.Version, Path: first.Path, To: absTo, Size: written, Sha256: sum}, nil
}

func init() {
	artifactCallCmd.Flags().String("params", "", "操作参数（JSON 对象）")
	artifactCallCmd.Flags().String("params-file", "", "从文件读取操作参数（JSON 对象）")
	artifactCallCmd.Flags().Bool("json", false, "以 JSON 输出整个结果")
	artifactPullCmd.Flags().String("to", "", "保存到的本机路径（必填）")
	artifactPullCmd.Flags().Int("version", 0, "取哪个版本（缺省最新）")
	artifactPullCmd.Flags().String("path", "", "取哪个文件（缺省入口文件）")
	artifactPullCmd.Flags().Bool("overwrite", false, "保存路径已存在时覆盖")
	artifactPullCmd.Flags().Bool("json", false, "以 JSON 输出结果")
	artifactCmd.AddCommand(artifactCallCmd, artifactPullCmd)
	root.AddCommand(artifactCmd)
}
