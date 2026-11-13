// Package transcript 是线程转录的纯函数：活动分支、消息文本、公开视图与 Markdown 导出。
// CLI（show / attach）、公开分享与通知共用；不依赖其它内部包。
//
// 语义与 web/src/state/transcript.ts 一致：只有主会话文件（不含 "/"）参与活动分支，子 agent 转录不含在内；
// 公开视图对隐藏类型的条目「保留链、清空内容」，因为活动分支沿 parentId 回溯，丢掉条目会让链断开。
package transcript

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"
)

// Entry 是一条已复制入库的会话 JSONL 行（字段与 threads.Entry / apigen.Entry 同形）。
type Entry struct {
	Seq      int64           `json:"seq"`
	File     string          `json:"file"`
	EntryID  string          `json:"entryId"`
	ParentID *string         `json:"parentId,omitempty"`
	Type     string          `json:"type"`
	TS       *int64          `json:"ts,omitempty"`
	Payload  json.RawMessage `json:"payload"`
}

// IsMainFile 报告 file 是否是主会话文件：不含 "/" 且以 .jsonl 结尾（子 agent 转录在产物子目录里）。
func IsMainFile(file string) bool {
	return !strings.Contains(file, "/") && strings.HasSuffix(file, ".jsonl")
}

// isSynthetic 报告条目 id 是否是入库时合成的（标题槽 `__title__`、会话头 `__session__`）。
func isSynthetic(id string) bool { return strings.HasPrefix(id, "__") }

