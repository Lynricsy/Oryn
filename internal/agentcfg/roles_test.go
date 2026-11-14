package agentcfg

import (
	"os"
	"path/filepath"
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
