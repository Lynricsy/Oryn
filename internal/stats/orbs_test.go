package stats

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// orb 在夹具里建一台 orb。
func (f *fixture) orb(id, typ string, created int64, project, snapshot string, coordinator bool) {
	f.t.Helper()
	if _, err := f.st.Q.GetProvider(f.ctx, "prov_1"); err != nil {
		require.NoError(f.t, f.st.Q.CreateProvider(f.ctx, db.CreateProviderParams{ID: "prov_1", Name: "p", TokenHash: "x", CreatedAt: 1}))
	}
	p := db.CreateOrbParams{ID: id, ProviderID: "prov_1", Type: typ, Cpus: 2, MemoryMb: 4096, State: "ready", StateChangedAt: 1, TokenHash: id, CreatedAt: created, Coordinator: coordinator}
	if typ == "large" {
		p.Cpus, p.MemoryMb = 8, 16384
	}
	if project != "" {
		p.ProjectID = new(project)
	}
	if snapshot != "" {
		p.SnapshotOf = new(snapshot)
	}
	require.NoError(f.t, f.st.Q.CreateOrb(f.ctx, p))
}

func (f *fixture) ev(orb string, ts int64, from, to string, detail ...string) {
	f.t.Helper()
	d := "{}"
	if len(detail) > 0 {
		d = detail[0]
	}
	require.NoError(f.t, f.st.Q.InsertOrbEvent(f.ctx, db.InsertOrbEventParams{OrbID: orb, Ts: ts, FromState: from, ToState: to, Detail: d}))
}

func (f *fixture) setupRun(orb, phase string, started, dur int64, ok, ran bool) {
	f.t.Helper()
	b := func(v bool) int64 {
		if v {
			return 1
		}
		return 0
	}
	f.seq++
	require.NoError(f.t, f.st.Q.InsertOrbSetupRun(f.ctx, db.InsertOrbSetupRunParams{
		ID: fmt.Sprintf("osr_%d", f.seq), OrbID: orb, Phase: phase, Trigger: "auto", StartedAt: started, FinishedAt: started + dur,
		Ok: b(ok), SetupRan: b(ran), DurationMs: dur, LogTail: []byte{},
	}))
}

func orbFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.orb("orb-a", "small", 1, "prj_a", "", false)
	f.orb("orb-b", "large", 1, "", "", false)
	f.orb("orb-c", "small", 1, "", "", true)
	f.orb("orb-s", "small", 40, "prj_a", "oryn-snap-x", false)
	f.ev("orb-a", 0, "", "requested")
	f.ev("orb-a", 10, "requested", "spawning")
	f.ev("orb-a", 20, "spawning", "connecting")
	f.ev("orb-a", 25, "connecting", "setting_up")
	f.ev("orb-a", 30, "setting_up", "ready")
	f.ev("orb-a", 100, "ready", "freezing")
	f.ev("orb-a", 110, "freezing", "frozen")
	f.ev("orb-a", 150, "frozen", "frozen") // project_scripts_set：同状态事件不计次数
	f.ev("orb-a", 200, "frozen", "stopping")
	f.ev("orb-a", 210, "stopping", "stopped")
	f.ev("orb-a", 400, "stopped", "waking") // 窗口外
	f.ev("orb-b", 50, "", "requested")
	f.ev("orb-b", 60, "requested", "spawning")
	f.ev("orb-b", 70, "spawning", "failed", `{"reason":"boom"}`)
	f.ev("orb-c", 30, "", "ready") // 协调宿主：不计
	f.ev("orb-s", 40, "", "requested")
	f.ev("orb-s", 45, "requested", "spawning")
	f.ev("orb-s", 50, "spawning", "connecting")
	f.ev("orb-s", 60, "connecting", "ready")
	return f
}

func orbGroup(t *testing.T, gs []apigen.OrbStat, key string) apigen.OrbStat {
	t.Helper()
	for _, g := range gs {
		if g.Key == key {
			return g
		}
	}
	t.Fatalf("没有分组 %q", key)
	return apigen.OrbStat{}
}

