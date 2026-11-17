// Package ownermem 是全局助手的主人记忆（M17）：把助手对话与工作线程写入 Hindsight bank `oryn-owner`，
// 整理主题、注入系统提示词并提供检索工具。设计见 docs/research/research-AssistantMemory.md，计划见 docs/plan-m17.md。
package ownermem

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/transcript"
)

// 来源种类（document id 前缀，计划「全局约束」）。
const (
	SourceConv   = "conv"   // 助手对话与 Space 聊天线程（threads.kind = assistant | space）
	SourceThread = "thread" // 工作线程（threads.kind = work）
)

// 输入构造的固定值（计划「全局约束」与 R1–R3）。
const (
	chunkRunes    = 2500 // 一块累计超过这么多字符就开新块（不拆开回合单元）
	replyRunes    = 600  // 助手 / agent 回复的截断长度
	ownerRunes    = 2000 // 主人消息的截断长度
	autoRunes     = 300  // 自动消息的截断长度（只在工作线程里出现）
	maxCodeLines  = 8    // 代码块超过这么多行就省略
	truncMark     = "……（截断）"
	ownerEntity   = "主人"
	tagAssistant  = "src:assistant"
	tagThread     = "src:thread"
	tagProjectPre = "project:"
)

// Entity 是 retain item 的实体提示（Hindsight `entities` 元素）。
type Entity struct {
	Text string `json:"text"`
	Type string `json:"type,omitempty"`
}

