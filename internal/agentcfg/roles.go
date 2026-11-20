package agentcfg

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// ConfigFile 是 omp-home 里的 omp 主配置文件名。
const ConfigFile = "config.yml"

// DefaultCycleOrder 是 omp `cycleOrder` 的缺省值。
var DefaultCycleOrder = []string{"smol", "default", "slow"}

// BuiltinRoles 是 omp 自带的 role（未在 config.yml 中出现也存在）；与前端 web/src/lib/model-roles.ts 的
// BUILTIN_ROLE_NAMES 保持一致。
var BuiltinRoles = []string{"default", "smol", "slow", "vision", "plan", "commit", "tiny", "memory", "task", "advisor", "widget"}

// CycleRole 是 cycleOrder 里的一个 role。
type CycleRole struct {
	Role string
	// Name 是 `modelTags.<role>.name`；没有为空。
	Name string
	// Selector 是 `modelRoles.<role>` 的原始值（数组以 ", " 连接）；没有配置为空。
	// 仅供展示，不参与任何判定。
	Selector string
}

// KnownRole 是 Oryn 认识的一个 role：cycleOrder、modelRoles 的键与 BuiltinRoles 的并集中的一项。
type KnownRole struct {
	Role string
	// Name 是 `modelTags.<role>.name`；没有为空。
	Name string
	// Selector 同 CycleRole.Selector。
	Selector string
	// InCycle 表示该 role 在 cycleOrder 里。
	InCycle bool
}

// ParseCycleRoles 从 config.yml 的内容解析出 cycleOrder 里的 role（按其顺序）。
// 内容为空按缺省配置；YAML 不合法或顶层不是映射返回错误。
func ParseCycleRoles(config []byte) ([]CycleRole, error) {
	root, err := parseConfigRoot(config)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return defaultCycleRoles(), nil
	}
	order := cycleOrder(mapGetDeref(root, "cycleOrder"))
	roles := mapGetDeref(root, "modelRoles")
	tags := mapGetDeref(root, "modelTags")
	out := make([]CycleRole, len(order))
	for i, role := range order {
		out[i] = CycleRole{Role: role, Name: tagName(tags, role), Selector: selector(mapGetDeref(roles, role))}
	}
	return out, nil
}

// ParseKnownRoles 从 config.yml 的内容解析出全部已知 role：先是 cycleOrder（按其顺序，InCycle），
// 再是 modelRoles 里其余的键（按字典序），最后是 BuiltinRoles 里其余的项（按其顺序）。大小写原样区分。
// 内容为空按缺省配置；YAML 不合法或顶层不是映射返回错误。
func ParseKnownRoles(config []byte) ([]KnownRole, error) {
	root, err := parseConfigRoot(config)
	if err != nil {
		return nil, err
	}
	return knownRoles(root), nil
}

// knownRoles 按 ParseKnownRoles 的规则合并；root 为 nil 按缺省配置。
func knownRoles(root *yaml.Node) []KnownRole {
	roles := mapGetDeref(root, "modelRoles")
	tags := mapGetDeref(root, "modelTags")
	order := cycleOrder(mapGetDeref(root, "cycleOrder"))
	out := make([]KnownRole, 0, len(order)+len(BuiltinRoles))
	seen := make(map[string]bool, cap(out))
	add := func(role string, inCycle bool) {
		if seen[role] {
			return
		}
		seen[role] = true
		out = append(out, KnownRole{Role: role, Name: tagName(tags, role), Selector: selector(mapGetDeref(roles, role)), InCycle: inCycle})
	}
	for _, r := range order {
		add(r, true)
	}
	var extra []string
	if roles != nil && roles.Kind == yaml.MappingNode {
		for _, kv := range mapPairs(roles) {
			if k, ok := str(kv[0]); ok && k != "" && !seen[k] {
				extra = append(extra, k)
			}
		}
	}
	slices.Sort(extra)
	for _, r := range extra {
		add(r, false)
	}
	for _, r := range BuiltinRoles {
		add(r, false)
	}
	return out
}

// parseConfigRoot 解析 config.yml 的顶层映射；内容为空或为 null 返回 nil。
func parseConfigRoot(config []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(config, &doc); err != nil {
		return nil, fmt.Errorf("%s 不是合法的 YAML: %w", ConfigFile, err)
	}
	var root *yaml.Node
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		root = deref(doc.Content[0])
	}
	if root == nil || isNull(root) {
		return nil, nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s 的顶层须是映射", ConfigFile)
	}
	return root, nil
}

