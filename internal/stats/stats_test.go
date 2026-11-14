package stats

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func openStore(t *testing.T) *store.Store {
	st, err := store.Open(context.Background(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedOrb(t *testing.T, st *store.Store, id, typ string) {
	ctx := context.Background()
	if _, err := st.Q.GetProvider(ctx, "prov_1"); err != nil {
		require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "p", TokenHash: "x", CreatedAt: 1}))
	}
	require.NoError(t, st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: id, ProviderID: "prov_1", Type: typ, State: "ready", StateChangedAt: 1, TokenHash: id, CreatedAt: 1}))
}

func TestMetricsDownsample(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	seedOrb(t, st, "orb-a", "small")
	// 0s、15s、30s、45s、60s 五个采样。
	for i := range 5 {
		require.NoError(t, st.Q.InsertOrbSample(ctx, db.InsertOrbSampleParams{
			OrbID: "orb-a", Ts: int64(i) * 15000, CpuPct: float64(i * 10), MemUsedMb: int64(100 + i), MemLimitMb: 1000,
			DiskUsedMb: 5, NetRxBytes: int64(i * 100), NetTxBytes: int64(i * 10),
		}))
	}
	raw, err := Metrics(ctx, st.Q, "orb-a", 0, 1e9, 0)
	require.NoError(t, err)
	assert.Equal(t, RawStepSeconds, raw.Step)
	assert.Len(t, raw.Samples, 5)

	m, err := Metrics(ctx, st.Q, "orb-a", 0, 1e9, 30)
	require.NoError(t, err)
	assert.Equal(t, 30, m.Step)
	require.Len(t, m.Samples, 3) // [0,30) [30,60) [60,90)
	assert.EqualValues(t, 0, m.Samples[0].TS)
	assert.InDelta(t, 5, m.Samples[0].CPUPct, 0.001)
	assert.InDelta(t, 100.5, m.Samples[0].MemUsedMB, 0.001)
	assert.EqualValues(t, 100, m.Samples[0].NetRxBytes, "网络字节取桶内末值")
	assert.EqualValues(t, 30000, m.Samples[1].TS)
	assert.InDelta(t, 25, m.Samples[1].CPUPct, 0.001)
	assert.EqualValues(t, 300, m.Samples[1].NetRxBytes)
	assert.InDelta(t, 40, m.Samples[2].CPUPct, 0.001)

	// 窗口裁剪：to 不含。
	m, err = Metrics(ctx, st.Q, "orb-a", 15000, 45000, 0)
	require.NoError(t, err)
	assert.Len(t, m.Samples, 2)
}

