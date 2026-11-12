package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// providerCmd 是 `oryn provider`；`run` 子命令由 provider_run.go 挂载。
var providerCmd = &cobra.Command{
	Use:   "provider",
	Short: "orb provider 管理",
}

var providerCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "新建 provider，stdout 只打印令牌（只显示这一次）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		var res apigen.ProviderCreateResponse
		if err := c.do(cmd.Context(), http.MethodPost, "/api/v1/providers", apigen.ProviderCreateRequest{Name: args[0]}, &res); err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, res.Token)
		return err
	},
}

var providerLsJSON bool

var providerLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "列出 provider 与在线状态",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		var list apigen.ProviderList
		if err := c.do(cmd.Context(), http.MethodGet, "/api/v1/providers", nil, &list); err != nil {
			return err
		}
		if providerLsJSON {
			return printJSON(list)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tID\tSTATUS\tDRIVER\tTYPES")
		for _, p := range list.Providers {
			status := "offline"
			switch {
			case p.DisabledAt != nil:
				status = "disabled"
			case p.Online:
				status = "online"
			}
			driver := "-"
			if p.Driver != nil {
				driver = string(*p.Driver)
			}
			types := make([]string, 0, len(p.OrbTypes))
			for _, t := range p.OrbTypes {
				name := t.Name
				if p.DefaultType != nil && *p.DefaultType == t.Name {
					name += "*"
				}
				types = append(types, name)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.Name, p.ID, status, driver, strings.Join(types, ","))
		}
		return tw.Flush()
	},
}

func init() {
	providerLsCmd.Flags().BoolVar(&providerLsJSON, "json", false, "输出 JSON")
	providerCmd.AddCommand(providerCreateCmd, providerLsCmd)
	root.AddCommand(providerCmd)
}
