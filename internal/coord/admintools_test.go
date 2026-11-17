package coord

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/github"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// fakeProjects 是测试用的 Projects：Create / Update 直接写库（与 REST 同一张表），Delete 只记下调用。
type fakeProjects struct {
	e       *env
	updates []apigen.ProjectPatch
	deleted []string
}

func (f *fakeProjects) Create(ctx context.Context, in apigen.ProjectCreate) (apigen.Project, error) {
	id := f.e.project(in.Name)
	return apigen.Project{ID: id, Name: in.Name, RepoURL: in.RepoURL}, nil
}

func (f *fakeProjects) Update(_ context.Context, id string, p apigen.ProjectPatch) (apigen.Project, error) {
	f.updates = append(f.updates, p)
	out := apigen.Project{ID: id, Name: "renamed"}
	if p.Archived != nil && *p.Archived {
		out.ArchivedAt = new(int64(1))
	}
	return out, nil
}

func (f *fakeProjects) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

// orbRecMachines 在 fakeMachines 之上记下 orb 操作并返回预置的 orb 列表。
type orbRecMachines struct {
	fakeMachines
	list  []apigen.Orb
	calls []string
}

func (m *orbRecMachines) ListOrbs(context.Context) ([]apigen.Orb, error) { return m.list, nil }
func (m *orbRecMachines) Pause(_ context.Context, id, mode string) error {
	m.calls = append(m.calls, "pause "+id+" "+mode)
	return nil
}
func (m *orbRecMachines) Wake(_ context.Context, id string) error {
	m.calls = append(m.calls, "wake "+id)
	return nil
}
func (m *orbRecMachines) Delete(_ context.Context, id string) error {
	m.calls = append(m.calls, "delete "+id)
	return nil
}

// latest 返回最新的一条审计。
func (e *env) latest() db.CoordAction {
	e.t.Helper()
	acts := e.actions()
	require.NotEmpty(e.t, acts)
	return acts[0]
}

// approve 让主人批准确认卡 id。
func (e *env) approve(conv apigen.Thread, r threads.HostToolResult) {
	e.t.Helper()
	m := details(e.t, r)
	require.Equal(e.t, "awaiting_owner", m["status"])
	id := m["confirmationId"].(string)
	e.c.UIAnswered(e.ctx, conv.ID, id, answer(id, true))
}

// 管理工具只出现在助手的工具表里；list_projects 的 archived 参数只给助手。
func TestAdminToolDefs(t *testing.T) {
	names := func(defs []apigen.OmpFrame) map[string]apigen.OmpFrame {
		out := map[string]apigen.OmpFrame{}
		for _, d := range defs {
			out[d["name"].(string)] = d
		}
		return out
	}
	space, asst := names(ToolDefs(ToolSet{InSpace: true})), names(ToolDefs(ToolSet{}))
	for _, n := range []string{ToolCreateProject, ToolUpdateProject, ToolDeleteProject, ToolListSpaces, ToolCreateSpace, ToolUpdateSpace,
		ToolDeleteSpace, ToolListTriggers, ToolSetTrigger, ToolDeleteTrigger, ToolListOrbs, ToolPauseOrb, ToolWakeOrb, ToolDeleteOrb} {
		assert.Contains(t, asst, n)
		assert.NotContains(t, space, n)
	}
	props := func(d apigen.OmpFrame) obj { return d["parameters"].(obj)["properties"].(obj) }
	assert.Contains(t, props(asst[ToolListProjects]), "archived")
	assert.Empty(t, props(space[ToolListProjects]))
}