// RetainItem 是写入 Hindsight 的一个 item（字段与 Hindsight 1.2.1 retain 的 item 同形）。
type RetainItem struct {
	Content    string            `json:"content"`
	Timestamp  string            `json:"timestamp,omitempty"` // RFC3339
	Context    string            `json:"context,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	DocumentID string            `json:"document_id"`
	Entities   []Entity          `json:"entities,omitempty"`
	Tags       []string          `json:"tags,omitempty"`
}

// Chunk 是一个来源的一块（一个 Hindsight document）：Hash 是 Items 的 JSON 的 sha256 十六进制，
// 写入循环据此只重写内容变了的块；SeqFrom / SeqTo 是块内条目 seq 的最小 / 最大值。
type Chunk struct {
	Idx        int
	DocumentID string
	SeqFrom    int64
	SeqTo      int64
	Hash       string
	Items      []RetainItem
}

// ThreadMeta 是构造块头与标签用的线程信息。
type ThreadMeta struct {
	ID, Kind, Title        string // Kind 是 threads.kind：assistant | space | work
	ProjectID, ProjectName string
	State                  string
	Archived, Shipped      bool
	Pulls                  []int64
}

// SourceKindOf 把 threads.kind 映射为来源种类：assistant / space → SourceConv，work → SourceThread，其余为空串。
func SourceKindOf(kind string) string {
	switch kind {
	case "assistant", "space":
		return SourceConv
	case "work":
		return SourceThread
	}
	return ""
}

// 行的角色（conv 的 metadata `roles` 直接用 owner / assistant；工作线程把 assistant 写作 agent）。
const (
	roleOwner     = "owner"
	roleAssistant = "assistant"
	roleAuto      = "auto"
)

// row 是清洗后的一条消息。
type row struct {
	seq  int64
	ts   *int64
	role string
	text string
}

// unit 是分块时不可拆开的回合单元：rows 是其中的消息，text 是渲染后的正文。
type unit struct {
	rows []row
	text string
}

// Build 把一个线程活动分支上的主会话条目确定性地转换成要写入 bank 的块（纯函数，不访问网络与数据库）。
//
// branch 是 transcript.ActiveBranch 的结果（按 seq 升序）：调用方分页读出线程的全部 entries
// （db.ListEntriesAfter），以 threads.leaf_entry_id 为 leaf 求活动分支，做法同 coord.(*Service).activeBranch。
// redact 对每段文本做密钥脱敏（通常包一层 share.(*Service).RedactText），为 nil 时不脱敏。
//
// 只取 message 条目：user 消息里 transcript.IsAutomatedUserText 为真的是自动消息，其余是主人消息；assistant 只取 text 块；
// 其余角色（toolResult 等）、thinking 与工具调用一律丢弃。kind 不认识或线程里没有任何主人消息时返回 nil。
func Build(meta ThreadMeta, branch []transcript.Entry, redact func(string) string) []Chunk {
	src := SourceKindOf(meta.Kind)
	if src == "" {
		return nil
	}
	if redact == nil {
		redact = func(s string) string { return s }
	}
	rows := cleanRows(branch, redact, src == SourceThread)
	hasOwner := false
	for _, r := range rows {
		if r.role == roleOwner {
			hasOwner = true
			break
		}
	}
	if !hasOwner {
		return nil
	}
	if src == SourceConv {
		return convChunks(meta, rows, redact)
	}
	return threadChunks(meta, rows, redact)
}

// cleanRows 把活动分支清洗成行：去标签块、脱敏、省略长代码块、截断；空文本丢弃；keepAuto 为假时丢弃自动消息。
// 相邻的多条助手消息（中间只隔着被丢弃的工具结果、空文本或自动消息）只保留最后一条。
func cleanRows(branch []transcript.Entry, redact func(string) string, keepAuto bool) []row {
	var out []row
	for _, e := range branch {
		if !transcript.IsMainFile(e.File) {
			continue
		}
		m, ok := transcript.ParseMessage(e)
		if !ok {
			continue
		}
		var r row
		switch m.Role {
		case "user":
			text := stripTags(m.Text)
			if transcript.IsAutomatedUserText(text) {
				if !keepAuto {
					continue
				}
				r = row{role: roleAuto, text: cleanText(text, redact, autoRunes)}
			} else {
				r = row{role: roleOwner, text: cleanText(text, redact, ownerRunes)}
			}
		case "assistant":
			r = row{role: roleAssistant, text: cleanText(stripTags(m.Text), redact, replyRunes)}
		default:
			continue
		}
		if r.text == "" {
			continue
		}
		r.seq, r.ts = e.Seq, e.TS
		if r.role == roleAssistant && len(out) > 0 && out[len(out)-1].role == roleAssistant {
			out[len(out)-1] = r
			continue
		}
		out = append(out, r)
	}
	return out
}

// tagBlocks 是要整段去掉的注入标签块（omp 的系统提醒、子任务结果、技能正文等）。
var tagBlocks = func() *regexp.Regexp {
	names := []string{"system-reminder", "system-notice", "system-interrupt", "advisory", "task-result", "skill", "system"}
	alts := make([]string, len(names))
	for i, n := range names {
		alts[i] = `<` + n + `(?:\s[^>]*)?>.*?</` + n + `\s*>`
	}
	return regexp.MustCompile(`(?s)` + strings.Join(alts, "|"))
}()

var blankRuns = regexp.MustCompile(`\n[ \t]*\n(?:[ \t]*\n)+`)

// stripTags 去掉注入标签块，并把因此留下的连续空行压成一行空行。
func stripTags(s string) string {
	s = tagBlocks.ReplaceAllString(s, "")
	return blankRuns.ReplaceAllString(s, "\n\n")
}

// cleanText 依次脱敏、省略长代码块、去首尾空白，再截断到 limit 个字符。
func cleanText(s string, redact func(string) string, limit int) string {
	return truncRunes(strings.TrimSpace(elideCode(redact(s))), limit)
}

// elideCode 把正文超过 maxCodeLines 行的围栏代码块（``` 或 ~~~，未闭合的到文末）整块替换为「（代码 N 行，已省略）」。
func elideCode(s string) string {
	if !strings.Contains(s, "```") && !strings.Contains(s, "~~~") {
		return s
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		fence := fenceOf(lines[i])
		if fence == "" {
			out = append(out, lines[i])
			continue
		}
		end := len(lines) // 闭合围栏所在行；未闭合时为 len(lines)
		for j := i + 1; j < len(lines); j++ {
			if t := strings.TrimSpace(lines[j]); strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]) == "" {
				end = j
				break
			}
		}
		n := end - i - 1
		if n > maxCodeLines {
			out = append(out, fmt.Sprintf("（代码 %d 行，已省略）", n))
		} else {
			out = append(out, lines[i:min(end+1, len(lines))]...)
		}
		i = end
	}
	return strings.Join(out, "\n")
}

