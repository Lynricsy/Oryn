package stats

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func outputGroup(t *testing.T, gs []apigen.OutputStat, key string) apigen.OutputStat {
	t.Helper()
	for _, g := range gs {
		if g.Key == key {
			return g
		}
	}
	t.Fatalf("没有分组 %q", key)
	return apigen.OutputStat{}
}

func TestOutput(t *testing.T) {
	f := newFixture(t)
	h := hourMs
	f.thread("W1", "prj_a", "", "work")
	f.thread("W2", "prj_b", "sp_1", "work")
	f.thread("W3", "", "", "work")
	f.thread("S-x", "", "", "space")
	f.exec(`UPDATE threads SET created_at = ?, shipped_at = ? WHERE id = 'W1'`, dayD+1*h, dayD+25*h)
	f.exec(`UPDATE threads SET created_at = ?, archived_at = ? WHERE id = 'W2'`, dayD+2*h, dayD+26*h)
	// 交付的线程同时也是归档的：只算交付
	f.exec(`UPDATE threads SET created_at = ?, shipped_at = ?, archived_at = ? WHERE id = 'W3'`, dayD-5*dayMs, dayD+50*h, dayD+50*h)
	f.exec(`UPDATE threads SET created_at = ? WHERE id = 'S-x'`, dayD+1*h)
	f.usage("W1", dayD-3*dayMs, "m", 1.0) // 窗口前的花费也算进全生命周期成本
	f.usage("W1", dayD+2*h, "m", 0.5)
	f.usage("W3", dayD-4*dayMs, "m", 2.0)
	f.turn("W1", dayD+1*h)
	f.turn("S-x", dayD+1*h) // 回合不按线程种类过滤
	for _, c := range []db.InsertThreadCommitParams{
		{ThreadID: "W1", Sha: "aaa", Ts: dayD + 3*h},
		{ThreadID: "W1", Sha: "bbb", Ts: dayD + 27*h},
		{ThreadID: "W3", Sha: "aaa", Ts: dayD + 3*h}, // fork 共享的历史：同一 sha 只计一次
	} {
		require.NoError(t, f.st.Q.InsertThreadCommit(f.ctx, c))
	}

	o, err := Output(f.ctx, f.st.Q, OutputQuery{From: dayD, To: dayD + 3*dayMs})
	require.NoError(t, err)
	assert.Equal(t, apigen.OutputGroupByDay, o.GroupBy)
	require.Len(t, o.Groups, 3)
	d0, d1, d2 := o.Groups[0], o.Groups[1], o.Groups[2]
	assert.Equal(t, "2026-11-10", d0.Key)
	assert.EqualValues(t, 2, d0.ThreadsCreated, "只数工作线程")
	assert.EqualValues(t, 2, d0.Turns)
	assert.EqualValues(t, 1, d0.Commits, "同一 sha 在同一桶只计一次")
	assert.EqualValues(t, 1, d1.ThreadsShipped)
	assert.InDelta(t, 1.5, d1.ShippedCostUsd, 1e-9, "全生命周期成本")
	assert.EqualValues(t, 1, d1.ThreadsArchived)
	assert.EqualValues(t, 1, d1.Commits)
	assert.EqualValues(t, 1, d2.ThreadsShipped)
	assert.EqualValues(t, 0, d2.ThreadsArchived, "已交付的不算归档")
	assert.InDelta(t, 2.0, d2.ShippedCostUsd, 1e-9)
	tot := o.Total
	assert.Equal(t, apigen.OutputStat{Key: "total", ThreadsCreated: 2, ThreadsShipped: 2, ThreadsArchived: 1, ShippedCostUsd: 3.5, Commits: 2, Turns: 2}, tot)

	o, err = Output(f.ctx, f.st.Q, OutputQuery{GroupBy: "project", From: dayD, To: dayD + 3*dayMs})
	require.NoError(t, err)
	pa := outputGroup(t, o.Groups, "prj_a")
	assert.Equal(t, "alpha", *pa.Label)
	assert.EqualValues(t, 2, pa.Commits)
	assert.InDelta(t, 1.5, pa.ShippedCostUsd, 1e-9)
	assert.EqualValues(t, 1, outputGroup(t, o.Groups, "prj_b").ThreadsArchived)
	none := outputGroup(t, o.Groups, "")
	assert.EqualValues(t, 1, none.Commits)
	assert.EqualValues(t, 1, none.Turns)
	assert.EqualValues(t, 2, o.Total.Commits, "总数按 sha 去重：aaa 出现在两个分组里只计一次")

	o, err = Output(f.ctx, f.st.Q, OutputQuery{GroupBy: "space", From: dayD, To: dayD + 3*dayMs, Space: new("sp_1")})
	require.NoError(t, err)
	require.Len(t, o.Groups, 1)
	assert.Equal(t, "S1", *o.Groups[0].Label)
	assert.EqualValues(t, 1, o.Groups[0].ThreadsCreated)
	assert.EqualValues(t, 1, o.Groups[0].ThreadsArchived)

	o, err = Output(f.ctx, f.st.Q, OutputQuery{GroupBy: "week", From: dayD, To: dayD + 3*dayMs, Project: new("none")})
	require.NoError(t, err)
	assert.EqualValues(t, 1, o.Total.ThreadsShipped)
	assert.EqualValues(t, 0, o.Total.ThreadsCreated)

	_, err = Output(f.ctx, f.st.Q, OutputQuery{GroupBy: "model", From: dayD, To: dayD + dayMs})
	assert.Error(t, err)
}
