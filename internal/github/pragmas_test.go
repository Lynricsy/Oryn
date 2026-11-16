package github

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

func TestPragmaParse(t *testing.T) {
	body := "please fix\n/model opus\n  /thinking hi  \n```\n/model ignored\n```\n/model too many args\n/MODEL sonnet\n"
	p := parsePragmas(body)
	assert.Equal(t, "sonnet", p.Model, "同一指令取最后一次；指令名不区分大小写；代码块与多参数行不算")
	assert.Equal(t, "hi", p.Thinking)
	assert.Equal(t, pragmas{}, parsePragmas("inline /model opus is not a pragma"))
}

func TestPragmaThinkingAliases(t *testing.T) {
	for alias, want := range map[string]string{
		"off": "off", "none": "off", "no": "off", "lo": "low", "Low": "low", "med": "medium",
		"medium": "medium", "hi": "high", "high": "high", "xhi": "xhigh", "xhigh": "xhigh", "max": "max",
	} {
		got, ok := resolveThinking(alias)
		assert.True(t, ok, alias)
		assert.Equal(t, want, got, alias)
	}
	_, ok := resolveThinking("turbo")
	assert.False(t, ok)
}

func TestPragmaModelResolution(t *testing.T) {
	models := []apigen.ModelRef{
		{Provider: "anthropic", ID: "claude-opus-5"},
		{Provider: "anthropic", ID: "claude-sonnet-5"},
		{Provider: "openai", ID: "gpt-6"},
		{Provider: "openrouter", ID: "gpt-6"},
	}
	cases := []struct {
		alias string
		want  apigen.ModelRef
		ok    bool
	}{
		{"anthropic/claude-opus-5", models[0], true}, // 完整 id
		{"OPENAI/gpt-6", models[2], true},            // 完整 id 优先于短名歧义
		{"claude-sonnet-5", models[1], true},         // 短名
		{"gpt-6", apigen.ModelRef{}, false},          // 短名命中两个 → 歧义
		{"opus", models[0], true},                    // 子串
		{"claude", apigen.ModelRef{}, false},         // 子串歧义
		{"nope", apigen.ModelRef{}, false},
		{"", apigen.ModelRef{}, false},
	}
	for _, c := range cases {
		got, ok := resolveModel(c.alias, models)
		assert.Equal(t, c.ok, ok, c.alias)
		assert.Equal(t, c.want, got, c.alias)
	}
}

func TestPragmaPickModel(t *testing.T) {
	assert.Nil(t, pickModel(nil, func(int) int { return 0 }))
	pool := []apigen.ModelRef{{Provider: "a", ID: "x"}, {Provider: "b", ID: "y"}}
	got := pickModel(pool, func(n int) int { return n - 1 })
	assert.Equal(t, &pool[1], got)
}