// fenceOf 返回围栏开头行的围栏标记（连续 3 个以上的 ` 或 ~），不是围栏行时返回空串。
func fenceOf(line string) string {
	t := strings.TrimSpace(line)
	for _, c := range []byte{'`', '~'} {
		n := 0
		for n < len(t) && t[n] == c {
			n++
		}
		if n >= 3 {
			return t[:n]
		}
	}
	return ""
}

// truncRunes 超过 n 个字符时截到 n 个并加截断标记。
func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + truncMark
}

// convChunks 构造助手对话的块：每条主人消息一个 item（带上它回应的前一条助手回复），
// 最后一条主人消息之后若还有助手回复，再加一个只含该回复的 item。
func convChunks(meta ThreadMeta, rows []row, redact func(string) string) []Chunk {
	var units []unit
	var prev *row
	for i := range rows {
		r := rows[i]
		if r.role == roleAssistant {
			prev = &rows[i]
			continue
		}
		u := unit{}
		if prev != nil {
			u.rows = append(u.rows, *prev)
			u.text = "助手（上一条）：" + prev.text + "\n"
			prev = nil
		}
		u.rows = append(u.rows, r)
		u.text += "主人：" + r.text
		units = append(units, u)
	}
	if prev != nil {
		units = append(units, unit{rows: []row{*prev}, text: "助手：" + prev.text})
	}
	ctx := convContext(meta, redact)
	return finish(SourceConv, meta.ID, pack(units), func(us []unit, _ bool) []RetainItem {
		items := make([]RetainItem, 0, len(us))
		for _, u := range us {
			seqs := make([]string, len(u.rows))
			roles := make([]string, len(u.rows))
			for i, r := range u.rows {
				seqs[i] = strconv.FormatInt(r.seq, 10)
				roles[i] = r.role
			}
			items = append(items, newItem(u.text, u.rows, ctx, map[string]string{
				"seqs":   strings.Join(seqs, ","),
				"roles":  strings.Join(roles, ","),
				"thread": meta.ID,
			}, []string{tagAssistant}))
		}
		return items
	})
}

// threadChunks 构造工作线程的块：一个主人回合（主人消息及其后的回复与自动消息，直到下一条主人消息）是一个单元，
// 第一条主人消息之前的行自成一个单元；每块渲染成一个 item。
func threadChunks(meta ThreadMeta, rows []row, redact func(string) string) []Chunk {
	label := map[string]string{roleOwner: "主人：", roleAssistant: "agent：", roleAuto: "自动消息："}
	var units []unit
	for _, r := range rows {
		if r.role == roleOwner || len(units) == 0 {
			units = append(units, unit{})
		}
		u := &units[len(units)-1]
		if u.text != "" {
			u.text += "\n"
		}
		u.rows = append(u.rows, r)
		u.text += label[r.role] + r.text
	}
	tags := []string{tagThread}
	if meta.ProjectID != "" {
		tags = append(tags, tagProjectPre+strings.ToLower(meta.ProjectID))
	}
	// 状态与 PR 只写在最后一块：交付、归档或关联 PR 时只需重写最后一块，前面的块哈希不变。
	head, tail := threadContext(meta, redact, false), threadContext(meta, redact, true)
	return finish(SourceThread, meta.ID, pack(units), func(us []unit, last bool) []RetainItem {
		ctx := head
		if last {
			ctx = tail
		}
		var rs []row
		texts := make([]string, len(us))
		for i, u := range us {
			rs = append(rs, u.rows...)
			texts[i] = u.text
		}
		from, to := seqRange(rs)
		present := map[string]bool{}
		for _, r := range rs {
			present[r.role] = true
		}
		var roles []string
		for _, r := range []struct{ role, name string }{{roleOwner, "owner"}, {roleAssistant, "agent"}, {roleAuto, "auto"}} {
			if present[r.role] {
				roles = append(roles, r.name)
			}
		}
		return []RetainItem{newItem(strings.Join(texts, "\n"), rs, ctx, map[string]string{
			"seq_from": strconv.FormatInt(from, 10),
			"seq_to":   strconv.FormatInt(to, 10),
			"roles":    strings.Join(roles, ","),
			"thread":   meta.ID,
		}, tags)}
	})
}

