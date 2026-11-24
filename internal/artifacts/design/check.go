package design

import "encoding/json"

// 设计检查状态（plan-m29 C8）。stale 不落库：读出时由 Fingerprint 与当前 SourceHash 比较得出。
const (
	CheckClean      = "clean"
	CheckIssues     = "issues"
	CheckStale      = "stale"
	CheckUnverified = "unverified"
)

// Check 是 artifact_versions.check_json 的形状：封存时（seal_design 的 check）或控制面探针（check_artifact）写入。
// Fingerprint 是检查时源码的 SourceHash；REST 不暴露它。
type Check struct {
	Status      string  `json:"status"`
	Issues      []Issue `json:"issues"`
	Fingerprint string  `json:"fingerprint,omitempty"`
}

// Issue 是一条探针发现（与 apigen.ArtifactCheckIssue 同形）：Rule 必有，Frame 是画框 id，
// Loc 是 `src/frames/x.tsx:行:列`，Text 是相关可见文本，Detail 是规则自带的数据。
type Issue struct {
	Rule   string          `json:"rule"`
	Frame  string          `json:"frame,omitempty"`
	Loc    string          `json:"loc,omitempty"`
	Text   string          `json:"text,omitempty"`
	Detail json.RawMessage `json:"detail,omitempty"`
}

// Effective 返回对外呈现的状态：已检查过但 Fingerprint 与当前源码指纹不同时为 stale。
func (c Check) Effective(currentHash string) string {
	if c.Status != CheckUnverified && c.Fingerprint != "" && currentHash != "" && c.Fingerprint != currentHash {
		return CheckStale
	}
	return c.Status
}
