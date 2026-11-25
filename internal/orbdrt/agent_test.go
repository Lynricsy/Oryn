package orbdrt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	nonEmpty := []string{"read", "grep"}
	empty := []string{}
	for name, p := range map[string]apigen.LinkCtlAgentStartParams{
		"appendSystemPrompt": {
			ThreadID: "T-01932f6e-8d1a-7c3b-9f2e-0123456789b1", Profile: &coord, SystemPrompt: &prompt,
			AppendSystemPrompt: &extra,
		},
		"tools": {
			ThreadID: "T-01932f6e-8d1a-7c3b-9f2e-0123456789b2", Profile: &coord, SystemPrompt: &prompt,
			Tools: &nonEmpty,
		},
		"empty tools": {
			ThreadID: "T-01932f6e-8d1a-7c3b-9f2e-0123456789b3", Profile: &coord, SystemPrompt: &prompt,
			Tools: &empty,
		},
	} {
		err := cp.call(t, "agent.start", p, nil)
		assert.Equal(t, link.CodeInvalidParams, ctlCode(err), "%s: %v", name, err)
	}
}

// 普通工作线程按 subagent 配置启动：
// - 非空 tools 白名单：写出 role-prompt.md 并以 --append-system-prompt 传入，设置 ORYN_ROLE_TOOLS（覆盖 p.Env 里的伪造值），不加 --no-tools；
// - 显式空 tools: []：设 ORYN_ROLE_TOOLS="[]"，同样不加 --no-tools（omp 19.2.0 起 --no-tools 与 -e 互斥，由 oryn-orb.ts 的 setActiveTools([]) 禁用全部工具）；
// - 省略 tools (nil)：不加 --no-tools，且清除 p.Env 试图注入的 ORYN_ROLE_TOOLS。
func TestWorkThreadRolePromptAndTools(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)

	dump := filepath.Join(rt.home, "dump")
	extra := "# Agent Role: scout\n\nInvestigate rapidly."
	allow := []string{" read ", "grep", "read"}
	var res apigen.LinkCtlAgentStartResult
	require.NoError(t, cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{
		ThreadID:           thread1,
		SessionDir:         "~/.oryn/threads/" + thread1,
		Env:                map[string]string{"FAKEOMP_DUMP_DIR": dump, "ORYN_ROLE_TOOLS": `["bash"]`},
		InitFrames:         []apigen.OmpFrame{{"type": "open_session", "sessionDir": "~/.oryn/threads/" + thread1}},
		AppendSystemPrompt: &extra,
		Tools:              &allow,
	}, &res))
	cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))

	rec := setupDump(t, dump, thread1)
	promptFile := filepath.Join(rt.home, ".oryn", "threads", thread1, "role-prompt.md")
	assert.Contains(t, rec.Args, "--append-system-prompt")
	assert.Contains(t, rec.Args, promptFile)
	assert.NotContains(t, rec.Args, "--no-tools")
	assert.Equal(t, `["read","grep"]`, rec.Env["ORYN_ROLE_TOOLS"])
	assert.Equal(t, "0", rec.Env["PI_AUTO_QA"])
	gotPrompt, err := os.ReadFile(promptFile)
	require.NoError(t, err)
	assert.Equal(t, extra, string(gotPrompt))

	// 显式空数组 tools: [] → ORYN_ROLE_TOOLS="[]" 且不传 --no-tools（避免与 -e 冲突）
	const threadEmpty = "T-01932f6e-8d1a-7c3b-9f2e-0123456789c1"
	empty := []string{}
	require.NoError(t, cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{
		ThreadID:   threadEmpty,
		SessionDir: "~/.oryn/threads/" + threadEmpty,
		Env:        map[string]string{"FAKEOMP_DUMP_DIR": dump},
		InitFrames: []apigen.OmpFrame{{"type": "open_session", "sessionDir": "~/.oryn/threads/" + threadEmpty}},
		Tools:      &empty,
	}, &res))
	cp.waitNotif(t, 0, "agent.status", statusIs(threadEmpty, "ready"))
	recEmpty := setupDump(t, dump, threadEmpty)
	assert.NotContains(t, recEmpty.Args, "--no-tools")
	assert.Contains(t, recEmpty.Args, "-e")
	assert.Equal(t, `[]`, recEmpty.Env["ORYN_ROLE_TOOLS"])
	assert.Equal(t, "0", recEmpty.Env["PI_AUTO_QA"])
	// 省略 tools (nil) → 不限制，且 p.Env 试图伪造的 ORYN_ROLE_TOOLS 被清除
	const threadNil = "T-01932f6e-8d1a-7c3b-9f2e-0123456789c2"
	require.NoError(t, cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{
		ThreadID:   threadNil,
		SessionDir: "~/.oryn/threads/" + threadNil,
		Env:        map[string]string{"FAKEOMP_DUMP_DIR": dump, "ORYN_ROLE_TOOLS": `["read"]`},
		InitFrames: []apigen.OmpFrame{{"type": "open_session", "sessionDir": "~/.oryn/threads/" + threadNil}},
	}, &res))
	cp.waitNotif(t, 0, "agent.status", statusIs(threadNil, "ready"))
	recNil := setupDump(t, dump, threadNil)
	assert.NotContains(t, recNil.Args, "--no-tools")
	assert.NotContains(t, recNil.Env, "ORYN_ROLE_TOOLS")
}
