package coord

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/boxes"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// fakeBoxes 是测试用的 Boxes：box 按 id 存在内存里，按 id 或名字查找，记下写操作与操作者。
type fakeBoxes struct {
	byID    map[string]*apigen.Box
	order   []string
	calls   []string
	actors  []boxes.Actor
	deleted []string
	// target 是 ThreadTarget 的结果（orb id 与 cwd）。
	targetOrb string
}

func newFakeBoxes() *fakeBoxes { return &fakeBoxes{byID: map[string]*apigen.Box{}} }

func (f *fakeBoxes) add(id, name string) {
	f.byID[id] = &apigen.Box{ID: id, Name: name, Apps: []apigen.BoxApp{{Name: "hello"}}}
	f.order = append(f.order, id)
}

func (f *fakeBoxes) find(ref string) (*apigen.Box, error) {
	if b, ok := f.byID[ref]; ok {
		return b, nil
	}
	for _, id := range f.order {
		if b, ok := f.byID[id]; ok && b.Name == ref {
			return b, nil
		}
	}
	return nil, &boxes.Error{Code: boxes.CodeBoxNotFound, Message: "box " + ref + " not found"}
}

func (f *fakeBoxes) rec(op string, by boxes.Actor) {
	f.calls = append(f.calls, op)
	f.actors = append(f.actors, by)
}

func (f *fakeBoxes) List(context.Context) ([]apigen.Box, error) {
	var out []apigen.Box
	for _, id := range f.order {
		if b, ok := f.byID[id]; ok {
			out = append(out, *b)
		}
	}
	return out, nil
}

func (f *fakeBoxes) Get(_ context.Context, ref string) (apigen.Box, error) {
	b, err := f.find(ref)
	if err != nil {
		return apigen.Box{}, err
	}
	return *b, nil
}

func (f *fakeBoxes) Create(_ context.Context, name, provider, typ string, by boxes.Actor) (apigen.Box, error) {
	f.rec("create "+name+" "+provider+" "+typ, by)
	f.add("box_"+name, name)
	return *f.byID["box_"+name], nil
}

func (f *fakeBoxes) Start(_ context.Context, ref string, by boxes.Actor) (apigen.Box, error) {
	b, err := f.find(ref)
	if err != nil {
		return apigen.Box{}, err
	}
	f.rec("start "+b.ID, by)
	return *b, nil
}

func (f *fakeBoxes) Stop(_ context.Context, ref string, by boxes.Actor) (apigen.Box, error) {
	b, err := f.find(ref)
	if err != nil {
		return apigen.Box{}, err
	}
	f.rec("stop "+b.ID, by)
	return *b, nil
}

func (f *fakeBoxes) SetSchedule(_ context.Context, ref string, sch *apigen.BoxSchedule, by boxes.Actor) (apigen.Box, error) {
	b, err := f.find(ref)
	if err != nil {
		return apigen.Box{}, err
	}
	if sch == nil {
		f.rec("schedule "+b.ID+" clear", by)
	} else {
		f.rec("schedule "+b.ID+" "+sch.On+"-"+sch.Off+" "+sch.Tz, by)
	}
	return *b, nil
}

func (f *fakeBoxes) SetPublic(_ context.Context, ref, app string, public bool, by boxes.Actor) (apigen.BoxApp, error) {
	b, err := f.find(ref)
	if err != nil {
		return apigen.BoxApp{}, err
	}
	if public {
		f.rec("public "+b.ID+" "+app, by)
	} else {
		f.rec("private "+b.ID+" "+app, by)
	}
	return apigen.BoxApp{Name: app, Public: public}, nil
}

func (f *fakeBoxes) Delete(_ context.Context, ref string, by boxes.Actor) error {
	b, err := f.find(ref)
	if err != nil {
		return err
	}
	f.rec("delete "+b.ID, by)
	f.deleted = append(f.deleted, b.ID)
	delete(f.byID, b.ID)
	return nil
}

func (f *fakeBoxes) ThreadTarget(_ context.Context, boxRef, app string) (string, string, error) {
	if _, err := f.find(boxRef); err != nil {
		return "", "", err
	}
	if app == "" {
		return f.targetOrb, "~", nil
	}
	return f.targetOrb, "~/apps/" + app + "/current", nil
}