// ActiveBranch 返回活动分支上的主会话条目：主会话文件按首次出现的 seq 拼接；
// 含 leaf 的文件从 leaf 沿 parentId 回溯，其余文件从其最后一条（按输入顺序）回溯。
// 合成 id 的条目与子 agent 文件不在结果里；遇到环或断链即停止回溯。
// 只用到 Seq / File / EntryID / ParentID（Payload 可为空），输入应按 seq 升序。
func ActiveBranch(entries []Entry, leaf string) []Entry {
	type fileEntries struct {
		first int64
		byID  map[string]Entry
		last  string
	}
	files := map[string]*fileEntries{}
	for _, e := range entries {
		if !IsMainFile(e.File) || isSynthetic(e.EntryID) {
			continue
		}
		f := files[e.File]
		if f == nil {
			f = &fileEntries{first: e.Seq, byID: map[string]Entry{}}
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
	var out []Entry
	for _, n := range names {
		f := files[n]
		start := f.last
		if _, ok := f.byID[leaf]; ok {
			start = leaf
		}
		var chain []Entry
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

// ToolCall 是 assistant 消息里的一次工具调用。
type ToolCall struct {
	ID, Name string
	Args     json.RawMessage
}

// Message 是 message 条目里 omp 消息的文本化视图。
type Message struct {
	Role       string // user | assistant | toolResult | custom | hookMessage | bashExecution …
	ToolName   string // toolResult
	ToolCallID string // toolResult
	Text       string // text 块按出现顺序以 "\n" 连接（content 为字符串时即该串）
	Thinking   string // thinking 块按出现顺序以 "\n" 连接
	ToolCalls  []ToolCall
	Images     int
	IsError    bool
}

// rawMessage 是 payload.message 中本包关心的字段。
type rawMessage struct {
	Role       string          `json:"role"`
	ToolName   string          `json:"toolName"`
	ToolCallID string          `json:"toolCallId"`
	Content    json.RawMessage `json:"content"`
	IsError    bool            `json:"isError"`
	Display    *bool           `json:"display"`
}

// block 是消息 content 数组的一个元素。
type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// parseRaw 解出 message 条目的 payload.message；非 message 条目或无 message 对象时返回 false。
func parseRaw(e Entry) (rawMessage, bool) {
	if e.Type != "message" {
		return rawMessage{}, false
	}
	var p struct {
		Message *rawMessage `json:"message"`
	}
	if json.Unmarshal(e.Payload, &p) != nil || p.Message == nil {
		return rawMessage{}, false
	}
	return *p.Message, true
}

// blocks 把 content 规整为块序列：字符串 content 视为一个 text 块。
func blocks(content json.RawMessage) []block {
	var s string
	if json.Unmarshal(content, &s) == nil {
		if s == "" {
			return nil
		}
		return []block{{Type: "text", Text: s}}
	}
	var bs []block
	_ = json.Unmarshal(content, &bs)
	return bs
}

// ParseMessage 解出 message 条目里的消息；非 message 条目（或 payload 已被清空）返回 false。
func ParseMessage(e Entry) (Message, bool) {
	r, ok := parseRaw(e)
	if !ok {
		return Message{}, false
	}
	m := Message{Role: r.Role, ToolName: r.ToolName, ToolCallID: r.ToolCallID, IsError: r.IsError}
	var text, thinking []string
	for _, b := range blocks(r.Content) {
		switch b.Type {
		case "text":
			text = append(text, b.Text)
		case "thinking":
			thinking = append(thinking, b.Thinking)
		case "toolCall":
			m.ToolCalls = append(m.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Args: b.Arguments})
		case "image":
			m.Images++
		}
	}
	m.Text = strings.Join(text, "\n")
	m.Thinking = strings.Join(thinking, "\n")
	return m, true
}

// LineText 返回消息条目的角色与单段文本（`oryn thread show --entries` 的输出）：
// text 块原样、toolCall 写成 `<tool 名 参数>`、image 写成 `<image>`，按 "\n" 连接；
// toolResult 的角色带工具名（`toolResult:bash`）。非消息条目返回空角色。
func LineText(e Entry) (role, text string) {
	r, ok := parseRaw(e)
	if !ok {
		return "", ""
	}
	var s string
	if json.Unmarshal(r.Content, &s) == nil {
		text = s
	} else {
		var bs []block
		_ = json.Unmarshal(r.Content, &bs)
		var parts []string
		for _, b := range bs {
			switch b.Type {
			case "text":
				parts = append(parts, b.Text)
			case "toolCall":
				parts = append(parts, "<tool "+b.Name+" "+string(b.Arguments)+">")
			case "image":
				parts = append(parts, "<image>")
			}
		}
		text = strings.Join(parts, "\n")
	}
	role = r.Role
	if role == "toolResult" && r.ToolName != "" {
		role += ":" + r.ToolName
	}
	return role, text
}

// publicTypes 是公开视图保留内容的条目类型（站内渲染的类型：消息、压缩、分支摘要、自定义消息与元信息行）。
var publicTypes = map[string]bool{
	"message":               true,
	"compaction":            true,
	"branch_summary":        true,
	"custom_message":        true,
	"model_change":          true,
	"thinking_level_change": true,
	"mode_change":           true,
	"ttsr_injection":        true,
	"title_change":          true,
}

// Public 返回条目的公开视图。子 agent 文件 → ok=false（不公开）。
// 类型不在白名单、`role ∈ {custom, hookMessage}` 且 `display:false` 的消息、`display:false` 的 custom_message
// → Payload 换成 `{"type":"<type>"}`（保留链、清空内容）；其余原样返回。
func Public(e Entry) (Entry, bool) {
	if !IsMainFile(e.File) {
		return Entry{}, false
	}
	if !publicTypes[e.Type] || hiddenByDisplay(e) {
		b, _ := json.Marshal(struct {
			Type string `json:"type"`
		}{e.Type})
		e.Payload = b
	}
	return e, true
}

// hiddenByDisplay 报告条目是否是 `display:false` 的注入消息。
func hiddenByDisplay(e Entry) bool {
	switch e.Type {
	case "message":
		r, ok := parseRaw(e)
		return ok && (r.Role == "custom" || r.Role == "hookMessage") && r.Display != nil && !*r.Display
	case "custom_message":
		var p struct {
			Display *bool `json:"display"`
		}
		return json.Unmarshal(e.Payload, &p) == nil && p.Display != nil && !*p.Display
	}
	return false
}

// Snippet 把 Markdown 文本压成一行摘要：去掉反引号围栏行与行首的 `#` / `>` / `-` / `*` / `+` 标记，
// 空白折叠为单个空格；超过 maxRunes 个字符时截断并以 "…" 结尾（结果至多 maxRunes 个字符）。
func Snippet(text string, maxRunes int) string {
	var parts []string
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "```") || strings.HasPrefix(l, "~~~") {
			continue
		}
		l = stripMarkers(l)
		if l != "" {
			parts = append(parts, l)
		}
	}
	s := strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
	if maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	r := []rune(s)[:maxRunes-1]
	return strings.TrimRight(string(r), " ") + "…"
}

// stripMarkers 去掉行首的引用 `>`、标题 `# `、列表 `- ` / `* ` / `+ ` 标记（可叠加，如 `> - item`）。
func stripMarkers(l string) string {
	for {
		l = strings.TrimLeft(l, " \t")
		switch {
		case strings.HasPrefix(l, ">"):
			l = l[1:]
		case strings.HasPrefix(l, "#"):
			t := strings.TrimLeft(l, "#")
			if t == "" {
				return ""
			}
			if t[0] != ' ' && t[0] != '\t' {
				return l
			}
			l = t
		case len(l) >= 2 && (l[0] == '-' || l[0] == '*' || l[0] == '+') && (l[1] == ' ' || l[1] == '\t'):
			l = l[2:]
		default:
			return l
		}
	}
}