// 项目工具：create / update 直接执行并写审计；delete 走确认卡，批准后才删；list_projects 的 archived；
// Space 协调者没有这些工具；未注入 Projects 时回 coordinator_unavailable。
func TestProjectTools(t *testing.T) {
	e := newEnv(t, true)
	asst := e.assistant()
	sp := e.space("Alpha", "")
	chat := e.spaceChat(sp)

	assert.Equal(t, CodeCoordinatorUnavailable, errCodeOf(t, e.call(asst, ToolCreateProject, `{"name":"Beta"}`, true)))
	fp := &fakeProjects{e: e}
	e.c.SetProjects(fp)
	assert.Equal(t, "unknown_tool", errCodeOf(t, e.call(chat, ToolCreateProject, `{"name":"Beta"}`, true)))

	m := details(t, e.call(asst, ToolCreateProject, `{"name":"Beta","repoUrl":"https://example.com/b.git"}`, true))
	prj := m["project"].(map[string]any)
	assert.Equal(t, "Beta", prj["name"])
	assert.Equal(t, false, prj["archived"])
	a := e.latest()
	assert.Equal(t, ToolCreateProject, a.Action)
	assert.True(t, a.Ok)
	assert.Equal(t, "Beta ("+prj["id"].(string)+")", a.Summary)

	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolUpdateProject, `{"project":"Beta"}`, true)))
	assert.Equal(t, CodeNotFound, errCodeOf(t, e.call(asst, ToolUpdateProject, `{"project":"Nope","archived":true}`, true)))
	m = details(t, e.call(asst, ToolUpdateProject, `{"project":"Beta","archived":true,"warmOrbs":2,"setupScript":""}`, true))
	assert.Equal(t, true, m["project"].(map[string]any)["archived"])
	require.Len(t, fp.updates, 1)
	assert.True(t, *fp.updates[0].Archived)
	assert.Equal(t, 2, *fp.updates[0].WarmOrbs)
	assert.Equal(t, "", *fp.updates[0].SetupScript)
	assert.Equal(t, "Beta ("+prj["id"].(string)+") · archived, setupScript, warmOrbs", e.latest().Summary)

	// list_projects：缺省只列未归档；助手传 archived 时含已归档且每项带 archived；Space 协调者的结果没有 archived
	_, err := e.st.DB.ExecContext(e.ctx, `UPDATE projects SET archived_at = 5 WHERE id = ?`, prj["id"])
	require.NoError(t, err)
	gamma := e.project("Gamma")
	list := details(t, e.call(asst, ToolListProjects, `{}`, true))["projects"].([]any)
	require.Len(t, list, 1)
	assert.Equal(t, gamma, list[0].(map[string]any)["id"])
	assert.Equal(t, false, list[0].(map[string]any)["archived"])
	list = details(t, e.call(asst, ToolListProjects, `{"archived":true}`, true))["projects"].([]any)
	require.Len(t, list, 2)
	assert.Equal(t, true, list[0].(map[string]any)["archived"], "Beta")
	list = details(t, e.call(chat, ToolListProjects, `{"archived":true}`, true))["projects"].([]any)
	require.Len(t, list, 1, "Space 协调者的 list_projects 不变")
	assert.NotContains(t, list[0].(map[string]any), "archived")

	// delete_project：挂卡、不写审计；批准后执行一次，审计 origin=owner
	n := len(e.actions())
	r := e.call(asst, ToolDeleteProject, `{"project":"Gamma"}`, false)
	assert.Len(t, e.actions(), n)
	assert.Empty(t, fp.deleted)
	e.approve(asst, r)
	assert.Equal(t, []string{gamma}, fp.deleted)
	a = e.latest()
	assert.Equal(t, ToolDeleteProject, a.Action)
	assert.Equal(t, "owner", a.Origin)
	assert.True(t, a.Ok)
	assert.Equal(t, CodeNotFound, errCodeOf(t, e.call(asst, ToolDeleteProject, `{"project":"Nope"}`, true)))
	assert.False(t, e.latest().Ok, "挂卡之前失败的请求记失败审计")
}

