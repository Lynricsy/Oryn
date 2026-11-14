package threads

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"
	"time"
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

// usageJSON 是 omp `Usage` 中入库用到的字段（assistant 消息与 model_usage 条目同形）。
type usageJSON struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Cost       *struct {
		Total float64 `json:"total"`
	} `json:"cost"`
}

func (u *usageJSON) record(ts int64, provider, model, purpose, stopReason string) *UsageRecord {
	r := &UsageRecord{TS: ts, Provider: provider, Model: model, Purpose: purpose, StopReason: stopReason,
		Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrt: u.CacheWrite}
	if u.Cost != nil {
		r.CostUSD = u.Cost.Total
	}
	return r
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
				u := m.Usage.record(ts, m.Provider, m.Model, "", m.StopReason)
				u.DurationMs, u.TTFTMs = roundMs(m.Duration), roundMs(m.TTFT)
				p.Usage = u
			}
		case "toolResult":
			p.Tool = &ToolCall{TS: ts, Tool: m.ToolName, IsError: m.IsError}
		case "user":
			p.TurnTS = &ts
		}
	}
	if h.Type == "model_usage" && h.Usage != nil {
		var ts int64
		if p.TS != nil {
			ts = *p.TS
		}
		purpose := h.Purpose
		if purpose == "" {
			purpose = "unknown"
		}
		p.Usage = h.Usage.record(ts, h.Provider, h.Model, purpose, h.StopReason)
	}
	return p, nil
}

func roundMs(v *float64) *int64 {
	if v == nil {
		return nil
	}
	n := int64(math.Round(*v))
	return &n
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