func TestOrbStats(t *testing.T) {
	f := orbFixture(t)
	f.setupRun("orb-a", "setup", 26, 1000, true, true)
	f.setupRun("orb-a", "setup", 27, 130000, true, true)
	f.setupRun("orb-a", "setup", 28, 5000, false, true)
	f.setupRun("orb-a", "setup", 29, 9, true, false) // 没有真正运行脚本：不计
	f.setupRun("orb-a", "resume", 31, 10, true, false)
	f.setupRun("orb-a", "resume", 32, 10, false, false)
	f.setupRun("orb-c", "setup", 33, 99, false, true) // 协调宿主：不计
	f.setupRun("orb-a", "setup", 500, 1, false, true) // 窗口外

	s, err := Orbs(f.ctx, f.st.Q, OrbQuery{From: 25, To: 300}, 10_000)
	require.NoError(t, err)
	assert.Equal(t, apigen.OrbGroupByType, s.GroupBy)
	require.Len(t, s.Groups, 2)
	b, a := s.Groups[0], s.Groups[1]
	assert.Equal(t, "large", b.Key)
	assert.Equal(t, 8, *b.Cpus)
	assert.Equal(t, 1, b.Spawns)
	assert.Equal(t, 1, b.Failures)
	assert.Equal(t, "small", a.Key)
	assert.Equal(t, 2, a.Orbs, "orb-a 与 orb-s；协调宿主的 orb-c 不计")
	assert.Equal(t, 3, s.Total.Orbs)

	// 只看 orb-a：窗口前状态取自 state_at_from（connecting）
	s, err = Orbs(f.ctx, f.st.Q, OrbQuery{GroupBy: "orb", From: 25, To: 300, Orb: new("orb-a")}, 10_000)
	require.NoError(t, err)
	require.Len(t, s.Groups, 1)
	o := s.Groups[0]
	// running: setting_up[25,30) + ready[30,100) + freezing[100,110) + stopping[200,210)
	assert.EqualValues(t, 95, o.RunningMs)
	assert.EqualValues(t, 5, o.SetupMs)
	assert.EqualValues(t, 90, o.FrozenMs, "同状态事件不打断区间")
	assert.EqualValues(t, 90, o.StoppedMs, "[210,300)")
	assert.Equal(t, 0, o.Spawns, "spawning 在窗口前")
	assert.Equal(t, 1, o.Freezes, "frozen→frozen 不计次数")
	assert.Equal(t, 1, o.Stops)
	assert.Equal(t, 0, o.Wakes)
	assert.Equal(t, "small", *o.Type)

	setup := s.Setup
	assert.Equal(t, 3, setup.Runs)
	assert.Equal(t, 1, setup.Failures)
	assert.Equal(t, 1, setup.SlowRuns)
	assert.EqualValues(t, 5000, *setup.P50Ms)
	assert.EqualValues(t, 130000, *setup.P95Ms)
	assert.EqualValues(t, 130000, *setup.MaxMs)
	assert.Equal(t, 2, setup.ResumeRuns)
	assert.Equal(t, 1, setup.ResumeFailures)

	// 末段截到 now：orb-s 的 ready 区间只算到 now + 1
	s, err = Orbs(f.ctx, f.st.Q, OrbQuery{GroupBy: "orb", From: 25, To: 1000, Orb: new("orb-s")}, 350)
	require.NoError(t, err)
	assert.EqualValues(t, 10+291, s.Groups[0].RunningMs, "connecting[50,60) + ready[60,351)")
	assert.Equal(t, 1, s.Groups[0].SnapshotSpawns)
	assert.Equal(t, 1, s.Groups[0].Spawns)

	// 按项目：orb-a 与 orb-s 都在 prj_a
	s, err = Orbs(f.ctx, f.st.Q, OrbQuery{GroupBy: "project", From: 25, To: 300}, 10_000)
	require.NoError(t, err)
	pa := orbGroup(t, s.Groups, "prj_a")
	assert.Equal(t, "alpha", *pa.Label)
	assert.Equal(t, 2, pa.Orbs)
	assert.Equal(t, 1, pa.SnapshotSpawns)
	assert.Nil(t, pa.Type)
	assert.Equal(t, 1, orbGroup(t, s.Groups, "").Failures)

	// 项目筛选 none
	s, err = Orbs(f.ctx, f.st.Q, OrbQuery{From: 25, To: 300, Project: new("none")}, 10_000)
	require.NoError(t, err)
	assert.Equal(t, 1, s.Total.Orbs, "只有 orb-b（orb-c 是协调宿主）")

	_, err = Orbs(f.ctx, f.st.Q, OrbQuery{GroupBy: "model", From: 25, To: 300}, 10_000)
	assert.Error(t, err)
}

