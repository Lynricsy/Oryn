package agentcfg

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 主人的真实形状：default 是别名、五个自定义 role 各一个带思考级别的选择器、部分带显示名。
const ownerConfig = `
cycleOrder: [Cyrene, Mare, Hyacinthia, Nahida, Kafka]
modelRoles:
  default: "@Cyrene"
  Cyrene: anthropic/claude-opus-5-5:high
  Mare: openai/gpt-6.1:high
  Hyacinthia: google/gemini-4-pro:high
  Nahida: deepseek/deepseek-v4:high
  Kafka: openrouter/qwen/qwen3-max:exacto
modelTags:
  Cyrene: { name: 昔涟 }
  Mare: { name: "  Mare the Fair  " }
  Nahida: { name: "" }
  Kafka: { name: 42 }
`

func TestParseCycleRoles(t *testing.T) {
	defaults := []CycleRole{{Role: "smol"}, {Role: "default"}, {Role: "slow"}}
	cases := []struct {
		name string
		in   string
		want []CycleRole
	}{
		{"空内容", "", defaults},
		{"只有注释", "# nothing\n", defaults},
		{"null 文档", "~\n", defaults},
		{"无相关键", "theme: dark\nmodelRoles: { default: a/b }\n", []CycleRole{{Role: "smol"}, {Role: "default", Selector: "a/b"}, {Role: "slow"}}},
		{"主人的形状", ownerConfig, []CycleRole{
			{Role: "Cyrene", Name: "昔涟", Selector: "anthropic/claude-opus-5-5:high"},
			{Role: "Mare", Name: "Mare the Fair", Selector: "openai/gpt-6.1:high"},
			{Role: "Hyacinthia", Selector: "google/gemini-4-pro:high"},
			{Role: "Nahida", Selector: "deepseek/deepseek-v4:high"},
			{Role: "Kafka", Selector: "openrouter/qwen/qwen3-max:exacto"},
		}},
		{"别名选择器原样保留", "cycleOrder: [default, slow, smol]\nmodelRoles: { default: \" @Cyrene \", slow: 'pi/slow', smol: '*:high' }\n", []CycleRole{
			{Role: "default", Selector: "@Cyrene"}, {Role: "slow", Selector: "pi/slow"}, {Role: "smol", Selector: "*:high"},
		}},
		{"字符串回退链原样", "cycleOrder: [a]\nmodelRoles: { a: 'x/y, gpt-6.1-sol ,@b' }\n", []CycleRole{{Role: "a", Selector: "x/y, gpt-6.1-sol ,@b"}}},
		{"数组逐项去空白丢空项", "cycleOrder: [a]\nmodelRoles:\n  a: [' x/y:high ', '', 3, '@b']\n", []CycleRole{{Role: "a", Selector: "x/y:high, @b"}}},
		{"非字符串选择器忽略", "cycleOrder: [a, b]\nmodelRoles: { a: 7, b: { x: 1 } }\n", []CycleRole{{Role: "a"}, {Role: "b"}}},
		{"cycleOrder 去重过滤", "cycleOrder: [b, '', '  ', 1, true, b, ' a ', null, [x]]\n", []CycleRole{{Role: "b"}, {Role: "a"}}},
		{"cycleOrder 为空数组", "cycleOrder: []\n", defaults},
		{"cycleOrder 只有非法项", "cycleOrder: [1, '']\n", defaults},
		{"cycleOrder 不是数组", "cycleOrder: smol\n", defaults},
		{"大小写原样", "cycleOrder: [Mare, mare]\nmodelTags: { Mare: { name: M } }\n", []CycleRole{{Role: "Mare", Name: "M"}, {Role: "mare"}}},
		{"YAML 锚点", "x: &o [a, b]\ncycleOrder: *o\nmodelRoles: { a: &s p/m }\nmodelTags: { b: { name: *s } }\n", []CycleRole{{Role: "a", Selector: "p/m"}, {Role: "b", Name: "p/m"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseCycleRoles([]byte(c.in))
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}

	for name, bad := range map[string]string{
		"非法 YAML":  "cycleOrder: [a, b\nmodelRoles: {",
		"顶层是数组":    "- a\n- b\n",
		"顶层是字符串":   "just text\n",
		"tab 缩进错误": "modelRoles:\n\ta: x/y\n",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ParseCycleRoles([]byte(bad))
			require.Error(t, err)
			assert.Nil(t, got)
		})
	}
}

// 返回值互不共享底层数组：改动一次结果不影响缺省值与下一次结果。
func TestParseCycleRolesDefaultsNotShared(t *testing.T) {
	a, err := ParseCycleRoles(nil)
	require.NoError(t, err)
	a[0].Role = "mutated"
	b, err := ParseCycleRoles([]byte("cycleOrder: []"))
	require.NoError(t, err)
	b[1].Role = "mutated"
	assert.Equal(t, []string{"smol", "default", "slow"}, DefaultCycleOrder)
	c, err := ParseCycleRoles(nil)
	require.NoError(t, err)
	assert.Equal(t, "smol", c[0].Role)
}

func TestHomeCycleRoles(t *testing.T) {
	h, err := NewHome(t.TempDir())
	require.NoError(t, err)
	defaults := []CycleRole{{Role: "smol"}, {Role: "default"}, {Role: "slow"}}

	got, err := h.CycleRoles()
	require.NoError(t, err, "文件不存在按缺省")
	assert.Equal(t, defaults, got)

	write := func(s string) {
		require.NoError(t, os.WriteFile(filepath.Join(h.Dir(), ConfigFile), []byte(s), 0o600))
	}
	write("cycleOrder: [Mare]\nmodelRoles: { Mare: [a/b, c/d] }\nmodelTags: { Mare: { name: 玛蕾 } }\n")
	got, err = h.CycleRoles()
	require.NoError(t, err)
	assert.Equal(t, []CycleRole{{Role: "Mare", Name: "玛蕾", Selector: "a/b, c/d"}}, got)

	write("cycleOrder: [Mare\n")
	got, err = h.CycleRoles()
	require.Error(t, err)
	assert.Contains(t, err.Error(), ConfigFile)
	assert.Equal(t, defaults, got, "解析失败返回缺省 cycleOrder")
}

func TestParseKnownRoles(t *testing.T) {
	builtinTail := func(skip ...string) []KnownRole {
		var out []KnownRole
		for _, r := range BuiltinRoles {
			if !slices.Contains(skip, r) {
				out = append(out, KnownRole{Role: r})
			}
		}
		return out
	}
	defaults := append([]KnownRole{
		{Role: "smol", InCycle: true}, {Role: "default", InCycle: true}, {Role: "slow", InCycle: true},
	}, builtinTail("smol", "default", "slow")...)

	got, err := ParseKnownRoles(nil)
	require.NoError(t, err)
	assert.Equal(t, defaults, got, "空内容：缺省 cycleOrder 在前，其余内置 role 按 BuiltinRoles 顺序")

	// cycleOrder（按序）→ modelRoles 其余键（字典序）→ BuiltinRoles 其余项；三者去重，大小写原样区分。
	got, err = ParseKnownRoles([]byte(ownerConfig + "  zeta: { name: Z }\n" + `
  vision: { name: 看 }
`))
	require.NoError(t, err)
	want := []KnownRole{
		{Role: "Cyrene", Name: "昔涟", Selector: "anthropic/claude-opus-5-5:high", InCycle: true},
		{Role: "Mare", Name: "Mare the Fair", Selector: "openai/gpt-6.1:high", InCycle: true},
		{Role: "Hyacinthia", Selector: "google/gemini-4-pro:high", InCycle: true},
		{Role: "Nahida", Selector: "deepseek/deepseek-v4:high", InCycle: true},
		{Role: "Kafka", Selector: "openrouter/qwen/qwen3-max:exacto", InCycle: true},
		{Role: "default", Selector: "@Cyrene"},
	}
	want = append(want, KnownRole{Role: "smol"}, KnownRole{Role: "slow"}, KnownRole{Role: "vision", Name: "看"})
	want = append(want, builtinTail("default", "smol", "slow", "vision")...)
	assert.Equal(t, want, got, "modelTags 里出现但不在 modelRoles / 内置中的 zeta 不算")

	got, err = ParseKnownRoles([]byte(`
cycleOrder: [task, Mare]
modelRoles:
  Zed: z/z
  task: t/t
  Mare: m/m
  apple: [a/a, b/b]
  '  ': x/x
  7: n/n
  vision: v/v
`))
	require.NoError(t, err)
	want = []KnownRole{
		{Role: "task", Selector: "t/t", InCycle: true},
		{Role: "Mare", Selector: "m/m", InCycle: true},
		{Role: "Zed", Selector: "z/z"},
		{Role: "apple", Selector: "a/a, b/b"},
		{Role: "vision", Selector: "v/v"},
	}
	want = append(want, builtinTail("task", "vision")...)
	assert.Equal(t, want, got, "cycleOrder 里的内置 role 只出现一次；非字符串与空白键忽略")

	_, err = ParseKnownRoles([]byte("- a\n"))
	require.Error(t, err)
}

func TestHomeKnownRoles(t *testing.T) {
	h, err := NewHome(t.TempDir())
	require.NoError(t, err)
	defaults, err := ParseKnownRoles(nil)
	require.NoError(t, err)

	got, err := h.KnownRoles()
	require.NoError(t, err, "文件不存在按缺省")
	assert.Equal(t, defaults, got)

	write := func(s string) {
		require.NoError(t, os.WriteFile(filepath.Join(h.Dir(), ConfigFile), []byte(s), 0o600))
	}
	write("cycleOrder: [Mare]\nmodelRoles: { Mare: a/b, plan: p/q }\n")
	got, err = h.KnownRoles()
	require.NoError(t, err)
	require.Len(t, got, 1+len(BuiltinRoles))
	assert.Equal(t, KnownRole{Role: "Mare", Selector: "a/b", InCycle: true}, got[0])
	assert.Equal(t, KnownRole{Role: "plan", Selector: "p/q"}, got[1], "modelRoles 里的内置 role 排在其余内置 role 之前")
	assert.Equal(t, "default", got[2].Role)

	write("cycleOrder: [Mare\n")
	got, err = h.KnownRoles()
	require.Error(t, err)
	assert.Contains(t, err.Error(), ConfigFile)
	assert.Equal(t, defaults, got, "解析失败返回缺省配置下的已知 role")
}
