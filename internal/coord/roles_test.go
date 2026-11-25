package coord

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
)

type fakeModelCatalog struct {
	models  []ModelInfo
	bundled []agentcfg.AgentSpec
	err     error
}

func (f *fakeModelCatalog) Models(context.Context) ([]ModelInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.models, nil
}

func (f *fakeModelCatalog) BundledAgents(context.Context) ([]agentcfg.AgentSpec, error) {
	return f.bundled, nil
}

// Space 协调者 create_thread：
//   - 既不指定 role 也不指定 model → 显式以 default role 开场（不追加角色提示词、不限制工具）；
//   - 显式指定 model + thinkingLevel → 使用显式模型与思考强度（不发 default role 覆盖显式模型）；
//   - 指定 subagent role（内置或用户 agents/*.md）→ 作为普通工作线程启动，写入角色提示词、工具白名单、
//     按 agentModelOverrides > frontmatter model > default 解析模型，且显式 model / thinkingLevel 优先覆盖；
//   - 拒绝 cycle role、被禁用的 subagent、未知 role、非法 thinkingLevel。
func TestSpaceCreateThreadAgentRoles(t *testing.T) {
	e := newEnv(t, true)
	home, err := agentcfg.NewHome(t.TempDir())
	require.NoError(t, err)

	writeHome := func(rel, content string) {
		t.Helper()
		p := filepath.Join(home.Dir(), filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	}
	writeHome("config.yml", `
cycleOrder: [Cyrene, Mare, Nahida]
modelRoles:
  default: "@Cyrene"
  Cyrene: anthropic/claude-opus-4:high
  Mare: openai/gpt-5:medium
  smol: openai/gpt-5-mini
  slow: anthropic/claude-opus-4
task:
  disabledAgents: [sonic]
  agentModelOverrides:
    reviewer: "google/gemini-4-pro:xhigh"
`)
	writeHome("agents/writer.md", `---
name: writer
description: Technical documentation specialist
model: "@slow"
thinkingLevel: high
tools: [read, write, edit]
---
Write crisp technical docs.
`)
	writeHome("agents/pure.md", `---
name: pure
description: Pure text analyst without tools
tools: []
---
Reason only from the prompt.
`)
	// 如果用户碰巧定义了与 cycle role 同名的文件，Space 协调者仍必须排除 cycle role
	writeHome("agents/mare.md", `---
name: Mare
description: Collides with cycle role
---
Should be excluded.
`)

	e.c.SetAgentConfig(home, nil)
	e.c.SetModelCatalog(&fakeModelCatalog{
		bundled: []agentcfg.AgentSpec{
			{
				Name:          "scout",
				Description:   "Fast codebase scout",
				Model:         []string{"@smol"},
				ThinkingLevel: "medium",
				Tools:         []string{"read", "find", "grep", "glob"},
				SystemPrompt:  "Investigate rapidly.",
				Source:        "bundled",
			},
			{
				Name:         "reviewer",
				Description:  "Code review specialist",
				Model:        []string{"@slow"},
				Tools:        []string{"read", "grep", "bash"},
				SystemPrompt: "Find real bugs.",
				Source:       "bundled",
			},
			{
				Name:         "security-reviewer",
				Description:  "Security specialist",
				Tools:        []string{"read", "grep"},
				SystemPrompt: "Audit security boundaries.",
				Source:       "bundled",
			},
			{
				Name:         "sonic",
				Description:  "Disabled worker",
				Model:        []string{"@smol"},
				SystemPrompt: "Mechanical work.",
				Source:       "bundled",
			},
		},
		models: []ModelInfo{
			{Provider: "anthropic", ID: "claude-opus-4", Name: "Claude Opus 4", Reasoning: true, Thinking: []string{"low", "medium", "high", "xhigh"}},
			{Provider: "openai", ID: "gpt-5", Name: "GPT-5", Reasoning: true, Thinking: []string{"low", "medium", "high"}},
			{Provider: "google", ID: "gemini-4-pro", Name: "Gemini 4 Pro", Reasoning: true, Thinking: []string{"low", "medium", "high", "xhigh"}},
		},
	})

	sp := e.space("Backend", "")
	chat := e.spaceChat(sp)

	// 1. Space 系统提示词包含默认用 default 原则、可用 subagent 角色列表，不含 cycle role 与被禁用的 sonic
	prof, err := e.c.AgentProfile(e.ctx, chat.ID)
	require.NoError(t, err)
	assert.Contains(t, prof.SystemPrompt, "Most threads you dispatch should use `default`")
	assert.Contains(t, prof.SystemPrompt, "`default`")
	assert.Contains(t, prof.SystemPrompt, "`scout`: Fast codebase scout")
	assert.Contains(t, prof.SystemPrompt, "`reviewer`: Code review specialist")
	assert.Contains(t, prof.SystemPrompt, "`writer`: Technical documentation specialist")
	assert.NotContains(t, prof.SystemPrompt, "`Cyrene`")
	assert.NotContains(t, prof.SystemPrompt, "`Mare`")
	assert.NotContains(t, prof.SystemPrompt, "`sonic`")

	// 2. 既不指定 role 也不指定 model → 使用 default role
	r := details(t, e.call(chat, ToolCreateThread, `{"prompt":"implement feature A"}`, true))
	assert.Equal(t, "default", r["role"])
	row := e.row(r["threadId"].(string))
	assert.Nil(t, row.Model)
	assert.Nil(t, row.AgentRole)
	assert.Nil(t, row.AppendSystemPrompt)
	assert.Nil(t, row.AllowedTools)
	assert.Contains(t, string(row.StartupFrames), `"type":"oryn_select_model"`)
	assert.Contains(t, string(row.StartupFrames), `"role":"default"`)

	// 3. 显式指定 model + thinkingLevel（不传 role）→ 直接用该模型与思考强度，不发 default role
	r = details(t, e.call(chat, ToolCreateThread, `{"prompt":"hard algorithm","model":"openai/gpt-5","thinkingLevel":"high"}`, true))
	assert.NotContains(t, r, "role")
	row = e.row(r["threadId"].(string))
	assert.Equal(t, "openai/gpt-5", deref(row.Model))
	assert.Equal(t, "high", deref(row.ThinkingLevel))
	assert.Nil(t, row.AgentRole)
	assert.NotContains(t, string(row.StartupFrames), `"oryn_select_model"`)

	// 4. 指定内置 subagent role (scout) → 使用其 @smol 模型角色、medium 思考强度、提示词与工具白名单
	r = details(t, e.call(chat, ToolCreateThread, `{"prompt":"map auth flow","role":"scout"}`, true))
	assert.Equal(t, "scout", r["role"])
	row = e.row(r["threadId"].(string))
	assert.Nil(t, row.Model)
	assert.Equal(t, "medium", deref(row.ThinkingLevel))
	assert.Equal(t, "scout", deref(row.AgentRole))
	assert.Contains(t, deref(row.AppendSystemPrompt), "# Agent Role: scout")
	assert.Contains(t, deref(row.AppendSystemPrompt), "Investigate rapidly.")
	assert.Contains(t, deref(row.AppendSystemPrompt), "not as a nested subagent")
	assert.JSONEq(t, `["read","find","grep","glob"]`, deref(row.AllowedTools))
	assert.Contains(t, string(row.StartupFrames), `"role":"smol"`)
	assert.Contains(t, string(row.StartupFrames), `"thinkingLevel":"medium"`)

	// 5. 指定 reviewer（config.yml 配了 agentModelOverrides: google/gemini-4-pro:xhigh）→ 覆盖生效
	r = details(t, e.call(chat, ToolCreateThread, `{"prompt":"review PR #12","role":"reviewer"}`, true))
	row = e.row(r["threadId"].(string))
	assert.Equal(t, "reviewer", deref(row.AgentRole))
	assert.Equal(t, "google/gemini-4-pro", deref(row.Model))
	assert.Equal(t, "xhigh", deref(row.ThinkingLevel))
	assert.NotContains(t, string(row.StartupFrames), `"oryn_select_model"`)

	// 6. 指定用户自定义 subagent (writer) 并显式覆盖 model 与 thinkingLevel → 显式覆盖优先
	r = details(t, e.call(chat, ToolCreateThread, `{"prompt":"write guide","role":"writer","model":"anthropic/claude-opus-4","thinkingLevel":"low"}`, true))
	row = e.row(r["threadId"].(string))
	assert.Equal(t, "writer", deref(row.AgentRole))
	assert.Equal(t, "anthropic/claude-opus-4", deref(row.Model))
	assert.Equal(t, "low", deref(row.ThinkingLevel))
	assert.Contains(t, deref(row.AppendSystemPrompt), "Write crisp technical docs.")
	assert.JSONEq(t, `["read","write","edit"]`, deref(row.AllowedTools))

	// 7b. 显式 tools: [] 的 subagent (pure) → 落库 "[]" 而非 NULL，禁用全部内置工具
	r = details(t, e.call(chat, ToolCreateThread, `{"prompt":"analyze design","role":"pure"}`, true))
	row = e.row(r["threadId"].(string))
	assert.Equal(t, "pure", deref(row.AgentRole))
	require.NotNil(t, row.AllowedTools)
	assert.Equal(t, "[]", *row.AllowedTools)
	// 7. 未配 model 的 subagent (security-reviewer) → 模型回退到 default，仍保留其提示词与工具白名单
	r = details(t, e.call(chat, ToolCreateThread, `{"prompt":"audit auth","role":"security-reviewer"}`, true))
	row = e.row(r["threadId"].(string))
	assert.Equal(t, "security-reviewer", deref(row.AgentRole))
	assert.Contains(t, string(row.StartupFrames), `"role":"default"`)
	assert.JSONEq(t, `["read","grep"]`, deref(row.AllowedTools))

	// 8. 拒绝 cycle role、禁用 role、未知 role、非法 thinkingLevel
	for name, args := range map[string]string{
		"cycle role Cyrene": `{"prompt":"x","role":"Cyrene"}`,
		"cycle role Mare":   `{"prompt":"x","role":"Mare"}`,
		"disabled sonic":    `{"prompt":"x","role":"sonic"}`,
		"unknown role":      `{"prompt":"x","role":"nonexistent"}`,
		"bad thinkingLevel": `{"prompt":"x","thinkingLevel":"turbo"}`,
	} {
		res := e.call(chat, ToolCreateThread, args, true)
		assert.Equal(t, CodeInvalid, errCodeOf(t, res), "%s: %s", name, res.Text)
	}
}

func TestSpaceListModelsTool(t *testing.T) {
	e := newEnv(t, true)
	sp := e.space("Alpha", "")
	chat := e.spaceChat(sp)
	asst := e.assistant()

	// 未配置模型目录 → coordinator_unavailable
	assert.Equal(t, CodeCoordinatorUnavailable, errCodeOf(t, e.call(chat, ToolListModels, `{}`, true)))
	// 全局助手没有 list_models（走 oryn 工具）
	assert.Equal(t, "unknown_tool", errCodeOf(t, e.call(asst, ToolListModels, `{}`, true)))

	e.c.SetModelCatalog(&fakeModelCatalog{
		models: []ModelInfo{
			{Provider: "anthropic", ID: "claude-opus-4", Name: "Claude Opus 4", Reasoning: true, Thinking: []string{"low", "high"}},
			{Provider: "openai", ID: "gpt-5", Name: "GPT-5", Reasoning: true, Thinking: []string{"medium", "high"}},
			{Provider: "openai", ID: "gpt-5-mini", Name: "GPT-5 Mini", Reasoning: true, Thinking: []string{"low", "medium"}},
		},
	})

	// 全部列表（带不可信包裹）
	res := e.call(chat, ToolListModels, `{}`, true)
	require.False(t, res.IsError, res.Text)
	assert.Contains(t, res.Text, "<untrusted-thread-content")
	d := details(t, res)
	assert.Equal(t, float64(3), d["total"])
	raw, _ := json.Marshal(d["models"])
	var got []ModelInfo
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Len(t, got, 3)

	// 按 query 过滤 + limit 截断
	d = details(t, e.call(chat, ToolListModels, `{"query":"openai","limit":1}`, true))
	assert.Equal(t, float64(2), d["total"])
	raw, _ = json.Marshal(d["models"])
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Len(t, got, 1)
	assert.Equal(t, "openai", got[0].Provider)
	assert.Equal(t, "gpt-5", got[0].ID)
}
