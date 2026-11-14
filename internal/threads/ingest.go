package threads

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// 无 id 行的合成 id。
const (
	EntryIDTitle   = "__title__"
	EntryIDSession = "__session__"
)

var blobRefRe = regexp.MustCompile(`blob:sha256:([0-9a-f]{64})`)

// ParsedEntry 是一行会话 JSONL 解析出的条目与派生记录。
type ParsedEntry struct {
	EntryID  string
	ParentID *string
	Type     string
	TS       *int64 // 毫秒
	Payload  []byte
	// BlobRefs 是行内引用的 blob sha256（去重）。
	BlobRefs []string
	// Usage 非 nil 时应写一行 usage_records（带 usage 的 assistant 消息，或 model_usage 条目）。
	Usage *UsageRecord
	// Tool 非 nil 时应写一行 tool_calls。
	Tool *ToolCall
	// TurnTS 非 nil 时该条目是一条 role=user 的消息（回合）：消息时间，0 时回落条目时间。
	// 是否主会话文件由调用方判断（子 agent 转录里的 user 消息不算回合）。
	TurnTS *int64
	// Title 非空时为标题槽中的标题。
	Title string
	// OffBranch 为真时该条目写在活动分支之外（omp 的 `activeLeafId` 字段，如迟到的 model_usage）：
	// 叶子应取 ActiveLeaf 而不是该条目本身；ActiveLeaf 为 nil 表示叶子被清空。
	OffBranch  bool
	ActiveLeaf *string
}

// UsageRecord 对应 usage_records 的一行（不含 thread / seq / ukey）。
type UsageRecord struct {
	TS                                 int64
	Provider, Model                    string
	// Purpose 是 model_usage 的用途（缺失记 unknown）；对话回合的 assistant 消息为空串。
	Purpose                            string
	StopReason                         string
	Input, Output, CacheRead, CacheWrt int64
	CostUSD                            float64
	DurationMs, TTFTMs                 *int64
}

// ToolCall 对应 tool_calls 的一行（不含 thread / seq）。
type ToolCall struct {
	TS      int64
	Tool    string
	IsError bool
}

// 事实行的取值上限（会话 JSONL 由 orb 写，不可信）。越界的数值记 0（时长类记 NULL），越长的字符串截断；
// 它们保证单行不会把聚合推到溢出：SUM 要约 920 万次满额请求才会超过 int64，成本要 1e302 行才会到 +Inf。
const (
	// maxFactTokens 是单次请求每类 token 的上限：比任何真实上下文窗口（百万级）高六个数量级。
	maxFactTokens = 1e12
	// maxFactCostUSD 是单次请求的成本上限（美元）。
	maxFactCostUSD = 1e6
	// maxFactMs 是单次请求时长 / 首字延迟的上限（毫秒，约 11.6 天）。
	maxFactMs = 1e9
	// maxPurposeLen 是 purpose 的字节上限；超长或含 purposeOK 之外的字符记 unknown。
	maxPurposeLen = 64
	// maxNameLen 是 provider / model / tool 的字节上限（超出按字符边界截断）。
	maxNameLen = 128
	// maxStopReasonLen 是 stop_reason 的字节上限（omp 的取值是 stop / length / toolUse / error / aborted）。
	maxStopReasonLen = 32
)

// usageJSON 是 omp `Usage` 中入库用到的字段（assistant 消息与 model_usage 条目同形）。
// token 按 float64 解析：越界的大数（如 5e18、1e30）不会让整行解析失败，而是在 record 里被判越界记 0。
type usageJSON struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Cost       *struct {
		Total float64 `json:"total"`
	} `json:"cost"`
}

// record 生成一行用量并钳制数值与字符串；clamped 为真时至少有一个数值越界（记 0）。
func (u *usageJSON) record(ts int64, provider, model, purpose, stopReason string) (r *UsageRecord, clamped bool) {
	tok := func(v float64) int64 {
		n, ok := clampTokens(v)
		clamped = clamped || !ok
		return n
	}
	r = &UsageRecord{TS: ts, Provider: truncBytes(provider, maxNameLen), Model: truncBytes(model, maxNameLen),
		Purpose: purpose, StopReason: truncBytes(stopReason, maxStopReasonLen),
		Input: tok(u.Input), Output: tok(u.Output), CacheRead: tok(u.CacheRead), CacheWrt: tok(u.CacheWrite)}
	if u.Cost != nil {
		var ok bool
		r.CostUSD, ok = clampCost(u.Cost.Total)
		clamped = clamped || !ok
	}
	return r, clamped
}

// clampTokens 把 token 数限制在 [0, maxFactTokens]：越界（含负数）记 0，ok = false。小数部分截掉。
func clampTokens(v float64) (int64, bool) {
	if !(v >= 0 && v <= maxFactTokens) {
		return 0, false
	}
	return int64(v), true
}

// clampCost 要求成本有限且在 [0, maxFactCostUSD]：否则记 0，ok = false。
func clampCost(v float64) (float64, bool) {
	if !(v >= 0 && v <= maxFactCostUSD) {
		return 0, false
	}
	return v, true
}

// clampMs 要求时长在 [0, maxFactMs]：否则为 nil。
func clampMs(v int64) *int64 {
	if v < 0 || v > maxFactMs {
		return nil
	}
	return &v
}

// truncBytes 把 s 截到至多 n 字节，不切断 UTF-8 字符。
func truncBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := n
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}

