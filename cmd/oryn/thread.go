package main

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/Lynricsy/Oryn/internal/transcript"
)

var threadCmd = &cobra.Command{
	Use:   "thread",
	Short: "线程",
}

// threadNewOptions 是 `thread new` 的参数。
type threadNewOptions struct {
	project, provider, orbType, orb, title string
	labels                                 []string
	noProject, json                        bool
	// browserProfile 是浏览器 Profile 的名字或 id（none = 不用；空 = 项目默认）；readonly / takeover 为选用方式。
	browserProfile     string
	readonly, takeover bool
	// space 是 Space 的名字或 id（M9）：新线程加入该 Space，未给 --project 时取 Space 的项目。
	space string
}

var threadNewOpts threadNewOptions

var threadNewCmd = &cobra.Command{
	Use:   "new <prompt>",
	Short: "新建线程（默认新建 orb；--orb 挂到已有 orb）；orb 内在本项目开一个子线程",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if inOrb() {
			return orbThreadNewCmd(cmd, args[0])
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		t, err := threadNew(cmd.Context(), c, args[0], threadNewOpts)
		if err != nil {
			return err
		}
		if threadNewOpts.json {
			return printJSON(t)
		}
		fmt.Println(t.ID)
		return nil
	},
}

// threadNew 按参数组装 `POST threads` 并返回新线程。
func threadNew(ctx context.Context, c *apiClient, prompt string, o threadNewOptions) (apigen.Thread, error) {
	if o.project != "" && o.noProject {
		return apigen.Thread{}, errors.New("--project 与 --no-project 不能同时使用")
	}
	if o.orb != "" && (o.provider != "" || o.orbType != "") {
		return apigen.Thread{}, errors.New("--orb 与 --provider / --type 不能同时使用")
	}
	if o.readonly && o.takeover {
		return apigen.Thread{}, errors.New("--readonly 与 --takeover 不能同时使用")
	}
	if o.orb != "" && (o.browserProfile != "" || o.readonly || o.takeover) {
		return apigen.Thread{}, errors.New("--browser-profile / --readonly / --takeover 只对新建 orb 有效，不能与 --orb 同时使用")
	}
	req := apigen.CreateThreadRequest{Prompt: prompt, Labels: o.labels}
	if o.title != "" {
		req.Title = &o.title
	}
	if o.orb != "" {
		if err := req.Executor.FromThreadExecutorOrb(apigen.ThreadExecutorOrb{Kind: apigen.ThreadExecutorOrbKindOrb, OrbID: o.orb}); err != nil {
			return apigen.Thread{}, err
		}
	} else {
		exec := apigen.ThreadExecutorNewOrb{Kind: apigen.NewOrb}
		if o.provider != "" {
			exec.Provider = &o.provider
		}
		if o.orbType != "" {
			exec.Type = &o.orbType
		}
		if err := req.Executor.FromThreadExecutorNewOrb(exec); err != nil {
			return apigen.Thread{}, err
		}
	}
	if o.project != "" {
		id, err := resolveProject(ctx, c, o.project)
		if err != nil {
			return apigen.Thread{}, err
		}
		req.ProjectID = &id
	}
	if o.space != "" {
		id, err := resolveSpace(ctx, c, o.space)
		if err != nil {
			return apigen.Thread{}, err
		}
		req.SpaceID = &id
	}
	if o.browserProfile != "" {
		id, err := resolveBrowserProfile(ctx, c, o.browserProfile)
		if err != nil {
			return apigen.Thread{}, err
		}
		req.BrowserProfileID = &id
	}
	switch {
	case o.readonly:
		req.ProfileMode = new(apigen.BrowserProfileModeReadonly)
	case o.takeover:
		req.ProfileMode = new(apigen.BrowserProfileModeTakeover)
	}
	var env apigen.ThreadEnvelope
	if err := c.do(ctx, http.MethodPost, "/api/v1/threads", req, &env); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Code == "profile_in_use" {
			return apigen.Thread{}, fmt.Errorf("%w\n加 --readonly 以只读副本使用（不回写），或加 --takeover 接管（对方降级为只读）", err)
		}
		return apigen.Thread{}, err
	}
	return env.Thread, nil
}

