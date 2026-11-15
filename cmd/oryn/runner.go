package main

import (
	"context"
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

// runnerCmd 是 `oryn runner`：管理 runner（用户自己的机器以 `orbd run --runner` 接入的执行位置，M11b）。
var runnerCmd = &cobra.Command{
	Use:   "runner",
	Short: "runner 管理（自己的机器以 orbd run --runner 接入）",
}

var runnerCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "新建 runner，stdout 只打印令牌（只显示这一次），启动命令写到 stderr",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		return runnerCreate(cmd.Context(), c, args[0], os.Stdout, os.Stderr)
	},
}

// runnerCreate 新建 runner：stdout 只有令牌一行（便于 `$(oryn runner create x)`），stderr 给出完整启动命令。
func runnerCreate(ctx context.Context, c *apiClient, name string, out, errw io.Writer) error {
	var res apigen.RunnerCreated
	if err := c.do(ctx, http.MethodPost, "/api/v1/runners", apigen.RunnerCreate{Name: name}, &res); err != nil {
		return err
	}
	token := termText(res.Token)
	if _, err := fmt.Fprintln(out, token); err != nil {
		return err
	}
	fmt.Fprintf(errw, "在那台机器上以普通用户运行（--dir 可重复）：\nORYN_URL=%s ORYN_RUNNER_TOKEN=%s orbd run --runner --dir <目录>\n", termText(c.base), token)
	return nil
}

var runnerLsJSON bool

var runnerLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "列出 runner、在线状态、主机与目录",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		return runnerLs(cmd.Context(), c, runnerLsJSON, time.Now(), os.Stdout)
	},
}

// runnerLs 打印 runner 列表；服务端文本（名字、主机名、目录、特性、版本）一律经 termText。
func runnerLs(ctx context.Context, c *apiClient, asJSON bool, now time.Time, w io.Writer) error {
	var list apigen.RunnerList
	if err := c.do(ctx, http.MethodGet, "/api/v1/runners", nil, &list); err != nil {
		return err
	}
	if asJSON {
		return printJSONTo(w, list)
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tSTATUS\tHOST\tDIRS\tFEATURES\tVERSION\tLAST SEEN")
	for _, r := range list.Runners {
		status := "offline"
		if r.Online {
			status = "online"
		}
		dirs := make([]string, 0, len(r.Dirs))
		for _, d := range r.Dirs {
			p := termText(d.Path)
			if d.Git {
				p += "(git)"
			}
			dirs = append(dirs, p)
		}
		feats := make([]string, 0, len(r.Features))
		for _, f := range r.Features {
			feats = append(feats, termText(f))
		}
		seen := "-"
		if r.LastSeenAt != nil {
			seen = fmtDur(max(0, now.UnixMilli()-*r.LastSeenAt)) + " ago"
		}
		host := deref(r.Hostname)
		if r.Os != nil && *r.Os != "" {
			host += " " + *r.Os + "/" + deref(r.Arch)
		}
		host = termText(host)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", termText(r.Name), termText(r.ID), status, orDash(host),
			orDash(strings.Join(dirs, ",")), orDash(strings.Join(feats, ",")), orDash(termText(deref(r.OrbdVersion))), seen)
	}
	return tw.Flush()
}

var runnerRmCmd = &cobra.Command{
	Use:   "rm <名称|id>",
	Short: "删除 runner（令牌随即失效；线程保留转录）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		return runnerRm(cmd.Context(), c, args[0], os.Stderr)
	},
}

// resolveRunner 按名称或 id 找到 runner。
func resolveRunner(ctx context.Context, c *apiClient, nameOrID string) (apigen.Runner, error) {
	var list apigen.RunnerList
	if err := c.do(ctx, http.MethodGet, "/api/v1/runners", nil, &list); err != nil {
		return apigen.Runner{}, err
	}
	for _, r := range list.Runners {
		if r.Name == nameOrID || r.ID == nameOrID {
			return r, nil
		}
	}
	return apigen.Runner{}, fmt.Errorf("runner %q 不存在", nameOrID)
}

// runnerRm 按名称或 id 找到 runner 并删除。
func runnerRm(ctx context.Context, c *apiClient, nameOrID string, errw io.Writer) error {
	r, err := resolveRunner(ctx, c, nameOrID)
	if err != nil {
		return err
	}
	if err := c.do(ctx, http.MethodDelete, "/api/v1/runners/"+url.PathEscape(r.ID), nil, nil); err != nil {
		return err
	}
	fmt.Fprintln(errw, "已删除", termText(r.Name))
	return nil
}

func init() {
	runnerLsCmd.Flags().BoolVar(&runnerLsJSON, "json", false, "输出 JSON")
	runnerCmd.AddCommand(runnerCreateCmd, runnerLsCmd, runnerRmCmd)
	root.AddCommand(runnerCmd)
}
