package orbdrt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// M26 步骤 1.2：agent.start 的应答报回在跑的监督器实际使用的变体。幂等返回（已有活动监督器）时是已有的那个，
// 不是本次请求的——控制面靠它判断主线 agent 是否受限；停掉之后再启动才换成新的变体。没有变体时省略。
func TestAgentStartReportsVariant(t *testing.T) {
	_, cp, _, _ := startCoordinator(t)
	cp.waitNotif(t, 0, "hello", nil)
	const thread = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ae"
	coord := apigen.AgentProfileCoordinator
	prompt := "You are the Oryn assistant.\nKeep replies short."
	start := func(thread string, variant apigen.LinkCtlAgentStartParamsVariant) apigen.LinkCtlAgentStartResult {
		t.Helper()
		p := apigen.LinkCtlAgentStartParams{
			ThreadID: thread, SessionDir: "~/.oryn/threads/" + thread, Profile: &coord, SystemPrompt: &prompt,
			InitFrames: []apigen.OmpFrame{{"type": "open_session", "sessionDir": "~/.oryn/threads/" + thread}},
		}
		if variant != "" {
			p.Variant = &variant
		}
		var res apigen.LinkCtlAgentStartResult
		require.NoError(t, cp.call(t, "agent.start", p, &res))
		require.Positive(t, res.Pid)
		return res
	}

	first := start(thread, apigen.AgentVariantResearchWeb)
	require.NotNil(t, first.Variant)
	assert.Equal(t, "research_web", *first.Variant)
	cp.waitNotif(t, 0, "agent.status", statusIs(thread, "ready"))

	// 幂等：以另一个变体再调 agent.start，在跑的仍是原来的进程与变体
	again := start(thread, apigen.AgentVariantMainRestricted)
	assert.Equal(t, first.Pid, again.Pid)
	require.NotNil(t, again.Variant)
	assert.Equal(t, "research_web", *again.Variant)
	// 省略变体的幂等调用同样报回已有的变体
	plain := start(thread, "")
	require.NotNil(t, plain.Variant)
	assert.Equal(t, "research_web", *plain.Variant)

	// 停掉之后以新变体启动：报回新的
	require.NoError(t, cp.call(t, "agent.stop", apigen.LinkCtlAgentStopParams{ThreadID: thread, GraceSeconds: 5}, nil))
	next := start(thread, apigen.AgentVariantMainRestricted)
	require.NotNil(t, next.Variant)
	assert.Equal(t, "main_restricted", *next.Variant)
	assert.NotEqual(t, first.Pid, next.Pid)

	// 没有变体的 agent：应答里省略
	const other = "T-01932f6e-8d1a-7c3b-9f2e-0123456789af"
	assert.Nil(t, start(other, "").Variant)
}

// 普通 orb 不接受 variant（与 profile 一样只有协调宿主接受）。
func TestOrdinaryOrbRejectsVariant(t *testing.T) {
	cp := newFakeCP(t)
	startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)
	v := apigen.AgentVariantResearchWeb
	err := cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{
		ThreadID: "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab", Variant: &v,
	}, nil)
	assert.Equal(t, link.CodeInvalidParams, ctlCode(err), "%v", err)
}

// 协调宿主拒绝工作线程专属的 appendSystemPrompt 与 tools 参数。
func TestCoordinatorRejectsWorkThreadRoleParams(t *testing.T) {
	_, cp, _, _ := startCoordinator(t)
	cp.waitNotif(t, 0, "hello", nil)
	coord := apigen.AgentProfileCoordinator
	prompt := "You are the Oryn coordinator.\nKeep replies short."
	extra := "# Role: scout"
	for name, p := range map[string]apigen.LinkCtlAgentStartParams{
		"appendSystemPrompt": {
			ThreadID: "T-01932f6e-8d1a-7c3b-9f2e-0123456789b1", Profile: &coord, SystemPrompt: &prompt,
			AppendSystemPrompt: &extra,
		},
		"tools": {
			ThreadID: "T-01932f6e-8d1a-7c3b-9f2e-0123456789b2", Profile: &coord, SystemPrompt: &prompt,
			Tools: []string{"read", "grep"},
		},
	} {
		err := cp.call(t, "agent.start", p, nil)
		assert.Equal(t, link.CodeInvalidParams, ctlCode(err), "%s: %v", name, err)
	}
}

// 普通工作线程带 appendSystemPrompt 与 tools 启动：
// 写出 role-prompt.md 并以 --append-system-prompt 传入；写出 role-overlay.yml 保留原 config.yml 的
// disabledExtensions 并禁用白名单外内置工具，以 --config 传入。
func TestWorkThreadRolePromptAndToolsOverlay(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)

	// 预先放一份带 disabledExtensions 的 config.yml，验证覆盖层不会丢掉用户原有禁用项
	cfgPath := filepath.Join(rt.ompAgentDir(), agentcfg.ConfigFile)
	require.NoError(t, os.MkdirAll(filepath.Dir(cfgPath), 0o755))
	require.NoError(t, os.WriteFile(cfgPath, []byte("disabledExtensions:\n  - rule:custom\n"), 0o600))

	dump := filepath.Join(rt.home, "dump")
	extra := "# Agent Role: scout\n\nInvestigate rapidly."
	var res apigen.LinkCtlAgentStartResult
	require.NoError(t, cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{
		ThreadID:           thread1,
		SessionDir:         "~/.oryn/threads/" + thread1,
		Env:                map[string]string{"FAKEOMP_DUMP_DIR": dump},
		InitFrames:         []apigen.OmpFrame{{"type": "open_session", "sessionDir": "~/.oryn/threads/" + thread1}},
		AppendSystemPrompt: &extra,
		Tools:              []string{"read", "grep", "glob"},
	}, &res))
	cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))

	rec := setupDump(t, dump, thread1)
	threadDir := filepath.Join(rt.home, ".oryn", "threads", thread1)
	promptFile := filepath.Join(threadDir, "role-prompt.md")
	overlayFile := filepath.Join(threadDir, "role-overlay.yml")

	assert.Contains(t, rec.Args, "--append-system-prompt")
	assert.Contains(t, rec.Args, promptFile)
	assert.Contains(t, configArgs(rec.Args), overlayFile)

	gotPrompt, err := os.ReadFile(promptFile)
	require.NoError(t, err)
	assert.Equal(t, extra, string(gotPrompt))

	gotOverlay, err := os.ReadFile(overlayFile)
	require.NoError(t, err)
	var parsed struct {
		DisabledExtensions []string `yaml:"disabledExtensions"`
	}
	require.NoError(t, yaml.Unmarshal(gotOverlay, &parsed))
	assert.Contains(t, parsed.DisabledExtensions, "rule:custom", "保留用户原有的 disabledExtensions")
	assert.Contains(t, parsed.DisabledExtensions, "tool:edit")
	assert.Contains(t, parsed.DisabledExtensions, "tool:write")
	assert.Contains(t, parsed.DisabledExtensions, "tool:bash")
	assert.NotContains(t, parsed.DisabledExtensions, "tool:read")
	assert.NotContains(t, parsed.DisabledExtensions, "tool:grep")
	assert.NotContains(t, parsed.DisabledExtensions, "tool:glob")
}
