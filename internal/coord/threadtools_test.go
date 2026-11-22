package coord

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"regexp"
	"slices"
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
func (f *fakeMachines) Pause(context.Context, string, string) error          { return nil }
func (f *fakeMachines) Wake(context.Context, string) error                   { return nil }
func (f *fakeMachines) Delete(context.Context, string) error                 { return nil }
func (f *fakeMachines) EnsureUsable(context.Context, string, string, bool, time.Duration) error {
	return nil
}
func (f *fakeMachines) Do(context.Context, string, *http.Request) (*http.Response, error) {
	return nil, http.ErrNotSupported
}
func (f *fakeMachines) CreateComputer(context.Context, string, string) (apigen.Orb, error) {
	return apigen.Orb{}, http.ErrNotSupported
}
func (f *fakeMachines) Computer(context.Context) (apigen.Orb, bool, error) {
	return apigen.Orb{}, false, nil
}
func (f *fakeMachines) TouchAgent(context.Context, string) {}
func (f *fakeMachines) ComputerCall(context.Context, string, string, any, any) error {
	return http.ErrNotSupported
}
func (f *fakeMachines) OnComputerUpdate(func(string, string, json.RawMessage)) {}

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
	assert.Equal(t, "unknown_tool", errCodeOf(t, e.call(chat, ToolOryn, `{"op":"updateThread","args":{"id":"x","body":{"title":"y"}}}`, true)))
	assert.Equal(t, CodeInvalidTarget, errCodeOf(t, e.call(chat, ToolReadThread, `{"threadId":"`+other.ID+`"}`, true)))

	for _, tc := range []struct{ tool, args string }{
		{ToolSendMessage, `{"threadId":"` + asst2.ID + `","message":"hi"}`},
		{ToolArchiveThread, `{"threadId":"` + asst2.ID + `"}`},
		{ToolAbortThread, `{"threadId":"` + asst2.ID + `"}`},
		{ToolSetThreadModel, `{"threadId":"` + asst2.ID + `","model":"a/b"}`},
		{ToolSetSchedule, `{"action":"set","mode":"thread","threadId":"` + asst2.ID + `","kind":"every","every":"1h","prompt":"x"}`},
		{ToolArchiveThread, `{"threadId":"` + chat.ID + `"}`},
		{ToolAbortThread, `{"threadId":"` + chat.ID + `"}`},
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
	prov, runnerOrb, err := e.orbs.AddRunner("laptop", []apigen.RunnerDir{{Path: "/w/app", Name: "app"}}, nil)
	require.NoError(t, err)
	e.orbs.ConnectRunner(runnerOrb)
	e.c.SetMachines(&fakeMachines{runners: []apigen.Runner{{ID: prov, Name: "laptop", Online: true, Hostname: new("box"),
		Dirs: []apigen.RunnerDir{{Path: "/w/app", Name: "app"}}}}})

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

// 线程操作：abort / set_thread_model 经 oryn 转到原实现，直接执行并写审计（动作名 = 操作名）。
// 用真实拉起的 fakeomp：中止与改模型需要线程的 agent。
func TestThreadTools(t *testing.T) {
	e := newEnv(t, false)
	asst := e.assistant()
	w := e.work("hi")
	require.Eventually(t, func() bool { r := e.row(w.ID); return r.State == threads.StateIdle && r.LeafEntryID != nil },
		20*time.Second, 20*time.Millisecond)

	details(t, e.call(asst, ToolOryn, `{"op":"set_thread_model","args":{"threadId":"`+w.ID+`","model":"anthropic/claude","thinkingLevel":"high"}}`, true))
	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolOryn, `{"op":"set_thread_model","args":{"threadId":"`+w.ID+`","model":"claude"}}`, true)))
	details(t, e.call(asst, ToolOryn, `{"op":"abort_thread","args":{"threadId":"`+w.ID+`"}}`, true))
	// 线程文件操作同样落到原实现（没有 Machines 时回 coordinator_unavailable）
	assert.Equal(t, CodeCoordinatorUnavailable, errCodeOf(t, e.call(asst, ToolOryn, `{"op":"list_thread_files","args":{"threadId":"`+w.ID+`"}}`, true)))

	acts := e.actions() // 新的在前
	byAction := map[string][]db.CoordAction{}
	for _, a := range acts {
		byAction[a.Action] = append(byAction[a.Action], a)
	}
	for _, name := range []string{ToolSetThreadModel, ToolAbortThread} {
		require.NotEmpty(t, byAction[name], name)
		a := byAction[name][len(byAction[name])-1] // 最早的一条是成功的
		assert.True(t, a.Ok, name)
		assert.Equal(t, "owner", a.Origin, name)
		assert.Equal(t, threads.KindAssistant, a.ActorKind, name)
		assert.Equal(t, w.ID, deref(a.TargetThreadID), name)
	}
	assert.Equal(t, "anthropic/claude · high", byAction[ToolSetThreadModel][1].Summary)
	assert.False(t, byAction[ToolSetThreadModel][0].Ok, "失败也记审计")
}

