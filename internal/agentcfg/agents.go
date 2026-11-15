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

// BundledAgents 是 omp 自带的子代理（不需要 `agents/*.md` 也存在）。
var BundledAgents = []string{"task", "sonic", "scout", "reviewer", "security-reviewer", "advisor"}

// agentsGlob 匹配 omp-home 里的用户子代理定义（只认 agents/ 下一层，不含子目录）。
const agentsGlob = "agents/*.md"

// AgentDef 是一个用户子代理定义（`agents/*.md` 的 frontmatter）。
type AgentDef struct {
	Name        string
	Description string
	// Path 是 omp-home 内的相对路径（以 / 分隔）。
	Path string
}

// AgentWarning 是读取子代理定义时被跳过的文件及原因。
type AgentWarning struct {
	Path    string
	Message string
}

// ParseAgentFrontmatter 解析 Markdown 开头的 `---` YAML frontmatter，取 name 与 description（去首尾空白）。
// 只读开头的 frontmatter，不看正文。没有 frontmatter、frontmatter 未闭合或不是合法的 YAML 映射时返回错误；
// 缺 name 时 name 为空、不报错（由调用方决定如何处理）。
func ParseAgentFrontmatter(content []byte) (name, description string, err error) {
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	line, rest, _ := bytes.Cut(content, []byte("\n"))
	if !isFence(line, false) {
		return "", "", errors.New("缺少 frontmatter（文件须以 --- 开头）")
	}
	start := len(content) - len(rest)
	var block []byte
	closed := false
	for len(rest) > 0 {
		end := len(content) - len(rest)
		line, rest, _ = bytes.Cut(rest, []byte("\n"))
		if isFence(line, true) {
			block, closed = content[start:end], true
			break
		}
	}
	if !closed {
		return "", "", errors.New("frontmatter 未闭合（缺少结尾的 ---）")
	}
	var fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal(block, &fm); err != nil {
		return "", "", fmt.Errorf("frontmatter 不是合法的 YAML 映射: %w", err)
	}
	return strings.TrimSpace(fm.Name), strings.TrimSpace(fm.Description), nil
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
	files, err := h.List()
	if err != nil {
		return nil, nil, fmt.Errorf("列出 agents: %w", err)
	}
	defs := []AgentDef{}
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
		name, desc, err := ParseAgentFrontmatter(b)
		switch {
		case err != nil:
			warns = append(warns, AgentWarning{Path: f.Path, Message: err.Error()})
		case name == "":
			warns = append(warns, AgentWarning{Path: f.Path, Message: "frontmatter 缺少 name"})
		case seen[name] != "":
			warns = append(warns, AgentWarning{Path: f.Path, Message: fmt.Sprintf("name %q 与 %s 重复，已忽略", name, seen[name])})
		default:
			seen[name] = f.Path
			defs = append(defs, AgentDef{Name: name, Description: desc, Path: f.Path})
		}
	}
	slices.SortFunc(defs, func(a, b AgentDef) int { return strings.Compare(a.Name, b.Name) })
	return defs, warns, nil
}
