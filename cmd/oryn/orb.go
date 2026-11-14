package main

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

var orbCmd = &cobra.Command{
	Use:   "orb",
	Short: "管理 orb（列表、详情、冻结 / 停机、唤醒、删除、资源采样）",
}

var orbLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "列出 orb",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		q := url.Values{}
		if s, _ := cmd.Flags().GetString("state"); s != "" {
			q.Set("state", s)
		}
		if all, _ := cmd.Flags().GetBool("all"); all {
			q.Set("includeDeleted", "true")
		}
		path := "/api/v1/orbs"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		var list apigen.OrbList
		if err := c.do(cmd.Context(), "GET", path, nil, &list); err != nil {
			return err
		}
		if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
			return printJSON(list)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tSTATE\tPROVIDER\tTYPE\tTHREADS\tCHANGED")
		for _, o := range list.Orbs {
			prov := o.ProviderID
			if o.ProviderName != nil {
				prov = *o.ProviderName
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n", o.ID, o.State, prov, o.Type, len(o.ThreadIds),
				time.UnixMilli(o.StateChangedAt).Local().Format(time.DateTime))
		}
		return tw.Flush()
	},
}

var orbShowCmd = &cobra.Command{
	Use:   "show <orb-id>",
	Short: "显示 orb 详情（JSON）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return orbRequest(cmd, "GET", "/api/v1/orbs/"+url.PathEscape(args[0]), nil)
	},
}

var orbPauseCmd = &cobra.Command{
	Use:   "pause <orb-id>",
	Short: "冻结（--mode freeze，默认）或停机（--mode stop）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mode, _ := cmd.Flags().GetString("mode")
		return orbRequest(cmd, "POST", "/api/v1/orbs/"+url.PathEscape(args[0])+"/pause", apigen.OrbPauseRequest{Mode: apigen.PauseMode(mode)})
	},
}

var orbResumeCmd = &cobra.Command{
	Use:   "resume <orb-id>",
	Short: "唤醒 orb",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return orbRequest(cmd, "POST", "/api/v1/orbs/"+url.PathEscape(args[0])+"/resume", nil)
	},
}

var orbRmCmd = &cobra.Command{
	Use:   "rm <orb-id>",
	Short: "删除 orb（其线程保留条目）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return orbRequest(cmd, "DELETE", "/api/v1/orbs/"+url.PathEscape(args[0]), nil)
	},
}

var orbMetricsCmd = &cobra.Command{
	Use:   "metrics <orb-id>",
	Short: "显示 orb 资源采样",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		q := url.Values{}
		if since, _ := cmd.Flags().GetDuration("since"); since > 0 {
			q.Set("from", strconv.FormatInt(time.Now().Add(-since).UnixMilli(), 10))
		}
		if step, _ := cmd.Flags().GetInt("step"); step > 0 {
			q.Set("step", strconv.Itoa(step))
		}
		path := "/api/v1/orbs/" + url.PathEscape(args[0]) + "/metrics"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		var m apigen.OrbMetrics
		if err := c.do(cmd.Context(), "GET", path, nil, &m); err != nil {
			return err
		}
		if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
			return printJSON(m)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "TIME\tCPU%\tMEM MB\tLIMIT MB\tDISK MB\tRX\tTX")
		for _, s := range m.Samples {
			fmt.Fprintf(tw, "%s\t%.1f\t%.0f\t%.0f\t%.0f\t%d\t%d\n", time.UnixMilli(s.TS).Local().Format(time.DateTime),
				s.CPUPct, s.MemUsedMB, s.MemLimitMB, s.DiskUsedMB, s.NetRxBytes, s.NetTxBytes)
		}
		return tw.Flush()
	},
}

// orbRequest 发送请求并以 JSON 打印返回的 orb。
func orbRequest(cmd *cobra.Command, method, path string, body any) error {
	c, err := newClient()
	if err != nil {
		return err
	}
	var o apigen.Orb
	if err := c.do(cmd.Context(), method, path, body, &o); err != nil {
		return err
	}
	return printJSON(o)
}

func init() {
	orbLsCmd.Flags().Bool("json", false, "输出 JSON")
	orbLsCmd.Flags().String("state", "", "只列出该状态")
	orbLsCmd.Flags().Bool("all", false, "包含已删除的 orb")
	orbPauseCmd.Flags().String("mode", "freeze", "freeze 或 stop")
	orbMetricsCmd.Flags().Bool("json", false, "输出 JSON")
	orbMetricsCmd.Flags().Duration("since", time.Hour, "起始时间（距今）")
	orbMetricsCmd.Flags().Int("step", 0, "降采样步长（秒）；0 = 原始采样")
	orbCmd.AddCommand(orbLsCmd, orbShowCmd, orbPauseCmd, orbResumeCmd, orbRmCmd, orbMetricsCmd)
	orbCmd.AddCommand(orbSetupCmd, orbServiceCmd, orbDesktopCmd)
	root.AddCommand(orbCmd)
}
