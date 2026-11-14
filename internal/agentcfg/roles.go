package agentcfg

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"gopkg.in/yaml.v3"
)

// ConfigFile 是 omp-home 里的 omp 主配置文件名。
const ConfigFile = "config.yml"

// DefaultCycleOrder 是 omp `cycleOrder` 的缺省值。
var DefaultCycleOrder = []string{"smol", "default", "slow"}

// CycleRole 是 cycleOrder 里的一个 role。
type CycleRole struct {
	Role string
	// Name 是 `modelTags.<role>.name`；没有为空。
	Name string
	// Selector 是 `modelRoles.<role>` 的原始值（数组以 ", " 连接）；没有配置为空。
	// 仅供展示，不参与任何判定。
	Selector string
}

// ParseCycleRoles 从 config.yml 的内容解析出 cycleOrder 里的 role（按其顺序）。
// 内容为空按缺省配置；YAML 不合法或顶层不是映射返回错误。
func ParseCycleRoles(config []byte) ([]CycleRole, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(config, &doc); err != nil {
		return nil, fmt.Errorf("%s 不是合法的 YAML: %w", ConfigFile, err)
	}
	var root *yaml.Node
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		root = deref(doc.Content[0])
	}
	if root == nil || isNull(root) {
		return defaultCycleRoles(), nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s 的顶层须是映射", ConfigFile)
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

// CycleRoles 读 omp-home 的 config.yml 并解析；文件不存在按缺省配置。
// 读取或解析失败时返回缺省 cycleOrder（Name / Selector 为空）与该错误。
func (h *Home) CycleRoles() ([]CycleRole, error) {
	b, _, err := h.Get(ConfigFile)
	if errors.Is(err, fs.ErrNotExist) {
		return defaultCycleRoles(), nil
	}
	if err != nil {
		return defaultCycleRoles(), fmt.Errorf("读取 %s: %w", ConfigFile, err)
	}
	roles, err := ParseCycleRoles(b)
	if err != nil {
		return defaultCycleRoles(), err
	}
	return roles, nil
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