// readConfig 读 omp-home 的 config.yml；文件不存在返回 nil, nil。
func (h *Home) readConfig() ([]byte, error) {
	b, _, err := h.Get(ConfigFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 %s: %w", ConfigFile, err)
	}
	return b, nil
}

// CycleRoles 读 omp-home 的 config.yml 并解析；文件不存在按缺省配置。
// 读取或解析失败时返回缺省 cycleOrder（Name / Selector 为空）与该错误。
func (h *Home) CycleRoles() ([]CycleRole, error) {
	b, err := h.readConfig()
	if err != nil {
		return defaultCycleRoles(), err
	}
	roles, err := ParseCycleRoles(b)
	if err != nil {
		return defaultCycleRoles(), err
	}
	return roles, nil
}

// KnownRoles 读 omp-home 的 config.yml 并按 ParseKnownRoles 解析；文件不存在按缺省配置。
// 读取或解析失败时返回缺省配置下的已知 role（缺省 cycleOrder 加 BuiltinRoles，Name / Selector 为空）与该错误。
func (h *Home) KnownRoles() (roles []KnownRole, configErr error) {
	b, err := h.readConfig()
	if err == nil {
		roles, err = ParseKnownRoles(b)
	}
	if err != nil {
		return knownRoles(nil), err
	}
	return roles, nil
}

// fallbackThinkingLevel 是 omp `defaultThinkingLevel` 的缺省值。
const fallbackThinkingLevel = "high"

// thinkingLevels 是 `defaultThinkingLevel` 的合法取值（omp THINKING_EFFORTS、off 与 auto）。
var thinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max", "auto"}

// ParseDefaultThinkingLevel 从 config.yml 的内容取 `defaultThinkingLevel`；没写、不是合法取值或 YAML 不合法时
// 按 omp 的缺省值（high）——与 omp 读取设置时的回退一致。
func ParseDefaultThinkingLevel(config []byte) string {
	root, err := parseConfigRoot(config)
	if err != nil {
		return fallbackThinkingLevel
	}
	if v, ok := str(mapGetDeref(root, "defaultThinkingLevel")); ok && slices.Contains(thinkingLevels, v) {
		return v
	}
	return fallbackThinkingLevel
}

// DefaultThinkingLevel 读 omp-home 的 config.yml 并按 ParseDefaultThinkingLevel 取值；读取失败按缺省值。
func (h *Home) DefaultThinkingLevel() string {
	b, err := h.readConfig()
	if err != nil {
		return fallbackThinkingLevel
	}
	return ParseDefaultThinkingLevel(b)
}

func defaultCycleRoles() []CycleRole {
	out := make([]CycleRole, len(DefaultCycleOrder))
	for i, r := range DefaultCycleOrder {
		out[i] = CycleRole{Role: r}
	}
	return out
}

// cycleOrder 取字符串项（去首尾空白、丢空项、去重保序）；不是数组或结果为空 → 缺省。
func cycleOrder(n *yaml.Node) []string {
	var out []string
	if n != nil && n.Kind == yaml.SequenceNode {
		seen := make(map[string]bool, len(n.Content))
		for _, it := range n.Content {
			v, ok := str(it)
			if !ok || v == "" || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return DefaultCycleOrder // 只读：调用方据此另建 []CycleRole
	}
	return out
}

func tagName(tags *yaml.Node, role string) string {
	tag := mapGetDeref(tags, role)
	if tag == nil || tag.Kind != yaml.MappingNode {
		return ""
	}
	v, _ := str(mapGetDeref(tag, "name"))
	return v
}

// selector 返回 modelRoles.<role> 的原始值：字符串去首尾空白；数组逐项去空白、丢空项后以 ", " 连接。
func selector(n *yaml.Node) string {
	if n == nil {
		return ""
	}
	if v, ok := str(n); ok {
		return v
	}
	if n.Kind != yaml.SequenceNode {
		return ""
	}
	parts := make([]string, 0, len(n.Content))
	for _, it := range n.Content {
		if v, ok := str(it); ok && v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, ", ")
}

// str 返回字符串标量去首尾空白后的值；非字符串（数字、布尔、null、映射、数组）返回 false。
func str(n *yaml.Node) (string, bool) {
	n = deref(n)
	if n == nil || n.Kind != yaml.ScalarNode || n.ShortTag() != "!!str" {
		return "", false
	}
	return strings.TrimSpace(n.Value), true
}

func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null" }

// deref 解开 YAML 别名（*anchor）。
func deref(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// mapGetDeref 在映射里取键（nil 或非映射返回 nil），并解开别名。
func mapGetDeref(m *yaml.Node, key string) *yaml.Node {
	m = deref(m)
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	return deref(mapGet(m, key))
}
