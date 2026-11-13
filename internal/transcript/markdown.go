package transcript

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// MarkdownMeta 是 Markdown 导出的页眉信息。
type MarkdownMeta struct {
	Title    string    // 空则 `Untitled`
	Mode     string    // snapshot | live
	SharedAt time.Time // 以 RFC 3339 UTC 输出
}

const (
	// maxResultRunes 是单个工具结果在导出中的上限（字符）。
	maxResultRunes = 4000
	// maxSummaryRunes 是工具调用参数摘要的上限（字符）。
	maxSummaryRunes = 200
)

// summaryKeys 是工具参数摘要依次尝试的字段。
var summaryKeys = []string{"command", "path", "pattern", "query"}

// attachmentBlockRE 匹配控制面追加在用户消息末尾的附件块；attachmentLineRE 匹配其中的一行（同 web/src/lib/attachments.ts）。
var (
	attachmentBlockRE = regexp.MustCompile(`(?:\n\n)?<oryn-attachments>\n([\s\S]*?)</oryn-attachments>\s*$`)
	attachmentLineRE  = regexp.MustCompile(`^- (.+) \(([^,()]+), (\d+) bytes\)$`)
)

// Markdown 把活动分支（通常已经过 Public）写成 Markdown。标签固定为英文：
// `# 标题`、`> Shared from Oryn · <mode> · <时间>`、`## User` / `## Assistant` 段落；
// 连续的 assistant 条目合并在一个 `## Assistant` 下，思考块与工具结果折叠在 `<details>` 里，
// 工具结果按 toolCallId 跟在对应调用之后（找不到则省略）；会话文件之间插一行 `---`；
// compaction / branch_summary 写成引用行；payload 已清空的条目与其它类型跳过。
func Markdown(w io.Writer, meta MarkdownMeta, branch []Entry) error {
	results := map[string]rawMessage{}
	for _, e := range branch {
		if r, ok := parseRaw(e); ok && r.Role == "toolResult" && r.ToolCallID != "" {
			results[r.ToolCallID] = r
		}
	}
	title := oneLine(meta.Title)
	if title == "" {
		title = "Untitled"
	}
	md := &mdWriter{}
	md.out = append(md.out, "# "+title, "> Shared from Oryn · "+meta.Mode+" · "+meta.SharedAt.UTC().Format(time.RFC3339))
	prevFile := ""
	for _, e := range branch {
		if prevFile != "" && e.File != prevFile {
			md.sep = true
			md.section = ""
		}
		prevFile = e.File
		switch e.Type {
		case "compaction":
			md.emit("", "> *(context compacted)*")
		case "branch_summary":
			md.emit("", "> *(branch summary)*")
		case "custom_message":
			var p struct {
				Content json.RawMessage `json:"content"`
				Display *bool           `json:"display"`
			}
			if json.Unmarshal(e.Payload, &p) != nil || (p.Display != nil && !*p.Display) {
				continue
			}
			if t := blockText(blocks(p.Content)); strings.TrimSpace(t) != "" {
				md.emit("", quote(t))
			}
		case "message":
			r, ok := parseRaw(e)
			if !ok {
				continue
			}
			switch r.Role {
			case "user":
				if body := userBody(blocks(r.Content)); body != "" {
					md.section = ""
					md.emit("user", body)
				}
			case "assistant":
				for _, b := range assistantBlocks(blocks(r.Content), results) {
					md.emit("assistant", b)
				}
			case "custom", "hookMessage":
				if r.Display != nil && !*r.Display {
					continue
				}
				if t := blockText(blocks(r.Content)); strings.TrimSpace(t) != "" {
					md.emit("", quote(t))
				}
			}
		}
	}
	_, err := io.WriteString(w, strings.Join(md.out, "\n\n")+"\n")
	return err
}

// mdWriter 累积以空行分隔的块，并维护 `## User` / `## Assistant` 标题与文件分隔线。
type mdWriter struct {
	out     []string
	section string // 当前标题对应的角色；"" = 不在任何角色段落里
	sep     bool   // 下一个内容块之前要插 `---`
	content bool   // 页眉之后是否已输出过内容块
}

