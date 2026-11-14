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

// Y18：规模基准（只记录数字，不设门槛）。自带播种：40 万行用量、45 万行工具调用、5 万行回合、2000 个线程，跨 400 天，
// 行按时间追加（条目随时间入库）。两种分布：sessions（主场景，贴近真实）按「会话」播种——随机起点、随机线程、
// 会话内固定 1–2 个模型、20–60 个请求、间隔 15–90 秒，工具调用（平均每请求 1.125 个）与回合（平均每 8 个请求 1 个）
// 跟着会话走；ordered（最坏情况）时间均匀分布、相邻行属于不同线程与模型，15 分钟汇总几乎不压缩。
//
//	go test ./internal/stats -run '^$' -bench BenchmarkUsage -benchtime 3x -v
func BenchmarkUsage(b *testing.B) {
	for _, layout := range []string{"sessions", "ordered"} {
		b.Run(layout, func(b *testing.B) { runBenchUsage(b, layout == "sessions") })
	}
}

// benchRow 是一行待播种的事实：线程下标、时间、模型下标（仅用量）。
type benchRow struct {
	thread int
	ts     int64
	model  int
}

const (
	benchThreads = 2000
	benchUsage   = 400_000
	benchTools   = 450_000
	benchTurns   = 50_000
	benchDays    = 400
)

var benchModels = []string{"opus", "sonnet", "haiku", "gpt", "gemini", "deepseek"}

// orderedRows：时间均匀随机后排序，第 i 行属于线程 i % 2000、模型 i % 6。
func orderedRows(rng *rand.Rand, start int64) (usage, tools, turns []benchRow) {
	gen := func(n int) []benchRow {
		ts := make([]int64, n)
		for i := range ts {
			ts[i] = start + rng.Int64N(benchDays*dayMs)
		}
		slices.Sort(ts)
		out := make([]benchRow, n)
		for i := range out {
			out[i] = benchRow{thread: i % benchThreads, ts: ts[i], model: i % len(benchModels)}
		}
		return out
	}
	return gen(benchUsage), gen(benchTools), gen(benchTurns)
}

// sessionRows：按会话播种（见 BenchmarkUsage），工具与回合的行数补齐 / 截断到目标值后各自按时间排序。
func sessionRows(rng *rand.Rand, start int64) (usage, tools, turns []benchRow) {
	var bounds [][2]int // 每个会话在 usage 里的 [起, 止)
	for len(usage) < benchUsage {
		th := rng.IntN(benchThreads)
		m1 := rng.IntN(len(benchModels))
		m2 := m1
		if rng.IntN(2) == 0 {
			m2 = rng.IntN(len(benchModels))
		}
		t := start + rng.Int64N(benchDays*dayMs-2*hourMs)
		from := len(usage)
		for n := 20 + rng.IntN(41); n > 0 && len(usage) < benchUsage; n-- {
			t += 15_000 + rng.Int64N(75_001)
			m := m1
			if rng.IntN(2) == 0 {
				m = m2
			}
			usage = append(usage, benchRow{thread: th, ts: t, model: m})
			tools = append(tools, benchRow{thread: th, ts: t + 1000 + rng.Int64N(9000)})
			if rng.IntN(8) == 0 {
				tools = append(tools, benchRow{thread: th, ts: t + 1000 + rng.Int64N(9000)})
			}
			if rng.IntN(8) == 0 {
				turns = append(turns, benchRow{thread: th, ts: t - 1000 - rng.Int64N(9000)})
			}
		}
		bounds = append(bounds, [2]int{from, len(usage)})
	}
	// 补齐：在随机会话的随机请求之后追加
	fill := func(rows []benchRow, n int, delta func() int64) []benchRow {
		for len(rows) < n {
			s := bounds[rng.IntN(len(bounds))]
			u := usage[s[0]+rng.IntN(s[1]-s[0])]
			rows = append(rows, benchRow{thread: u.thread, ts: u.ts + delta()})
		}
		return rows[:n]
	}
	tools = fill(tools, benchTools, func() int64 { return 1000 + rng.Int64N(9000) })
	turns = fill(turns, benchTurns, func() int64 { return -1000 - rng.Int64N(9000) })
	byTS := func(a, b benchRow) int { return int(a.ts - b.ts) }
	slices.SortStableFunc(usage, byTS)
	slices.SortStableFunc(tools, byTS)
	slices.SortStableFunc(turns, byTS)
	return usage, tools, turns
}

func runBenchUsage(b *testing.B, sessions bool) {
	ctx := context.Background()
	st, err := store.Open(ctx, b.TempDir())
	require.NoError(b, err)
	b.Cleanup(func() { _ = st.Close() })
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC).UnixMilli()
	start := now - benchDays*dayMs
	rng := rand.New(rand.NewPCG(1, 2))
	var usage, tools, turns []benchRow
	if sessions {
		usage, tools, turns = sessionRows(rng, start)
	} else {
		usage, tools, turns = orderedRows(rng, start)
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
		VALUES (?, ?, ?, 'auto', 'idle', 1, 'd', 1, 1, 'work')`, benchThreads,
		func(i int) []any {
			return []any{fmt.Sprintf("T-%d", i), fmt.Sprintf("prj_%d", i%20), fmt.Sprintf("thread %d", i)}
		})
	purposes := []string{"", "", "", "", "title", "cache-warm", "memory"}
	exec(`INSERT INTO usage_records (thread_id, entry_seq, ts, provider, model, purpose, stop_reason, ukey,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd, duration_ms, ttft_ms, is_subagent)
		VALUES (?, ?, ?, 'p', ?, ?, 'stop', ?, 1000, 200, 5000, 100, 0.01, 3000, 400, 0)`, len(usage),
		func(i int) []any {
			r := usage[i]
			return []any{fmt.Sprintf("T-%d", r.thread), i, r.ts, benchModels[r.model], purposes[i%len(purposes)], fmt.Sprintf("u%d", i)}
		})
	toolNames := []string{"bash", "read", "edit", "write", "grep", "find", "task"}
	exec(`INSERT INTO tool_calls (thread_id, entry_seq, ts, tool, is_error, ukey) VALUES (?, ?, ?, ?, ?, ?)`, len(tools),
		func(i int) []any {
			r := tools[i]
			return []any{fmt.Sprintf("T-%d", r.thread), len(usage) + i, r.ts, toolNames[i%len(toolNames)], i%17 == 0, fmt.Sprintf("c%d", i)}
		})
	exec(`INSERT INTO turns (thread_id, entry_seq, ts, ukey) VALUES (?, ?, ?, ?)`, len(turns),
		func(i int) []any {
			r := turns[i]
			return []any{fmt.Sprintf("T-%d", r.thread), len(usage) + len(tools) + i, r.ts, fmt.Sprintf("r%d", i)}
		})
	require.NoError(b, tx.Commit())
	var nu, nt, nr int
	require.NoError(b, st.DB.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM usage_q), (SELECT COUNT(*) FROM tool_q), (SELECT COUNT(*) FROM turn_q)`).Scan(&nu, &nt, &nr))
	b.Logf("播种 %d 行耗时 %s；汇总行数 usage_q %d、tool_q %d、turn_q %d", len(usage)+len(tools)+len(turns),
		time.Since(seedStart).Round(time.Millisecond), nu, nt, nr)

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
