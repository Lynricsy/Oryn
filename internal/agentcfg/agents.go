package agentcfg

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// BundledAgents 是 omp 自带的子代理（不需要 `agents/*.md` 也存在；含 advisor 运行时，用于头像列表）。
var BundledAgents = []string{"task", "sonic", "scout", "reviewer", "security-reviewer", "advisor"}

// BundledTaskAgents 是 omp 自带、可作为独立任务角色使用的内置子代理（不含 advisor 运行时）。
var BundledTaskAgents = []string{"reviewer", "scout", "security-reviewer", "sonic", "task"}

// agentsGlob 匹配 omp-home 里的用户子代理定义（只认 agents/ 下一层，不含子目录）。
const agentsGlob = "agents/*.md"

// AgentDef 是一个用户子代理定义（`agents/*.md` 的 frontmatter 摘要）。
type AgentDef struct {
	Name        string
	Description string
	// Path 是 omp-home 内的相对路径（以 / 分隔）。
	Path string
}

// AgentSpec 是一个子代理（subagent）的完整配置：frontmatter 字段与正文系统提示词。
type AgentSpec struct {
	Name          string
	Description   string
	Model         []string
	ThinkingLevel string
	Tools         []string
	SystemPrompt  string
	// Source 为 "bundled" 或 "user"。
	Source string
	// Path 是 omp-home 内的相对路径（仅 Source == "user" 时非空）。
	Path string
}

// AgentWarning 是读取子代理定义时被跳过的文件及原因。
type AgentWarning struct {
	Path    string
	Message string
}

// DefaultBundledTaskAgents 返回内置子代理的兜底元数据副本（无真实 omp 二进制解包时使用）。
func DefaultBundledTaskAgents() []AgentSpec {
	return []AgentSpec{
		{
			Name:        "reviewer",
			Description: "Code review specialist for quality/security analysis",
			Model:       []string{"@slow"},
			Tools:       []string{"read", "find", "grep", "glob", "bash", "lsp", "web_search", "ast_grep"},
			Source:      "bundled",
		},
		{
			Name:          "scout",
			Description:   "Fast scout for exploratory codebase research, rapid code analysis, and broad pattern searches; returns compressed context for handoff.",
			Model:         []string{"@smol"},
			ThinkingLevel: "medium",
			Tools:         []string{"read", "find", "grep", "glob", "ast_grep", "web_search"},
			Source:        "bundled",
		},
		{
			Name:        "security-reviewer",
			Description: "Read-only security specialist for evidence-backed repository vulnerability discovery",
			Tools:       []string{"read", "find", "grep", "glob", "lsp", "ast_grep"},
			Source:      "bundled",
		},
		{
			Name:          "sonic",
			Description:   "Low-reasoning agent for strictly mechanical updates or data collection only",
			Model:         []string{"@smol"},
			ThinkingLevel: "medium",
			Source:        "bundled",
		},
		{
			Name:          "task",
			Description:   "General-purpose subagent with full capabilities for delegated multi-step tasks",
			Model:         []string{"@task"},
			ThinkingLevel: "auto",
			Source:        "bundled",
		},
	}
}

// ParseAgentFrontmatter 解析 Markdown 开头的 `---` YAML frontmatter，取 name 与 description（去首尾空白）。
// 只读开头的 frontmatter，不看正文。没有 frontmatter、frontmatter 未闭合或不是合法的 YAML 映射时返回错误；
// 缺 name 时 name 为空、不报错（由调用方决定如何处理）。
func ParseAgentFrontmatter(content []byte) (name, description string, err error) {
	spec, err := ParseAgentSpec(content)
	if err != nil {
		return "", "", err
	}
	return spec.Name, spec.Description, nil
}

