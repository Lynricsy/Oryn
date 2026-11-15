package cfgsync

import (
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// omfgRule 是一条 /omfg 规则解析、规范化之后的要素（比较预览与读回内容用）。
type omfgRule struct {
	Name, Description, Question string
	Condition, AstCondition     []string
	Scope                       []string
	Body                        string
}

// ruleKeys 是 Core 的 omfg 生成器（`omfg-rule.ts` 的 assembleRuleMarkdown）会写出的全部 frontmatter 键。其它键一律
// 拒收：`alwaysApply`（每个会话无条件注入）、`globs`、`agents`、`interruptMode` 等都不是 /omfg 能产出的。
var ruleKeys = map[string]bool{"name": true, "description": true, "condition": true, "astCondition": true, "question": true, "scope": true}

// ruleKeyLine 是 frontmatter 的一行：生成器每个键恰好一行、顶格、值在同一行。
var ruleKeyLine = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*):(?: |$)`)

// parseRule 按 /omfg 生成器的形状严格解析规则文件；不合规返回 ok=false。
//
// 分隔规则照 Core 的 parseFrontmatter（换行规范化后以 `---` 开头，元数据止于第一个 `\n---`），在此之上更严：
// 元数据里不许有制表符与 HTML 注释（Core 会先改写它们再解析），每个非空行都必须是白名单键顶格起头的单行键值
// （Core 的 YAML 解析失败时会退回逐行 `key: value`，这样两条路径看到的键相同）；值按 YAML 解析，条件类字段只收字符串
// （Core 忽略非字符串的条件）。必须至少有一个 TTSR 触发条件：condition、astCondition 或 question，否则 Core 不把它
// 当 TTSR 规则。
func parseRule(content []byte) (omfgRule, bool) {
	s := strings.ReplaceAll(string(content), "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return omfgRule{}, false
	}
	end := strings.Index(s[3:], "\n---")
	if end < 0 {
		return omfgRule{}, false
	}
	end += 3
	meta := ""
	if end > 4 {
		meta = s[4:end]
	}
	if strings.ContainsAny(meta, "\t") || strings.Contains(meta, "<!--") {
		return omfgRule{}, false
	}
	for line := range strings.SplitSeq(meta, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		m := ruleKeyLine.FindStringSubmatch(line)
		if m == nil || !ruleKeys[m[1]] {
			return omfgRule{}, false
		}
	}
	var doc yaml.Node
	if yaml.Unmarshal([]byte(meta), &doc) != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return omfgRule{}, false
	}
	r := omfgRule{Body: strings.TrimSpace(s[end+4:])}
	seen := map[string]bool{}
	mp := doc.Content[0].Content
	for i := 0; i+1 < len(mp); i += 2 {
		k, v := mp[i], mp[i+1]
		if k.Kind != yaml.ScalarNode || !ruleKeys[k.Value] || seen[k.Value] {
			return omfgRule{}, false
		}
		seen[k.Value] = true
		var ok bool
		switch k.Value {
		case "name":
			// 生成器不加引号（`name: 123` 是合法规则名）：取原文
			r.Name, ok = strings.TrimSpace(v.Value), v.Kind == yaml.ScalarNode
		case "description":
			r.Description, ok = strScalar(v)
		case "question":
			r.Question, ok = strScalar(v)
		case "condition":
			r.Condition, ok = strList(v)
		case "astCondition":
			r.AstCondition, ok = strList(v)
		case "scope":
			r.Scope, ok = strList(v)
		}
		if !ok {
			return omfgRule{}, false
		}
	}
	if r.Name == "" || (len(r.Condition) == 0 && len(r.AstCondition) == 0 && r.Question == "") {
		return omfgRule{}, false
	}
	return r, true
}

// strScalar 取字符串标量（去首尾空白）。
func strScalar(n *yaml.Node) (string, bool) {
	if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!str" {
		return "", false
	}
	return strings.TrimSpace(n.Value), true
}

// strList 取字符串或字符串序列，按 Core 的 normalizeRuleField 去空白、去空项、去重（保序）。
func strList(n *yaml.Node) ([]string, bool) {
	items := []*yaml.Node{n}
	if n.Kind == yaml.SequenceNode {
		items = n.Content
	}
	var out []string
	for _, it := range items {
		v, ok := strScalar(it)
		if !ok {
			return nil, false
		}
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out, true
}

// equal 按要素比较两条规则（容许引号、单元素数组与标量、空白之类的格式差异）。
func (r omfgRule) equal(o omfgRule) bool {
	return r.Name == o.Name && r.Description == o.Description && r.Question == o.Question &&
		slices.Equal(r.Condition, o.Condition) && slices.Equal(r.AstCondition, o.AstCondition) &&
		slices.Equal(r.Scope, o.Scope) && r.Body == o.Body
}