// box 工具只出现在全局助手的工具表里；全部工具名在助手的工具表里唯一。
func TestBoxToolDefs(t *testing.T) {
	names := func(defs []apigen.OmpFrame) map[string]int {
		out := map[string]int{}
		for _, d := range defs {
			out[d["name"].(string)]++
		}
		return out
	}
	space := names(ToolDefs(ToolSet{InSpace: true}))
	asst := names(ToolDefs(ToolSet{Memory: true, Main: true}))
	for _, n := range []string{ToolListBoxes, ToolGetBox, ToolCreateBox, ToolStartBox, ToolStopBox, ToolSetBoxSchedule,
		ToolSetBoxAppVisibility, ToolDeleteBox} {
		assert.Contains(t, asst, n)
		assert.NotContains(t, space, n)
	}
	for n, c := range asst {
		assert.Equal(t, 1, c, "工具名 %s 重复", n)
	}
	for _, a := range []apigen.AutomationActionAction{apigen.ActionCreateBox, apigen.ActionStartBox, apigen.ActionStopBox,
		apigen.ActionSetBoxSchedule, apigen.ActionSetBoxAppVisibility, apigen.ActionDeleteBox} {
		assert.Contains(t, asst, string(a), "审计动作名 = 工具名")
	}
}

// box 工具：只读工具包不可信包裹；写工具直接执行、带助手操作者并写审计；boxes.Error 的码原样出现在结果与审计里；
// Space 协调者没有这些工具；未注入 Boxes 时回 coordinator_unavailable。
func TestBoxTools(t *testing.T) {
	e := newEnv(t, true)
	asst := e.assistant()
	chat := e.spaceChat(e.space("Alpha", ""))

	assert.Equal(t, CodeCoordinatorUnavailable, errCodeOf(t, e.call(asst, ToolListBoxes, `{}`, true)))
	fb := newFakeBoxes()
	e.c.SetBoxes(fb)
	assert.Equal(t, "unknown_tool", errCodeOf(t, e.call(chat, ToolListBoxes, `{}`, true)), "Space 协调者没有 box 工具")
	assert.Equal(t, "unknown_tool", errCodeOf(t, e.call(chat, ToolDeleteBox, `{"box":"b1"}`, true)))

	m := details(t, e.call(asst, ToolCreateBox, `{"name":"b1","type":"small"}`, true))
	assert.Equal(t, "b1", m["box"].(map[string]any)["name"])
	assert.Equal(t, []string{"create b1  small"}, fb.calls)
	assert.Equal(t, boxes.Actor{Kind: boxes.ActorAssistant, ThreadID: asst.ID}, fb.actors[0])
	a := e.latest()
	assert.Equal(t, ToolCreateBox, a.Action)
	assert.True(t, a.Ok)
	assert.Equal(t, "b1 (box_b1)", a.Summary)

	lr := e.call(asst, ToolListBoxes, `{}`, true)
	assert.True(t, strings.HasPrefix(lr.Text, "<untrusted-thread-content"), "box 与 app 的名字包在不可信包裹里")
	require.Len(t, details(t, lr)["boxes"], 1)
	gr := e.call(asst, ToolGetBox, `{"box":"b1"}`, true)
	assert.True(t, strings.HasPrefix(gr.Text, "<untrusted-thread-content"))
	assert.Equal(t, "box_b1", details(t, gr)["box"].(map[string]any)["id"])

	details(t, e.call(asst, ToolStopBox, `{"box":"b1"}`, true))
	details(t, e.call(asst, ToolStartBox, `{"box":"b1"}`, true))
	details(t, e.call(asst, ToolSetBoxSchedule, `{"box":"b1","on":"05:00","off":"23:00","tz":"Europe/Berlin"}`, true))
	details(t, e.call(asst, ToolSetBoxSchedule, `{"box":"b1","clear":true}`, true))
	am := details(t, e.call(asst, ToolSetBoxAppVisibility, `{"box":"b1","app":"hello","public":false}`, true))
	assert.Equal(t, false, am["app"].(map[string]any)["public"])
	assert.Equal(t, []string{"create b1  small", "stop box_b1", "start box_b1", "schedule box_b1 05:00-23:00 Europe/Berlin",
		"schedule box_b1 clear", "private box_b1 hello"}, fb.calls)
	a = e.latest()
	assert.Equal(t, ToolSetBoxAppVisibility, a.Action)
	assert.Equal(t, "b1 · hello · private", a.Summary)

	// 参数不全与 boxes.Error：码原样出现在工具结果与审计里
	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolSetBoxSchedule, `{"box":"b1","on":"05:00"}`, true)))
	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolSetBoxAppVisibility, `{"box":"b1","app":"hello"}`, true)))
	r := e.call(asst, ToolStartBox, `{"box":"nope"}`, true)
	assert.Equal(t, boxes.CodeBoxNotFound, errCodeOf(t, r))
	assert.Contains(t, r.Text, "box nope not found")
	a = e.latest()
	assert.False(t, a.Ok)
	assert.Equal(t, boxes.CodeBoxNotFound, deref(a.Error))
	assert.Equal(t, boxes.CodeBoxNotFound, errCodeOf(t, e.call(asst, ToolGetBox, `{"box":"nope"}`, true)))
	assert.Len(t, fb.calls, 6, "失败的调用不改 box")
}

