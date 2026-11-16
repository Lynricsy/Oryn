package threads

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// memOrbs 在任意 Orbs 之上按 orb 固定种类与 hello 特性（未登记的 orb：普通 orb、无特性）。
type memOrbs struct {
	Orbs
	kinds map[string]string
	feats map[string][]string
}

func (o *memOrbs) Features(orbID string) (string, []string) {
	kind := o.kinds[orbID]
	if kind == "" {
		kind = orbs.KindOrb
	}
	return kind, o.feats[orbID]
}

// fakeMemory 记录 AgentMemory 的调用；err 非 nil 时以它失败，否则按 project 回一个启用的参数。
type fakeMemory struct {
	mu    sync.Mutex
	calls []*string
	err   error
}

func (m *fakeMemory) AgentMemory(_ context.Context, _ string, projectID *string) (*apigen.LinkCtlAgentMemory, error) {
	m.mu.Lock()
	m.calls = append(m.calls, projectID)
	m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	if projectID == nil {
		return &apigen.LinkCtlAgentMemory{AutoRetain: false}, nil
	}
	p := "lc-" + *projectID
	return &apigen.LinkCtlAgentMemory{Project: &p, AutoRetain: true}, nil
}

func (f *fixture) projectWithID(id string) {
	f.t.Helper()
	require.NoError(f.t, f.st.Q.CreateProject(f.ctx, db.CreateProjectParams{ID: id, Name: id, CreatedAt: 1, UpdatedAt: 1}))
}

// orbIn 插入一台属于项目 projectID（空 = 无项目）的 ready orb。
func (f *fixture) orbIn(projectID string) string {
	f.t.Helper()
	id := f.orb("ready")
	if projectID != "" {
		_, err := f.st.DB.ExecContext(f.ctx, `UPDATE orbs SET project_id = ? WHERE id = ?`, projectID, id)
		require.NoError(f.t, err)
	}
	return id
}

// threadIn 在 orbID 上插入一个属于项目 projectID（空 = 无项目）的工作线程并加载。
func (f *fixture) threadIn(orbID, projectID string) *thread {
	f.t.Helper()
	id := ids.Thread()
	p := db.CreateThreadParams{Kind: "work", ID: id, OrbID: &orbID, TitleSource: "auto", State: StateIdle, SessionDir: sessionDirOf(id), CreatedAt: 1, UpdatedAt: 1}
	if projectID != "" {
		p.ProjectID = &projectID
	}
	require.NoError(f.t, f.st.Q.CreateThread(f.ctx, p))
	th, err := f.s.get(f.ctx, id)
	require.NoError(f.t, err)
	return th
}

// startParams 执行一次 startAgent，返回 orbd 收到的 agent.start 参数。
func startParams(t *testing.T, f *fixture, o *ctlOrbs, th *thread) apigen.LinkCtlAgentStartParams {
	t.Helper()
	f.s.startAgent(f.ctx, th.id)
	c := o.nextCall(t)
	require.Equal(t, "agent.start", c.method)
	var p apigen.LinkCtlAgentStartParams
	require.NoError(t, json.Unmarshal(c.params, &p))
	return p
}

// agent.start 的 memory：orb 声明了 memory 特性才查询；普通 orb 取 orb 的项目，runner 取线程自己的项目；
// 查询出错时不带 memory 照常启动；未配置 Memory 时不查询。
func TestMemoryAgentStartParams(t *testing.T) {
	mem := &fakeMemory{}
	f := newFixture(t, Config{Memory: mem})
	f.projectWithID("prj_a")
	f.projectWithID("prj_b")
	o := newCtlOrbs(t, f.orbs)
	mo := &memOrbs{Orbs: o, kinds: map[string]string{}, feats: map[string][]string{}}
	f.s.orbs = mo

	orbA := f.orbIn("prj_a")
	mo.feats[orbA] = []string{FeatureMemory}
	p := startParams(t, f, o, f.threadIn(orbA, "prj_b"))
	require.NotNil(t, p.Memory)
	require.NotNil(t, p.Memory.Project)
	assert.Equal(t, "lc-prj_a", *p.Memory.Project, "普通 orb 取 orb 的项目")
	assert.True(t, p.Memory.AutoRetain)

	runner := f.orb("ready")
	mo.kinds[runner], mo.feats[runner] = orbs.KindRunner, []string{"fs", FeatureMemory}
	p = startParams(t, f, o, f.threadIn(runner, "prj_b"))
	require.NotNil(t, p.Memory)
	require.NotNil(t, p.Memory.Project)
	assert.Equal(t, "lc-prj_b", *p.Memory.Project, "runner 取线程的项目")

	p = startParams(t, f, o, f.threadIn(runner, ""))
	require.NotNil(t, p.Memory)
	assert.Nil(t, p.Memory.Project, "无项目线程")
	assert.False(t, p.Memory.AutoRetain)

	mem.mu.Lock()
	n := len(mem.calls)
	mem.mu.Unlock()
	noFeat := f.orbIn("prj_a")
	p = startParams(t, f, o, f.threadIn(noFeat, "prj_a"))
	assert.Nil(t, p.Memory, "orb 没有 memory 特性")
	mem.mu.Lock()
	assert.Len(t, mem.calls, n, "不支持记忆的 orb 不查询")
	mem.mu.Unlock()

	mem.err = errors.New("boom")
	p = startParams(t, f, o, f.threadIn(orbA, "prj_a"))
	assert.Nil(t, p.Memory, "查询出错时照常启动、不带记忆")
}

