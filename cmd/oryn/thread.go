package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/coder/websocket"
	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

var threadCmd = &cobra.Command{
	Use:   "thread",
	Short: "线程",
}

var threadNewOpts struct {
	project, provider, orbType string
	noProject, json            bool
}

var threadNewCmd = &cobra.Command{
	Use:   "new <prompt>",
	Short: "新建线程（默认新建 orb）",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		o := threadNewOpts
		if o.project != "" && o.noProject {
			return errors.New("--project 与 --no-project 不能同时使用")
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		req := apigen.CreateThreadRequest{Prompt: args[0]}
		exec := apigen.ThreadExecutorNewOrb{Kind: "new_orb"}
		if o.provider != "" {
			exec.Provider = &o.provider
		}
		if o.orbType != "" {
			exec.Type = &o.orbType
		}
		if err := req.Executor.FromThreadExecutorNewOrb(exec); err != nil {
			return err
		}
		if o.project != "" {
			id, err := resolveProject(ctx, c, o.project)
			if err != nil {
				return err
			}
			req.ProjectID = &id
		}
		var env apigen.ThreadEnvelope
		if err := c.do(ctx, http.MethodPost, "/api/v1/threads", req, &env); err != nil {
			return err
		}
		if o.json {
			return printJSON(env.Thread)
		}
		fmt.Println(env.Thread.ID)
		return nil
	},
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

var threadListJSON bool

var threadListCmd = &cobra.Command{
	Use:   "list",
	Short: "列出未归档线程",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		var list apigen.ThreadList
		if err := c.do(cmd.Context(), http.MethodGet, "/api/v1/threads", nil, &list); err != nil {
			return err
		}
		if threadListJSON {
			return printJSON(list.Threads)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tSTATE\tUNREAD\tUPDATED\tTITLE")
		for _, t := range list.Threads {
			unread := ""
			if t.Unread {
				unread = "●"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.State, unread, time.UnixMilli(t.UpdatedAt).Local().Format("01-02 15:04"), t.Title)
		}
		return tw.Flush()
	},
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
			for _, e := range activeBranch(entries, leaf) {
				role, text := entryText(e)
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

// cliEntry 是 `GET threads/{id}/entries` 的条目。
type cliEntry struct {
	Seq      int64           `json:"seq"`
	File     string          `json:"file"`
	EntryID  string          `json:"entryId"`
	ParentID *string         `json:"parentId"`
	Type     string          `json:"type"`
	Payload  json.RawMessage `json:"payload"`
}

func fetchEntries(ctx context.Context, c *apiClient, id string) ([]cliEntry, string, error) {
	var all []cliEntry
	var after int64
	leaf := ""
	for {
		var page struct {
			Entries     []cliEntry `json:"entries"`
			HasMore     bool       `json:"hasMore"`
			LeafEntryID *string    `json:"leafEntryId"`
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

// activeBranch 返回活动分支上的条目：主会话文件按首次出现顺序拼接；
// 叶子所在文件从叶子沿 parentId 回溯，其余文件从其最后一条条目回溯。子 agent 转录不含在内。
func activeBranch(entries []cliEntry, leaf string) []cliEntry {
	type fileEntries struct {
		first int64
		byID  map[string]cliEntry
		last  string
	}
	files := map[string]*fileEntries{}
	for _, e := range entries {
		if strings.Contains(e.File, "/") || strings.HasPrefix(e.EntryID, "__") {
			continue
		}
		f := files[e.File]
		if f == nil {
			f = &fileEntries{first: e.Seq, byID: map[string]cliEntry{}}
			files[e.File] = f
		}
		f.byID[e.EntryID] = e
		f.last = e.EntryID
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return files[names[i]].first < files[names[j]].first })
	var out []cliEntry
	for _, n := range names {
		f := files[n]
		start := f.last
		if _, ok := f.byID[leaf]; ok {
			start = leaf
		}
		var chain []cliEntry
		seen := map[string]bool{}
		for id := start; id != "" && !seen[id]; {
			e, ok := f.byID[id]
			if !ok {
				break
			}
			seen[id] = true
			chain = append(chain, e)
			if e.ParentID == nil {
				break
			}
			id = *e.ParentID
		}
		for i := len(chain) - 1; i >= 0; i-- {
			out = append(out, chain[i])
		}
	}
	return out
}

// entryText 提取消息条目的角色与文本；非消息条目返回空角色。
func entryText(e cliEntry) (string, string) {
	if e.Type != "message" {
		return "", ""
	}
	var p struct {
		Message struct {
			Role     string          `json:"role"`
			ToolName string          `json:"toolName"`
			Content  json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(e.Payload, &p) != nil {
		return "", ""
	}
	m := p.Message
	var text string
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		text = s
	} else {
		var blocks []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(m.Content, &blocks)
		var parts []string
		for _, b := range blocks {
			switch b.Type {
			case "text":
				parts = append(parts, b.Text)
			case "toolCall":
				parts = append(parts, fmt.Sprintf("<tool %s %s>", b.Name, b.Arguments))
			case "image":
				parts = append(parts, "<image>")
			}
		}
		text = strings.Join(parts, "\n")
	}
	role := m.Role
	if role == "toolResult" && m.ToolName != "" {
		role += ":" + m.ToolName
	}
	return role, text
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

// wsCommand 经 WebSocket 下发一条 omp 命令并等待其应答。
func wsCommand(ctx context.Context, c *apiClient, threadID string, frame any) (json.RawMessage, error) {
	var tk apigen.WsTicket
	if err := c.do(ctx, http.MethodPost, "/api/v1/ws-ticket", nil, &tk); err != nil {
		return nil, err
	}
	u, err := url.Parse(c.base)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/api/v1/ws"
	u.RawQuery = url.Values{"ticket": {tk.Ticket}}.Encode()
	ws, _, err := websocket.Dial(ctx, u.String(), nil)
	if err != nil {
		return nil, err
	}
	defer ws.CloseNow()
	ws.SetReadLimit(64 << 20)
	const id = "cli-1"
	msg, _ := json.Marshal(map[string]any{"type": "cmd", "threadId": threadID, "id": id, "frame": frame})
	if err := ws.Write(ctx, websocket.MessageText, msg); err != nil {
		return nil, err
	}
	for {
		_, b, err := ws.Read(ctx)
		if err != nil {
			return nil, err
		}
		var m struct {
			Type     string          `json:"type"`
			ID       string          `json:"id"`
			Code     string          `json:"code"`
			Message  string          `json:"message"`
			Response json.RawMessage `json:"response"`
		}
		if json.Unmarshal(b, &m) != nil || m.ID != id {
			continue
		}
		switch m.Type {
		case "res":
			_ = ws.Close(websocket.StatusNormalClosure, "")
			return m.Response, nil
		case "error":
			return nil, &apiError{Code: m.Code, Message: m.Message}
		}
	}
}

func init() {
	f := threadNewCmd.Flags()
	f.StringVar(&threadNewOpts.project, "project", "", "项目名")
	f.BoolVar(&threadNewOpts.noProject, "no-project", false, "不关联项目")
	f.StringVar(&threadNewOpts.provider, "provider", "", "orb provider 名称")
	f.StringVar(&threadNewOpts.orbType, "type", "", "orb 类型")
	f.BoolVar(&threadNewOpts.json, "json", false, "以 JSON 输出线程")
	threadListCmd.Flags().BoolVar(&threadListJSON, "json", false, "以 JSON 输出")
	threadShowCmd.Flags().BoolVar(&threadShowOpts.json, "json", false, "以 JSON 输出线程")
	threadShowCmd.Flags().BoolVar(&threadShowOpts.entries, "entries", false, "按活动分支打印条目")
	threadSendCmd.Flags().BoolVar(&threadSendOpts.steer, "steer", false, "插入当前回合（steer）")
	threadSendCmd.Flags().DurationVar(&threadSendOpts.timeout, "timeout", 5*time.Minute, "等待 agent 应答的时长（含唤醒 orb）")
	threadCmd.AddCommand(threadNewCmd, threadListCmd, threadShowCmd, threadSendCmd)
	root.AddCommand(threadCmd)
}
