package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// M9：协调档的转储、宿主工具（set_host_tools / get_active_tools / host_tool_call）与沙箱探测（probe）。

// dumpRecord 是转储文件的内容。
type dumpRecord struct {
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
	UID  int               `json:"uid"`
	Cwd  string            `json:"cwd"`
	// SystemPrompt 是按 resolvePromptInput 解析后的系统提示词正文。
	SystemPrompt string `json:"systemPrompt"`
}

// resolvePromptInput 照 omp（oryn-core system-prompt.ts 的同名函数）解析 --system-prompt：不含换行的值当作文件路径读，
// 读成功取文件内容，读失败（不存在等）保留原值；含换行的值原样使用。
func resolvePromptInput(v string) string {
	if v == "" || strings.Contains(v, "\n") {
		return v
	}
	b, err := os.ReadFile(v)
	if err != nil {
		return v
	}
	return string(b)
}

// dumpInvocation 把启动参数与环境写成 JSON：环境变量 FAKEOMP_DUMP_DIR 非空时写到
// `<dir>/<ORYN_THREAD_ID 或 pid>.json`；--system-prompt 的首行为 `#dump <路径>` 时写到该路径
// （协调档的环境从零构造、带不进 FAKEOMP_DUMP_DIR；路径必须在 HOME 之内）。失败只记 stderr。
func dumpInvocation(o options, getenv func(string) string, stderr io.Writer) {
	prompt := resolvePromptInput(o.systemPrompt)
	rec := dumpRecord{Args: nonNilStrings(o.args), Env: map[string]string{}, UID: os.Getuid(), Cwd: o.cwd, SystemPrompt: prompt}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			rec.Env[k] = v
		}
	}
	b, _ := json.MarshalIndent(rec, "", "  ")
	var targets []string
	if dir := getenv("FAKEOMP_DUMP_DIR"); dir != "" {
		name := getenv("ORYN_THREAD_ID")
		if name == "" {
			name = strconv.Itoa(os.Getpid())
		}
		targets = append(targets, filepath.Join(dir, name+".json"))
	}
	first, _, _ := strings.Cut(prompt, "\n")
	if p, ok := strings.CutPrefix(strings.TrimSpace(first), "#dump "); ok {
		p = strings.TrimSpace(p)
		home := getenv("HOME")
		if rel, err := filepath.Rel(home, p); home == "" || !filepath.IsAbs(p) || err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			fmt.Fprintf(stderr, "fakeomp: #dump path %q is not inside HOME %q; ignored\n", p, home)
		} else {
			targets = append(targets, p)
		}
	}
	for _, p := range targets {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			fmt.Fprintln(stderr, "fakeomp: dump:", err)
			continue
		}
		if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
			fmt.Fprintln(stderr, "fakeomp: dump:", err)
		}
	}
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// hostTool 是 set_host_tools 注册的一个工具。
type hostTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// handleHost 处理宿主工具相关的命令与侧通道帧；不是这类命令时返回 false。
func (s *server) handleHost(typ, id string, line []byte) bool {
	switch typ {
	case "set_host_tools":
		var c struct {
			Tools []hostTool `json:"tools"`
		}
		if err := json.Unmarshal(line, &c); err != nil {
			s.fail(id, typ, "invalid set_host_tools: "+err.Error())
			return true
		}
		names := make([]string, 0, len(c.Tools))
		for _, t := range c.Tools {
			names = append(names, t.Name)
		}
		s.mu.Lock()
		s.hostDescs = c.Tools
		s.mu.Unlock()
		s.ok(id, typ, map[string]any{"toolNames": names})
	case "get_active_tools":
		s.mu.Lock()
		tools := make([]map[string]any, 0, len(s.hostDescs))
		for _, t := range s.hostDescs {
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "source": "extension"})
		}
		s.mu.Unlock()
		s.ok(id, typ, map[string]any{"tools": tools})
	case "host_tool_result":
		s.mu.Lock()
		ch := s.hostWait[id]
		delete(s.hostWait, id)
		s.mu.Unlock()
		if ch != nil {
			ch <- json.RawMessage(line)
		}
	case "host_tool_update":
		// 部分结果：只确认收到，不影响回合。
	default:
		return false
	}
	return true
}

// specialStep 是提示词中的一行 `hosttool <name> <json>` 或 `probe <op> [<arg>]`。
type specialStep struct {
	host bool
	name string         // 宿主工具名或 probe 操作
	args map[string]any // 宿主工具参数
	arg  string         // probe 参数
}

// parseSpecial 逐行找出提示词里的 hosttool / probe 行（其余行忽略，例如来源头行）。
func parseSpecial(message string) []specialStep {
	var steps []specialStep
	for _, l := range strings.Split(message, "\n") {
		l = strings.TrimSpace(l)
		if rest, ok := strings.CutPrefix(l, "hosttool "); ok {
			name, raw, _ := strings.Cut(strings.TrimSpace(rest), " ")
			args := map[string]any{}
			if raw = strings.TrimSpace(raw); raw != "" {
				if err := json.Unmarshal([]byte(raw), &args); err != nil {
					args = map[string]any{"_invalid": raw}
				}
			}
			steps = append(steps, specialStep{host: true, name: name, args: args})
		} else if rest, ok := strings.CutPrefix(l, "probe "); ok {
			op, arg, _ := strings.Cut(strings.TrimSpace(rest), " ")
			steps = append(steps, specialStep{name: op, arg: strings.TrimSpace(arg)})
		}
	}
	return steps
}

