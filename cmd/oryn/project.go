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

var projectCmd = &cobra.Command{
	Use:   "project",
	Short: "项目管理",
}

var projectCreateOpts struct {
	repo, branch, setup, resume, provider, orbType, color string
	gitName, gitEmail                                     string
	shallow, json                                         bool
}

var projectCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "新建项目（--setup / --resume 可用 @文件 读取脚本）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		o := projectCreateOpts
		req := apigen.ProjectCreate{Name: args[0], ShallowClone: new(o.shallow)}
		var err error
		set := func(dst **string, v string) {
			if v != "" && err == nil {
				if path, ok := strings.CutPrefix(v, "@"); ok {
					var b []byte
					if b, err = os.ReadFile(path); err != nil {
						return
					}
					v = string(b)
				}
				*dst = new(v)
			}
		}
		set(&req.RepoURL, o.repo)
		set(&req.BaseBranch, o.branch)
		set(&req.SetupScript, o.setup)
		set(&req.ResumeScript, o.resume)
		set(&req.DefaultProvider, o.provider)
		set(&req.DefaultOrbType, o.orbType)
		set(&req.Color, o.color)
		if err != nil {
			return err
		}
		id, err := gitIdentityFlags(o.gitName, o.gitEmail)
		if err != nil {
			return err
		}
		if id != nil {
			req.GitAuthorName, req.GitAuthorEmail = &id.Name, &id.Email
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		var p apigen.Project
		if err := c.do(cmd.Context(), http.MethodPost, "/api/v1/projects", req, &p); err != nil {
			return err
		}
		if o.json {
			return printJSON(p)
		}
		fmt.Println(p.ID)
		return nil
	},
}

var projectUpdateOpts struct {
	gitName, gitEmail string
	clearGit, json    bool
}

var projectUpdateCmd = &cobra.Command{
	Use:   "update <name|id>",
	Short: "修改项目（目前支持 git 提交身份覆盖）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		o := projectUpdateOpts
		id, err := gitIdentityFlags(o.gitName, o.gitEmail)
		if err != nil {
			return err
		}
		var req apigen.ProjectPatch
		switch {
		case o.clearGit && id != nil:
			return fmt.Errorf("--clear-git 不能与 --git-name / --git-email 同用")
		case o.clearGit:
			req.GitAuthorName, req.GitAuthorEmail = new(""), new("")
		case id != nil:
			req.GitAuthorName, req.GitAuthorEmail = &id.Name, &id.Email
		default:
			return fmt.Errorf("没有要修改的字段（--git-name 与 --git-email，或 --clear-git）")
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		pid, err := resolveProject(cmd.Context(), c, args[0])
		if err != nil {
			return err
		}
		var p apigen.Project
		if err := c.do(cmd.Context(), http.MethodPatch, "/api/v1/projects/"+pid, req, &p); err != nil {
			return err
		}
		if o.json {
			return printJSON(p)
		}
		fmt.Println(p.ID)
		return nil
	},
}

var projectLsOpts struct{ json, archived bool }

var projectLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "列出项目",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		path := "/api/v1/projects"
		if projectLsOpts.archived {
			path += "?archived=true"
		}
		var list apigen.ProjectList
		if err := c.do(cmd.Context(), http.MethodGet, path, nil, &list); err != nil {
			return err
		}
		if projectLsOpts.json {
			return printJSON(list)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tID\tREPO\tBRANCH")
		for _, p := range list.Projects {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Name, p.ID, deref(p.RepoURL), deref(p.BaseBranch))
		}
		return tw.Flush()
	},
}

func deref(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return *s
}

var projectSnapshotsCmd = &cobra.Command{
	Use:   "snapshots",
	Short: "项目快照（ls / rm）",
}

var projectSnapshotsLsOpts struct{ json bool }

var projectSnapshotsLsCmd = &cobra.Command{
	Use:   "ls <name|id>",
	Short: "列出项目快照（含 building / ready / retired）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		return projectSnapshotsLs(cmd.Context(), c, args[0], projectSnapshotsLsOpts.json, time.Now(), os.Stdout)
	},
}

var projectSnapshotsRmOpts struct{ orbType string }

var projectSnapshotsRmCmd = &cobra.Command{
	Use:   "rm <name|id>",
	Short: "清除项目快照（把 ready 快照退役，镜像由控制面异步回收）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		return projectSnapshotsRm(cmd.Context(), c, args[0], projectSnapshotsRmOpts.orbType, os.Stdout)
	},
}

