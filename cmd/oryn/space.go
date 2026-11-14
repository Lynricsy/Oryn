package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// `oryn space …`（M9）：Space = 名称 + 目标 + 默认项目 + 一个协调聊天线程。

var spaceCmd = &cobra.Command{
	Use:   "space",
	Short: "Space（目标 + 协调聊天 + 关联项目）",
}

var spaceCreateOpts struct {
	goal, project string
	json          bool
}

var spaceCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "新建 Space（同时建出它的协调聊天线程），打印 Space id",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if inOrb() {
			return errUnavailableInOrb("space create")
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		sp, err := spaceCreate(cmd.Context(), c, args[0], spaceCreateOpts.goal, spaceCreateOpts.project)
		if err != nil {
			return err
		}
		if spaceCreateOpts.json {
			return printJSON(sp)
		}
		fmt.Println(sp.ID)
		return nil
	},
}

func spaceCreate(ctx context.Context, c *apiClient, name, goal, project string) (apigen.Space, error) {
	req := apigen.SpaceCreate{Name: name}
	if goal != "" {
		req.Goal = &goal
	}
	if project != "" {
		id, err := resolveProject(ctx, c, project)
		if err != nil {
			return apigen.Space{}, err
		}
		req.ProjectID = &id
	}
	var sp apigen.Space
	err := c.do(ctx, http.MethodPost, "/api/v1/spaces", req, &sp)
	return sp, err
}

var spaceLsOpts struct{ archived, json bool }

var spaceLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "列出 Space（--archived 含已归档）",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if inOrb() {
			return errUnavailableInOrb("space ls")
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		list, err := spaceList(cmd.Context(), c, spaceLsOpts.archived)
		if err != nil {
			return err
		}
		if spaceLsOpts.json {
			return printJSON(apigen.SpaceList{Spaces: list})
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tTHREAD\tGOAL")
		for _, sp := range list {
			name := sp.Name
			if sp.ArchivedAt != nil {
				name += "（已归档）"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", sp.ID, name, sp.ThreadID, oneLine(sp.Goal, 60))
		}
		return tw.Flush()
	},
}

func spaceList(ctx context.Context, c *apiClient, archived bool) ([]apigen.Space, error) {
	path := "/api/v1/spaces"
	if archived {
		path += "?archived=true"
	}
	var list apigen.SpaceList
	if err := c.do(ctx, http.MethodGet, path, nil, &list); err != nil {
		return nil, err
	}
	return list.Spaces, nil
}

// resolveSpace 把 Space 名字（或 id）解析为 id（含已归档的 Space）。
func resolveSpace(ctx context.Context, c *apiClient, ref string) (string, error) {
	if strings.HasPrefix(ref, "spc_") {
		return ref, nil
	}
	list, err := spaceList(ctx, c, true)
	if err != nil {
		return "", err
	}
	for _, sp := range list {
		if sp.Name == ref || sp.ID == ref {
			return sp.ID, nil
		}
	}
	return "", fmt.Errorf("Space %q 不存在", ref)
}

var spaceShowOpts struct{ json bool }

var spaceShowCmd = &cobra.Command{
	Use:   "show <名称|id>",
	Short: "显示 Space",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if inOrb() {
			return errUnavailableInOrb("space show")
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		id, err := resolveSpace(ctx, c, args[0])
		if err != nil {
			return err
		}
		var sp apigen.Space
		if err := c.do(ctx, http.MethodGet, "/api/v1/spaces/"+url.PathEscape(id), nil, &sp); err != nil {
			return err
		}
		if spaceShowOpts.json {
			return printJSON(sp)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintf(tw, "ID\t%s\nName\t%s\nThread\t%s\n", sp.ID, sp.Name, sp.ThreadID)
		if sp.ProjectID != nil {
			fmt.Fprintf(tw, "Project\t%s\n", *sp.ProjectID)
		}
		if sp.Goal != "" {
			fmt.Fprintf(tw, "Goal\t%s\n", oneLine(sp.Goal, 200))
		}
		if sp.ArchivedAt != nil {
			fmt.Fprintf(tw, "Archived\t%s\n", time.UnixMilli(*sp.ArchivedAt).Local().Format(time.DateTime))
		}
		fmt.Fprintf(tw, "Created\t%s\n", time.UnixMilli(sp.CreatedAt).Local().Format(time.DateTime))
		return tw.Flush()
	},
}

var spaceRmCmd = &cobra.Command{
	Use:   "rm <名称|id>",
	Short: "删除 Space（连同它的协调聊天；Space 里的线程保留，只是不再属于它）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if inOrb() {
			return errUnavailableInOrb("space rm")
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		id, err := resolveSpace(ctx, c, args[0])
		if err != nil {
			return err
		}
		if err := c.do(ctx, http.MethodDelete, "/api/v1/spaces/"+url.PathEscape(id), nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "已删除", id)
		return nil
	},
}

// oneLine 把多行文本压成一行并截到 n 个字符。
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func init() {
	cf := spaceCreateCmd.Flags()
	cf.StringVar(&spaceCreateOpts.goal, "goal", "", "目标（一句话）")
	cf.StringVar(&spaceCreateOpts.project, "project", "", "新线程的默认项目（名称或 id）")
	cf.BoolVar(&spaceCreateOpts.json, "json", false, "以 JSON 输出")
	spaceLsCmd.Flags().BoolVar(&spaceLsOpts.archived, "archived", false, "含已归档的 Space")
	spaceLsCmd.Flags().BoolVar(&spaceLsOpts.json, "json", false, "以 JSON 输出")
	spaceShowCmd.Flags().BoolVar(&spaceShowOpts.json, "json", false, "以 JSON 输出")
	spaceCmd.AddCommand(spaceCreateCmd, spaceLsCmd, spaceShowCmd, spaceRmCmd)
	root.AddCommand(spaceCmd)
}
