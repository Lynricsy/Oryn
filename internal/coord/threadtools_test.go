package coord

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// fakeMachines 是测试用的 Machines：只有 ListRunners 返回预置值，其余记下调用。
type fakeMachines struct {
	runners []apigen.Runner
}

func (f *fakeMachines) ListRunners(context.Context) ([]apigen.Runner, error) { return f.runners, nil }
func (f *fakeMachines) ListOrbs(context.Context) ([]apigen.Orb, error)       { return nil, nil }
func (f *fakeMachines) Pause(context.Context, string, string) error          { return nil }
func (f *fakeMachines) Wake(context.Context, string) error                   { return nil }
func (f *fakeMachines) Delete(context.Context, string) error                 { return nil }
func (f *fakeMachines) EnsureUsable(context.Context, string, string, bool, time.Duration) error {
	return nil
}
func (f *fakeMachines) Do(context.Context, string, *http.Request) (*http.Response, error) {
	return nil, http.ErrNotSupported
}
func (f *fakeMachines) ScheduleOmpApply(string) {}

func errCodeOf(t *testing.T, r threads.HostToolResult) string {
	t.Helper()
	require.True(t, r.IsError, r.Text)
	code, _, _ := strings.Cut(r.Text, ":")
	return code
}

// M19 A4：助手以 space 名向 Space 的协调对话发话（真实 fakeomp），带标签投递的回合结束后回报到达发话的助手对话；
// read_thread 可读 Space 对话。
func TestAssistantMessagesSpace(t *testing.T) {
	e := newEnv(t, false)
	sp := e.space("Alpha", "")
	asst := e.assistant()
	require.Eventually(t, func() bool { return e.row(sp.ThreadID).State == threads.StateIdle }, 20*time.Second, 20*time.Millisecond)

	r := e.call(asst, ToolSendMessage, `{"space":"Alpha","message":"status?"}`, true)
	assert.Equal(t, []any{sp.ThreadID}, details(t, r)["threadIds"])
	select {
	case text := <-e.texts:
		assert.True(t, strings.HasPrefix(text, "[Oryn thread report · automatic] "+sp.ThreadID+" is now idle.\n"), text)
	case <-time.After(30 * time.Second):
		t.Fatal("Space 那一轮结束后没有回报")
	}
	require.Eventually(t, func() bool { return !e.hasReport(sp.ThreadID) }, 5*time.Second, 20*time.Millisecond, "回报后消费登记")
	acts := e.actions()
	require.NotEmpty(t, acts)
	assert.Equal(t, "send_message", acts[0].Action)
	assert.Equal(t, sp.ThreadID, deref(acts[0].TargetThreadID))

	details(t, e.call(asst, ToolReadThread, `{"threadId":"`+sp.ThreadID+`"}`, true))
	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolSendMessage, `{"space":"Alpha","threadId":"`+sp.ThreadID+`","message":"x"}`, true)))
	assert.Equal(t, CodeSpaceNotFound, errCodeOf(t, e.call(asst, ToolSendMessage, `{"space":"Nope","message":"x"}`, true)))
}

// D10：Space 协调者的目标规则不变（不能操作协调对话、space 参数被忽略）；助手对话之间仍不能互相操作；
// 助手的新工具不能以 Space 对话为目标（只有 send_message 与定时可以）。
func TestActTargetRules(t *testing.T) {
	e := newEnv(t, true)
	sp := e.space("Alpha", "")
	chat := e.spaceChat(sp)
	other := e.spaceChat(e.space("Beta", ""))
	asst := e.assistant()
	asst2 := e.assistant()

	for _, args := range []string{
		`{"threadId":"` + asst.ID + `","message":"hi"}`,
		`{"threadId":"` + other.ID + `","message":"hi"}`,
	} {
		assert.Equal(t, CodeInvalidTarget, errCodeOf(t, e.call(chat, ToolSendMessage, args, true)), args)
	}
	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(chat, ToolSendMessage, `{"space":"Beta","message":"hi"}`, true)), "Space 协调者没有 space 参数")
	assert.Equal(t, "unknown_tool", errCodeOf(t, e.call(chat, ToolUpdateThread, `{"threadId":"x","title":"y"}`, true)))
	assert.Equal(t, CodeInvalidTarget, errCodeOf(t, e.call(chat, ToolReadThread, `{"threadId":"`+other.ID+`"}`, true)))

	for _, tc := range []struct{ tool, args string }{
		{ToolSendMessage, `{"threadId":"` + asst2.ID + `","message":"hi"}`},
		{ToolArchiveThread, `{"threadId":"` + asst2.ID + `"}`},
		{ToolUpdateThread, `{"threadId":"` + asst2.ID + `","title":"x"}`},
		{ToolDeleteThread, `{"threadId":"` + asst2.ID + `"}`},
		{ToolSetSchedule, `{"action":"set","mode":"thread","threadId":"` + asst2.ID + `","kind":"every","every":"1h","prompt":"x"}`},
		{ToolArchiveThread, `{"threadId":"` + chat.ID + `"}`},
		{ToolAbortThread, `{"threadId":"` + chat.ID + `"}`},
		{ToolForkThread, `{"threadId":"` + chat.ID + `"}`},
	} {
		assert.Equal(t, CodeInvalidTarget, errCodeOf(t, e.call(asst, tc.tool, tc.args, true)), tc.tool+" "+tc.args)
	}
	// 线程内定时可以挂在 Space 对话上
	details(t, e.call(asst, ToolSetSchedule, `{"action":"set","mode":"thread","threadId":"`+chat.ID+`","kind":"every","every":"1h","prompt":"x"}`, true))
}