// mutateRules：同一对话的新写类工具与确认请求合计 10 分钟至多 30 次；别的对话不受影响。
func TestMutateRateLimit(t *testing.T) {
	e := newEnv(t, true)
	e.c.SetAPI(&fakeAPI{})
	asst := e.assistant()
	w := e.work("hi")
	update := `{"op":"updateThread","args":{"id":"` + w.ID + `","body":{"pinned":true}}}`
	for range 30 {
		require.False(t, e.call(asst, ToolOryn, update, true).IsError)
	}
	assert.Equal(t, CodeRateLimited, errCodeOf(t, e.call(asst, ToolOryn, `{"op":"wakeThread","args":{"id":"`+w.ID+`"}}`, true)))
	assert.Equal(t, CodeRateLimited, errCodeOf(t, e.call(asst, ToolOryn, `{"op":"deleteThread","args":{"id":"`+w.ID+`"}}`, true)))
	assert.Equal(t, CodeRateLimited, errCodeOf(t, e.call(asst, ToolSetThreadModel, `{"threadId":"`+w.ID+`","model":"a/b"}`, true)))
	assert.Equal(t, CodeRateLimited, deref(e.actions()[0].Error))
	require.False(t, e.call(e.assistant(), ToolOryn, update, true).IsError, "别的对话不受影响")
	e.clk.Add(10 * time.Minute)
	require.False(t, e.call(asst, ToolOryn, update, true).IsError)
	l := newLimiter()
	now := time.Unix(0, 0)
	for i := range 200 {
		require.True(t, l.allow("k", now, mutateRules), i)
		now = now.Add(21 * time.Second)
	}
	assert.False(t, l.allow("k", now, mutateRules), "24 小时内第 201 次")
}

// 顶层工具表：Space 协调者恰为基础 9 个；全局助手恰为 23 个（其余能力都是 oryn 技能的操作），oryn 以顶层工具出现。
func TestAssistantToolDefs(t *testing.T) {
	names := func(defs []apigen.OmpFrame) map[string]apigen.OmpFrame {
		out := map[string]apigen.OmpFrame{}
		for _, d := range defs {
			out[d["name"].(string)] = d
		}
		return out
	}
	space, asst := names(ToolDefs(ToolSet{InSpace: true})), names(ToolDefs(ToolSet{}))
	base := []string{ToolListThreads, ToolReadThread, ToolCreateThread, ToolSendMessage, ToolArchiveThread, ToolSetSchedule,
		ToolListSchedules, ToolListProjects, ToolGetStats}
	assert.ElementsMatch(t, base, slices.Collect(maps.Keys(space)))
	want := append(slices.Clone(base), ToolSaveFile, ToolListFiles, ToolShareFile,
		ToolCreateArtifact, ToolReadArtifact, ToolUpdateArtifact, ToolListArtifacts, ToolReadArtifactGuide, ToolScreenshotArtifact,
		ToolOryn, ToolComputerStatus, ToolComputerUpload, ToolComputerDownload, ToolComputerRecord)
	assert.ElementsMatch(t, want, slices.Collect(maps.Keys(asst)))
	assert.Equal(t, "essential", asst[ToolOryn]["loadMode"])
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

// 系统提示词：助手的能力边界换成确认卡一句，第三层用法指向 oryn 技能、不再提已删除的工具；Space 协调者保持原句。
func TestAssistantPromptThirdLayer(t *testing.T) {
	a := SystemPrompt(nil, "", "T-1", true)
	assert.Contains(t, a, "\n"+assistantLimits+"\n")
	assert.NotContains(t, a, spaceLimits)
	assert.Contains(t, a, "- Use get_stats for questions about usage and cost.\n- Runner threads run on the owner's own machines")
	assert.Contains(t, a, "send_message with space")
	assert.Contains(t, a, "read skill://oryn")
	removed := regexp.MustCompile(`\b(update_thread|fork_thread|wake_thread|list_runners|delete_thread|create_project|update_project|delete_project|list_spaces|create_space|update_space|delete_space|list_triggers|set_trigger|delete_trigger|list_orbs|pause_orb|wake_orb|delete_orb|list_boxes|get_box|create_box|start_box|stop_box|set_box_schedule|set_box_app_visibility|delete_box|share_thread|list_shares|revoke_share|share_portal|unshare_portal|publish_artifact|unpublish_artifact|\w+_project_config|list_deleted_artifacts|restore_artifact)\b`)
	assert.Empty(t, removed.FindAllString(a, -1), "提示词不提已删除的工具")
	sp := SystemPrompt(&apigen.Space{Name: "S"}, "", "T-2", true)
	assert.Contains(t, sp, "\n"+spaceLimits+"\n")
	assert.Contains(t, sp, "- Use get_stats for questions about usage and cost.\n\nRules:")
	assert.NotContains(t, sp, "oryn")
	assert.NotContains(t, sp, "confirmation")
}