func TestOrbStatsByDayAndOrbLabel(t *testing.T) {
	f := newFixture(t)
	f.orb("orb-d", "small", dayD-2*hourMs, "", "", false)
	f.ev("orb-d", dayD-hourMs, "", "ready")
	f.ev("orb-d", dayD+30*hourMs, "ready", "frozen")
	f.thread("T-late", "", "", "work")
	f.thread("T-first", "", "", "work")
	f.exec(`UPDATE threads SET orb_id = 'orb-d', created_at = 5 WHERE id = 'T-late'`)
	f.exec(`UPDATE threads SET orb_id = 'orb-d', created_at = 2 WHERE id = 'T-first'`)

	s, err := Orbs(f.ctx, f.st.Q, OrbQuery{GroupBy: "day", From: dayD, To: dayD + 3*dayMs}, dayD+2*dayMs+12*hourMs)
	require.NoError(t, err)
	require.Len(t, s.Groups, 3, "稠密")
	assert.Equal(t, "2026-11-10", s.Groups[0].Key)
	assert.EqualValues(t, dayD, *s.Groups[0].Start)
	assert.EqualValues(t, 24*hourMs, s.Groups[0].RunningMs, "跨午夜的区间按日分摊")
	assert.EqualValues(t, 6*hourMs, s.Groups[1].RunningMs)
	assert.EqualValues(t, 18*hourMs, s.Groups[1].FrozenMs)
	assert.EqualValues(t, 12*hourMs+1, s.Groups[2].FrozenMs, "末段截到 now + 1")
	assert.Equal(t, 1, s.Groups[1].Freezes)
	assert.Equal(t, 1, s.Groups[2].Orbs)
	assert.Equal(t, 1, s.Total.Orbs)
	assert.EqualValues(t, 30*hourMs, s.Total.RunningMs)

	s, err = Orbs(f.ctx, f.st.Q, OrbQuery{GroupBy: "orb", From: dayD, To: dayD + dayMs}, dayD+dayMs)
	require.NoError(t, err)
	require.Len(t, s.Groups, 1)
	assert.Equal(t, "title T-first", *s.Groups[0].Label, "label 取 orb 的第一个线程")
	assert.Equal(t, 4096, *s.Groups[0].MemoryMB)
}

func TestTimeline(t *testing.T) {
	f := orbFixture(t)
	tl, err := Timeline(f.ctx, f.st.Q, "orb-a", 25, 300, 10_000)
	require.NoError(t, err)
	type seg struct {
		from, to int64
		state    string
	}
	var got []seg
	for _, s := range tl.Segments {
		got = append(got, seg{s.From, s.To, string(s.State)})
	}
	assert.Equal(t, []seg{
		{25, 30, "setting_up"}, {30, 100, "ready"}, {100, 110, "freezing"}, {110, 200, "frozen"},
		{200, 210, "stopping"}, {210, 300, "stopped"},
	}, got, "相邻同状态合并（150 的 frozen→frozen）")
	assert.False(t, tl.Truncated)

	tl, err = Timeline(f.ctx, f.st.Q, "orb-b", 0, 1000, 500)
	require.NoError(t, err)
	last := tl.Segments[len(tl.Segments)-1]
	assert.Equal(t, apigen.OrbStateFailed, last.State)
	assert.Equal(t, "boom", *last.Reason)
	assert.EqualValues(t, 500, last.To, "末段截到 now")
	assert.EqualValues(t, 500, tl.To)

	_, err = Timeline(f.ctx, f.st.Q, "orb-nope", 0, 1000, 500)
	var se *Error
	require.ErrorAs(t, err, &se)
	assert.Equal(t, CodeNotFound, se.Code)

	// 超过 2000 段：只给前 2000 段并置 truncated
	f.orb("orb-x", "small", 1, "", "", false)
	states := []string{"ready", "frozen"}
	for i := range 2002 {
		f.ev("orb-x", int64(1000+i), states[(i+1)%2], states[i%2])
	}
	tl, err = Timeline(f.ctx, f.st.Q, "orb-x", 0, 10_000, 10_000)
	require.NoError(t, err)
	assert.Len(t, tl.Segments, MaxTimelineSegments)
	assert.True(t, tl.Truncated)
	assert.Equal(t, "x", *failureReason(`{"error":"x"}`), "没有 reason 时取 error")
	assert.Nil(t, failureReason(`{}`))
}