// cleanPurpose 规范 model_usage 的 purpose：空、超过 maxPurposeLen 字节或含 [A-Za-z0-9:._-] 之外的字符时记 unknown
// （omp 的实际取值如 title、find、cache-warm、judge_batch 都在这个字符集里）。
func cleanPurpose(s string) string {
	if s == "" || len(s) > maxPurposeLen {
		return "unknown"
	}
	for i := range len(s) {
		if !purposeOK(s[i]) {
			return "unknown"
		}
	}
	return s
}

func purposeOK(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ':' || c == '.' || c == '_' || c == '-'
}

type lineHead struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ParentID  *string         `json:"parentId"`
	Timestamp json.RawMessage `json:"timestamp"`
	Title     string          `json:"title"`
	// ActiveLeafID 存在（含 null）即表示该条目不在活动分支上。
	ActiveLeafID json.RawMessage `json:"activeLeafId"`
	// 以下为 model_usage 条目的顶层字段。
	Purpose    string     `json:"purpose"`
	Provider   string     `json:"provider"`
	Model      string     `json:"model"`
	StopReason string     `json:"stopReason"`
	Usage      *usageJSON `json:"usage"`
	Message    *struct {
		Role       string `json:"role"`
		Timestamp  int64  `json:"timestamp"`
		Provider   string `json:"provider"`
		Model      string `json:"model"`
		StopReason string `json:"stopReason"`
		ToolName   string `json:"toolName"`
		IsError    bool   `json:"isError"`
		// duration / ttft 在 omp 中是毫秒浮点数
		Duration *float64   `json:"duration"`
		TTFT     *float64   `json:"ttft"`
		Usage    *usageJSON `json:"usage"`
	} `json:"message"`
}

// ParseLine 解析一行（不含结尾换行）。
func ParseLine(line string) (ParsedEntry, error) {
	line = strings.TrimRight(line, "\r\n")
	var h lineHead
	if err := json.Unmarshal([]byte(line), &h); err != nil {
		return ParsedEntry{}, err
	}
	if h.Type == "" {
		return ParsedEntry{}, errors.New("缺少 type")
	}
	p := ParsedEntry{EntryID: h.ID, ParentID: h.ParentID, Type: h.Type, Payload: []byte(line)}
	if p.EntryID == "" {
		switch h.Type {
		case "title":
			p.EntryID = EntryIDTitle
		case "session":
			p.EntryID = EntryIDSession
		default:
			p.EntryID = "__" + h.Type + "__"
		}
	}
	if h.Type == "session" {
		// 会话头的 id 是会话 UUID，不是条目 id
		p.EntryID = EntryIDSession
		p.ParentID = nil
	}
	if h.Type == "title" {
		p.Title = h.Title
	}
	if len(h.ActiveLeafID) > 0 {
		p.OffBranch = true
		var leaf *string
		if json.Unmarshal(h.ActiveLeafID, &leaf) == nil {
			p.ActiveLeaf = leaf
		}
	}
	p.TS = parseTS(h.Timestamp)
	seen := map[string]bool{}
	for _, m := range blobRefRe.FindAllStringSubmatch(line, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			p.BlobRefs = append(p.BlobRefs, m[1])
		}
	}
	if h.Type == "message" && h.Message != nil {
		m := h.Message
		ts := m.Timestamp
		if ts == 0 && p.TS != nil {
			ts = *p.TS
		}
		switch m.Role {
		case "assistant":
			if m.Usage != nil {
				u, clamped := m.Usage.record(ts, m.Provider, m.Model, "", m.StopReason)
				u.DurationMs, u.TTFTMs = roundMs(m.Duration), roundMs(m.TTFT)
				warnClamped(p.EntryID, clamped || (m.Duration != nil && u.DurationMs == nil) || (m.TTFT != nil && u.TTFTMs == nil))
				p.Usage = u
			}
		case "toolResult":
			p.Tool = &ToolCall{TS: ts, Tool: truncBytes(m.ToolName, maxNameLen), IsError: m.IsError}
		case "user":
			p.TurnTS = &ts
		}
	}
	if h.Type == "model_usage" && h.Usage != nil {
		var ts int64
		if p.TS != nil {
			ts = *p.TS
		}
		u, clamped := h.Usage.record(ts, h.Provider, h.Model, cleanPurpose(h.Purpose), h.StopReason)
		warnClamped(p.EntryID, clamped)
		p.Usage = u
	}
	return p, nil
}

// warnClamped 在一条条目的用量数值越界（已记 0 / NULL）时打一条警告。
func warnClamped(entryID string, clamped bool) {
	if clamped {
		slog.Warn("threads: 会话行的用量数值越界，已按 0 记", "entry", truncBytes(entryID, maxFactKeyIDLen))
	}
}

// roundMs 把 omp 的毫秒浮点数取整：NaN / ±Inf / 负数 / 超过 maxFactMs 时为 nil（先判再转 int64，
// 避免浮点越界转换的结果由实现决定）。
func roundMs(v *float64) *int64 {
	if v == nil || !(*v >= 0 && *v <= maxFactMs) {
		return nil
	}
	return clampMs(int64(math.Round(*v)))
}

// parseTS 接受 ISO 8601 字符串或毫秒数。
func parseTS(raw json.RawMessage) *int64 {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return &n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			ms := t.UnixMilli()
			return &ms
		}
	}
	return nil
}

// IsSubagentFile 判断条目文件是否为子 agent 转录（位于会话产物子目录内）。
func IsSubagentFile(file string) bool { return strings.Contains(file, "/") }