// Space 工具：create / update / list 直接执行（审计目标是 Space 的协调对话）；delete 走确认卡，批准后删除 Space。
func TestSpaceTools(t *testing.T) {
	e := newEnv(t, true)
	asst := e.assistant()
	prj := e.project("Beta")

	m := details(t, e.call(asst, ToolCreateSpace, `{"name":"Ops","goal":"keep it running","project":"Beta"}`, true))
	sp := m["space"].(map[string]any)
	assert.Equal(t, "Beta", sp["project"])
	threadID := sp["threadId"].(string)
	assert.Equal(t, []any{threadID}, m["threadIds"])
	a := e.latest()
	assert.Equal(t, ToolCreateSpace, a.Action)
	assert.Equal(t, threadID, deref(a.TargetThreadID))
	assert.Equal(t, CodeNotFound, errCodeOf(t, e.call(asst, ToolCreateSpace, `{"name":"X","project":"Nope"}`, true)))

	m = details(t, e.call(asst, ToolUpdateSpace, `{"space":"Ops","goal":"new goal","project":""}`, true))
	sp = m["space"].(map[string]any)
	assert.Equal(t, "new goal", sp["goal"])
	assert.NotContains(t, sp, "project")
	got, err := e.st.Q.GetSpace(e.ctx, sp["id"].(string))
	require.NoError(t, err)
	assert.Nil(t, got.ProjectID)
	assert.Equal(t, CodeSpaceNotFound, errCodeOf(t, e.call(asst, ToolUpdateSpace, `{"space":"Nope","goal":"x"}`, true)))
	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolUpdateSpace, `{"space":"Ops"}`, true)))

	r := e.call(asst, ToolListSpaces, `{}`, true)
	assert.Contains(t, r.Text, "<untrusted-thread-content")
	list := details(t, r)["spaces"].([]any)
	require.Len(t, list, 1)
	assert.Equal(t, "Ops", list[0].(map[string]any)["name"])
	_ = prj

	e.approve(asst, e.call(asst, ToolDeleteSpace, `{"space":"Ops"}`, true))
	_, err = e.st.Q.GetSpace(e.ctx, sp["id"].(string))
	assert.Error(t, err, "批准后 Space 已删除")
	a = e.latest()
	assert.Equal(t, ToolDeleteSpace, a.Action)
	assert.Equal(t, "owner", a.Origin)
	assert.True(t, a.Ok)
	assert.Equal(t, threadID, deref(a.TargetThreadID))
}

// 触发规则工具（真实的 github.Service）：未绑定 GitHub 的项目建规则得到 github_not_bound；绑定后 options 并入输入、
// 未知键被拒；按 id 修改；不属于该项目的规则 not_found；删除走确认卡。
func TestTriggerTools(t *testing.T) {
	e := newEnv(t, true)
	asst := e.assistant()
	assert.Equal(t, CodeCoordinatorUnavailable, errCodeOf(t, e.call(asst, ToolListTriggers, `{"project":"Beta"}`, true)))
	e.c.SetTriggers(github.New(github.Config{}, e.st, e.th, nil, nil))
	prj := e.project("Beta")
	other := e.project("Other")

	create := `{"project":"Beta","name":"triage","event":"issue_opened","prompt":"triage it","options":{"maintainers":["alice"],"name":"ignored"}}`
	assert.Equal(t, "github_not_bound", errCodeOf(t, e.call(asst, ToolSetTrigger, create, true)))
	assert.Equal(t, "github_not_bound", deref(e.latest().Error))

	_, err := e.st.DB.ExecContext(e.ctx, `UPDATE projects SET github_repo_id = 42 WHERE id IN (?, ?)`, prj, other)
	require.NoError(t, err)
	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolSetTrigger, `{"project":"Beta","name":"x","event":"issue_opened","prompt":"p","options":{"bogus":1}}`, true)))
	rule := details(t, e.call(asst, ToolSetTrigger, create, true))["trigger"].(map[string]any)
	assert.Equal(t, "triage", rule["name"], "显式字段覆盖 options")
	assert.Equal(t, []any{"alice"}, rule["maintainers"])
	id := rule["id"].(string)
	a := e.latest()
	assert.Equal(t, ToolSetTrigger, a.Action)
	assert.Equal(t, "create · Beta · triage ("+id+") · issue_opened", a.Summary)

	rule = details(t, e.call(asst, ToolSetTrigger, `{"project":"Beta","id":"`+id+`","enabled":false}`, true))["trigger"].(map[string]any)
	assert.Equal(t, false, rule["enabled"])
	assert.Equal(t, "triage it", rule["prompt"])
	assert.Equal(t, CodeNotFound, errCodeOf(t, e.call(asst, ToolSetTrigger, `{"project":"Other","id":"`+id+`","enabled":true}`, true)))

	r := e.call(asst, ToolListTriggers, `{"project":"Beta"}`, true)
	assert.Contains(t, r.Text, "<untrusted-thread-content")
	assert.Len(t, details(t, r)["triggers"], 1)

	e.approve(asst, e.call(asst, ToolDeleteTrigger, `{"id":"`+id+`"}`, true))
	_, err = e.st.Q.GetGithubTrigger(e.ctx, id)
	assert.Error(t, err, "批准后规则已删除")
	assert.Equal(t, ToolDeleteTrigger, e.latest().Action)
	assert.Equal(t, CodeNotFound, errCodeOf(t, e.call(asst, ToolDeleteTrigger, `{"id":"ght_nope"}`, true)))
}