// delete_box 走确认卡：批准之前不删；卡片挂出之后把 box 改名（并有一台新 box 用了旧名字），批准时删的仍是原来那台；
// box 已不在时执行失败记 not_found。
func TestDeleteBoxConfirm(t *testing.T) {
	e := newEnv(t, true)
	asst := e.assistant()
	fb := newFakeBoxes()
	fb.add("box_A", "b1")
	e.c.SetBoxes(fb)

	r := e.call(asst, ToolDeleteBox, `{"box":"b1"}`, true)
	assert.Empty(t, fb.deleted, "批准之前不删除")
	fb.byID["box_A"].Name = "renamed"
	fb.add("box_B", "b1")
	e.approve(asst, r)
	assert.Equal(t, []string{"box_A"}, fb.deleted, "删的是挂卡时解析出的那台")
	assert.Contains(t, fb.byID, "box_B")
	assert.Equal(t, boxes.Actor{Kind: boxes.ActorAssistant, ThreadID: asst.ID}, fb.actors[len(fb.actors)-1])
	a := e.latest()
	assert.Equal(t, ToolDeleteBox, a.Action)
	assert.Equal(t, "owner", a.Origin)
	assert.True(t, a.Ok)

	// 挂卡之后 box 被别处删掉：执行时重新取，记 not_found
	r = e.call(asst, ToolDeleteBox, `{"box":"b1"}`, true)
	delete(fb.byID, "box_B")
	e.approve(asst, r)
	assert.Equal(t, []string{"box_A"}, fb.deleted)
	a = e.latest()
	assert.False(t, a.Ok)
	assert.Equal(t, CodeNotFound, deref(a.Error))

	// 挂卡之前就失败（不存在）：记失败审计
	assert.Equal(t, boxes.CodeBoxNotFound, errCodeOf(t, e.call(asst, ToolDeleteBox, `{"box":"nope"}`, true)))
	assert.False(t, e.latest().Ok)
}

// create_thread 带 box（+ app）：经 ThreadTarget 解析出 orb 与 cwd；与 runner / orbId / provider 同给被拒；app 要 box。
func TestCreateThreadOnBox(t *testing.T) {
	e := newEnv(t, true)
	asst := e.assistant()
	assert.Equal(t, CodeCoordinatorUnavailable, errCodeOf(t, e.call(asst, ToolCreateThread, `{"prompt":"go","box":"b1"}`, true)))

	orb, err := e.orbs.Create(e.ctx, threads.OrbCreate{})
	require.NoError(t, err)
	_, err = e.st.DB.ExecContext(e.ctx, `UPDATE orbs SET box = 1 WHERE id = ?`, orb.ID)
	require.NoError(t, err)
	fb := newFakeBoxes()
	fb.add("box_A", "b1")
	fb.targetOrb = orb.ID
	e.c.SetBoxes(fb)

	r := details(t, e.call(asst, ToolCreateThread, `{"prompt":"go","box":"b1","app":"hello"}`, true))
	row := e.row(r["threadId"].(string))
	assert.Equal(t, orb.ID, deref(row.OrbID))
	assert.Equal(t, "~/apps/hello/current", deref(row.Cwd))
	r = details(t, e.call(asst, ToolCreateThread, `{"prompt":"go","box":"b1"}`, true))
	assert.Equal(t, "~", deref(e.row(r["threadId"].(string)).Cwd))

	for _, args := range []string{
		`{"prompt":"go","box":"b1","runner":"laptop","dir":"/w/app"}`,
		`{"prompt":"go","box":"b1","orbId":"` + orb.ID + `"}`,
		`{"prompt":"go","box":"b1","provider":"docker"}`,
		`{"prompt":"go","app":"hello"}`,
	} {
		assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolCreateThread, args, true)), args)
	}
	assert.Equal(t, boxes.CodeBoxNotFound, errCodeOf(t, e.call(asst, ToolCreateThread, `{"prompt":"go","box":"nope"}`, true)))
	// 不经 box 直接挂到 box 的 orb：threads 拒绝
	assert.Equal(t, threads.CodeOrbIsBox, errCodeOf(t, e.call(asst, ToolCreateThread, `{"prompt":"go","orbId":"`+orb.ID+`"}`, true)))
}
