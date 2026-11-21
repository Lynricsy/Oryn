package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// `oryn secrets …`：全局密钥库（值只写不读）；--project（M24 阶段 ④）改为操作该项目的环境变量与 Git 令牌。

// newSecretsCmd 构造 `oryn secrets` 命令树；client 在测试时替换为假服务端。
func newSecretsCmd(client func() (*apiClient, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "密钥库（值只写不读）与项目级环境变量",
	}
	cmd.AddCommand(newSecretsSetCmd(client), newSecretsLsCmd(client), newSecretsRmCmd(client))
	return cmd
}

func init() { root.AddCommand(newSecretsCmd(newClient)) }

// projectSecretPath 返回项目级条目（name 为空 = 集合）的 REST 路径。
func projectSecretPath(projectID, name string) string {
	p := "/api/v1/projects/" + escapePathParam(projectID) + "/secrets"
	if name != "" {
		p += "/" + escapePathParam(name)
	}
	return p
}

func newSecretsSetCmd(client func() (*apiClient, error)) *cobra.Command {
	var opts struct {
		mode, project string
		hosts         []string
		plain         bool
	}
	cmd := &cobra.Command{
		Use:   "set <name> [value]",
		Short: "写入密钥；省略 value 时从标准输入读取",
		Long: `mode=proxy（默认）：值留在控制面，由 egress 代理注入请求；git 主机令牌命名为 git/<host>/token。
mode=env：以同名环境变量注入 agent 进程，名称须匹配 [A-Z_][A-Z0-9_]*。

--project <名称或 id>：写成该项目的条目，只注入本项目的 orb，与全局同名时以项目为准。
项目级条目只能是环境变量（env）或 Git 令牌 git/<host>/token（proxy，总是保密）；
不给 --mode 时按名称推断（git/ 开头为 proxy，否则 env）。模型供应商密钥只能全局设置。
--plain：只与 --project 同用，存为普通环境变量（值之后可用 ls --project 读回）。`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.plain && opts.project == "" {
				return usageErr("--plain 只能与 --project 一起使用")
			}
			if opts.project != "" && cmd.Flags().Changed("hosts") {
				return usageErr("--hosts 不能与 --project 一起使用：项目级 Git 令牌只注入它自己的主机")
			}
			name := args[0]
			value := ""
			if len(args) == 2 {
				value = args[1]
			} else {
				b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 1<<20))
				if err != nil {
					return err
				}
				value = strings.TrimRight(string(b), "\r\n")
			}
			c, err := client()
			if err != nil {
				return err
			}
			if opts.project == "" {
				var m apigen.SecretMeta
				if err := c.do(cmd.Context(), http.MethodPut, "/api/v1/secrets/"+escapePathParam(name), apigen.SecretPut{
					Value: value, Mode: apigen.SecretMode(opts.mode), Hosts: opts.hosts,
				}, &m); err != nil {
					return err
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "已写入 %s（%s）\n", m.Name, m.Mode)
				return nil
			}
			prj, err := lookupProject(cmd.Context(), c, opts.project)
			if err != nil {
				return err
			}
			mode := opts.mode
			if !cmd.Flags().Changed("mode") {
				mode = string(apigen.Env)
				if strings.HasPrefix(name, "git/") {
					mode = string(apigen.Proxy)
				}
			}
			var e apigen.ProjectSecret
			if err := c.do(cmd.Context(), http.MethodPut, projectSecretPath(prj.ID, name), apigen.ProjectSecretPut{
				Value: value, Mode: apigen.SecretMode(mode), Secret: new(!opts.plain),
			}, &e); err != nil {
				return err
			}
			kind := "保密"
			if !e.Secret {
				kind = "不保密"
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "已写入项目 %s 的 %s（%s，%s）\n", prj.Name, e.Name, e.Mode, kind)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.mode, "mode", "proxy", "proxy | env（--project 时缺省按名称推断）")
	cmd.Flags().StringSliceVar(&opts.hosts, "hosts", nil, "proxy 模式下允许注入的上游主机，逗号分隔（仅全局）")
	cmd.Flags().StringVar(&opts.project, "project", "", "写成该项目（名称或 id）的条目")
	cmd.Flags().BoolVar(&opts.plain, "plain", false, "不保密：存为普通环境变量，值可读（需 --project）")
	return cmd
}

func newSecretsLsCmd(client func() (*apiClient, error)) *cobra.Command {
	var (
		asJSON  bool
		project string
	)
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "列出密钥名称与元数据（--project 列该项目的条目）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if project == "" {
				var list apigen.SecretList
				if err := c.do(cmd.Context(), http.MethodGet, "/api/v1/secrets", nil, &list); err != nil {
					return err
				}
				if asJSON {
					return printJSONTo(out, list)
				}
				tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tMODE\tHOSTS\tUPDATED")
				for _, s := range list.Secrets {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Name, s.Mode, strings.Join(s.Hosts, ","), fmtTime(s.UpdatedAt))
				}
				return tw.Flush()
			}
			pid, err := resolveProject(cmd.Context(), c, project)
			if err != nil {
				return err
			}
			var list apigen.ProjectSecretList
			if err := c.do(cmd.Context(), http.MethodGet, projectSecretPath(pid, ""), nil, &list); err != nil {
				return err
			}
			if asJSON {
				return printJSONTo(out, list)
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tKIND\tSECRET\tVALUE\tUPDATED")
			for _, s := range list.Secrets {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Name, projectSecretKind(s), yesNo(s.Secret), projectSecretValue(s), fmtTime(s.UpdatedAt))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出 JSON")
	cmd.Flags().StringVar(&project, "project", "", "列出该项目（名称或 id）的条目")
	return cmd
}

func newSecretsRmCmd(client func() (*apiClient, error)) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "删除密钥（--project 删该项目的条目）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			if project == "" {
				return c.do(cmd.Context(), http.MethodDelete, "/api/v1/secrets/"+escapePathParam(args[0]), nil, nil)
			}
			pid, err := resolveProject(cmd.Context(), c, project)
			if err != nil {
				return err
			}
			return c.do(cmd.Context(), http.MethodDelete, projectSecretPath(pid, args[0]), nil, nil)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "删除该项目（名称或 id）的条目")
	return cmd
}

// projectSecretKind 是 ls --project 的 KIND 列：proxy 条目只能是 Git 令牌。
func projectSecretKind(s apigen.ProjectSecret) string {
	if s.Mode == apigen.Proxy {
		return "git"
	}
	return "env"
}

// projectSecretValue 是 ls --project 的 VALUE 列：保密条目为 "-"；含制表或换行的值加引号，不打乱表格。
func projectSecretValue(s apigen.ProjectSecret) string {
	if s.Secret || s.Value == nil {
		return "-"
	}
	if strings.ContainsAny(*s.Value, "\t\n\r") {
		return strconv.Quote(*s.Value)
	}
	return *s.Value
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
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