// emit 输出一个块；role 非空且与当前段落不同时先输出对应标题。
func (m *mdWriter) emit(role, block string) {
	if m.sep && m.content {
		m.out = append(m.out, "---")
	}
	m.sep = false
	if role != m.section {
		switch role {
		case "user":
			m.out = append(m.out, "## User")
		case "assistant":
			m.out = append(m.out, "## Assistant")
		}
		m.section = role
	}
	m.out = append(m.out, block)
	m.content = true
}

// userBody 渲染用户消息：文本（附件块换成 `*[attachment: 名]*` 行），每张图片一行 `*[image]*`。
func userBody(bs []block) string {
	text := blockText(bs)
	var lines []string
	if m := attachmentBlockRE.FindStringSubmatchIndex(text); m != nil {
		inner := text[m[2]:m[3]]
		text = text[:m[0]]
		for _, l := range strings.Split(inner, "\n") {
			if a := attachmentLineRE.FindStringSubmatch(l); a != nil {
				lines = append(lines, "*[attachment: "+a[1][strings.LastIndex(a[1], "/")+1:]+"]*")
			}
		}
	}
	for _, b := range bs {
		if b.Type == "image" {
			lines = append(lines, "*[image]*")
		}
	}
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return strings.Join(lines, "\n")
	}
	if len(lines) == 0 {
		return text
	}
	return text + "\n\n" + strings.Join(lines, "\n")
}

// assistantBlocks 按内容块顺序渲染 assistant 消息：思考块、文本、工具调用行及其结果。
func assistantBlocks(bs []block, results map[string]rawMessage) []string {
	var out []string
	for _, b := range bs {
		switch b.Type {
		case "thinking":
			if strings.TrimSpace(b.Thinking) != "" {
				out = append(out, "<details><summary>Thinking</summary>\n\n"+strings.TrimRight(b.Thinking, "\n")+"\n\n</details>")
			}
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				out = append(out, strings.TrimRight(b.Text, "\n"))
			}
		case "toolCall":
			line := "**▸ " + oneLine(b.Name) + "**"
			if s := toolSummary(b.Arguments); s != "" {
				line += " " + codeSpan(s)
			}
			out = append(out, line)
			r, ok := results[b.ID]
			if !ok || b.ID == "" {
				continue
			}
			label := "Result"
			if r.IsError {
				label = "Error"
			}
			body := resultText(blocks(r.Content))
			if utf8.RuneCountInString(body) > maxResultRunes {
				body = string([]rune(body)[:maxResultRunes]) + "\n… (truncated)"
			}
			fence := strings.Repeat("`", max(3, longestRun(body, '`')+1))
			out = append(out, "<details><summary>"+label+"</summary>\n\n"+fence+"text\n"+body+"\n"+fence+"\n\n</details>")
		}
	}
	return out
}

// blockText 连接 text 块。
func blockText(bs []block) string {
	var parts []string
	for _, b := range bs {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// resultText 连接工具结果的 text 块，图片写成 `[image]`。
func resultText(bs []block) string {
	var parts []string
	for _, b := range bs {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "image":
			parts = append(parts, "[image]")
		}
	}
	return strings.TrimRight(strings.Join(parts, "\n"), "\n")
}

// toolSummary 取参数里 command / path / pattern / query 中第一个非空字符串，压成单行、至多 200 字符。
func toolSummary(args json.RawMessage) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(args, &m) != nil {
		return ""
	}
	for _, k := range summaryKeys {
		var s string
		if json.Unmarshal(m[k], &s) != nil {
			continue
		}
		if s = oneLine(s); s != "" {
			if utf8.RuneCountInString(s) > maxSummaryRunes {
				s = string([]rune(s)[:maxSummaryRunes-1]) + "…"
			}
			return s
		}
	}
	return ""
}

// oneLine 把空白折叠为单个空格并去掉首尾空白。
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// quote 把文本写成 Markdown 引用块。
func quote(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + l
		}
	}
	return strings.Join(lines, "\n")
}

// codeSpan 把 s 写成行内代码：反引号数 = 内容里最长连续反引号 + 1；内容首尾是反引号时两侧补空格。
func codeSpan(s string) string {
	ticks := strings.Repeat("`", longestRun(s, '`')+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return ticks + s + ticks
}

// longestRun 返回 s 中字节 c 的最长连续长度。
func longestRun(s string, c byte) int {
	best, cur := 0, 0
	for i := range len(s) {
		if s[i] == c {
			cur++
			best = max(best, cur)
		} else {
			cur = 0
		}
	}
	return best
}