// ParseAgentSpec 解析子代理 Markdown 文件：提取 frontmatter（name / description / model / thinkingLevel / tools）
// 与闭合分隔线之后的系统提示词正文。没有 frontmatter、未闭合或不是合法 YAML 映射时返回错误。
func ParseAgentSpec(content []byte) (AgentSpec, error) {
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	line, rest, _ := bytes.Cut(content, []byte("\n"))
	if !isFence(line, false) {
		return AgentSpec{}, errors.New("缺少 frontmatter（文件须以 --- 开头）")
	}
	start := len(content) - len(rest)
	var block, body []byte
	closed := false
	for len(rest) > 0 {
		end := len(content) - len(rest)
		line, rest, _ = bytes.Cut(rest, []byte("\n"))
		if isFence(line, true) {
			block, body, closed = content[start:end], rest, true
			break
		}
	}
	if !closed {
		return AgentSpec{}, errors.New("frontmatter 未闭合（缺少结尾的 ---）")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(block, &doc); err != nil {
		return AgentSpec{}, fmt.Errorf("frontmatter 不是合法的 YAML 映射: %w", err)
	}
	var root *yaml.Node
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		root = deref(doc.Content[0])
	}
	spec := AgentSpec{SystemPrompt: strings.TrimSpace(string(body))}
	if root == nil || isNull(root) {
		return spec, nil
	}
	if root.Kind != yaml.MappingNode {
		return AgentSpec{}, errors.New("frontmatter 不是合法的 YAML 映射")
	}
	if n := mapGetDeref(root, "name"); n != nil && !isNull(n) {
		if n.Kind != yaml.ScalarNode {
			return AgentSpec{}, errors.New("frontmatter 不是合法的 YAML 映射: name 须为字符串")
		}
		spec.Name = strings.TrimSpace(n.Value)
	}
	if d := mapGetDeref(root, "description"); d != nil && !isNull(d) {
		if d.Kind != yaml.ScalarNode {
			return AgentSpec{}, errors.New("frontmatter 不是合法的 YAML 映射: description 须为字符串")
		}
		spec.Description = strings.TrimSpace(d.Value)
	}
	spec.Model = yamlStringList(mapGetDeref(root, "model"))
	spec.Tools = yamlStringList(mapGetDeref(root, "tools"))
	if v, ok := str(mapGetDeref(root, "thinkingLevel")); ok && v != "" {
		spec.ThinkingLevel = v
	} else if v, ok := str(mapGetDeref(root, "thinking")); ok && v != "" {
		spec.ThinkingLevel = v
	}
	return spec, nil
}

// yamlStringList 把 YAML 字符串（按逗号拆分）或字符串数组解析成去空白、非空的列表。
func yamlStringList(n *yaml.Node) []string {
	n = deref(n)
	if n == nil || isNull(n) {
		return nil
	}
	var out []string
	if v, ok := str(n); ok {
		for part := range strings.SplitSeq(v, ",") {
			if s := strings.TrimSpace(part); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	if n.Kind != yaml.SequenceNode {
		return nil
	}
	for _, it := range n.Content {
		if v, ok := str(it); ok && v != "" {
			out = append(out, v)
		}
	}
	return out
}

// isFence 判断一行是否为 frontmatter 分隔线（容忍行尾空白与 \r）；closing 时 `...` 也算结尾。
func isFence(line []byte, closing bool) bool {
	t := bytes.TrimRight(line, " \t\r")
	return string(t) == "---" || closing && string(t) == "..."
}

// Agents 读取 omp-home 里 `agents/*.md`（不含子目录）的 frontmatter，按 name 排序返回。
// 读取或解析失败、缺 name 的文件跳过并作为警告返回；同名的定义只保留路径在前的一个，其余作为警告。
// 仅列目录失败时返回错误。
func (h *Home) Agents() ([]AgentDef, []AgentWarning, error) {
	specs, warns, err := h.AgentSpecs()
	if err != nil {
		return nil, nil, err
	}
	defs := make([]AgentDef, len(specs))
	for i, s := range specs {
		defs[i] = AgentDef{Name: s.Name, Description: s.Description, Path: s.Path}
	}
	return defs, warns, nil
}

// AgentSpecs 读取 omp-home 里 `agents/*.md`（不含子目录）的完整子代理定义，按 name 排序返回。
// 读取或解析失败、缺 name 的文件跳过并作为警告返回；同名的定义只保留路径在前的一个，其余作为警告。
// 仅列目录失败时返回错误。
func (h *Home) AgentSpecs() ([]AgentSpec, []AgentWarning, error) {
	files, err := h.List()
	if err != nil {
		return nil, nil, fmt.Errorf("列出 agents: %w", err)
	}
	specs := []AgentSpec{}
	var warns []AgentWarning
	seen := map[string]string{}
	for _, f := range files { // List 已按路径排序
		if ok, _ := path.Match(agentsGlob, f.Path); !ok {
			continue
		}
		b, _, err := h.Get(f.Path)
		if errors.Is(err, fs.ErrNotExist) {
			continue // 列出后被删
		}
		if err != nil {
			warns = append(warns, AgentWarning{Path: f.Path, Message: "读取失败: " + err.Error()})
			continue
		}
		spec, err := ParseAgentSpec(b)
		switch {
		case err != nil:
			warns = append(warns, AgentWarning{Path: f.Path, Message: err.Error()})
		case spec.Name == "":
			warns = append(warns, AgentWarning{Path: f.Path, Message: "frontmatter 缺少 name"})
		case seen[spec.Name] != "":
			warns = append(warns, AgentWarning{Path: f.Path, Message: fmt.Sprintf("name %q 与 %s 重复，已忽略", spec.Name, seen[spec.Name])})
		default:
			seen[spec.Name] = f.Path
			spec.Source = "user"
			spec.Path = f.Path
			specs = append(specs, spec)
		}
	}
	slices.SortFunc(specs, func(a, b AgentSpec) int { return strings.Compare(a.Name, b.Name) })
	return specs, warns, nil
}
