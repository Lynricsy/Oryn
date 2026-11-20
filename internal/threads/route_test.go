package threads

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 会换会话文件的命令：破坏线程↔会话绑定、Oryn 跟随不了的拦下；Oryn 能跟随或原地改动的放行。
func TestRouteSessionSwitchCommands(t *testing.T) {
	cases := []struct {
		frame, route string
	}{
		// 切到 / 移走 / 删除会话文件，或把旁路问答提升成新会话文件
		{`{"type":"new_session"}`, RouteManaged},
		{`{"type":"drop_session"}`, RouteManaged},
		{`{"type":"switch_session","sessionPath":"/x.jsonl"}`, RouteManaged},
		{`{"type":"open_session","sessionDir":"/x"}`, RouteManaged},
		{`{"type":"move_session","path":"/x"}`, RouteManaged},
		{`{"type":"collab_join","link":"x"}`, RouteManaged},
		{`{"type":"btw_branch"}`, RouteManaged},
		// 把历史复制进新会话文件 = 分叉
		{`{"type":"fork","entryId":"e1"}`, RouteUseOrynFork},
		{`{"type":"fork_from","entryId":"e1"}`, RouteUseOrynFork},
		{`{"type":"branch","entryId":"e1"}`, RouteUseOrynFork},
		// 原地改动，或在本目录开 Oryn 能拼接跟随的新会话文件
		{`{"type":"clear_context"}`, RoutePass},
		{`{"type":"fresh"}`, RoutePass},
		{`{"type":"handoff"}`, RoutePass},
		{`{"type":"compact"}`, RoutePass},
		{`{"type":"navigate_tree","entryId":"e1"}`, RoutePass},
		{`{"type":"switch_leaf","entryId":"e1"}`, RoutePass},
		{`{"type":"tan","work":"x"}`, RoutePass},
		{`{"type":"btw","question":"x"}`, RoutePass},
		{`{"type":"btw_inject","recordId":"r1"}`, RoutePass},
		{`{"type":"get_btw_history"}`, RoutePass},
		{`{"type":"plan_approval","approved":true,"option":"execute"}`, RoutePass},
		{`{"type":"set_loop_mode","enabled":true}`, RoutePass},
		{`{"type":"import_foreign_session","source":"claude","foreignId":"x"}`, RoutePass},
		{`{"type":"set_session_name","name":"x"}`, RoutePass},
	}
	for _, c := range cases {
		got, typ := RouteCommand(raw(c.frame))
		assert.Equal(t, c.route, got, c.frame)
		assert.NotEmpty(t, typ, c.frame)
	}
}

// prompt 正文里会换会话文件的内置斜杠命令（omp 在 RPC 里直接执行）按 omp 的解析规则拦下，cmdType 报出被拦的命令。
func TestRoutePromptSlashCommands(t *testing.T) {
	cases := []struct {
		frame, route, cmdType string
	}{
		{`{"type":"prompt","message":"/move /tmp/x"}`, RouteManaged, "/move"},
		{`{"type":"prompt","message":"/move"}`, RouteManaged, "/move"},
		{`{"type":"prompt","message":"/wt"}`, RouteManaged, "/wt"},
		{`{"type":"prompt","message":"/worktree feat"}`, RouteManaged, "/worktree"},
		{`{"type":"prompt","message":"/move:x"}`, RouteManaged, "/move"},
		{`{"type":"prompt","message":"/move\ufeffx"}`, RouteManaged, "/move"}, // JS \s 含 U+FEFF
		{`{"type":"prompt","message":"/session delete"}`, RouteManaged, "/session delete"},
		{`{"type":"prompt","message":"/session  DELETE \n"}`, RouteManaged, "/session delete"},
		{`{"type":"prompt","message":"/session:delete"}`, RouteManaged, "/session delete"},
		{`{"type":"prompt","message":"hi","message":"/move x"}`, RouteManaged, "/move"}, // 重复键取最后一个（同 JS）
		{`{"type":"prompt","message":"/session delete now"}`, RoutePass, "prompt"},      // omp 只在无余参时删除
		{`{"type":"prompt","message":"/session info"}`, RoutePass, "prompt"},
		{`{"type":"prompt","message":"/session"}`, RoutePass, "prompt"},
		{`{"type":"prompt","message":" /move x"}`, RoutePass, "prompt"}, // omp 不去前导空白
		{`{"type":"prompt","message":"/Move x"}`, RoutePass, "prompt"},  // 命令名大小写敏感
		{`{"type":"prompt","message":"/moved"}`, RoutePass, "prompt"},
		{`{"type":"prompt","message":"/move\u0085x"}`, RoutePass, "prompt"}, // U+0085 不是 JS \s
		{`{"type":"prompt","message":"/compact"}`, RoutePass, "prompt"},
		{`{"type":"prompt","message":"please /move x"}`, RoutePass, "prompt"},
		{`{"type":"prompt","Message":"/move x"}`, RoutePass, "prompt"}, // omp 只读精确键
		{`{"type":"prompt","message":null}`, RoutePass, "prompt"},
		{`{"type":"prompt","message":7}`, RoutePass, "prompt"},
		// 只有 prompt 执行内置命令
		{`{"type":"steer","message":"/move x"}`, RoutePass, "steer"},
		{`{"type":"follow_up","message":"/move x"}`, RoutePass, "follow_up"},
		{`{"type":"abort_and_prompt","message":"/session delete"}`, RoutePass, "abort_and_prompt"},
	}
	for _, c := range cases {
		route, typ := RouteCommand(raw(c.frame))
		assert.Equal(t, c.route, route, c.frame)
		assert.Equal(t, c.cmdType, typ, c.frame)
	}
}

