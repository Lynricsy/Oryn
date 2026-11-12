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
	// Usage 非 nil 时应写一行 usage_records。
	Usage *UsageRecord
	// Tool 非 nil 时应写一行 tool_calls。
	Tool *ToolCall
	// Title 非空时为标题槽中的标题。
	Title string
}

// UsageRecord 对应 usage_records 的一行（不含 thread / seq）。
type UsageRecord struct {
	TS                                 int64
	Provider, Model                    string
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

type lineHead struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ParentID  *string         `json:"parentId"`
	Timestamp json.RawMessage `json:"timestamp"`
	Title     string          `json:"title"`
	Message   *struct {
		Role      string `json:"role"`
		Timestamp int64  `json:"timestamp"`
		Provider  string `json:"provider"`
		Model     string `json:"model"`
		ToolName  string `json:"toolName"`
		IsError   bool   `json:"isError"`
		// duration / ttft 在 omp 中是毫秒浮点数
		Duration *float64 `json:"duration"`
		TTFT     *float64 `json:"ttft"`
		Usage    *struct {
			Input      int64 `json:"input"`
			Output     int64 `json:"output"`
			CacheRead  int64 `json:"cacheRead"`
			CacheWrite int64 `json:"cacheWrite"`
			Cost       *struct {
				Total float64 `json:"total"`
			} `json:"cost"`
		} `json:"usage"`
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
				u := &UsageRecord{TS: ts, Provider: m.Provider, Model: m.Model,
					Input: m.Usage.Input, Output: m.Usage.Output, CacheRead: m.Usage.CacheRead, CacheWrt: m.Usage.CacheWrite,
					DurationMs: roundMs(m.Duration), TTFTMs: roundMs(m.TTFT)}
				if m.Usage.Cost != nil {
					u.CostUSD = m.Usage.Cost.Total
				}
				p.Usage = u
			}
		case "toolResult":
			p.Tool = &ToolCall{TS: ts, Tool: m.ToolName, IsError: m.IsError}
		}
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