// orb 工具：list 经 Machines；pause / wake 直接执行并写审计；delete 走确认卡；协调宿主、预热池 orb、助手的电脑
// 作目标一律 invalid_target（确认执行时也重新校验）。
func TestOrbTools(t *testing.T) {
	e := newEnv(t, true)
	asst := e.assistant()
	assert.Equal(t, CodeCoordinatorUnavailable, errCodeOf(t, e.call(asst, ToolListOrbs, `{}`, true)))
	work, err := e.orbs.Create(e.ctx, threads.OrbCreate{})
	require.NoError(t, err)
	pool, err := e.orbs.Create(e.ctx, threads.OrbCreate{})
	require.NoError(t, err)
	comp, err := e.orbs.Create(e.ctx, threads.OrbCreate{})
	require.NoError(t, err)
	_, err = e.st.DB.ExecContext(e.ctx, `UPDATE orbs SET pool_state = 'idle' WHERE id = ?`, pool.ID)
	require.NoError(t, err)
	_, err = e.st.DB.ExecContext(e.ctx, `UPDATE orbs SET computer = 1 WHERE id = ?`, comp.ID)
	require.NoError(t, err)

	yes := true
	m := &orbRecMachines{list: []apigen.Orb{
		{ID: work.ID, State: apigen.OrbStateReady, Type: "medium", ThreadIds: []string{"T-1"}},
		{ID: e.coordOrb, State: apigen.OrbStateReady, Type: "host", Coordinator: &yes},
		{ID: "orb_x", State: apigen.OrbStateFrozen, Type: "small"},
	}}
	e.c.SetMachines(m)

	list := details(t, e.call(asst, ToolListOrbs, `{}`, true))["orbs"].([]any)
	require.Len(t, list, 3)
	assert.Equal(t, true, list[1].(map[string]any)["coordinator"])
	list = details(t, e.call(asst, ToolListOrbs, `{"state":"frozen"}`, true))["orbs"].([]any)
	require.Len(t, list, 1)
	assert.Equal(t, "orb_x", list[0].(map[string]any)["id"])
	assert.Equal(t, []any{}, list[0].(map[string]any)["threadIds"])

	for _, id := range []string{e.coordOrb, pool.ID, comp.ID} {
		assert.Equal(t, CodeInvalidTarget, errCodeOf(t, e.call(asst, ToolPauseOrb, `{"orbId":"`+id+`","mode":"freeze"}`, true)), id)
		assert.Equal(t, CodeInvalidTarget, errCodeOf(t, e.call(asst, ToolWakeOrb, `{"orbId":"`+id+`"}`, true)), id)
		assert.Equal(t, CodeInvalidTarget, errCodeOf(t, e.call(asst, ToolDeleteOrb, `{"orbId":"`+id+`"}`, true)), id)
	}
	assert.Equal(t, CodeNotFound, errCodeOf(t, e.call(asst, ToolWakeOrb, `{"orbId":"orb_nope"}`, true)))
	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolPauseOrb, `{"orbId":"`+work.ID+`","mode":"sleep"}`, true)))
	assert.Empty(t, m.calls)

	details(t, e.call(asst, ToolPauseOrb, `{"orbId":"`+work.ID+`","mode":"stop"}`, true))
	details(t, e.call(asst, ToolWakeOrb, `{"orbId":"`+work.ID+`"}`, true))
	assert.Equal(t, []string{"pause " + work.ID + " stop", "wake " + work.ID}, m.calls)
	a := e.latest()
	assert.Equal(t, ToolWakeOrb, a.Action)
	assert.True(t, a.Ok)
	assert.Equal(t, work.ID, a.Summary)

	r := e.call(asst, ToolDeleteOrb, `{"orbId":"`+work.ID+`"}`, true)
	assert.Len(t, m.calls, 2, "批准之前不删除")
	e.approve(asst, r)
	assert.Equal(t, "delete "+work.ID, m.calls[2])
	a = e.latest()
	assert.Equal(t, ToolDeleteOrb, a.Action)
	assert.Equal(t, "owner", a.Origin)

	// 挂卡之后目标变成了不可操作的 orb：执行时重新校验，记 failed
	r = e.call(asst, ToolDeleteOrb, `{"orbId":"`+work.ID+`"}`, true)
	_, err = e.st.DB.ExecContext(e.ctx, `UPDATE orbs SET computer = 1 WHERE id = ?`, work.ID)
	require.NoError(t, err)
	e.approve(asst, r)
	assert.Len(t, m.calls, 3)
	a = e.latest()
	assert.False(t, a.Ok)
	assert.Equal(t, CodeInvalidTarget, deref(a.Error))
}