// M18：改动 SSH 主机目录的命令（ssh_manage、`/ssh add|remove|rm`）可识别；只读的查询与测试不算。
func TestSSHManageCommand(t *testing.T) {
	cases := []struct {
		cmdType, frame string
		want           bool
	}{
		{"prompt", `{"type":"prompt","message":"/ssh add a --host b"}`, true},
		{"prompt", `{"type":"prompt","message":"/ssh RM a"}`, true},
		{"prompt", `{"type":"prompt","message":"/ssh remove a"}`, true},
		{"prompt", `{"type":"prompt","message":"/ssh add"}`, true},
		{"prompt", `{"type":"prompt","message":"/ssh  Add\ta"}`, true},
		{"prompt", `{"type":"prompt","message":"/ssh list"}`, false},
		{"prompt", `{"type":"prompt","message":"/ssh help"}`, false},
		{"prompt", `{"type":"prompt","message":"/ssh"}`, false},
		{"prompt", `{"type":"prompt","message":"/sshx add"}`, false},
		{"prompt", `{"type":"prompt","message":"/SSH add a"}`, false}, // 命令名大小写敏感
		{"prompt", `{"type":"prompt","message":"/ssh adder"}`, false},
		{"prompt", `{"type":"prompt","message":" /ssh add a"}`, false},
		{"steer", `{"type":"steer","message":"/ssh add a"}`, false}, // 只有 prompt 执行内置命令
		{"ssh_manage", `{"type":"ssh_manage","action":"create"}`, true},
		{"ssh_test", `{"type":"ssh_test","host":{"name":"a"}}`, false},
		{"get_ssh_hosts", `{"type":"get_ssh_hosts"}`, false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, sshManageCommand(c.cmdType, json.RawMessage(c.frame)), c.frame)
	}
}

// orb 线程改 SSH 主机目录的命令 → managed_by_oryn，不写进 agent 流；查询照常直通。旧版 runner 的线程放行；支持
// omphome-apply 的 runner（M21，SSH 主机由 Oryn 下发）与 orb 相同。
func TestSSHManageRoutedByOrb(t *testing.T) {
	f := newFixture(t, Config{})
	th := f.thread(f.orb("ready"), StateIdle)
	a := fakeAgent(8)
	attach(th, a, apigen.AgentStatusReady)
	for _, frame := range []string{`{"type":"ssh_manage","action":"create","scope":"user","name":"x","host":"y"}`, `{"type":"prompt","message":"/ssh add x --host y"}`} {
		err := f.s.Command(f.ctx, th.id, json.RawMessage(frame), noReply)
		var te *Error
		require.ErrorAs(t, err, &te, frame)
		assert.Equal(t, RouteManaged, te.Code, frame)
		assert.Contains(t, te.Message, "设置 → SSH 主机", frame)
	}
	assert.Empty(t, sent(a), "被拒的命令不写进 agent 流")
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"ssh_test","host":{"name":"x","host":"y"}}`), noReply))
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"/ssh list"}`), noReply))
	assert.Len(t, sent(a), 2)

	_, runnerOrb := f.runner("box", "ready")
	rt := f.thread(runnerOrb, StateIdle)
	ra := fakeAgent(8)
	attach(rt, ra, apigen.AgentStatusReady)
	require.NoError(t, f.s.Command(f.ctx, rt.id, json.RawMessage(`{"type":"ssh_manage","action":"create","scope":"user","name":"x","host":"y"}`), noReply))
	require.NoError(t, f.s.Command(f.ctx, rt.id, json.RawMessage(`{"type":"prompt","message":"/ssh rm x"}`), noReply))
	frames := sent(ra)
	require.Len(t, frames, 2)
	assert.Contains(t, frames[0], `"ssh_manage"`)
	assert.Contains(t, frames[1], `/ssh rm x`)

	_, newRunner := f.runner("box2", "ready")
	f.orbs.mu.Lock()
	f.orbs.feats = map[string][]string{newRunner: {"fs", "omphome-apply", "ssh"}}
	f.orbs.mu.Unlock()
	nt := f.thread(newRunner, StateIdle)
	na := fakeAgent(8)
	attach(nt, na, apigen.AgentStatusReady)
	err := f.s.Command(f.ctx, nt.id, json.RawMessage(`{"type":"ssh_manage","action":"create","scope":"user","name":"x","host":"y"}`), noReply)
	var te *Error
	require.ErrorAs(t, err, &te)
	assert.Equal(t, RouteManaged, te.Code)
	assert.Empty(t, sent(na))
}