// resolveProject 把项目名（或 id）解析为项目 id。
func resolveProject(ctx context.Context, c *apiClient, name string) (string, error) {
	var list apigen.ProjectList
	if err := c.do(ctx, http.MethodGet, "/api/v1/projects", nil, &list); err != nil {
		return "", err
	}
	for _, p := range list.Projects {
		if p.Name == name || p.ID == name {
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("项目 %q 不存在", name)
}

// resolveBrowserProfile 把浏览器 Profile 名（或 id）解析为 id；none 表示不用 Profile（空串）。
func resolveBrowserProfile(ctx context.Context, c *apiClient, name string) (string, error) {
	if name == "none" {
		return "", nil
	}
	var list apigen.BrowserProfileList
	if err := c.do(ctx, http.MethodGet, "/api/v1/browser-profiles", nil, &list); err != nil {
		return "", err
	}
	for _, p := range list.Profiles {
		if p.Name == name || p.ID == name {
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("浏览器 Profile %q 不存在", name)
}

// threadListOptions 对应 `GET threads` 的查询参数。
type threadListOptions struct {
	state, project, query string
	archived, json        bool
}

var threadListOpts threadListOptions

var threadListCmd = &cobra.Command{
	Use:   "list",
	Short: "列出线程（默认未归档）",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if inOrb() {
			return errUnavailableInOrb("thread list")
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		list, err := threadList(cmd.Context(), c, threadListOpts)
		if err != nil {
			return err
		}
		if threadListOpts.json {
			return printJSON(list)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tSTATE\tUNREAD\tUPDATED\tTITLE")
		for _, t := range list {
			unread := ""
			if t.Unread {
				unread = "●"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.State, unread, time.UnixMilli(t.UpdatedAt).Local().Format("01-02 15:04"), t.Title)
		}
		return tw.Flush()
	},
}

// threadList 调 `GET threads`；--project 接受项目名、id 或 none。
func threadList(ctx context.Context, c *apiClient, o threadListOptions) ([]apigen.Thread, error) {
	q := url.Values{}
	if o.archived {
		q.Set("archived", "true")
	}
	if o.state != "" {
		if !apigen.ThreadState(o.state).Valid() {
			return nil, fmt.Errorf("未知状态 %q（provisioning / working / awaiting_input / idle / error）", o.state)
		}
		q.Set("state", o.state)
	}
	switch o.project {
	case "":
	case "none":
		q.Set("project", "none")
	default:
		id, err := resolveProject(ctx, c, o.project)
		if err != nil {
			return nil, err
		}
		q.Set("project", id)
	}
	if o.query != "" {
		q.Set("q", o.query)
	}
	path := "/api/v1/threads"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var list apigen.ThreadList
	if err := c.do(ctx, http.MethodGet, path, nil, &list); err != nil {
		return nil, err
	}
	return list.Threads, nil
}

var threadArchiveOpts struct{ undo, json bool }

var threadArchiveCmd = &cobra.Command{
	Use:   "archive <id>…",
	Short: "归档线程（--undo 取消归档）",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if inOrb() {
			return errUnavailableInOrb("thread archive")
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		out, err := threadArchive(cmd.Context(), c, args, threadArchiveOpts.undo, os.Stderr)
		if threadArchiveOpts.json && len(out) > 0 {
			if jerr := printJSON(out); err == nil {
				err = jerr
			}
		}
		return err
	},
}

// threadArchive 逐个 `PATCH threads/{id} {archived}`，每个成功的打一行到 errw；遇错即停。
func threadArchive(ctx context.Context, c *apiClient, ids []string, undo bool, errw io.Writer) ([]apigen.Thread, error) {
	archived := !undo
	verb := "已归档"
	if undo {
		verb = "已取消归档"
	}
	out := []apigen.Thread{}
	for _, id := range ids {
		var t apigen.Thread
		if err := c.do(ctx, http.MethodPatch, "/api/v1/threads/"+url.PathEscape(id), apigen.ThreadPatch{Archived: &archived}, &t); err != nil {
			return out, fmt.Errorf("%s: %w", id, err)
		}
		out = append(out, t)
		fmt.Fprintln(errw, verb, t.ID)
	}
	return out, nil
}

var threadShowOpts struct{ json, entries bool }

var threadShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "显示线程（--entries 按活动分支逐条打印角色与文本）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		o := threadShowOpts
		if o.json && o.entries {
			return errors.New("--json 与 --entries 不能同时使用")
		}
		if inOrb() {
			if o.entries {
				return usageErr("orb 内的 `thread show` 不支持 --entries")
			}
			info, err := orbThreadGet(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if o.json {
				return printJSON(info)
			}
			return printThreadInfo(os.Stdout, info)
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		id := url.PathEscape(args[0])
		if o.entries {
			entries, leaf, err := fetchEntries(ctx, c, id)
			if err != nil {
				return err
			}
			for _, e := range transcript.ActiveBranch(entries, leaf) {
				role, text := transcript.LineText(e)
				if role == "" {
					continue
				}
				fmt.Printf("[%s] %s\n", role, text)
			}
			return nil
		}
		var t apigen.Thread
		if err := c.do(ctx, http.MethodGet, "/api/v1/threads/"+id, nil, &t); err != nil {
			return err
		}
		if o.json {
			return printJSON(t)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintf(tw, "ID\t%s\nTitle\t%s\nState\t%s\n", t.ID, t.Title, t.State)
		if t.OrbID != nil {
			fmt.Fprintf(tw, "Orb\t%s\n", *t.OrbID)
		}
		if t.Model != nil {
			fmt.Fprintf(tw, "Model\t%s/%s\n", t.Model.Provider, t.Model.ID)
		}
		if len(t.Labels) > 0 {
			fmt.Fprintf(tw, "Labels\t%s\n", strings.Join(t.Labels, ", "))
		}
		fmt.Fprintf(tw, "Updated\t%s\n", time.UnixMilli(t.UpdatedAt).Local().Format(time.DateTime))
		return tw.Flush()
	},
}

func fetchEntries(ctx context.Context, c *apiClient, id string) ([]transcript.Entry, string, error) {
	var all []transcript.Entry
	var after int64
	leaf := ""
	for {
		var page struct {
			Entries     []transcript.Entry `json:"entries"`
			HasMore     bool               `json:"hasMore"`
			LeafEntryID *string            `json:"leafEntryId"`
		}
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/threads/%s/entries?after=%d&limit=500", id, after), nil, &page); err != nil {
			return nil, "", err
		}
		all = append(all, page.Entries...)
		if page.LeafEntryID != nil {
			leaf = *page.LeafEntryID
		}
		if !page.HasMore || len(page.Entries) == 0 {
			return all, leaf, nil
		}
		after = page.Entries[len(page.Entries)-1].Seq
	}
}

var threadSendOpts struct {
	steer   bool
	timeout time.Duration
}

var threadSendCmd = &cobra.Command{
	Use:   "send <id> <message>",
	Short: "向线程发送消息（agent 忙时排在当前回合之后；--steer 插入当前回合）",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if inOrb() {
			if err := orbThreadSend(cmd.Context(), args[0], args[1], threadSendOpts.steer); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "已发送")
			return nil
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), threadSendOpts.timeout)
		defer cancel()
		behavior := "followUp"
		if threadSendOpts.steer {
			behavior = "steer"
		}
		frame := map[string]any{"type": "prompt", "message": args[1], "streamingBehavior": behavior}
		resp, err := wsCommand(ctx, c, args[0], frame)
		if err != nil {
			return err
		}
		var r struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(resp, &r)
		if !r.Success {
			return fmt.Errorf("agent 拒绝了消息: %s", r.Error)
		}
		fmt.Fprintln(os.Stderr, "已发送")
		return nil
	},
}

// orbThreadNewRejected 是 orb 内 `thread new` 不接受的参数（只接受 --title、--type、--json）。
var orbThreadNewRejected = []string{"project", "no-project", "provider", "orb", "label", "browser-profile", "readonly", "takeover", "space"}

// orbThreadNewCmd 是 orb 内的 `thread new`：项目与 Space 取调用方的，自带新 orb。
func orbThreadNewCmd(cmd *cobra.Command, prompt string) error {
	var bad []string
	for _, name := range orbThreadNewRejected {
		if cmd.Flags().Changed(name) {
			bad = append(bad, "--"+name)
		}
	}
	if len(bad) > 0 {
		return usageErr("orb 内的 `thread new` 只接受 --title、--type、--json（不支持 %s）", strings.Join(bad, " "))
	}
	o := threadNewOpts
	info, err := orbThreadNew(cmd.Context(), prompt, o.title, o.orbType)
	if err != nil {
		return err
	}
	if o.json {
		return printJSON(info)
	}
	fmt.Println(info.ID)
	return nil
}

var threadWaitOpts struct {
	timeout time.Duration
	json    bool
}

var threadWaitCmd = &cobra.Command{
	Use:   "wait <id>",
	Short: "orb 内：等线程静止并打印最后的回复（退出码 0 完成 / 3 等主人回答 / 4 出错 / 124 超时）",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if code := threadWait(cmd.Context(), args[0], threadWaitOpts.timeout, threadWaitOpts.json, os.Stdout, os.Stderr); code != exitWaitIdle {
			os.Exit(code)
		}
	},
}

func init() {
	f := threadNewCmd.Flags()
	f.StringVar(&threadNewOpts.project, "project", "", "项目名")
	f.BoolVar(&threadNewOpts.noProject, "no-project", false, "不关联项目")
	f.StringVar(&threadNewOpts.provider, "provider", "", "orb provider 名称")
	f.StringVar(&threadNewOpts.orbType, "type", "", "orb 类型")
	f.StringVar(&threadNewOpts.orb, "orb", "", "挂到已有 orb（orb-…；与 --provider / --type 互斥）")
	f.StringVar(&threadNewOpts.title, "title", "", "线程标题")
	f.StringArrayVar(&threadNewOpts.labels, "label", nil, "标签（可重复）")
	f.BoolVar(&threadNewOpts.json, "json", false, "以 JSON 输出线程")
	f.StringVar(&threadNewOpts.browserProfile, "browser-profile", "", "新 orb 选用的浏览器 Profile（名字或 id；none = 不用；缺省 = 项目默认）")
	f.BoolVar(&threadNewOpts.readonly, "readonly", false, "以只读副本使用浏览器 Profile（不取租约、不回写）")
	f.BoolVar(&threadNewOpts.takeover, "takeover", false, "接管正被别的 orb 使用的浏览器 Profile（对方降级为只读）")
	f.StringVar(&threadNewOpts.space, "space", "", "加入 Space（名字或 id；未给 --project 时取 Space 的项目）")
	lf := threadListCmd.Flags()
	lf.BoolVar(&threadListOpts.json, "json", false, "以 JSON 输出")
	lf.BoolVar(&threadListOpts.archived, "archived", false, "只列已归档线程")
	lf.StringVar(&threadListOpts.state, "state", "", "按状态过滤（provisioning / working / awaiting_input / idle / error）")
	lf.StringVar(&threadListOpts.project, "project", "", "按项目过滤（项目名、id 或 none）")
	lf.StringVarP(&threadListOpts.query, "query", "q", "", "标题 / 标签关键字")
	threadArchiveCmd.Flags().BoolVar(&threadArchiveOpts.undo, "undo", false, "取消归档")
	threadArchiveCmd.Flags().BoolVar(&threadArchiveOpts.json, "json", false, "以 JSON 输出线程")
	threadShowCmd.Flags().BoolVar(&threadShowOpts.json, "json", false, "以 JSON 输出线程")
	threadShowCmd.Flags().BoolVar(&threadShowOpts.entries, "entries", false, "按活动分支打印条目")
	threadSendCmd.Flags().BoolVar(&threadSendOpts.steer, "steer", false, "插入当前回合（steer）")
	threadSendCmd.Flags().DurationVar(&threadSendOpts.timeout, "timeout", 5*time.Minute, "等待 agent 应答的时长（含唤醒 orb）")
	threadWaitCmd.Flags().DurationVar(&threadWaitOpts.timeout, "timeout", defaultWaitTimeout, "最长等待时间（上限 24h）")
	threadWaitCmd.Flags().BoolVar(&threadWaitOpts.json, "json", false, "以 JSON 输出线程信息")
	threadCmd.AddCommand(threadNewCmd, threadListCmd, threadShowCmd, threadSendCmd, threadArchiveCmd, threadWaitCmd)
	root.AddCommand(threadCmd)
}
