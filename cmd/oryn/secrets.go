package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

var secretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "密钥库（值只写不读）",
}

var secretsSetOpts struct {
	mode  string
	hosts []string
}

var secretsSetCmd = &cobra.Command{
	Use:   "set <name> [value]",
	Short: "写入密钥；省略 value 时从标准输入读取",
	Long: `mode=proxy（默认）：值留在控制面，由 egress 代理注入请求；git 主机令牌命名为 git/<host>/token。
mode=env：以同名环境变量注入 agent 进程，名称须匹配 [A-Z_][A-Z0-9_]*。`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		value := ""
		if len(args) == 2 {
			value = args[1]
		} else {
			b, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
			if err != nil {
				return err
			}
			value = strings.TrimRight(string(b), "\r\n")
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		var m apigen.SecretMeta
		if err := c.do(cmd.Context(), http.MethodPut, "/api/v1/secrets/"+escapePathParam(args[0]), apigen.SecretPut{
			Value: value, Mode: apigen.SecretMode(secretsSetOpts.mode), Hosts: secretsSetOpts.hosts,
		}, &m); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "已写入 %s（%s）\n", m.Name, m.Mode)
		return nil
	},
}

var secretsLsJSON bool

var secretsLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "列出密钥名称与元数据",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		var list apigen.SecretList
		if err := c.do(cmd.Context(), http.MethodGet, "/api/v1/secrets", nil, &list); err != nil {
			return err
		}
		if secretsLsJSON {
			return printJSON(list)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tMODE\tHOSTS\tUPDATED")
		for _, s := range list.Secrets {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Name, s.Mode, strings.Join(s.Hosts, ","), fmtTime(s.UpdatedAt))
		}
		return tw.Flush()
	},
}

var secretsRmCmd = &cobra.Command{
	Use:   "rm <name>",
	Short: "删除密钥",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		return c.do(cmd.Context(), http.MethodDelete, "/api/v1/secrets/"+escapePathParam(args[0]), nil, nil)
	},
}

func init() {
	secretsSetCmd.Flags().StringVar(&secretsSetOpts.mode, "mode", "proxy", "proxy | env")
	secretsSetCmd.Flags().StringSliceVar(&secretsSetOpts.hosts, "hosts", nil, "proxy 模式下允许注入的上游主机，逗号分隔")
	secretsLsCmd.Flags().BoolVar(&secretsLsJSON, "json", false, "输出 JSON")
	secretsCmd.AddCommand(secretsSetCmd, secretsLsCmd, secretsRmCmd)
	root.AddCommand(secretsCmd)
}

// escapePathParam 编码路径参数，其中的 / 也编码为 %2F（服务端按 RawPath 路由）。
func escapePathParam(s string) string {
	return strings.ReplaceAll(url.PathEscape(s), "/", "%2F")
}

func fmtTime(ms int64) string {
	if ms == 0 {
		return "-"
	}
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04")
}