// create_thread 的 Space 与 Runner 参数（只给助手）：Space 名 → space_id 并取其项目；Runner 名 → 在 runner 上建线程。
func TestCreateThreadAssistantParams(t *testing.T) {
	e := newEnv(t, true)
	alpha := e.project("alpha")
	e.space("Alpha", alpha)
	asst := e.assistant()

	r := details(t, e.call(asst, ToolCreateThread, `{"prompt":"go","space":"Alpha","labels":["x"],"title":"T"}`, true))
	assert.Equal(t, "alpha", r["project"])
	row := e.row(r["threadId"].(string))
	sp, err := e.st.Q.GetSpaceByName(e.ctx, "Alpha")
	require.NoError(t, err)
	assert.Equal(t, sp.ID, deref(row.SpaceID))
	assert.Equal(t, alpha, deref(row.ProjectID))
	assert.Equal(t, asst.ID, e.report(row.ID).ReportTo)

	// Runner：未注入 Machines → coordinator_unavailable
	assert.Equal(t, CodeCoordinatorUnavailable, errCodeOf(t, e.call(asst, ToolCreateThread, `{"prompt":"go","runner":"laptop","dir":"/w/app"}`, true)))
	assert.Equal(t, CodeCoordinatorUnavailable, errCodeOf(t, e.call(asst, ToolListRunners, `{}`, true)))
	prov, runnerOrb, err := e.orbs.AddRunner("laptop", []apigen.RunnerDir{{Path: "/w/app", Name: "app"}}, nil)
	require.NoError(t, err)
	e.orbs.ConnectRunner(runnerOrb)
	e.c.SetMachines(&fakeMachines{runners: []apigen.Runner{{ID: prov, Name: "laptop", Online: true, Hostname: new("box"),
		Dirs: []apigen.RunnerDir{{Path: "/w/app", Name: "app"}}}}})

	lr := e.call(asst, ToolListRunners, `{}`, true)
	assert.True(t, strings.HasPrefix(lr.Text, "<untrusted-thread-content"), "runner 名与目录包在不可信包裹里")
	assert.Equal(t, []any{map[string]any{"name": "laptop", "online": true, "hostname": "box",
		"dirs": []any{map[string]any{"path": "/w/app", "name": "app", "git": false}}}}, details(t, lr)["runners"])

	r = details(t, e.call(asst, ToolCreateThread, `{"prompt":"go","runner":"laptop","dir":"/w/app"}`, true))
	row = e.row(r["threadId"].(string))
	assert.Equal(t, "/w/app", deref(row.Cwd))
	assert.Equal(t, runnerOrb, deref(row.OrbID))

	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolCreateThread, `{"prompt":"go","runner":"laptop"}`, true)), "runner 要 dir")
	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolCreateThread, `{"prompt":"go","dir":"/w/app"}`, true)), "dir 要 runner")
	assert.Equal(t, CodeNotFound, errCodeOf(t, e.call(asst, ToolCreateThread, `{"prompt":"go","runner":"nope","dir":"/w/app"}`, true)))
	assert.Equal(t, CodeSpaceNotFound, errCodeOf(t, e.call(asst, ToolCreateThread, `{"prompt":"go","space":"Nope"}`, true)))

	// Space 协调者传了助手专属参数也被忽略
	chat := e.spaceChat(e.space("Beta", ""))
	r = details(t, e.call(chat, ToolCreateThread, `{"prompt":"go","runner":"laptop","dir":"/w/app","space":"Alpha"}`, true))
	row = e.row(r["threadId"].(string))
	assert.Nil(t, row.Cwd)
	sb, err := e.st.Q.GetSpaceByName(e.ctx, "Beta")
	require.NoError(t, err)
	assert.Equal(t, sb.ID, deref(row.SpaceID))
}

