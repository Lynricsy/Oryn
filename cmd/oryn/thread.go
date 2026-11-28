package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
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
	// runner 是 runner 的名字或 id（M11b）；dir 是 runner 上的绝对路径；worktree 非空时在 dir 旁新建同名 worktree；
	// shareDir 允许与其它线程共用 dir。
	runner, dir, worktree string
	shareDir              bool
	// box 是 box 的名字或 id（M23）：线程开在这台 box 上；app 非空时工作目录是该 app 的线上目录，否则是机器本身。
	box, app string
}

var threadNewOpts threadNewOptions

var threadNewCmd = &cobra.Command{
	Use:   "new <prompt>",
	Short: "新建线程（默认新建 orb；--orb 挂到已有 orb；--runner --dir 在 runner 的目录里）；orb 内在本项目开一个子线程",
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
	if err := checkRunnerOptions(o); err != nil {
		return apigen.Thread{}, err
	}
	if err := checkBoxOptions(o); err != nil {
		return apigen.Thread{}, err
	}
	req := apigen.CreateThreadRequest{Prompt: prompt, Labels: o.labels}
	if o.title != "" {
		req.Title = &o.title
	}
	switch {
	case o.box != "":
		exec := apigen.ThreadExecutorBox{Kind: apigen.ThreadExecutorBoxKindBox, BoxID: o.box}
		if o.app != "" {
			exec.App = &o.app
		}
		if err := req.Executor.FromThreadExecutorBox(exec); err != nil {
			return apigen.Thread{}, err
		}
	case o.runner != "":
		r, err := resolveRunner(ctx, c, o.runner)
		if err != nil {
			return apigen.Thread{}, err
		}
		exec := apigen.ThreadExecutorRunner{Kind: apigen.ThreadExecutorRunnerKindRunner, RunnerID: r.ID, Dir: o.dir}
		if o.worktree != "" {
			exec.Worktree = &apigen.ThreadExecutorRunnerWorktree{Name: o.worktree}
		}
		if o.shareDir {
			exec.ShareDir = &o.shareDir
		}
		if err := req.Executor.FromThreadExecutorRunner(exec); err != nil {
			return apigen.Thread{}, err
		}
	case o.orb != "":
		if err := req.Executor.FromThreadExecutorOrb(apigen.ThreadExecutorOrb{Kind: apigen.ThreadExecutorOrbKindOrb, OrbID: o.orb}); err != nil {
			return apigen.Thread{}, err
		}
	default:
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
		if errors.As(err, &ae) {
			switch ae.Code {
			case "profile_in_use":
				return apigen.Thread{}, fmt.Errorf("%w\n加 --readonly 以只读副本使用（不回写），或加 --takeover 接管（对方降级为只读）", err)
			case "dir_in_use":
				return apigen.Thread{}, dirInUseError(ae)
			}
		}
		return apigen.Thread{}, err
	}
	return env.Thread, nil
}

// checkBoxOptions 校验 --box / --app 的组合（不发请求）。
func checkBoxOptions(o threadNewOptions) error {
	if o.box == "" {
		if o.app != "" {
			return errors.New("--app 需要与 --box 一起使用")
		}
		return nil
	}
	if o.runner != "" || o.orb != "" || o.provider != "" || o.orbType != "" {
		return errors.New("--box 与 --runner / --orb / --provider / --type 不能同时使用")
	}
	if o.browserProfile != "" || o.readonly || o.takeover {
		return errors.New("--browser-profile / --readonly / --takeover 只对新建 orb 有效，不能与 --box 同时使用")
	}
	return nil
}

// checkRunnerOptions 校验 --runner / --dir / --worktree / --share-dir 的组合（不发请求）。
func checkRunnerOptions(o threadNewOptions) error {
	if o.runner == "" {
		if o.dir != "" || o.worktree != "" || o.shareDir {
			return errors.New("--dir / --worktree / --share-dir 需要与 --runner 一起使用")
		}
		return nil
	}
	if o.orb != "" || o.provider != "" || o.orbType != "" {
		return errors.New("--runner 与 --orb / --provider / --type 不能同时使用")
	}
	if o.browserProfile != "" || o.readonly || o.takeover {
		return errors.New("runner 上没有浏览器 Profile：--runner 不能与 --browser-profile / --readonly / --takeover 同时使用")
	}
	if o.dir == "" {
		return errors.New("--runner 需要 --dir（runner 上的绝对路径）")
	}
	// 路径属于 runner 那台机器，不在本机解析；runner 只接受 POSIX 绝对路径
	if !path.IsAbs(o.dir) {
		return fmt.Errorf("--dir %q 须是 runner 上的绝对路径", o.dir)
	}
	if o.worktree != "" && o.shareDir {
		return errors.New("--worktree 与 --share-dir 不能同时使用")
	}
	return nil
}

