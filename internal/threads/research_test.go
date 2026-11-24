package threads

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// M26：研究员对话只能是隐藏的普通助手对话；每种至多一个。
func TestCreateCoordinatorResearch(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	_, err := f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindAssistant, OrbID: orb, Research: "web"})
	require.Error(t, err, "不隐藏")
	_, err = f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindSpace, OrbID: orb, Hidden: true, Research: "web"})
	require.Error(t, err, "不是助手对话")
	_, err = f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindAssistant, OrbID: orb, Hidden: true, Research: "other"})
	require.Error(t, err, "未知角色")

	out, err := f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindAssistant, OrbID: orb, Hidden: true, Research: "web"})
	require.NoError(t, err)
	id, ok, err := f.s.ResearchThread(f.ctx, "web")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, out.ID, id)
	_, ok, err = f.s.ResearchThread(f.ctx, "private")
	require.NoError(t, err)
	assert.False(t, ok)
	_, err = f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindAssistant, OrbID: orb, Hidden: true, Research: "web"})
	require.Error(t, err, "每种至多一个")
}

// 研究员对话每次启动都先 clear_context：上一条运行的上下文不进下一条。
func TestInitFramesResearch(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	out, err := f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindAssistant, OrbID: orb, Hidden: true, Research: "private"})
	require.NoError(t, err)
	th, err := f.s.get(f.ctx, out.ID)
	require.NoError(t, err)
	frames := initFrames(th, &AgentProfile{}, false, true)
	assert.Equal(t, []string{"open_session", "clear_context", "set_event_filter", "set_host_tools"}, frameTypes(frames))
	assert.Equal(t, []apigen.OmpFrame{}, frames[3]["tools"], "空工具表照样下发")
	assert.Equal(t, []string{"open_session", "set_event_filter", "set_host_tools"}, frameTypes(initFrames(th, &AgentProfile{}, false, false)))
}

// 研究员对话永远不会被当作备用对话认领。
func TestSpareIgnoresResearcher(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	r, err := f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindAssistant, OrbID: orb, Hidden: true, Research: "web"})
	require.NoError(t, err)
	_, ok, err := f.s.FindSpare(f.ctx, orb)
	require.NoError(t, err)
	assert.False(t, ok)
	th, err := f.s.get(f.ctx, r.ID)
	require.NoError(t, err)
	attach(th, fakeAgent(16), apigen.AgentStatusReady)
	_, err = f.s.ClaimSpare(f.ctx, r.ID, CoordinatorParams{Prompt: "hi"})
	require.Error(t, err)
	row, err := f.st.Q.GetThread(f.ctx, r.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, row.Hidden, "认领不动研究员对话")
}

// agent.start 的应答报回变体：启动中未知；幂等应答（已有监督器）报回旧变体。
func TestAgentVariantReported(t *testing.T) {
	f := newFixture(t, Config{})
	o := newCtlOrbs(t, f.orbs)
	f.s.orbs = o
	coord := newFakeCoord()
	coord.prof.Variant = string(apigen.AgentVariantMainRestricted)
	f.s.SetCoordinator(coord)
	o.features["coordinator"] = true
	th := f.coordThread(f.orb("ready"), KindAssistant, StateIdle)

	_, known := f.s.AgentVariant(f.ctx, th.id)
	assert.False(t, known, "从没启动过")
	f.s.startAgent(f.ctx, th.id)
	c := o.nextCall(t)
	require.Equal(t, "agent.start", c.method)
	var p apigen.LinkCtlAgentStartParams
	require.NoError(t, json.Unmarshal(c.params, &p))
	require.NotNil(t, p.Variant)
	assert.Equal(t, apigen.AgentVariantMainRestricted, *p.Variant)
	assert.Nil(t, p.Web)
	_, known = f.s.AgentVariant(f.ctx, th.id)
	assert.False(t, known, "应答到了但流还没接上")
	attach(th, fakeAgent(16), apigen.AgentStatusReady)
	v, known := f.s.AgentVariant(f.ctx, th.id)
	assert.True(t, known)
	assert.Equal(t, "main_restricted", v)

	// 启动中：未知
	th.mu.Lock()
	th.starting = true
	th.mu.Unlock()
	_, known = f.s.AgentVariant(f.ctx, th.id)
	assert.False(t, known)
	th.mu.Lock()
	th.starting = false
	th.mu.Unlock()

	// 以完整表再 agent.start，orbd 幂等返回旧监督器的变体
	coord.prof.Variant = ""
	old := "main_restricted"
	o.mu.Lock()
	o.oldVariant = &old
	o.mu.Unlock()
	f.s.startAgent(f.ctx, th.id)
	c = o.nextCall(t)
	require.Equal(t, "agent.start", c.method)
	p = apigen.LinkCtlAgentStartParams{}
	require.NoError(t, json.Unmarshal(c.params, &p))
	assert.Nil(t, p.Variant)
	v, known = f.s.AgentVariant(f.ctx, th.id)
	assert.True(t, known)
	assert.Equal(t, "main_restricted", v, "在跑的仍是旧进程")
}

// AgentCold：没有进程且没有待下发的命令时为真；投递排队后为假。
func TestAgentCold(t *testing.T) {
	f := newFixture(t, Config{})
	th := f.coordThread(f.orb("ready"), KindAssistant, StateIdle)
	assert.True(t, f.s.AgentCold(f.ctx, th.id), "从没起过")
	attach(th, fakeAgent(16), apigen.AgentStatusReady)
	assert.False(t, f.s.AgentCold(f.ctx, th.id), "在跑")

	th.mu.Lock()
	th.agent, th.agentStatus = nil, apigen.AgentStatusExited
	th.mu.Unlock()
	assert.True(t, f.s.AgentCold(f.ctx, th.id), "停掉了")
	th.mu.Lock()
	th.unsynced = true
	th.mu.Unlock()
	assert.False(t, f.s.AgentCold(f.ctx, th.id), "orbd 正在重启它")
	th.mu.Lock()
	th.unsynced = false
	th.mu.Unlock()

	_, err := f.s.Deliver(f.ctx, th.id, AutoMessage{Text: "[Oryn test] x", Tag: "t1"}, nil)
	require.NoError(t, err)
	assert.False(t, f.s.AgentCold(f.ctx, th.id), "有排队的投递")
	assert.False(t, f.s.AgentCold(f.ctx, "T-missing"))
}

// 研究员对话不能归档、标记已交付或删除。
func TestResearcherCannotBeArchived(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	r, err := f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindAssistant, OrbID: orb, Hidden: true, Research: "private"})
	require.NoError(t, err)
	yes := true
	_, err = f.s.Patch(f.ctx, r.ID, apigen.ThreadPatch{Archived: &yes})
	var te *Error
	require.ErrorAs(t, err, &te)
	assert.Equal(t, CodeCoordinatorRestricted, te.Code)
	_, err = f.s.Patch(f.ctx, r.ID, apigen.ThreadPatch{Shipped: &yes})
	require.ErrorAs(t, err, &te)
	assert.Equal(t, CodeCoordinatorRestricted, te.Code)
	err = f.s.Delete(f.ctx, r.ID)
	require.ErrorAs(t, err, &te)
	assert.Equal(t, CodeCoordinatorRestricted, te.Code)
}
