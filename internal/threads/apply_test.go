package threads

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/modelcat"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// fakeSchema 是固定的设置 schema：shellPath 需重启，theme 不需要。
type fakeSchema struct{}

func (fakeSchema) SettingsSchema(context.Context) (modelcat.SettingsSchema, error) {
	return modelcat.SettingsSchema{Entries: []modelcat.SettingEntry{
		{Path: "theme", Type: "string"},
		{Path: "shellPath", Type: "string", RestartRequired: true},
	}}, nil
}

// applyFixture 是一个空闲（agent 就绪、会话静止）的工作线程。
func applyFixture(t *testing.T) (*fixture, *thread, *agentConn) {
	f := newFixture(t, Config{})
	f.s.SetConfigSchema(fakeSchema{})
	th := f.thread(f.orb("ready"), StateIdle)
	a := fakeAgent(16)
	attach(th, a, apigen.AgentStatusReady)
	return f, th, a
}

func report(orbID string, changed []string, before, after string) orbs.ApplyReport {
	rep := orbs.ApplyReport{Orbs: []orbs.OrbApply{{OrbID: orbID, OK: true}}, Changed: changed}
	if before != "" {
		rep.ConfigBefore = []byte(before)
	}
	if after != "" {
		rep.ConfigAfter = []byte(after)
	}
	return rep
}

// sentTypes 返回 agent 流上已写出的帧类型。
func sentTypes(a *agentConn) []string {
	var out []string
	for _, f := range sent(a) {
		if typ, _, ok := frameHeader(json.RawMessage(f)); ok {
			out = append(out, typ)
		}
	}
	return out
}

func restartReasons(f *fixture, id string) []string {
	for _, n := range f.s.ApplyStatus(f.ctx).Restart {
		if n.ThreadID == id {
			return n.Reasons
		}
	}
	return nil
}

// 改 skills/x/SKILL.md：空闲线程立即收到 reload_plugins；忙的线程在 session_settled 之后才收到。
func TestConfigAppliedReloadSkill(t *testing.T) {
	f, idle, ia := applyFixture(t)
	busy := f.thread(idle.orbID, StateIdle)
	ba := fakeAgent(16)
	attach(busy, ba, apigen.AgentStatusReady)
	busy.mu.Lock()
	busy.settled = false
	busy.mu.Unlock()

	f.s.ConfigApplied(f.ctx, report(idle.orbID, []string{"skills/x/SKILL.md"}, "", ""))
	assert.Equal(t, []string{"reload_plugins"}, sentTypes(ia))
	assert.Empty(t, sentTypes(ba), "回合进行中不发")
	assert.Equal(t, 1, f.s.ApplyStatus(f.ctx).PendingReloads)

	f.s.onFrame(f.ctx, busy, busy.orbID, []byte(`{"type":"session_settled"}`))
	assert.Equal(t, []string{"reload_plugins"}, sentTypes(ba))
	st := f.s.ApplyStatus(f.ctx)
	assert.Zero(t, st.PendingReloads)
	assert.Empty(t, st.Restart)
}

// config.yml 只往 disabledExtensions 里加 skill:x：收到 reload_plugins，且不标为需重启。
func TestConfigAppliedDisableSkill(t *testing.T) {
	f, th, a := applyFixture(t)
	f.s.ConfigApplied(f.ctx, report(th.orbID, []string{"config.yml"},
		"# 注释\ntheme: dark\n", "# 注释\ntheme: dark\ndisabledExtensions:\n  - skill:x\n"))
	assert.Equal(t, []string{"reload_plugins"}, sentTypes(a))
	assert.Empty(t, restartReasons(f, th.id))
}

// config.yml 只往 disabledExtensions 里加 hook:pre:bash:bash.ts：apply-status 列出该线程，原因含该条目；不发 reload。
func TestConfigAppliedDisableHook(t *testing.T) {
	f, th, a := applyFixture(t)
	f.s.ConfigApplied(f.ctx, report(th.orbID, []string{"config.yml"},
		"disabledExtensions:\n  - skill:x\n", "disabledExtensions:\n  - skill:x\n  - hook:pre:bash:bash.ts\n"))
	assert.Empty(t, sentTypes(a))
	assert.Equal(t, []string{"hook:pre:bash:bash.ts"}, restartReasons(f, th.id))
}

// config.yml 只改了一个非 restartRequired 的键：既不 reload 也不标记；改 restartRequired 的键则标记、原因为键名。
func TestConfigAppliedPlainKey(t *testing.T) {
	f, th, a := applyFixture(t)
	f.s.ConfigApplied(f.ctx, report(th.orbID, []string{"config.yml"}, "theme: dark\n", "theme: light\n"))
	assert.Empty(t, sentTypes(a))
	assert.Empty(t, f.s.ApplyStatus(f.ctx).Restart)

	// task.* 子 agent 键同样什么都不做
	f.s.ConfigApplied(f.ctx, report(th.orbID, []string{"config.yml"}, "task:\n  disabledAgents: []\n", "task:\n  disabledAgents: [scout]\n"))
	assert.Empty(t, sentTypes(a))
	assert.Empty(t, f.s.ApplyStatus(f.ctx).Restart)

	f.s.ConfigApplied(f.ctx, report(th.orbID, []string{"config.yml"}, "shellPath: /bin/sh\n", "shellPath: /bin/zsh\n"))
	assert.Equal(t, []string{"shellPath"}, restartReasons(f, th.id))
}

