package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// `oryn github …`（M15 R24）：手动分诊、重放投递、查看任务与 hook 状态、清理 issue 线程、查询写操作审计。

// githubRefRe 匹配 `<owner>/<repo>#<n>`：owner 为字母数字与连字符，repo 为字母数字与 . _ -，n 为正整数。
var githubRefRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9-]{0,38})/([A-Za-z0-9._-]{1,100})#([1-9][0-9]{0,17})$`)

// githubRuleRe 是触发规则 id 的格式。
var githubRuleRe = regexp.MustCompile(`^ght_[0-9A-Z]{26}$`)

// parseGithubRef 严格解析 `<owner>/<repo>#<n>`，返回 `owner/repo` 与编号。
func parseGithubRef(s string) (string, int64, error) {
	m := githubRefRe.FindStringSubmatch(s)
	if m == nil || m[2] == "." || m[2] == ".." {
		return "", 0, usageErr("%q 不是有效的 issue 引用，应为 <owner>/<repo>#<编号>（如 octo/demo#12）", s)
	}
	n, err := strconv.ParseInt(m[3], 10, 64)
	if err != nil {
		return "", 0, usageErr("%q 的编号无效", s)
	}
	return m[1] + "/" + m[2], n, nil
}

// newGithubCmd 构造 `oryn github` 命令树；client 在测试时替换为假服务端。
func newGithubCmd(client func() (*apiClient, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "GitHub 集成：分诊、重放投递、状态、清理、审计",
	}
	cmd.AddCommand(
		newGithubTriageCmd(client),
		newGithubReplayCmd(client),
		newGithubStatusCmd(client),
		newGithubCleanupCmd(client),
		newGithubAuditCmd(client),
	)
	return cmd
}

func init() { root.AddCommand(newGithubCmd(newClient)) }

// writeRawJSON 原样输出响应体并补换行（--json）。
func writeRawJSON(w io.Writer, raw json.RawMessage) error {
	if _, err := w.Write(raw); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func newGithubTriageCmd(client func() (*apiClient, error)) *cobra.Command {
	var rule string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "triage <owner>/<repo>#<n>",
		Short: "拉取线上 issue，合成 issues.opened 交给触发规则，打印线程 id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, n, err := parseGithubRef(args[0])
			if err != nil {
				return err
			}
			req := apigen.GitHubTriageRequest{Repo: repo, Number: n}
			if rule != "" {
				if !githubRuleRe.MatchString(rule) {
					return usageErr("--rule %q 不是有效的规则 id（应为 ght_ 加 26 位大写字母数字）", rule)
				}
				req.RuleID = &rule
			}
			c, err := client()
			if err != nil {
				return err
			}
			var raw json.RawMessage
			if err := c.do(cmd.Context(), http.MethodPost, "/api/v1/github/triage", req, &raw); err != nil {
				return err
			}
			if asJSON {
				return writeRawJSON(cmd.OutOrStdout(), raw)
			}
			var res apigen.GitHubTriageResult
			if err := json.Unmarshal(raw, &res); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), res.ThreadID)
			return err
		},
	}
	cmd.Flags().StringVar(&rule, "rule", "", "指定触发规则 id（ght_…）；缺省由服务端按规则匹配")
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出原始 JSON")
	return cmd
}

func newGithubReplayCmd(client func() (*apiClient, error)) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "replay <delivery-guid>",
		Short: "重放 7 天内已结束的 webhook 投递",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			guid := args[0]
			if guid == "" {
				return usageErr("投递 guid 不能为空")
			}
			c, err := client()
			if err != nil {
				return err
			}
			if err := c.do(cmd.Context(), http.MethodPost, "/api/v1/github/deliveries/"+escapePathParam(guid)+"/replay", nil, nil); err != nil {
				return err
			}
			// 202 无响应体：--json 时输出一个说明结果的对象
			if asJSON {
				return printJSONTo(cmd.OutOrStdout(), map[string]any{"guid": guid, "queued": true})
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s 已重新入队\n", guid)
			return err
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出 JSON")
	return cmd
}