// dirInUseError 把 409 `dir_in_use` 改写成带占用线程与两种出路的提示。
func dirInUseError(ae *apiError) error {
	var body apigen.DirInUseConflict
	_ = json.Unmarshal(ae.Body, &body)
	ids := make([]string, 0, len(body.ThreadIds))
	for _, id := range body.ThreadIds {
		ids = append(ids, termText(id))
	}
	msg := "该目录已是其它线程的工作目录"
	if len(ids) > 0 {
		msg += "（" + strings.Join(ids, ", ") + "）"
	}
	return fmt.Errorf("%s\n加 --worktree <名字> 在旁边新建 worktree（推荐，互不干扰），或加 --share-dir 仍然共用（两个 agent 会同时改同一份文件）", msg)
}

// resolveProject 把项目名（或 id）解析为项目 id。
func resolveProject(ctx context.Context, c *apiClient, name string) (string, error) {
	p, err := lookupProject(ctx, c, name)
	return p.ID, err
}

// lookupProject 按名称或 id 找未归档项目。
func lookupProject(ctx context.Context, c *apiClient, name string) (apigen.Project, error) {
	var list apigen.ProjectList
	if err := c.do(ctx, http.MethodGet, "/api/v1/projects", nil, &list); err != nil {
		return apigen.Project{}, err
	}
	for _, p := range list.Projects {
		if p.Name == name || p.ID == name {
			return p, nil
		}
	}
	return apigen.Project{}, fmt.Errorf("项目 %q 不存在", name)
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

func fetchEntries(ctx context.Context, c *apiClient, id string) ([]transcript.Entry, *string, error) {
	var all []transcript.Entry
	var after int64
	var leaf *string
	for {
		var page struct {
			Entries     []transcript.Entry `json:"entries"`
			HasMore     bool               `json:"hasMore"`
			LeafEntryID *string            `json:"leafEntryId"`
		}
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/threads/%s/entries?after=%d&limit=500", id, after), nil, &page); err != nil {
			return nil, nil, err
		}
		all = append(all, page.Entries...)
		if page.LeafEntryID != nil {
			leaf = page.LeafEntryID
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

var threadRPCOpts struct{ timeout time.Duration }

var threadRPCCmd = &cobra.Command{
	Use:   "rpc <id> <json>",
	Short: "向线程的 agent 下发任意 omp RPC 命令并打印应答（如 get_state、set_thinking_level、compact）",
	Long: "<json> 是带字符串 type 的 omp 命令对象，例如 '{\"type\":\"get_state\"}'。应答以缩进 JSON 打印到标准输出；\n" +
		"agent 拒绝（success: false）时错误打到标准错误并以 1 退出。会话绑定、凭据等由 Oryn 管理的命令会被拒绝（managed_by_oryn），\n" +
		"分叉请用 `oryn thread fork`。",
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if inOrb() {
			return errUnavailableInOrb("thread rpc")
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), threadRPCOpts.timeout)
		defer cancel()
		return threadRPC(ctx, c, args[0], args[1], os.Stdout)
	},
}

// threadRPC 校验 raw 是带 type 的 JSON 对象，经 WS 下发并把应答缩进打印到 out；agent 拒绝时返回错误。
func threadRPC(ctx context.Context, c *apiClient, id, raw string, out io.Writer) error {
	var head map[string]json.RawMessage
	var typ string
	if json.Unmarshal([]byte(raw), &head) != nil || json.Unmarshal(head["type"], &typ) != nil || typ == "" {
		return usageErr("帧必须是带 type 的 JSON 对象")
	}
	resp, err := wsCommand(ctx, c, id, json.RawMessage(raw))
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if json.Indent(&buf, resp, "", "  ") != nil {
		buf.Reset()
		buf.Write(resp)
	}
	buf.WriteByte('\n')
	if _, err := out.Write(buf.Bytes()); err != nil {
		return err
	}
	return agentRejected(resp)
}

// agentRejected 在 omp 应答为 success: false 时返回带其 error 的错误。
func agentRejected(resp json.RawMessage) error {
	var r struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(resp, &r) != nil || !r.Success {
		return fmt.Errorf("agent 拒绝了命令: %s", r.Error)
	}
	return nil
}

var threadAbortOpts struct{ timeout time.Duration }

var threadAbortCmd = &cobra.Command{
	Use:   "abort <id>",
	Short: "中止线程当前的回合",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if inOrb() {
			return errUnavailableInOrb("thread abort")
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), threadAbortOpts.timeout)
		defer cancel()
		return threadAbort(ctx, c, args[0])
	},
}

// threadAbort 经 WS 下发 `{type: "abort"}`。
func threadAbort(ctx context.Context, c *apiClient, id string) error {
	resp, err := wsCommand(ctx, c, id, map[string]any{"type": "abort"})
	if err != nil {
		return err
	}
	return agentRejected(resp)
}