// runSpecial 执行宿主工具调用与探测；回合最后的 assistant 文本每步一行：
// `host:<name>:<ok|error>:<结果文本前 200 字符>` 或 `probe:<op>:<ok|错误串>`。
func (t *turn) runSpecial(p promptReq, steps []specialStep) {
	t.appendEntry(map[string]any{
		"role": "user", "content": userContent(p.message, p.images), "attribution": "user", "timestamp": nowMs(),
	})
	t.s.emitEvent(map[string]any{"type": "agent_start"})
	var lines []string
	for _, st := range steps {
		if !st.host {
			lines = append(lines, "probe:"+st.name+":"+t.s.probe(t.ctx, st.name, st.arg))
			continue
		}
		line, ok := t.hostCall(st)
		if !ok {
			t.finish("aborted")
			return
		}
		lines = append(lines, line)
	}
	if !t.streamAssistant(strings.Join(lines, "\n"), nil, "stop") {
		t.finish("aborted")
		return
	}
	t.finish("completed")
}

// hostCall 发起一次宿主工具调用并等待 host_tool_result；回合被中断时发 host_tool_cancel 并返回 false。
func (t *turn) hostCall(st specialStep) (string, bool) {
	callID := "call_" + randHex(8)
	if !t.streamAssistant("Calling "+st.name+".", &toolCall{id: callID, name: st.name, args: st.args}, "toolUse") {
		return "", false
	}
	t.s.emitEvent(map[string]any{"type": "tool_execution_start", "toolCallId": callID, "toolName": st.name, "args": st.args})
	t.s.mu.Lock()
	t.s.hostSeq++
	id := "h-" + strconv.Itoa(t.s.hostSeq)
	ch := make(chan json.RawMessage, 1)
	t.s.hostWait[id] = ch
	t.s.mu.Unlock()
	t.s.emit(map[string]any{"type": "host_tool_call", "id": id, "toolCallId": callID, "toolName": st.name, "arguments": st.args})

	var raw json.RawMessage
	select {
	case raw = <-ch:
	case <-t.ctx.Done():
		t.s.mu.Lock()
		delete(t.s.hostWait, id)
		t.s.mu.Unlock()
		t.s.emit(map[string]any{"type": "host_tool_cancel", "id": "h-cancel-" + id, "targetId": id})
		aborted := map[string]any{"content": []any{map[string]any{"type": "text", "text": "aborted"}}, "details": map[string]any{}}
		t.s.emitEvent(map[string]any{"type": "tool_execution_end", "toolCallId": callID, "toolName": st.name, "result": aborted, "isError": true})
		return "", false
	}
	var r struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Details any `json:"details"`
		} `json:"result"`
		IsError bool `json:"isError"`
	}
	_ = json.Unmarshal(raw, &r)
	var text strings.Builder
	content := make([]any, 0, len(r.Result.Content))
	for _, c := range r.Result.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
		content = append(content, map[string]any{"type": c.Type, "text": c.Text})
	}
	details := r.Result.Details
	if details == nil {
		details = map[string]any{}
	}
	result := map[string]any{"content": content, "details": details}
	t.s.emitEvent(map[string]any{"type": "tool_execution_end", "toolCallId": callID, "toolName": st.name, "result": result, "isError": r.IsError})
	t.appendEntry(map[string]any{
		"role": "toolResult", "toolCallId": callID, "toolName": st.name,
		"content": content, "details": details, "isError": r.IsError, "timestamp": nowMs(),
	})
	status := "ok"
	if r.IsError {
		status = "error"
	}
	return "host:" + st.name + ":" + status + ":" + truncRunes(text.String(), 200), true
}

func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// probe 在当前进程里执行一次探测操作，成功返回 "ok"，否则返回错误串；`args` 返回参数、环境键与 uid 的 JSON。
func (s *server) probe(ctx context.Context, op, arg string) string {
	var err error
	switch op {
	case "args":
		keys := []string{}
		for _, kv := range os.Environ() {
			if k, _, ok := strings.Cut(kv, "="); ok {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		b, _ := json.Marshal(map[string]any{"args": nonNilStrings(s.opts.args), "envKeys": keys, "uid": os.Getuid()})
		return string(b)
	case "read":
		_, err = os.ReadFile(arg)
	case "write":
		err = os.WriteFile(arg, []byte("probe\n"), 0o600)
	case "exec":
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		err = exec.CommandContext(cctx, arg).Run()
	case "tcp", "unix":
		var c net.Conn
		c, err = net.DialTimeout(op, arg, 3*time.Second)
		if c != nil {
			_ = c.Close()
		}
	case "kill":
		var pid int
		if pid, err = strconv.Atoi(arg); err == nil {
			err = syscall.Kill(pid, 0)
		}
	default:
		return "unknown probe op " + strconv.Quote(op)
	}
	if err != nil {
		return err.Error()
	}
	return "ok"
}