func newGithubStatusCmd(client func() (*apiClient, error)) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "显示 issue 线程、发布哨兵与仓库 hook 的状态",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			var tasksRaw, hooksRaw json.RawMessage
			if err := c.do(cmd.Context(), http.MethodGet, "/api/v1/github/tasks", nil, &tasksRaw); err != nil {
				return err
			}
			if err := c.do(cmd.Context(), http.MethodGet, "/api/v1/github/hooks", nil, &hooksRaw); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				// 两个响应体原样嵌入同一个对象
				return printJSONTo(out, struct {
					Tasks json.RawMessage `json:"tasks"`
					Hooks json.RawMessage `json:"hooks"`
				}{tasksRaw, hooksRaw})
			}
			var tasks apigen.GitHubTaskList
			if err := json.Unmarshal(tasksRaw, &tasks); err != nil {
				return err
			}
			var hooks apigen.GitHubHookList
			if err := json.Unmarshal(hooksRaw, &hooks); err != nil {
				return err
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "KIND\tREPO\tREF\tSTATE\tTHREAD")
			for _, t := range tasks.Tasks {
				ref := "-"
				switch {
				case t.Number != nil:
					ref = "#" + strconv.FormatInt(*t.Number, 10)
				case t.Version != nil:
					ref = *t.Version
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", t.Kind, t.Repo, ref, t.State, strOrDash(t.ThreadID))
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			fmt.Fprintln(out)
			tw = tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "REPO\tSTATUS\tLAST DELIVERY")
			for _, h := range hooks.Hooks {
				last := "-"
				if h.LastDeliveryAt != nil {
					last = fmtTime(*h.LastDeliveryAt)
					if h.LastDeliverySource != nil {
						last += " (" + string(*h.LastDeliverySource) + ")"
					}
				}
				status := string(h.Status)
				if h.Error != nil && *h.Error != "" {
					status += ": " + *h.Error
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", h.FullName, status, last)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出原始 JSON（{tasks, hooks}）")
	return cmd
}

func newGithubCleanupCmd(client func() (*apiClient, error)) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "cleanup <owner>/<repo>#<n>",
		Short: "把 issue 线程置为 abandoned 并冻结其 orb（不改动远端）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, n, err := parseGithubRef(args[0])
			if err != nil {
				return err
			}
			c, err := client()
			if err != nil {
				return err
			}
			var raw json.RawMessage
			if err := c.do(cmd.Context(), http.MethodPost, "/api/v1/github/tasks/cleanup", apigen.GitHubTaskRef{Repo: repo, Number: n}, &raw); err != nil {
				return err
			}
			if asJSON {
				return writeRawJSON(cmd.OutOrStdout(), raw)
			}
			var t apigen.GitHubTask
			if err := json.Unmarshal(raw, &t); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s#%d → %s（线程 %s）\n", t.Repo, n, t.State, strOrDash(t.ThreadID))
			return err
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出原始 JSON")
	return cmd
}

func newGithubAuditCmd(client func() (*apiClient, error)) *cobra.Command {
	var thread, repo string
	var limit int
	var before int64
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "查询 GitHub 写操作审计（按时间倒序，保留 30 天）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if thread != "" {
				q.Set("thread", thread)
			}
			if repo != "" {
				q.Set("repo", repo)
			}
			if cmd.Flags().Changed("limit") {
				if limit < 1 || limit > 200 {
					return usageErr("--limit 须在 1–200 之间")
				}
				q.Set("limit", strconv.Itoa(limit))
			}
			if cmd.Flags().Changed("before") {
				q.Set("before", strconv.FormatInt(before, 10))
			}
			path := "/api/v1/github/audit"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			c, err := client()
			if err != nil {
				return err
			}
			var raw json.RawMessage
			if err := c.do(cmd.Context(), http.MethodGet, path, nil, &raw); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return writeRawJSON(out, raw)
			}
			var list apigen.GitHubAuditList
			if err := json.Unmarshal(raw, &list); err != nil {
				return err
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "TIME\tTHREAD\tSLOT\tMETHOD\tOP\tREPO\tSTATUS\tGATE")
			for _, e := range list.Entries {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
					fmtTime(e.At), strOrDash(e.ThreadID), e.Slot, e.Method, e.Op, strOrDash(e.Repo), e.Status, strOrDash(e.Gate))
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if list.NextBefore != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "还有更早的记录：--before %d\n", *list.NextBefore)
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&thread, "thread", "", "只看该线程（T-…）")
	fl.StringVar(&repo, "repo", "", "只看该仓库（owner/repo）")
	fl.IntVar(&limit, "limit", 50, "最多返回条数（1–200）")
	fl.Int64Var(&before, "before", 0, "只返回 id 小于该值的记录（分页游标）")
	fl.BoolVar(&asJSON, "json", false, "输出原始 JSON")
	return cmd
}

// strOrDash 返回 *s，nil 或空串时返回 "-"。
func strOrDash(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return *s
}