// projectSnapshotsLs 打印项目快照表：类型、provider、状态、退役原因、提交前 7 位、血统年龄、大小、使用次数。
func projectSnapshotsLs(ctx context.Context, c *apiClient, name string, asJSON bool, now time.Time, w io.Writer) error {
	pid, err := resolveProject(ctx, c, name)
	if err != nil {
		return err
	}
	var list apigen.ProjectSnapshotList
	if err := c.do(ctx, http.MethodGet, "/api/v1/projects/"+pid+"/snapshots", nil, &list); err != nil {
		return err
	}
	if asJSON {
		return printJSONTo(w, list)
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TYPE\tPROVIDER\tSTATE\tREASON\tCOMMIT\tAGE\tSIZE\tUSES")
	for _, s := range list.Snapshots {
		provider := s.ProviderID
		if s.ProviderName != nil && *s.ProviderName != "" {
			provider = *s.ProviderName
		}
		commit := "-"
		if s.BaseCommit != nil && *s.BaseCommit != "" {
			commit = (*s.BaseCommit)[:min(7, len(*s.BaseCommit))]
		}
		size := "-"
		if s.SizeBytes != nil {
			size = fmtBytes(*s.SizeBytes)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n", s.Type, provider, s.State, deref(s.RetireReason), commit,
			fmtDur(max(0, now.UnixMilli()-s.LineageAt)), size, s.UseCount)
	}
	return tw.Flush()
}

// projectSnapshotsRm 退役项目快照；orbType 非空时只退役该类型。
func projectSnapshotsRm(ctx context.Context, c *apiClient, name, orbType string, w io.Writer) error {
	pid, err := resolveProject(ctx, c, name)
	if err != nil {
		return err
	}
	path := "/api/v1/projects/" + pid + "/snapshots"
	if orbType != "" {
		path += "?" + url.Values{"type": {orbType}}.Encode()
	}
	if err := c.do(ctx, http.MethodDelete, path, nil, nil); err != nil {
		return err
	}
	if orbType != "" {
		fmt.Fprintf(w, "已清除项目 %s 的 %s 类型快照\n", name, orbType)
	} else {
		fmt.Fprintf(w, "已清除项目 %s 的全部快照\n", name)
	}
	return nil
}

// fmtBytes：字节数 → 812B / 4.0KiB / 1.3GiB。
func fmtBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTP"[exp])
}

func init() {
	f := projectCreateCmd.Flags()
	f.StringVar(&projectCreateOpts.repo, "repo", "", "仓库 URL")
	f.StringVar(&projectCreateOpts.branch, "branch", "", "基线分支")
	f.StringVar(&projectCreateOpts.setup, "setup", "", "setup 脚本内容，或 @文件")
	f.StringVar(&projectCreateOpts.resume, "resume", "", "resume 脚本内容，或 @文件")
	f.StringVar(&projectCreateOpts.provider, "provider", "", "默认 provider 名称")
	f.StringVar(&projectCreateOpts.orbType, "type", "", "默认 orb 类型")
	f.StringVar(&projectCreateOpts.color, "color", "", "颜色")
	f.StringVar(&projectCreateOpts.gitName, "git-name", "", "本项目 orb 的 git user.name（须与 --git-email 同用）")
	f.StringVar(&projectCreateOpts.gitEmail, "git-email", "", "本项目 orb 的 git user.email")
	f.BoolVar(&projectCreateOpts.shallow, "shallow", false, "浅克隆（--depth 1）")
	f.BoolVar(&projectCreateOpts.json, "json", false, "输出 JSON")
	f = projectUpdateCmd.Flags()
	f.StringVar(&projectUpdateOpts.gitName, "git-name", "", "本项目 orb 的 git user.name（须与 --git-email 同用）")
	f.StringVar(&projectUpdateOpts.gitEmail, "git-email", "", "本项目 orb 的 git user.email")
	f.BoolVar(&projectUpdateOpts.clearGit, "clear-git", false, "清除本项目的 git 身份覆盖（回落到全局默认）")
	f.BoolVar(&projectUpdateOpts.json, "json", false, "输出 JSON")
	projectLsCmd.Flags().BoolVar(&projectLsOpts.json, "json", false, "输出 JSON")
	projectLsCmd.Flags().BoolVar(&projectLsOpts.archived, "archived", false, "只列出已归档项目")
	projectSnapshotsLsCmd.Flags().BoolVar(&projectSnapshotsLsOpts.json, "json", false, "输出 JSON")
	projectSnapshotsRmCmd.Flags().StringVar(&projectSnapshotsRmOpts.orbType, "type", "", "只清除该 orb 类型的快照（缺省 = 全部类型）")
	projectSnapshotsCmd.AddCommand(projectSnapshotsLsCmd, projectSnapshotsRmCmd)
	projectCmd.AddCommand(projectCreateCmd, projectUpdateCmd, projectLsCmd, projectSnapshotsCmd)
	root.AddCommand(projectCmd)
}