// 未配置 Memory（nil）时 agent.start 不带 memory。
func TestMemoryAgentStartDisabled(t *testing.T) {
	f := newFixture(t, Config{})
	o := newCtlOrbs(t, f.orbs)
	orbID := f.orb("ready")
	f.s.orbs = &memOrbs{Orbs: o, feats: map[string][]string{orbID: {FeatureMemory}}}
	p := startParams(t, f, o, f.threadIn(orbID, ""))
	assert.Nil(t, p.Memory)
}

// 协调对话不接入记忆：即使协调宿主声明了 memory。
func TestMemoryAgentStartCoordinator(t *testing.T) {
	mem := &fakeMemory{}
	f := newFixture(t, Config{Memory: mem})
	o := newCtlOrbs(t, f.orbs)
	o.features[orbs.FeatureCoordinator] = true
	orbID := f.orb("ready")
	f.s.orbs = &memOrbs{Orbs: o, feats: map[string][]string{orbID: {orbs.FeatureCoordinator, FeatureMemory}}}
	f.s.SetCoordinator(newFakeCoord())
	th := f.coordThread(orbID, KindSpace, StateProvisioning)
	f.s.startAgent(f.ctx, th.id)
	var p apigen.LinkCtlAgentStartParams
	for {
		c := o.nextCall(t)
		if c.method == "agent.start" {
			require.NoError(t, json.Unmarshal(c.params, &p))
			break
		}
	}
	assert.Nil(t, p.Memory)
	mem.mu.Lock()
	assert.Empty(t, mem.calls)
	mem.mu.Unlock()
}

// MarkMemoryRestart：只标记 agent 在跑、归属该项目（普通 orb 看 orb 的项目，runner 看线程的项目）、orb 支持记忆的
// 工作线程，原因为 memory；nil 作用于全部；标记出现在 apply-status 里并由 RestartSessions 安排重启。
func TestMemoryMarkRestart(t *testing.T) {
	f := newFixture(t, Config{})
	f.projectWithID("prj_a")
	f.projectWithID("prj_b")
	mo := &memOrbs{Orbs: f.orbs, kinds: map[string]string{}, feats: map[string][]string{}}
	f.s.orbs = mo
	alive := func(th *thread) *thread {
		attach(th, fakeAgent(16), apigen.AgentStatusReady)
		th.mu.Lock()
		th.settled = false // 回合进行中：RestartSessions 只安排、不立即停
		th.mu.Unlock()
		return th
	}

	orbA, orbB, plain := f.orbIn("prj_a"), f.orbIn("prj_b"), f.orbIn("prj_a")
	runner := f.orb("ready")
	for _, id := range []string{orbA, orbB, runner} {
		mo.feats[id] = []string{FeatureMemory}
	}
	mo.kinds[runner] = orbs.KindRunner
	inA := alive(f.threadIn(orbA, "prj_b")) // orb 属于 prj_a
	inB := alive(f.threadIn(orbB, "prj_b"))
	runnerA := alive(f.threadIn(runner, "prj_a"))
	runnerNone := alive(f.threadIn(runner, ""))
	idleA := f.threadIn(orbA, "prj_a") // agent 不在跑
	noFeat := alive(f.threadIn(plain, "prj_a"))

	pa := "prj_a"
	f.s.MarkMemoryRestart(f.ctx, &pa)
	assert.Equal(t, []string{"memory"}, restartReasons(f, inA.id))
	assert.Equal(t, []string{"memory"}, restartReasons(f, runnerA.id))
	for _, th := range []*thread{inB, runnerNone, idleA, noFeat} {
		assert.Empty(t, restartReasons(f, th.id), th.id)
	}

	f.s.MarkMemoryRestart(f.ctx, &pa)
	assert.Equal(t, []string{"memory"}, restartReasons(f, inA.id), "原因去重")

	f.s.MarkMemoryRestart(f.ctx, nil)
	for _, th := range []*thread{inA, inB, runnerA, runnerNone} {
		assert.Equal(t, []string{"memory"}, restartReasons(f, th.id), th.id)
	}
	assert.Empty(t, restartReasons(f, idleA.id))
	assert.Empty(t, restartReasons(f, noFeat.id))

	want := []string{inA.id, inB.id, runnerA.id, runnerNone.id}
	got := f.s.RestartSessions(f.ctx)
	assert.ElementsMatch(t, want, got)
}