// 线程工具：update / abort / set_thread_model / fork / wake 直接执行并写审计；delete 走确认卡，批准后才删。
// 用真实拉起的 fakeomp：fork 需要源线程的 agent 写出会话。
func TestThreadTools(t *testing.T) {
	e := newEnv(t, false)
	sp := e.space("Alpha", "")
	asst := e.assistant()
	w := e.work("hi")
	require.Eventually(t, func() bool { r := e.row(w.ID); return r.State == threads.StateIdle && r.LeafEntryID != nil },
		20*time.Second, 20*time.Millisecond)

	details(t, e.call(asst, ToolUpdateThread, `{"threadId":"`+w.ID+`","title":"New","pinned":true,"labels":["a","b"],"space":"Alpha"}`, true))
	row := e.row(w.ID)
	assert.Equal(t, "New", row.Title)
	assert.Equal(t, sp.ID, deref(row.SpaceID))
	got, err := e.th.Get(e.ctx, w.ID)
	require.NoError(t, err)
	assert.True(t, got.Pinned)
	assert.Equal(t, []string{"a", "b"}, got.Labels)
	details(t, e.call(asst, ToolUpdateThread, `{"threadId":"`+w.ID+`","space":""}`, true))
	assert.Nil(t, e.row(w.ID).SpaceID)
	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolUpdateThread, `{"threadId":"`+w.ID+`"}`, true)))

	details(t, e.call(asst, ToolSetThreadModel, `{"threadId":"`+w.ID+`","model":"anthropic/claude","thinkingLevel":"high"}`, true))
	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolSetThreadModel, `{"threadId":"`+w.ID+`","model":"claude"}`, true)))
	details(t, e.call(asst, ToolAbortThread, `{"threadId":"`+w.ID+`"}`, true))
	details(t, e.call(asst, ToolWakeThread, `{"threadId":"`+w.ID+`"}`, true))
	f := details(t, e.call(asst, ToolForkThread, `{"threadId":"`+w.ID+`","env":"share"}`, true))
	forkID := f["threadId"].(string)
	assert.NotEqual(t, w.ID, forkID)
	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolForkThread, `{"threadId":"`+w.ID+`","env":"bogus"}`, true)))

	acts := e.actions() // 新的在前
	byAction := map[string][]db.CoordAction{}
	for _, a := range acts {
		byAction[a.Action] = append(byAction[a.Action], a)
	}
	for _, name := range []string{ToolUpdateThread, ToolSetThreadModel, ToolAbortThread, ToolWakeThread, ToolForkThread} {
		require.NotEmpty(t, byAction[name], name)
		a := byAction[name][len(byAction[name])-1] // 最早的一条是成功的
		assert.True(t, a.Ok, name)
		assert.Equal(t, "owner", a.Origin, name)
		assert.Equal(t, threads.KindAssistant, a.ActorKind, name)
		assert.Equal(t, w.ID, deref(a.TargetThreadID), name)
	}
	assert.Equal(t, "anthropic/claude · high", byAction[ToolSetThreadModel][1].Summary)
	assert.Equal(t, "env: share → "+forkID, byAction[ToolForkThread][1].Summary)
	assert.Equal(t, "title: New · pinned: true · labels: a, b · space: Alpha", byAction[ToolUpdateThread][2].Summary)
	assert.False(t, byAction[ToolUpdateThread][0].Ok, "失败也记审计")

	// delete_thread：先出确认卡，不删除也不写成功审计；批准后才删除，审计 origin=owner
	n := len(e.actions())
	r := details(t, e.call(asst, ToolDeleteThread, `{"threadId":"`+forkID+`"}`, false))
	assert.Equal(t, "awaiting_owner", r["status"])
	cfm := r["confirmationId"].(string)
	e.row(forkID)
	assert.Len(t, e.actions(), n, "确认之前不写审计")
	e.c.UIAnswered(e.ctx, asst.ID, cfm, answer(cfm, true))
	_, err = e.st.Q.GetThread(e.ctx, forkID)
	assert.Error(t, err, "批准后线程已删除")
	del := e.actions()[0]
	assert.Equal(t, "delete_thread", del.Action)
	assert.Equal(t, "owner", del.Origin)
	assert.True(t, del.Ok)
	assert.Equal(t, forkID, deref(del.TargetThreadID))

	// 拒绝则不删
	r = details(t, e.call(asst, ToolDeleteThread, `{"threadId":"`+w.ID+`"}`, true))
	cfm = r["confirmationId"].(string)
	e.c.UIAnswered(e.ctx, asst.ID, cfm, answer(cfm, false))
	e.row(w.ID)
}

