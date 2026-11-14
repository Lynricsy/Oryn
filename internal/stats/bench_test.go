package stats

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store"
)

// Y18：规模基准（只记录数字，不设门槛）。自带播种：40 万行用量、45 万行工具调用、5 万行回合、2000 个线程，跨 400 天。
// 两种行序各跑一遍：ordered = 行按时间追加（稳态：条目随时间入库）；shuffled = 行序与时间无关（最坏情况：
// 升级后的回填按线程逐个重写事实行，ts 索引的范围扫描变成随机回表）。
//
//	go test ./internal/stats -run '^$' -bench BenchmarkUsage -benchtime 1x
func BenchmarkUsage(b *testing.B) {
	for _, layout := range []string{"ordered", "shuffled"} {
		b.Run(layout, func(b *testing.B) { benchUsage(b, layout == "ordered") })
	}
}

func benchUsage(b *testing.B, ordered bool) {
	const (
		nThreads = 2000
		nUsage   = 400_000
		nTools   = 450_000
		nTurns   = 50_000
		days     = 400
	)
	ctx := context.Background()
	st, err := store.Open(ctx, b.TempDir())
	require.NoError(b, err)
	b.Cleanup(func() { _ = st.Close() })
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC).UnixMilli()
	start := now - days*dayMs
	rng := rand.New(rand.NewPCG(1, 2))
	times := func(n int) []int64 {
		out := make([]int64, n)
		for i := range out {
			out[i] = start + rng.Int64N(days*dayMs)
		}
		if ordered {
			slices.Sort(out)
		}
		return out
	}

	seedStart := time.Now()
	tx, err := st.DB.BeginTx(ctx, nil)
	require.NoError(b, err)
	exec := func(query string, rows int, args func(i int) []any) {
		stmt, err := tx.PrepareContext(ctx, query)
		require.NoError(b, err)
		defer stmt.Close()
		for i := range rows {
			_, err := stmt.ExecContext(ctx, args(i)...)
			require.NoError(b, err)
		}
	}
	exec(`INSERT INTO projects (id, name, shallow_clone, created_at, updated_at) VALUES (?, ?, 1, 1, 1)`, 20,
		func(i int) []any { return []any{fmt.Sprintf("prj_%d", i), fmt.Sprintf("project %d", i)} })
	exec(`INSERT INTO threads (id, project_id, title, title_source, state, state_changed_at, session_dir, created_at, updated_at, kind)
		VALUES (?, ?, ?, 'auto', 'idle', 1, 'd', 1, 1, 'work')`, nThreads,
		func(i int) []any {
			return []any{fmt.Sprintf("T-%d", i), fmt.Sprintf("prj_%d", i%20), fmt.Sprintf("thread %d", i)}
		})
	models := []string{"opus", "sonnet", "haiku", "gpt", "gemini", "deepseek"}
	purposes := []string{"", "", "", "", "title", "cache-warm", "memory"}
	uts := times(nUsage)
	exec(`INSERT INTO usage_records (thread_id, entry_seq, ts, provider, model, purpose, stop_reason, ukey,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd, duration_ms, ttft_ms, is_subagent)
		VALUES (?, ?, ?, 'p', ?, ?, 'stop', ?, 1000, 200, 5000, 100, 0.01, 3000, 400, 0)`, nUsage,
		func(i int) []any {
			return []any{fmt.Sprintf("T-%d", i%nThreads), i, uts[i], models[i%len(models)], purposes[i%len(purposes)], fmt.Sprintf("u%d", i)}
		})
	tools := []string{"bash", "read", "edit", "write", "grep", "find", "task"}
	tts := times(nTools)
	exec(`INSERT INTO tool_calls (thread_id, entry_seq, ts, tool, is_error, ukey) VALUES (?, ?, ?, ?, ?, ?)`, nTools,
		func(i int) []any {
			return []any{fmt.Sprintf("T-%d", i%nThreads), nUsage + i, tts[i], tools[i%len(tools)], i%17 == 0, fmt.Sprintf("c%d", i)}
		})
	rts := times(nTurns)
	exec(`INSERT INTO turns (thread_id, entry_seq, ts, ukey) VALUES (?, ?, ?, ?)`, nTurns,
		func(i int) []any {
			return []any{fmt.Sprintf("T-%d", i%nThreads), nUsage + nTools + i, rts[i], fmt.Sprintf("r%d", i)}
		})
	require.NoError(b, tx.Commit())
	b.Logf("播种耗时 %s", time.Since(seedStart).Round(time.Millisecond))

	run := func(name string, uq UsageQuery) {
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				_, err := Usage(ctx, st.Q, uq)
				require.NoError(b, err)
			}
		})
	}
	run("30d_day", UsageQuery{GroupBy: GrainDay, From: now - 30*dayMs, To: now})
	run("all_model", UsageQuery{GroupBy: GroupModel, From: start, To: now})
	run("all_thread_series_week", UsageQuery{GroupBy: GroupThread, Series: GrainWeek, From: start, To: now})
}
