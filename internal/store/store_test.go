package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// m2Tables 是计划 D 节列出的全部 M2 表，外加后续里程碑新增的表（M3：attachments）。
var m2Tables = []string{
	"attachments", "blobs", "device_pairings", "devices", "entries", "entry_cursors", "jobs", "orb_events",
	"orb_samples", "orb_setup_runs", "orbs", "owner", "passkeys", "projects", "providers", "recovery_codes",
	"secrets", "thread_labels", "threads", "tool_calls", "usage_records", "web_sessions",
}

func openTemp(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(context.Background(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenCreatesAllTablesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := openTemp(t, dir)

	rows, err := s.DB.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table'
		 AND name NOT LIKE 'sqlite_%' AND name != 'goose_db_version' ORDER BY name`)
	require.NoError(t, err)
	var got []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		got = append(got, n)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	assert.Equal(t, m2Tables, got)

	var mode string
	require.NoError(t, s.DB.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode))
	assert.Equal(t, "wal", mode)
	var fk int
	require.NoError(t, s.DB.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk))
	assert.Equal(t, 1, fk)

	// owner 只允许 id = 1。
	_, err = s.DB.ExecContext(ctx, `INSERT INTO owner (id, display_name, user_handle, created_at) VALUES (2, 'x', x'00', 0)`)
	require.Error(t, err)
	require.NoError(t, s.Q.CreateOwner(ctx, db.CreateOwnerParams{DisplayName: "me", UserHandle: []byte{1}, CreatedAt: 1}))
	require.NoError(t, s.Close())

	// 重新打开：迁移不重复执行，数据保留。
	s2 := openTemp(t, dir)
	o, err := s2.Q.GetOwner(ctx)
	require.NoError(t, err)
	assert.Equal(t, "me", o.DisplayName)
}

func TestUpsertEntryKeepsSeqAndClearsStale(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	q := s.Q
	const th = "T-1"
	require.NoError(t, q.CreateThread(ctx, db.CreateThreadParams{
		ID: th, Title: "", TitleSource: "auto", State: "provisioning", SessionDir: "/x", CreatedAt: 1, UpdatedAt: 1,
	}))

	up := func(file, id, payload string) int64 {
		seq, err := q.UpsertEntry(ctx, db.UpsertEntryParams{
			ThreadID: th, File: file, EntryID: id, Type: "message", Payload: []byte(payload),
		})
		require.NoError(t, err)
		return seq
	}
	assert.Equal(t, int64(1), up("a.jsonl", "e1", "1"))
	assert.Equal(t, int64(2), up("a.jsonl", "e2", "2"))
	assert.Equal(t, int64(3), up("a.jsonl", "e3", "3"))

	// entries.reset：全部标 stale，重发 e1、e3（e3 内容变化），e2 不再出现，另有新行 e4。
	n, err := q.MarkEntriesStale(ctx, db.MarkEntriesStaleParams{ThreadID: th, File: "a.jsonl"})
	require.NoError(t, err)
	assert.Equal(t, int64(3), n)
	assert.Equal(t, int64(1), up("a.jsonl", "e1", "1"))
	assert.Equal(t, int64(3), up("a.jsonl", "e3", "3b"))
	assert.Equal(t, int64(4), up("a.jsonl", "e4", "4"))

	// reset_done：删除仍为 stale 的 e2。
	n, err = q.DeleteStaleEntries(ctx, db.DeleteStaleEntriesParams{ThreadID: th, File: "a.jsonl"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	list, err := q.ListEntriesAfter(ctx, db.ListEntriesAfterParams{ThreadID: th, AfterSeq: 0, Limit: 500})
	require.NoError(t, err)
	require.Len(t, list, 3)
	var seqs []int64
	for _, e := range list {
		seqs = append(seqs, e.Seq)
		assert.False(t, e.Stale)
	}
	assert.Equal(t, []int64{1, 3, 4}, seqs)
	assert.Equal(t, "3b", string(list[1].Payload))

	// 删除 stale 行后 seq 不回退：下一条新行取 5。
	n, err = q.MarkEntriesStale(ctx, db.MarkEntriesStaleParams{ThreadID: th, File: "a.jsonl"})
	require.NoError(t, err)
	require.Equal(t, int64(3), n)
	up("a.jsonl", "e1", "1")
	up("a.jsonl", "e3", "3b")
	_, err = q.DeleteStaleEntries(ctx, db.DeleteStaleEntriesParams{ThreadID: th, File: "a.jsonl"})
	require.NoError(t, err)
	assert.Equal(t, int64(5), up("b.jsonl", "e1", "b1"))

	thr, err := q.GetThread(ctx, th)
	require.NoError(t, err)
	assert.Equal(t, int64(5), thr.LastSeq)

	// 游标 upsert / 读取。
	require.NoError(t, q.UpsertEntryCursor(ctx, db.UpsertEntryCursorParams{ThreadID: th, File: "a.jsonl", ByteOffset: 10}))
	require.NoError(t, q.UpsertEntryCursor(ctx, db.UpsertEntryCursorParams{ThreadID: th, File: "a.jsonl", ByteOffset: 42}))
	c, err := q.GetEntryCursor(ctx, db.GetEntryCursorParams{ThreadID: th, File: "a.jsonl"})
	require.NoError(t, err)
	assert.Equal(t, int64(42), c.ByteOffset)
}

func TestTxRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	err := s.Tx(ctx, func(q *db.Queries) error {
		if err := q.InsertBlob(ctx, db.InsertBlobParams{Sha256: "ab", Size: 1, CreatedAt: 1}); err != nil {
			return err
		}
		return assert.AnError
	})
	require.ErrorIs(t, err, assert.AnError)
	_, err = s.Q.GetBlob(ctx, "ab")
	require.Error(t, err)

	require.NoError(t, s.Tx(ctx, func(q *db.Queries) error {
		return q.InsertBlob(ctx, db.InsertBlobParams{Sha256: "ab", Size: 1, CreatedAt: 1})
	}))
	b, err := s.Q.GetBlob(ctx, "ab")
	require.NoError(t, err)
	assert.Equal(t, int64(1), b.Size)
}