func TestUsage(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	day := int64(86400000)
	require.NoError(t, st.Q.CreateProject(ctx, db.CreateProjectParams{ID: "prj_1", Name: "demo", ShallowClone: true, CreatedAt: 1, UpdatedAt: 1}))
	require.NoError(t, st.Q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: "T-1", ProjectID: new("prj_1"), Title: "first", TitleSource: "auto", State: "idle", StateChangedAt: 1, SessionDir: "d", CreatedAt: 1, UpdatedAt: 1}))
	require.NoError(t, st.Q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: "T-2", Title: "second", TitleSource: "auto", State: "idle", StateChangedAt: 1, SessionDir: "d", CreatedAt: 1, UpdatedAt: 1}))
	recs := []db.InsertUsageRecordParams{
		{ThreadID: new("T-1"), EntrySeq: 1, Ts: day + 10, Provider: "anthropic", Model: "opus", InputTokens: 100, OutputTokens: 200, CostUsd: 1.5, DurationMs: new(int64(2000)), TtftMs: new(int64(300))},
		{ThreadID: new("T-1"), EntrySeq: 2, Ts: day + 20, Provider: "anthropic", Model: "opus", InputTokens: 10, OutputTokens: 20, CostUsd: 0.5, DurationMs: new(int64(0)), TtftMs: new(int64(100)), IsSubagent: true},
		{ThreadID: new("T-2"), EntrySeq: 1, Ts: 2*day + 5, Provider: "openai", Model: "gpt", InputTokens: 1, OutputTokens: 2, CostUsd: 0.1},
		{ThreadID: new("T-2"), EntrySeq: 2, Ts: 10 * day, Provider: "openai", Model: "gpt", InputTokens: 999, OutputTokens: 999}, // 窗口外
	}
	for _, r := range recs {
		require.NoError(t, st.Q.InsertUsageRecord(ctx, r))
	}
	require.NoError(t, st.Q.InsertToolCall(ctx, db.InsertToolCallParams{ThreadID: new("T-1"), EntrySeq: 3, Ts: day + 30, Tool: "bash"}))
	require.NoError(t, st.Q.InsertToolCall(ctx, db.InsertToolCallParams{ThreadID: new("T-1"), EntrySeq: 4, Ts: day + 40, Tool: "bash", IsError: true}))
	require.NoError(t, st.Q.InsertToolCall(ctx, db.InsertToolCallParams{ThreadID: new("T-2"), EntrySeq: 5, Ts: 3*day + 1, Tool: "read"}))

	from, to := int64(0), 5*day
	u, err := Usage(ctx, st.Q, "model", from, to)
	require.NoError(t, err)
	require.Len(t, u.Groups, 2)
	g := u.Groups[0]
	assert.Equal(t, "anthropic/opus", g.Key)
	assert.EqualValues(t, 2, g.Requests)
	assert.EqualValues(t, 110, g.InputTokens)
	assert.EqualValues(t, 220, g.OutputTokens)
	assert.InDelta(t, 2.0, g.CostUsd, 0.001)
	assert.InDelta(t, 0.5, *g.SubagentCostUsd, 0.001)
	assert.InDelta(t, 200, *g.AvgTtftMs, 0.001)
	assert.InDelta(t, 100, *g.TokensPerSecond, 0.001, "200 tokens / 2s（duration 为 0 的记录不计入）")
	assert.Nil(t, u.Groups[1].AvgTtftMs)
	assert.Nil(t, u.Groups[1].TokensPerSecond)

	u, err = Usage(ctx, st.Q, "day", from, to)
	require.NoError(t, err)
	var keys []string
	for _, g := range u.Groups {
		keys = append(keys, g.Key)
	}
	assert.Equal(t, []string{"1970-01-02", "1970-01-03", "1970-01-04"}, keys)
	assert.EqualValues(t, 2, u.Groups[0].ToolCalls)
	assert.EqualValues(t, 1, u.Groups[0].ToolErrors)
	assert.EqualValues(t, 0, u.Groups[2].Requests, "只有工具调用的日期")
	assert.EqualValues(t, 1, u.Groups[2].ToolCalls)

	u, err = Usage(ctx, st.Q, "thread", from, to)
	require.NoError(t, err)
	require.Len(t, u.Groups, 2)
	assert.Equal(t, "T-1", u.Groups[0].Key)
	assert.Equal(t, "first", *u.Groups[0].Label)
	assert.EqualValues(t, 2, u.Groups[0].ToolCalls)

	u, err = Usage(ctx, st.Q, "project", from, to)
	require.NoError(t, err)
	require.Len(t, u.Groups, 2)
	assert.Equal(t, "prj_1", u.Groups[0].Key)
	assert.Equal(t, "demo", *u.Groups[0].Label)
	assert.Equal(t, "", u.Groups[1].Key)
	assert.EqualValues(t, 1, u.Groups[1].ToolCalls)

	_, err = Usage(ctx, st.Q, "nope", from, to)
	assert.Error(t, err)
}

func TestOrbStats(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	seedOrb(t, st, "orb-a", "small")
	seedOrb(t, st, "orb-b", "large")
	ev := func(orb string, ts int64, from, to string) {
		require.NoError(t, st.Q.InsertOrbEvent(ctx, db.InsertOrbEventParams{OrbID: orb, Ts: ts, FromState: from, ToState: to, Detail: "{}"}))
	}
	ev("orb-a", 0, "", "requested")
	ev("orb-a", 10, "requested", "spawning")
	ev("orb-a", 20, "spawning", "connecting")
	ev("orb-a", 30, "connecting", "ready")
	ev("orb-a", 100, "ready", "freezing")
	ev("orb-a", 110, "freezing", "frozen")
	ev("orb-a", 200, "frozen", "stopping")
	ev("orb-a", 210, "stopping", "stopped")
	ev("orb-a", 400, "stopped", "waking") // 窗口外
	ev("orb-b", 50, "", "requested")
	ev("orb-b", 60, "requested", "spawning")
	ev("orb-b", 70, "spawning", "failed")

	s, err := Orbs(ctx, st.Q, 25, 300)
	require.NoError(t, err)
	require.Len(t, s.Types, 2)
	b, a := s.Types[0], s.Types[1]
	assert.Equal(t, "large", b.Type)
	assert.Equal(t, 1, b.Spawns)
	assert.Equal(t, 1, b.Failures)
	assert.Equal(t, "small", a.Type)
	assert.Equal(t, 1, a.Orbs)
	// running: connecting[25,30) + ready[30,100) + freezing[100,110) + stopping[200,210) = 5+70+10+10
	assert.EqualValues(t, 95, a.RunningMs)
	assert.EqualValues(t, 90, a.FrozenMs)
	assert.EqualValues(t, 90, a.StoppedMs) // [210,300)
	assert.Equal(t, 0, a.Spawns, "spawning 在窗口前")
	assert.Equal(t, 1, a.Freezes)
	assert.Equal(t, 1, a.Stops)
	assert.Equal(t, 0, a.Wakes)
}

func TestWindow(t *testing.T) {
	now := time.UnixMilli(1_000_000_000)
	f, to := Window(nil, nil, now)
	assert.Equal(t, int64(1_000_000_001), to, "右开区间：当前毫秒计入")
	assert.Equal(t, to-DefaultWindow.Milliseconds(), f)
	x, y := int64(5), int64(9)
	f, to = Window(&x, &y, now)
	assert.Equal(t, [2]int64{5, 9}, [2]int64{f, to})
}