var threadForkOpts struct {
	entry, env string
	json       bool
}

var threadForkCmd = &cobra.Command{
	Use:   "fork <id>",
	Short: "分叉线程（默认从当前叶子、共用当前 orb；--env copy 连环境快照出副本 orb），打印新线程 id",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if inOrb() {
			return errUnavailableInOrb("thread fork")
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		o := threadForkOpts
		t, err := threadFork(cmd.Context(), c, args[0], o.entry, o.env)
		if err != nil {
			return err
		}
		if o.json {
			return printJSON(t)
		}
		fmt.Println(t.ID)
		return nil
	},
}

// threadFork 调 `POST threads/{id}/fork`；entry 为空表示从当前叶子分叉。
func threadFork(ctx context.Context, c *apiClient, id, entry, env string) (apigen.Thread, error) {
	e := apigen.ForkThreadRequestEnv(env)
	if !e.Valid() {
		return apigen.Thread{}, usageErr("--env 只能是 share 或 copy")
	}
	body := apigen.ForkThreadRequest{Env: e}
	if entry != "" {
		body.EntryID = &entry
	}
	var out apigen.ThreadEnvelope
	if err := c.do(ctx, http.MethodPost, "/api/v1/threads/"+url.PathEscape(id)+"/fork", body, &out); err != nil {
		return apigen.Thread{}, err
	}
	return out.Thread, nil
}

// orbThreadNewRejected 是 orb 内 `thread new` 不接受的参数（只接受 --title、--type、--json）。
var orbThreadNewRejected = []string{"project", "no-project", "provider", "orb", "label", "browser-profile", "readonly", "takeover", "space", "runner", "dir", "worktree", "share-dir"}

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

var threadPullOpts struct{ json bool }

var threadPullCmd = &cobra.Command{
	Use:   "pull <id>",
	Short: "orb 内：把父子线程或同项目 / 同 Space 线程仓库里的改动取进当前仓库的 refs/oryn/threads/<id>/{head,snapshot}（不动工作区）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := os.Getwd()
		if err != nil {
			return err
		}
		res, err := threadPull(cmd.Context(), dir, args[0])
		if err != nil {
			return err
		}
		if threadPullOpts.json {
			return printJSON(res)
		}
		return printThreadPull(os.Stdout, args[0], res)
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
	f.StringVar(&threadNewOpts.runner, "runner", "", "在 runner 上执行（名字或 id；需要 --dir）")
	f.StringVar(&threadNewOpts.dir, "dir", "", "runner 上的工作目录（绝对路径，须在 runner 声明的目录之内）")
	f.StringVar(&threadNewOpts.worktree, "worktree", "", "先在 --dir（git 仓库）旁新建该名字的 worktree 与分支，线程在其中工作")
	f.BoolVar(&threadNewOpts.shareDir, "share-dir", false, "允许与其它未归档线程共用 --dir")
	f.StringVar(&threadNewOpts.box, "box", "", "在 box 上开线程（名字或 id；与 --runner / --orb / --provider / --type 互斥）")
	f.StringVar(&threadNewOpts.app, "app", "", "与 --box 同用：工作目录是该 app 的线上目录（缺省为机器本身）")
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
	threadRPCCmd.Flags().DurationVar(&threadRPCOpts.timeout, "timeout", 5*time.Minute, "等待 agent 应答的时长（含唤醒 orb）")
	threadAbortCmd.Flags().DurationVar(&threadAbortOpts.timeout, "timeout", time.Minute, "等待 agent 应答的时长")
	threadForkCmd.Flags().StringVar(&threadForkOpts.entry, "entry", "", "从该条目分叉（缺省取当前叶子）")
	threadForkCmd.Flags().StringVar(&threadForkOpts.env, "env", string(apigen.Share), "share 共用当前 orb；copy 连环境快照出副本 orb")
	threadForkCmd.Flags().BoolVar(&threadForkOpts.json, "json", false, "以 JSON 输出新线程")
	threadWaitCmd.Flags().DurationVar(&threadWaitOpts.timeout, "timeout", defaultWaitTimeout, "最长等待时间（上限 24h）")
	threadWaitCmd.Flags().BoolVar(&threadWaitOpts.json, "json", false, "以 JSON 输出线程信息")
	threadPullCmd.Flags().BoolVar(&threadPullOpts.json, "json", false, "以 JSON 输出对方仓库的状态")
	threadCmd.AddCommand(threadNewCmd, threadListCmd, threadShowCmd, threadSendCmd, threadArchiveCmd, threadWaitCmd, threadPullCmd,
		threadRPCCmd, threadAbortCmd, threadForkCmd)
	root.AddCommand(threadCmd)
}