// mutateRules：同一对话的新写类工具与确认请求合计 10 分钟至多 30 次；别的对话不受影响。
func TestMutateRateLimit(t *testing.T) {
	e := newEnv(t, true)
	asst := e.assistant()
	w := e.work("hi")
	for i := range 30 {
		details(t, e.call(asst, ToolUpdateThread, `{"threadId":"`+w.ID+`","pinned":true}`, true))
		_ = i
	}
	assert.Equal(t, CodeRateLimited, errCodeOf(t, e.call(asst, ToolWakeThread, `{"threadId":"`+w.ID+`"}`, true)))
	assert.Equal(t, CodeRateLimited, errCodeOf(t, e.call(asst, ToolDeleteThread, `{"threadId":"`+w.ID+`"}`, true)))
	assert.Equal(t, CodeRateLimited, deref(e.actions()[0].Error))
	details(t, e.call(e.assistant(), ToolWakeThread, `{"threadId":"`+w.ID+`"}`, true))
	e.clk.Add(10 * time.Minute)
	details(t, e.call(asst, ToolWakeThread, `{"threadId":"`+w.ID+`"}`, true))

	l := newLimiter()
	now := time.Unix(0, 0)
	for i := range 200 {
		require.True(t, l.allow("k", now, mutateRules), i)
		now = now.Add(21 * time.Second)
	}
	assert.False(t, l.allow("k", now, mutateRules), "24 小时内第 201 次")
}

// 新工具只出现在助手的工具表里；Space 的工具表不变。
func TestThreadToolDefs(t *testing.T) {
	names := func(defs []apigen.OmpFrame) map[string]apigen.OmpFrame {
		out := map[string]apigen.OmpFrame{}
		for _, d := range defs {
			out[d["name"].(string)] = d
		}
		return out
	}
	space, asst := names(ToolDefs(true, false)), names(ToolDefs(false, false))
	for _, n := range []string{ToolUpdateThread, ToolAbortThread, ToolSetThreadModel, ToolForkThread, ToolWakeThread, ToolListRunners, ToolDeleteThread} {
		assert.Contains(t, asst, n)
		assert.NotContains(t, space, n)
	}
	props := func(d apigen.OmpFrame) obj { return d["parameters"].(obj)["properties"].(obj) }
	assert.NotContains(t, props(space[ToolSendMessage]), "space")
	assert.Equal(t, []string{"threadId", "message"}, space[ToolSendMessage]["parameters"].(obj)["required"])
	assert.Contains(t, props(asst[ToolSendMessage]), "space")
	assert.Equal(t, []string{"message"}, asst[ToolSendMessage]["parameters"].(obj)["required"])
	for _, k := range []string{"space", "runner", "dir", "orbId", "provider", "role", "labels"} {
		assert.Contains(t, props(asst[ToolCreateThread]), k)
		assert.NotContains(t, props(space[ToolCreateThread]), k)
	}
}

// 系统提示词：助手的能力边界换成确认卡一句并多出第三层用法；Space 协调者保持原句、没有这些用法。
func TestAssistantPromptThirdLayer(t *testing.T) {
	a := SystemPrompt(nil, "", "T-1", true)
	assert.Contains(t, a, "\n"+assistantLimits+"\n")
	assert.NotContains(t, a, spaceLimits)
	assert.Contains(t, a, "- Use get_stats for questions about usage and cost.\n- Runner threads run on the owner's own machines")
	assert.Contains(t, a, "send_message with space")
	sp := SystemPrompt(&apigen.Space{Name: "S"}, "", "T-2", true)
	assert.Contains(t, sp, "\n"+spaceLimits+"\n")
	assert.Contains(t, sp, "- Use get_stats for questions about usage and cost.\n\nRules:")
	assert.NotContains(t, sp, "list_runners")
	assert.NotContains(t, sp, "confirmation")
}
