package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// admin rebuild-stats：--thread 只重算该线程；不带参数时全量回填（purge），写版本号、清进度键。
func TestAdminRebuildStats(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	q := st.Q
	for i, id := range []string{"T-1", "T-2"} {
		require.NoError(t, q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: id, TitleSource: "auto", State: "idle",
			SessionDir: "/s", CreatedAt: int64(i + 1), UpdatedAt: 1}))
		for _, e := range []struct{ id, line string }{
			{"u", `{"type":"message","id":"u","parentId":null,"message":{"role":"user","content":"q","timestamp":1000}}`},
			{"a", `{"type":"message","id":"a","parentId":"u","message":{"role":"assistant","provider":"p","model":"m","stopReason":"stop","timestamp":2000,"usage":{"input":1,"output":2,"cost":{"total":0.5}}}}`},
		} {
			_, err := q.UpsertEntry(ctx, db.UpsertEntryParams{ThreadID: id, File: id + ".jsonl", EntryID: e.id, Type: "message", Payload: []byte(e.line)})
			require.NoError(t, err)
		}
		// 写坏的事实行：多余的一行，无回合
		_, err := q.InsertUsageRecord(ctx, db.InsertUsageRecordParams{ThreadID: new(id), EntrySeq: 999, Ts: 1, Provider: "junk", CostUsd: 9})
		require.NoError(t, err)
	}
	require.NoError(t, st.Close())

	facts := func() (string, string) {
		t.Helper()
		st, err := store.Open(ctx, dir)
		require.NoError(t, err)
		defer st.Close()
		var usage, turns string
		require.NoError(t, st.DB.QueryRowContext(ctx, `SELECT COALESCE(group_concat(thread_id || ':' || provider || ':' || COALESCE(ukey, ''), ','), '') FROM
			(SELECT * FROM usage_records ORDER BY thread_id, entry_seq)`).Scan(&usage))
		require.NoError(t, st.DB.QueryRowContext(ctx, `SELECT COALESCE(group_concat(thread_id || ':' || ukey, ','), '') FROM
			(SELECT * FROM turns ORDER BY thread_id, entry_seq)`).Scan(&turns))
		return usage, turns
	}
	state := func(key string) string {
		t.Helper()
		st, err := store.Open(ctx, dir)
		require.NoError(t, err)
		defer st.Close()
		v, err := st.Q.GetStatsState(ctx, key)
		if err != nil {
			return "<none>"
		}
		return v
	}
	cfg := config.Orynd{DataDir: dir}

	var out bytes.Buffer
	require.NoError(t, adminRebuildStats(ctx, cfg, []string{"--thread", "T-1"}, &out))
	assert.Equal(t, "rebuilt 1 threads\n", out.String())
	usage, turns := facts()
	assert.Equal(t, "T-1:p:a@2000,T-2:junk:", usage, "只重算 T-1")
	assert.Equal(t, "T-1:u@1000", turns)

	out.Reset()
	require.NoError(t, adminRebuildStats(ctx, cfg, nil, &out))
	assert.Equal(t, "rebuilt 2 threads\n", out.String())
	usage, turns = facts()
	assert.Equal(t, "T-1:p:a@2000", usage, "全量：T-2 的 a@2000 已归先创建的 T-1，多余行清除")
	assert.Equal(t, "T-1:u@1000", turns)
	assert.Equal(t, "2", state("facts_version"))
	for _, k := range []string{"backfill_cursor", "backfill_done", "backfill_total"} {
		assert.Equal(t, "<none>", state(k), k)
	}

	assert.Error(t, adminRebuildStats(ctx, cfg, []string{"--thread", "T-nope"}, &out))
	assert.Error(t, cmdAdmin(ctx, cfg, []string{"bogus"}))
}