// pack 从头贪心地把单元装进块：当前块非空且加上下一个单元会超过 chunkRunes 时开新块；单元本身超长时单独成块。
// 每个单元的归属只取决于它和它之前的单元，所以在末尾追加消息只会改变最后一块或新增块。
func pack(units []unit) [][]unit {
	var out [][]unit
	size := 0
	for _, u := range units {
		n := utf8.RuneCountInString(u.text)
		if len(out) == 0 || (size > 0 && size+n > chunkRunes) {
			out = append(out, nil)
			size = 0
		}
		out[len(out)-1] = append(out[len(out)-1], u)
		size += n
	}
	return out
}

// finish 把装好的单元组渲染成块：编号、document id、seq 范围与内容哈希。render 的 last 表示最后一块。
func finish(src, threadID string, groups [][]unit, render func(us []unit, last bool) []RetainItem) []Chunk {
	out := make([]Chunk, 0, len(groups))
	for idx, g := range groups {
		docID := src + ":" + threadID + ":" + strconv.Itoa(idx)
		items := render(g, idx == len(groups)-1)
		for i := range items {
			items[i].DocumentID = docID
		}
		var rs []row
		for _, u := range g {
			rs = append(rs, u.rows...)
		}
		from, to := seqRange(rs)
		b, err := json.Marshal(items)
		if err != nil {
			panic(err) // 只含字符串与切片，不会失败
		}
		sum := sha256.Sum256(b)
		out = append(out, Chunk{Idx: idx, DocumentID: docID, SeqFrom: from, SeqTo: to, Hash: hex.EncodeToString(sum[:]), Items: items})
	}
	return out
}

// newItem 构造一个 item（DocumentID 由 finish 填写）：timestamp 取首条带时间的消息，实体固定为「主人」。
func newItem(content string, rows []row, ctx string, md map[string]string, tags []string) RetainItem {
	it := RetainItem{
		Content:  content,
		Context:  ctx,
		Metadata: md,
		Entities: []Entity{{Text: ownerEntity}},
		Tags:     append([]string(nil), tags...),
	}
	for _, r := range rows {
		if r.ts != nil {
			it.Timestamp = time.UnixMilli(*r.ts).UTC().Format(time.RFC3339)
			break
		}
	}
	return it
}

// seqRange 返回行的最小与最大 seq。
func seqRange(rows []row) (from, to int64) {
	for i, r := range rows {
		if i == 0 || r.seq < from {
			from = r.seq
		}
		if i == 0 || r.seq > to {
			to = r.seq
		}
	}
	return from, to
}

// convContext 是助手对话 item 的 context：「主人与全局助手的对话「标题」」（Space 聊天线程写 Space 协调助手）。
func convContext(meta ThreadMeta, redact func(string) string) string {
	s := "主人与全局助手的对话"
	if meta.Kind == "space" {
		s = "主人与 Space 协调助手的对话"
	}
	if t := strings.TrimSpace(redact(meta.Title)); t != "" {
		s += "「" + t + "」"
	}
	return s
}

// threadContext 是工作线程的块头：线程「标题」· 项目 X，withStatus 时再加状态与 PR（· 已交付 · PR #12, #15）；
// 没有的部分省略。
func threadContext(meta ThreadMeta, redact func(string) string, withStatus bool) string {
	parts := []string{"线程"}
	if t := strings.TrimSpace(redact(meta.Title)); t != "" {
		parts[0] += "「" + t + "」"
	}
	project := meta.ProjectName
	if project == "" {
		project = meta.ProjectID
	}
	if p := strings.TrimSpace(redact(project)); p != "" {
		parts = append(parts, "项目 "+p)
	}
	if !withStatus {
		return strings.Join(parts, " · ")
	}
	switch {
	case meta.Shipped:
		parts = append(parts, "已交付")
	case meta.Archived:
		parts = append(parts, "已归档")
	case meta.State == "error":
		parts = append(parts, "出错")
	default:
		parts = append(parts, "进行中")
	}
	if len(meta.Pulls) > 0 {
		prs := make([]string, len(meta.Pulls))
		for i, n := range meta.Pulls {
			prs[i] = "#" + strconv.FormatInt(n, 10)
		}
		parts = append(parts, "PR "+strings.Join(prs, ", "))
	}
	return strings.Join(parts, " · ")
}