// 改 hooks/pre/x.ts：apply-status 列出该线程；下发失败的 orb 上的线程不处理。
func TestConfigAppliedHookFile(t *testing.T) {
	f, th, _ := applyFixture(t)
	failed := report(th.orbID, []string{"hooks/pre/x.ts"}, "", "")
	failed.Orbs[0].OK = false
	f.s.ConfigApplied(f.ctx, failed)
	assert.Empty(t, f.s.ApplyStatus(f.ctx).Restart, "orb 没收到新配置")

	f.s.ConfigApplied(f.ctx, report(th.orbID, []string{"hooks/pre/x.ts", "agents/a.md"}, "", ""))
	st := f.s.ApplyStatus(f.ctx)
	require.Len(t, st.Restart, 1)
	assert.Equal(t, th.id, st.Restart[0].ThreadID)
	assert.Equal(t, KindWork, st.Restart[0].Kind)
	assert.Equal(t, []string{"hooks/pre/x.ts"}, st.Restart[0].Reasons)
	assert.False(t, st.Restart[0].Scheduled)
}

// restart-sessions：线程在空闲时 agent.stop，标记随退出清除；下一条消息自动 agent.start。
func TestRestartSessions(t *testing.T) {
	f, th, _ := applyFixture(t)
	o := newCtlOrbs(t, f.orbs)
	f.s.orbs = o
	th.mu.Lock()
	th.settled = false // 正在跑回合
	th.mu.Unlock()
	f.s.ConfigApplied(f.ctx, report(th.orbID, []string{"SYSTEM.md"}, "", ""))

	assert.Equal(t, []string{th.id}, f.s.RestartSessions(f.ctx))
	o.mu.Lock()
	assert.Empty(t, o.calls, "回合进行中不停")
	o.mu.Unlock()
	assert.True(t, f.s.ApplyStatus(f.ctx).Restart[0].Scheduled)

	f.s.onFrame(f.ctx, th, th.orbID, []byte(`{"type":"session_settled"}`))
	c := o.nextCall(t)
	require.Equal(t, "agent.stop", c.method)

	raw, _ := json.Marshal(apigen.LinkCtlAgentStatusParams{ThreadID: th.id, Status: "exited"})
	f.s.onAgentStatus(f.ctx, th.orbID, raw)
	assert.Empty(t, f.s.ApplyStatus(f.ctx).Restart, "退出后标记清除")

	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"hi"}`), noReply))
	c = o.nextCall(t)
	require.Equal(t, "agent.start", c.method)
	var p apigen.LinkCtlAgentStartParams
	require.NoError(t, json.Unmarshal(c.params, &p))
	require.NotEmpty(t, p.InitFrames)
	assert.Equal(t, "open_session", p.InitFrames[0]["type"])
	assert.Equal(t, th.sessionDir, p.InitFrames[0]["sessionDir"], "带着原会话拉起")
}

// 项目层只作用于该项目 orb 上的工作线程；协调对话只看 config.yml / models.yml。
func TestConfigAppliedScope(t *testing.T) {
	f, th, _ := applyFixture(t)
	coord := f.coordThread(th.orbID, KindAssistant, StateIdle)
	attach(coord, fakeAgent(16), apigen.AgentStatusReady)

	f.s.ConfigApplied(f.ctx, report(th.orbID, []string{"hooks/pre/x.ts", "models.yml"}, "", ""))
	assert.Equal(t, []string{"hooks/pre/x.ts", "models.yml"}, restartReasons(f, th.id))
	assert.Equal(t, []string{"models.yml"}, restartReasons(f, coord.id))

	proj := report(th.orbID, []string{"tools/t.ts"}, "", "")
	proj.ProjectID = "prj-other"
	f.s.ConfigApplied(f.ctx, proj)
	assert.NotContains(t, restartReasons(f, th.id), "tools/t.ts", "orb 不属于该项目")
}

func TestConfigEffectOf(t *testing.T) {
	restart := func(k string) bool { return k == "shellPath" }
	e := configEffectOf([]string{"AGENTS.md", "agents/x.md", "APPEND_SYSTEM.md", "extensions/e.ts"}, nil, nil, false, restart)
	assert.True(t, e.reload)
	assert.Equal(t, []string{"APPEND_SYSTEM.md", "extensions/e.ts"}, e.restart)

	e = configEffectOf([]string{"config.yml"},
		[]byte("disabledExtensions: [rule:a, tool:t]\nmodelRoles:\n  default: a/b\n"),
		[]byte("disabledExtensions: [tool:t, context-file:project:AGENTS.md, extension-module:m]\nmodelRoles:\n  default: a/c\n"),
		false, restart)
	assert.True(t, e.reload, "rule: 移除、context-file: 新增")
	assert.Equal(t, []string{"extension-module:m"}, e.restart, "modelRoles.default 不需要重启")

	e = configEffectOf([]string{"config.yml"}, []byte("a: ["), []byte("a: 1\n"), false, restart)
	assert.Equal(t, []string{"config.yml"}, e.restart, "解析失败整份按需重启")

	e = configEffectOf([]string{"config.yml"}, []byte("theme: a\n"), []byte("theme: b\n"), false, nil)
	assert.Equal(t, []string{"theme"}, e.restart, "没有 schema 时按需重启")

	e = configEffectOf([]string{"skills/a/SKILL.md", "config.yml"}, nil, []byte("disabledExtensions: [hook:pre:x:x.ts]\n"), true, restart)
	assert.True(t, e.empty(), "协调宿主不加载技能与 hook")
}