// set_schedule{action: delete}：全局助手挂确认卡，批准后才删（审计 delete_schedule、origin=owner、带定时 id）；
// Space 协调者照旧直接删除。
func TestDeleteScheduleConfirm(t *testing.T) {
	e := newEnv(t, true)
	asst := e.assistant()
	sp := e.space("Alpha", e.project("alpha"))
	chat := e.spaceChat(sp)

	sc := details(t, e.call(asst, ToolSetSchedule, `{"action":"set","name":"tick","kind":"every","every":"1h","prompt":"p"}`, true))["schedule"].(map[string]any)
	id := sc["id"].(string)
	n := len(e.actions())
	r := e.call(asst, ToolSetSchedule, `{"action":"delete","scheduleId":"`+id+`"}`, false)
	assert.Len(t, e.actions(), n, "确认之前不写审计")
	_, err := e.sched.Get(e.ctx, id)
	require.NoError(t, err, "确认之前定时还在")
	e.approve(asst, r)
	_, err = e.sched.Get(e.ctx, id)
	assert.Error(t, err, "批准后定时已删除")
	a := e.latest()
	assert.Equal(t, string(apigen.ActionDeleteSchedule), a.Action)
	assert.Equal(t, "owner", a.Origin)
	assert.True(t, a.Ok)
	assert.Equal(t, id, deref(a.ScheduleID))
	assert.Equal(t, asst.ID, deref(a.TargetThreadID))

	other := details(t, e.call(chat, ToolSetSchedule, `{"action":"set","mode":"new_thread","name":"nightly","kind":"cron","cron":"0 3 * * *","prompt":"run"}`, true))["schedule"].(map[string]any)
	m := details(t, e.call(chat, ToolSetSchedule, `{"action":"delete","scheduleId":"`+other["id"].(string)+`"}`, false))
	assert.Equal(t, true, m["ok"])
	_, err = e.sched.Get(e.ctx, other["id"].(string))
	assert.Error(t, err, "Space 协调者直接删除")
	a = e.latest()
	assert.Equal(t, string(apigen.ActionDeleteSchedule), a.Action)
	assert.Equal(t, "auto", a.Origin)
}
