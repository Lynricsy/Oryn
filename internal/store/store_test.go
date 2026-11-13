package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// m2Tables 是计划 D 节列出的全部 M2 表，外加后续里程碑新增的表
// （M3：attachments；M4：orb_services、portal_shares、portal_sessions）。
var m2Tables = []string{
	"attachments", "blobs", "device_pairings", "devices", "entries", "entry_cursors", "jobs", "orb_events",
	"orb_samples", "orb_services", "orb_setup_runs", "orbs", "owner", "passkeys", "portal_sessions", "portal_shares",
	"projects", "providers", "recovery_codes", "secrets", "thread_labels", "threads", "tool_calls", "usage_records",
	"web_sessions",
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

// TestPortalQueries 覆盖 M4 的服务镜像、分享与 portal 会话查询（含子查询与 JOIN）。
func TestPortalQueries(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	q := s.Q
	const orb = "orb-01jcz8m3w4x5y6z7a8b9c0d1e2"
	require.NoError(t, q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "p", TokenHash: "h", CreatedAt: 1}))
	require.NoError(t, q.CreateOrb(ctx, db.CreateOrbParams{
		ID: orb, ProviderID: "prov_1", Type: "small", Cpus: 1, MemoryMb: 1, Image: "img", State: "ready",
		StateChangedAt: 1, TokenHash: "t", CreatedAt: 1,
	}))

	// 线程短码：唯一、可反查；未填的线程为 NULL 且可有多个。
	code := "3f9a1c0b7e2d"
	require.NoError(t, q.CreateThread(ctx, db.CreateThreadParams{
		ID: "T-01932f6e-8d1a-7c3b-9f2e-" + code, TitleSource: "auto", State: "idle", SessionDir: "/a",
		CreatedAt: 1, UpdatedAt: 1, ShortCode: &code, OrbID: ptr(orb),
	}))
	require.NoError(t, q.CreateThread(ctx, db.CreateThreadParams{ID: "T-x", TitleSource: "auto", State: "idle", SessionDir: "/b", CreatedAt: 1, UpdatedAt: 1}))
	require.NoError(t, q.CreateThread(ctx, db.CreateThreadParams{ID: "T-y", TitleSource: "auto", State: "idle", SessionDir: "/c", CreatedAt: 1, UpdatedAt: 1}))
	th, err := q.GetThreadByShortCode(ctx, code)
	require.NoError(t, err)
	assert.Equal(t, "T-01932f6e-8d1a-7c3b-9f2e-"+code, th.ID)
	assert.Error(t, q.CreateThread(ctx, db.CreateThreadParams{ID: "T-z", TitleSource: "auto", State: "idle", SessionDir: "/d", CreatedAt: 1, UpdatedAt: 1, ShortCode: &code}))

	// 服务镜像。
	p3000, p4000 := int64(3000), int64(4000)
	for _, sv := range []db.InsertOrbServiceParams{
		{OrbID: orb, Name: "web", Port: &p3000, HasPortal: 1, Doc: `{}`, UpdatedAt: 1},
		{OrbID: orb, Name: "api", Port: &p4000, HasPortal: 0, Doc: `{}`, UpdatedAt: 1},
		{OrbID: orb, Name: "worker", Doc: `{}`, UpdatedAt: 1},
	} {
		require.NoError(t, q.InsertOrbService(ctx, sv))
	}
	list, err := q.ListOrbServices(ctx, orb)
	require.NoError(t, err)
	require.Len(t, list, 3)
	assert.Equal(t, []string{"api", "web", "worker"}, []string{list[0].Name, list[1].Name, list[2].Name})
	got, err := q.GetOrbPortalService(ctx, db.GetOrbPortalServiceParams{OrbID: orb, Port: 3000})
	require.NoError(t, err)
	assert.Equal(t, "web", got.Name)
	_, err = q.GetOrbPortalService(ctx, db.GetOrbPortalServiceParams{OrbID: orb, Port: 4000})
	assert.Error(t, err) // 无 portal

	// 分享：upsert 续期只改 expires_at；未声明端口的分享被撤掉。
	require.NoError(t, q.UpsertPortalShare(ctx, db.UpsertPortalShareParams{OrbID: orb, Port: 3000, CreatedAt: 10, ExpiresAt: 100}))
	require.NoError(t, q.UpsertPortalShare(ctx, db.UpsertPortalShareParams{OrbID: orb, Port: 3000, CreatedAt: 20, ExpiresAt: 200}))
	require.NoError(t, q.UpsertPortalShare(ctx, db.UpsertPortalShareParams{OrbID: orb, Port: 4000, CreatedAt: 10, ExpiresAt: 100}))
	sh, err := q.GetPortalShare(ctx, db.GetPortalShareParams{OrbID: orb, Port: 3000})
	require.NoError(t, err)
	assert.Equal(t, int64(10), sh.CreatedAt)
	assert.Equal(t, int64(200), sh.ExpiresAt)
	n, err := q.DeleteUndeclaredPortalShares(ctx, orb)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	shares, err := q.ListPortalSharesByOrb(ctx, orb)
	require.NoError(t, err)
	require.Len(t, shares, 1)
	assert.Equal(t, int64(3000), shares[0].Port)
	all, err := q.ListPortalShares(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 1)
	n, err = q.DeletePortalShare(ctx, db.DeletePortalShareParams{OrbID: orb, Port: 3000})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	// portal 会话：随网页会话过期 / 恢复会话 / 自身过期失效，网页会话删除时级联删除。
	require.NoError(t, q.CreateWebSession(ctx, db.CreateWebSessionParams{IDHash: "wfull", Kind: "full", CreatedAt: 1, ExpiresAt: 1000, LastSeenAt: 1}))
	require.NoError(t, q.CreateWebSession(ctx, db.CreateWebSessionParams{IDHash: "wrec", Kind: "recovery", CreatedAt: 1, ExpiresAt: 1000, LastSeenAt: 1}))
	label := "t-" + code + "-p3000"
	for _, ps := range []db.InsertPortalSessionParams{
		{IDHash: "ok", WebSessionHash: "wfull", Label: label, CreatedAt: 1, ExpiresAt: 500},
		{IDHash: "rec", WebSessionHash: "wrec", Label: label, CreatedAt: 1, ExpiresAt: 500},
		{IDHash: "old", WebSessionHash: "wfull", Label: label, CreatedAt: 1, ExpiresAt: 50},
	} {
		require.NoError(t, q.InsertPortalSession(ctx, ps))
	}
	sess, err := q.GetPortalSession(ctx, db.GetPortalSessionParams{IDHash: "ok", Now: 100})
	require.NoError(t, err)
	assert.Equal(t, label, sess.Label)
	assert.Equal(t, "wfull", sess.WebSessionHash)
	_, err = q.GetPortalSession(ctx, db.GetPortalSessionParams{IDHash: "ok", Now: 600})
	assert.Error(t, err)
	_, err = q.GetPortalSession(ctx, db.GetPortalSessionParams{IDHash: "rec", Now: 100})
	assert.Error(t, err)
	_, err = q.GetPortalSession(ctx, db.GetPortalSessionParams{IDHash: "old", Now: 100})
	assert.Error(t, err)
	n, err = q.DeleteExpiredPortalSessions(ctx, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	_, err = s.DB.ExecContext(ctx, `DELETE FROM web_sessions WHERE id_hash = 'wfull'`)
	require.NoError(t, err)
	var left int
	require.NoError(t, s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM portal_sessions`).Scan(&left))
	assert.Equal(t, 1, left) // 只剩 rec

	// orb 删除级联清空镜像与分享。
	require.NoError(t, q.UpsertPortalShare(ctx, db.UpsertPortalShareParams{OrbID: orb, Port: 3000, CreatedAt: 1, ExpiresAt: 2}))
	require.NoError(t, q.DeletePortalSharesByOrb(ctx, orb))
	require.NoError(t, q.DeleteOrbServices(ctx, orb))
	list, err = q.ListOrbServices(ctx, orb)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func ptr[T any](v T) *T { return &v }
