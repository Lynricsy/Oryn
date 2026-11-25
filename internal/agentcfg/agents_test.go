package agentcfg

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAgentFrontmatter(t *testing.T) {
	cases := []struct {
		name, in     string
		wantN, wantD string
	}{
		{"常规", "---\nname: scout\ndescription: 快速侦察\ntools: [read]\n---\n# 正文\nname: other\n", "scout", "快速侦察"},
		{"去首尾空白", "---\nname: '  foo '\ndescription: \"  bar  \"\n---\n", "foo", "bar"},
		{"CRLF 与 BOM", "\ufeff---\r\nname: win\r\n---\r\nbody\r\n", "win", ""},
		{"... 结尾", "---\nname: dots\n...\nbody\n", "dots", ""},
		{"缺 name", "---\ndescription: 无名\n---\n", "", "无名"},
		{"空 frontmatter", "---\n---\nbody\n", "", ""},
		{"正文里的分隔线不影响", "---\nname: a\n---\n---\nname: b\n---\n", "a", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, d, err := ParseAgentFrontmatter([]byte(c.in))
			require.NoError(t, err)
			assert.Equal(t, c.wantN, n)
			assert.Equal(t, c.wantD, d)
		})
	}

	for name, bad := range map[string]string{
		"空内容":            "",
		"没有 frontmatter": "# 标题\nname: x\n",
		"前面有空行":          "\n---\nname: x\n---\n",
		"未闭合":            "---\nname: x\n",
		"只有开头":           "---",
		"不是映射":           "---\n- a\n- b\n---\n",
		"name 是映射":       "---\nname: { a: 1 }\n---\n",
		"非法 YAML":        "---\nname: [x\n---\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := ParseAgentFrontmatter([]byte(bad))
			require.Error(t, err)
		})
	}
}

func TestHomeAgents(t *testing.T) {
	h, err := NewHome(t.TempDir())
	require.NoError(t, err)

	defs, warns, err := h.Agents()
	require.NoError(t, err, "目录不存在也不报错")
	assert.Empty(t, defs)
	assert.Empty(t, warns)

	write := func(rel, s string) {
		p := filepath.Join(h.Dir(), filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(s), 0o600))
	}
	write("agents/a.md", "---\nname: zeta\ndescription: 最后\n---\n")
	write("agents/b.md", "---\nname: Alpha\ndescription: 最前\n---\n")
	write("agents/c.md", "---\nname: zeta\n---\n")
	write("agents/noname.md", "---\ndescription: 没有名字\n---\n")
	write("agents/plain.md", "# 没有 frontmatter\n")
	write("agents/nested/deep.md", "---\nname: nested\n---\n")
	write("agents/notes.txt", "---\nname: txt\n---\n")
	write("top.md", "---\nname: top\n---\n")

	defs, warns, err = h.Agents()
	require.NoError(t, err)
	assert.Equal(t, []AgentDef{
		{Name: "Alpha", Description: "最前", Path: "agents/b.md"},
		{Name: "zeta", Description: "最后", Path: "agents/a.md"},
	}, defs, "按 name 排序；只认 agents/ 下一层的 .md")
	require.Len(t, warns, 3)
	assert.Equal(t, "agents/c.md", warns[0].Path)
	assert.Contains(t, warns[0].Message, "重复")
	assert.Equal(t, AgentWarning{Path: "agents/noname.md", Message: "frontmatter 缺少 name"}, warns[1])
	assert.Equal(t, "agents/plain.md", warns[2].Path)
	assert.Contains(t, warns[2].Message, "frontmatter")
}

func TestParseAgentSpec(t *testing.T) {
	in := `---
name: scout
description: 快速侦察
model:
  - "@smol"
  - openai/gpt-5-mini
thinkingLevel: medium
tools: read, find, grep, glob
---

You are a scout.
---
Keep it short.
`
	spec, err := ParseAgentSpec([]byte(in))
	require.NoError(t, err)
	assert.Equal(t, AgentSpec{
		Name:          "scout",
		Description:   "快速侦察",
		Model:         []string{"@smol", "openai/gpt-5-mini"},
		ThinkingLevel: "medium",
		Tools:         []string{"read", "find", "grep", "glob"},
		SystemPrompt:  "You are a scout.\n---\nKeep it short.",
	}, spec)

	// thinkingLevel 优先于旧键 thinking；只有 thinking 时回退它
	legacy, err := ParseAgentSpec([]byte("---\nname: old\nthinking: low\nmodel: \"a/b, c/d\"\ntools: [read, bash]\n---\nBody\n"))
	require.NoError(t, err)
	assert.Equal(t, "low", legacy.ThinkingLevel)
	assert.Equal(t, []string{"a/b", "c/d"}, legacy.Model)
	assert.Equal(t, []string{"read", "bash"}, legacy.Tools)
	assert.Equal(t, "Body", legacy.SystemPrompt)

	prefer, err := ParseAgentSpec([]byte("---\nname: both\nthinking: low\nthinkingLevel: high\n---\n"))
	require.NoError(t, err)
	assert.Equal(t, "high", prefer.ThinkingLevel)

	// 区分 tools 省略（nil，不限制）与显式空数组 tools: []（非 nil 空切片，禁用全部内置工具）
	omitted, err := ParseAgentSpec([]byte("---\nname: open\n---\nAll tools.\n"))
	require.NoError(t, err)
	assert.Nil(t, omitted.Tools)

	emptyTools, err := ParseAgentSpec([]byte("---\nname: pure\ntools: []\n---\nNo tools.\n"))
	require.NoError(t, err)
	assert.NotNil(t, emptyTools.Tools)
	assert.Empty(t, emptyTools.Tools)
}

func TestHomeAgentSpecs(t *testing.T) {
	h, err := NewHome(t.TempDir())
	require.NoError(t, err)
	p := filepath.Join(h.Dir(), "agents", "writer.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("---\nname: writer\ndescription: 文档专家\nmodel: \"@slow\"\nthinkingLevel: high\ntools: [read, write, edit]\n---\nWrite clearly.\n"), 0o600))

	specs, warns, err := h.AgentSpecs()
	require.NoError(t, err)
	assert.Empty(t, warns)
	assert.Equal(t, []AgentSpec{{
		Name:          "writer",
		Description:   "文档专家",
		Model:         []string{"@slow"},
		ThinkingLevel: "high",
		Tools:         []string{"read", "write", "edit"},
		SystemPrompt:  "Write clearly.",
		Source:        "user",
		Path:          "agents/writer.md",
	}}, specs)
}
