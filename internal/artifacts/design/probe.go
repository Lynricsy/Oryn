package design

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// 探针结果的解析（plan-m29 C8）：控制面的 check_artifact 与 orbd 的 design.check 共用。页面是不可信的，结果节点
// 可以被页面伪造或撑大，所以解析时按下列上限清洗。
const (
	// ProbeResultID 是探针结果节点的 id（web/sandbox/probe/main.ts 的 RESULT_ID）。
	ProbeResultID = "oryn-probe"
	// ProbeQuery 是让页面加载探针的查询参数（值为 1）。
	ProbeQuery = "__oryn_probe"

	MaxProbeIssues    = 14 * 3 // 14 条规则，每条至多 3 处
	MaxIssueTextRunes = 200
	maxIssueLocBytes  = 512
	maxIssueDetail    = 200
)

var probeRuleRe = regexp.MustCompile(`^[a-z][a-z-]{0,39}$`)

// probeResult 是探针写进 DOM 的结果（web/sandbox/probe/main.ts 的 ProbeResult）。
type probeResult struct {
	V      int `json:"v"`
	Issues []struct {
		Rule   string          `json:"rule"`
		Loc    *string         `json:"loc"`
		Text   string          `json:"text"`
		Detail json.RawMessage `json:"detail"`
	} `json:"issues"`
}

// ParseProbe 从 --dump-dom 的输出里取最后一个 `<script id="oryn-probe" type="application/json">` 的内容并清洗：
// 规则名必须是小写字母与 -，文本与 loc 截短，detail 只留不太长的数字或字符串，总数至多 MaxProbeIssues。
// 返回的 Issue 不带 Frame（由调用方填）。没有结果节点（探针没加载或没跑完）或内容不合法时回错误。
func ParseProbe(dom []byte) ([]Issue, error) {
	var raw []byte
	found := false
	z := html.NewTokenizer(bytes.NewReader(dom))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt != html.StartTagToken {
			continue
		}
		name, hasAttr := z.TagName()
		if string(name) != "script" || !hasAttr {
			continue
		}
		isProbe := false
		for more := true; more; {
			var k, v []byte
			k, v, more = z.TagAttr()
			if string(k) == "id" && string(v) == ProbeResultID {
				isProbe = true
			}
		}
		if !isProbe {
			continue
		}
		found = true
		raw = nil
		if z.Next() == html.TextToken {
			raw = slices.Clone(z.Raw())
		}
	}
	if !found {
		return nil, errors.New("页面里没有探针结果（探针脚本没有加载或没有在时限内跑完）")
	}
	var pr probeResult
	if err := json.Unmarshal(raw, &pr); err != nil || pr.V != 1 {
		return nil, errors.New("探针结果不合法")
	}
	out := make([]Issue, 0, min(len(pr.Issues), MaxProbeIssues))
	for _, is := range pr.Issues {
		if len(out) == MaxProbeIssues {
			break
		}
		if !probeRuleRe.MatchString(is.Rule) {
			continue
		}
		d := Issue{Rule: is.Rule, Text: clipRunes(is.Text, MaxIssueTextRunes)}
		if is.Loc != nil && len(*is.Loc) <= maxIssueLocBytes && utf8.ValidString(*is.Loc) {
			d.Loc = *is.Loc
		}
		if det := bytes.TrimSpace(is.Detail); len(det) > 0 && len(det) <= maxIssueDetail && (det[0] == '"' || det[0] == '-' || (det[0] >= '0' && det[0] <= '9')) && json.Valid(det) {
			d.Detail = json.RawMessage(det)
		}
		out = append(out, d)
	}
	return out, nil
}

// clipRunes 把 s 截到至多 n 个码点（截掉时末尾换成 …）。
func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
