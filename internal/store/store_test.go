package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// m2Tables 是计划 D 节列出的全部 M2 表，外加后续里程碑新增的表
// （M3：attachments；M4：orb_services、portal_shares、portal_sessions；
// M5：thread_shares、thread_share_entries、published_files、notification_channels、push_subscriptions、notifications、notification_deliveries）。
var m2Tables = []string{
	"attachments", "blobs", "device_pairings", "devices", "entries", "entry_cursors", "jobs",
	"notification_channels", "notification_deliveries", "notifications", "orb_events",
	"orb_samples", "orb_services", "orb_setup_runs", "orbs", "owner", "passkeys", "portal_sessions", "portal_shares",
	"projects", "providers", "published_files", "push_subscriptions", "recovery_codes", "secrets", "thread_labels",
	"thread_share_entries", "thread_shares", "threads", "tool_calls", "usage_records", "web_sessions",
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

// TestShareQueries 覆盖 M5 的分享范围读取（instr 过滤子 agent 文件与 blob 引用）、发布文件（JOIN、部分唯一索引、sqlc.slice）。
func TestShareQueries(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	q := s.Q
	const th = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"
	require.NoError(t, q.CreateThread(ctx, db.CreateThreadParams{ID: th, TitleSource: "auto", State: "idle", SessionDir: "/a", CreatedAt: 1, UpdatedAt: 1}))
	sha := "ab" + strings.Repeat("0", 62)
	for _, e := range []db.UpsertEntryParams{
		{ThreadID: th, File: "a.jsonl", EntryID: "e1", Type: "message", Payload: []byte(`{"text":"hi"}`)},
		{ThreadID: th, File: "a.jsonl", EntryID: "e2", ParentID: ptr("e1"), Type: "message", Payload: []byte(`{"img":"blob:sha256:` + sha + `"}`)},
		{ThreadID: th, File: "a/sub.jsonl", EntryID: "s1", Type: "message", Payload: []byte(`{"img":"blob:sha256:` + sha + `"}`)},
		{ThreadID: th, File: "a.jsonl", EntryID: "e3", ParentID: ptr("e2"), Type: "model_change", Payload: []byte(`{}`)},
	} {
		_, err := q.UpsertEntry(ctx, e)
		require.NoError(t, err)
	}
	links, err := q.ListMainEntryLinks(ctx, db.ListMainEntryLinksParams{ThreadID: th, UntilSeq: 3})
	require.NoError(t, err)
	require.Len(t, links, 2) // s1（seq 3）是子 agent 文件；e3（seq 4）越界
	assert.Equal(t, "e2", links[1].EntryID)
	assert.Equal(t, "e1", *links[1].ParentID)
	rng, err := q.ListEntriesBySeqRange(ctx, db.ListEntriesBySeqRangeParams{ThreadID: th, AfterSeq: 1, UntilSeq: 10, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rng, 2)
	assert.Equal(t, []int64{2, 4}, []int64{rng[0].Seq, rng[1].Seq})
	seqs, err := q.ListEntrySeqsContaining(ctx, db.ListEntrySeqsContainingParams{ThreadID: th, UntilSeq: 10, Needle: []byte("blob:sha256:" + sha)})
	require.NoError(t, err)
	assert.Equal(t, []int64{2}, seqs)

	// 分享：按 slug 只取未撤销的；计数与按线程分组。
	until, leaf := int64(4), "e3"
	require.NoError(t, q.InsertThreadShare(ctx, db.InsertThreadShareParams{ID: "shr_1", ThreadID: th, Slug: "s1", Mode: "snapshot", UntilSeq: &until, LeafEntryID: &leaf, Findings: 2, CreatedAt: 10}))
	require.NoError(t, q.InsertThreadShare(ctx, db.InsertThreadShareParams{ID: "shr_2", ThreadID: th, Slug: "s2", Mode: "live", CreatedAt: 20}))
	assert.Error(t, q.InsertThreadShare(ctx, db.InsertThreadShareParams{ID: "shr_3", ThreadID: th, Slug: "s2", Mode: "live", CreatedAt: 30}))
	assert.Error(t, q.InsertThreadShare(ctx, db.InsertThreadShareParams{ID: "shr_4", ThreadID: th, Slug: "s4", Mode: "bogus", CreatedAt: 30}))
	act, err := q.ListActiveThreadShares(ctx)
	require.NoError(t, err)
	require.Len(t, act, 2)
	assert.Equal(t, "shr_2", act[0].ID)
	n, err := q.RevokeThreadShare(ctx, db.RevokeThreadShareParams{Now: 50, ID: "shr_2"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	n, err = q.RevokeThreadShare(ctx, db.RevokeThreadShareParams{Now: 60, ID: "shr_2"})
	require.NoError(t, err)
	assert.Zero(t, n)
	_, err = q.GetActiveThreadShareBySlug(ctx, "s2")
	assert.Error(t, err)
	got, err := q.GetActiveThreadShareBySlug(ctx, "s1")
	require.NoError(t, err)
	assert.Equal(t, int64(4), *got.UntilSeq)
	cnt, err := q.CountActiveThreadShares(ctx, th)
	require.NoError(t, err)
	assert.Equal(t, int64(1), cnt)
	counts, err := q.ListActiveThreadShareCounts(ctx)
	require.NoError(t, err)
	assert.Equal(t, []db.ListActiveThreadShareCountsRow{{ThreadID: th, N: 1}}, counts)

	// 发布文件：同一附件至多一条未撤回发布；撤回后可重新发布。
	require.NoError(t, q.InsertAttachment(ctx, db.InsertAttachmentParams{ID: "att_1", ThreadID: ptr(th), Name: "a.png", Mime: "image/png", Size: 3, Sha256: sha, CreatedAt: 1}))
	require.NoError(t, q.InsertPublishedFile(ctx, db.InsertPublishedFileParams{ID: "pub_1", AttachmentID: "att_1", Slug: "f1", CreatedAt: 5}))
	assert.Error(t, q.InsertPublishedFile(ctx, db.InsertPublishedFileParams{ID: "pub_2", AttachmentID: "att_1", Slug: "f2", CreatedAt: 6}))
	pf, err := q.GetActivePublishedFileBySlug(ctx, "f1")
	require.NoError(t, err)
	assert.Equal(t, "a.png", pf.Attachment.Name)
	assert.Equal(t, "pub_1", pf.PublishedFile.ID)
	byThread, err := q.ListActivePublishedFilesByThread(ctx, th)
	require.NoError(t, err)
	require.Len(t, byThread, 1)
	byAtt, err := q.ListActivePublishedFilesByAttachments(ctx, []string{"att_1", "att_x"})
	require.NoError(t, err)
	require.Len(t, byAtt, 1)
	n, err = q.RevokePublishedFile(ctx, db.RevokePublishedFileParams{Now: 7, ID: "pub_1"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	require.NoError(t, q.InsertPublishedFile(ctx, db.InsertPublishedFileParams{ID: "pub_2", AttachmentID: "att_1", Slug: "f2", CreatedAt: 8}))
	cur, err := q.GetActivePublishedFileByAttachment(ctx, "att_1")
	require.NoError(t, err)
	assert.Equal(t, "pub_2", cur.ID)
	all, err := q.ListActivePublishedFiles(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)

	// 删除附件 / 线程级联清除发布与分享。
	_, err = q.DeleteAttachment(ctx, "att_1")
	require.NoError(t, err)
	_, err = q.GetActivePublishedFileBySlug(ctx, "f2")
	assert.Error(t, err)
	_, err = s.DB.ExecContext(ctx, `DELETE FROM threads WHERE id = ?`, th)
	require.NoError(t, err)
	act, err = q.ListActiveThreadShares(ctx)
	require.NoError(t, err)
	assert.Empty(t, act)
}

// TestNotifyQueries 覆盖 M5 的通知去重（部分唯一索引 + ON CONFLICT DO NOTHING）、订阅 upsert 与可投递过滤、投递队列。
func TestNotifyQueries(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	q := s.Q
	const th = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"
	require.NoError(t, q.CreateThread(ctx, db.CreateThreadParams{ID: th, TitleSource: "auto", State: "idle", SessionDir: "/a", CreatedAt: 1, UpdatedAt: 1}))

	// 渠道。
	require.NoError(t, q.InsertNotificationChannel(ctx, db.InsertNotificationChannelParams{
		ID: "nch_1", Kind: "webhook", Name: "hook", Enabled: true, Events: `["done"]`, Locale: "en", ConfigEnc: []byte{1}, State: "{}", CreatedAt: 1, UpdatedAt: 1,
	}))
	assert.Error(t, q.InsertNotificationChannel(ctx, db.InsertNotificationChannelParams{
		ID: "nch_2", Kind: "email", Name: "x", Events: `[]`, Locale: "en", ConfigEnc: []byte{1}, State: "{}", CreatedAt: 1, UpdatedAt: 1,
	}))
	require.NoError(t, q.SetNotificationChannelError(ctx, db.SetNotificationChannelErrorParams{LastError: ptr("boom"), ID: "nch_1"}))
	ch, err := q.GetNotificationChannel(ctx, "nch_1")
	require.NoError(t, err)
	assert.Equal(t, "boom", *ch.LastError)
	k, err := q.CountNotificationChannelsByKind(ctx, "webhook")
	require.NoError(t, err)
	assert.Equal(t, int64(1), k)

	// 订阅：按 endpoint upsert 保留 id；吊销设备的订阅不可投递；结果写回。
	require.NoError(t, q.CreateDevice(ctx, db.CreateDeviceParams{ID: "dev_1", Name: "d", Kind: "cli", Platform: "linux", TokenHash: "h1", CreatedAt: 1}))
	sub, err := q.UpsertPushSubscription(ctx, db.UpsertPushSubscriptionParams{ID: "psub_1", Kind: "webpush", Endpoint: "https://p/1", P256dh: "k", Auth: "a", DeviceID: ptr("dev_1"), CreatedAt: 1})
	require.NoError(t, err)
	assert.Equal(t, "psub_1", sub.ID)
	require.NoError(t, q.SetPushSubscriptionResult(ctx, db.SetPushSubscriptionResultParams{LastError: ptr("gone?"), ID: "psub_1"}))
	sub, err = q.UpsertPushSubscription(ctx, db.UpsertPushSubscriptionParams{ID: "psub_9", Kind: "webpush", Endpoint: "https://p/1", P256dh: "k2", Auth: "a2", DeviceID: ptr("dev_1"), UserAgent: "ua", CreatedAt: 9})
	require.NoError(t, err)
	assert.Equal(t, "psub_1", sub.ID)
	assert.Equal(t, "k2", sub.P256dh)
	assert.Nil(t, sub.LastError)
	assert.Equal(t, int64(1), sub.CreatedAt)
	_, err = q.UpsertPushSubscription(ctx, db.UpsertPushSubscriptionParams{ID: "psub_2", Kind: "webpush", Endpoint: "https://p/2", CreatedAt: 2})
	require.NoError(t, err)
	require.NoError(t, q.SetPushSubscriptionResult(ctx, db.SetPushSubscriptionResultParams{LastSuccessAt: ptr(int64(40)), ID: "psub_2"}))
	require.NoError(t, q.SetPushSubscriptionResult(ctx, db.SetPushSubscriptionResultParams{LastError: ptr("500"), ID: "psub_2"}))
	p2, err := q.GetPushSubscriptionByEndpoint(ctx, "https://p/2")
	require.NoError(t, err)
	assert.Equal(t, int64(40), *p2.LastSuccessAt) // 失败不清掉上次成功时间
	assert.Equal(t, "500", *p2.LastError)
	_, err = q.RevokeDevice(ctx, db.RevokeDeviceParams{RevokedAt: ptr(int64(5)), ID: "dev_1"})
	require.NoError(t, err)
	deliverable, err := q.ListDeliverablePushSubscriptions(ctx)
	require.NoError(t, err)
	require.Len(t, deliverable, 1)
	assert.Equal(t, "psub_2", deliverable[0].ID)
	subs, err := q.ListPushSubscriptions(ctx)
	require.NoError(t, err)
	assert.Len(t, subs, 2)

	// 通知：同一未决请求只插入一次；解决后同一请求可再次通知。
	ins := func(id, req string, at int64) int64 {
		n, err := q.InsertNotification(ctx, db.InsertNotificationParams{ID: id, ThreadID: ptr(th), Kind: "input", Title: "t", RequestID: &req, Request: ptr(`{}`), CreatedAt: at})
		require.NoError(t, err)
		return n
	}
	assert.Equal(t, int64(1), ins("ntf_1", "r1", 10))
	assert.Equal(t, int64(0), ins("ntf_2", "r1", 11))
	assert.Equal(t, int64(1), ins("ntf_3", "r2", 12))
	ids, err := q.ResolveNotificationRequest(ctx, db.ResolveNotificationRequestParams{Now: 20, ThreadID: th, RequestID: "r1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"ntf_1"}, ids)
	assert.Equal(t, int64(1), ins("ntf_4", "r1", 13))
	n, err := q.InsertNotification(ctx, db.InsertNotificationParams{ID: "ntf_5", ThreadID: ptr(th), Kind: "done", Title: "t", CreatedAt: 14})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	list, err := q.ListNotifications(ctx, db.ListNotificationsParams{Before: 14, Limit: 2})
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, []string{"ntf_4", "ntf_3"}, []string{list[0].ID, list[1].ID})
	unread, err := q.CountUnreadNotifications(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(4), unread)
	threads, err := q.ListUnreadNotificationThreads(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{th}, threads)
	n, err = q.MarkThreadNotificationsRead(ctx, db.MarkThreadNotificationsReadParams{Now: 30, ThreadID: th})
	require.NoError(t, err)
	assert.Equal(t, int64(4), n)
	n, err = q.MarkAllNotificationsRead(ctx, 31)
	require.NoError(t, err)
	assert.Zero(t, n)
	threads, err = q.ListUnreadNotificationThreads(ctx)
	require.NoError(t, err)
	assert.Empty(t, threads)

	// 投递队列。
	next, err := q.GetNextNotificationDeliveryAt(ctx)
	require.NoError(t, err)
	assert.Zero(t, next)
	require.NoError(t, q.InsertNotificationDelivery(ctx, db.InsertNotificationDeliveryParams{ID: "ndl_1", NotificationID: "ntf_5", ChannelID: ptr("nch_1"), Target: "webhook", NextAttemptAt: 100, CreatedAt: 14}))
	require.NoError(t, q.InsertNotificationDelivery(ctx, db.InsertNotificationDeliveryParams{ID: "ndl_2", NotificationID: "ntf_5", SubscriptionID: ptr("psub_2"), Target: "webpush", NextAttemptAt: 50, CreatedAt: 14}))
	next, err = q.GetNextNotificationDeliveryAt(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(50), next)
	due, err := q.ListDueNotificationDeliveries(ctx, 60)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, "ndl_2", due[0].ID)
	require.NoError(t, q.RescheduleNotificationDelivery(ctx, db.RescheduleNotificationDeliveryParams{Attempts: 1, NextAttemptAt: 110, LastError: ptr("500"), ID: "ndl_2"}))
	require.NoError(t, q.FinishNotificationDelivery(ctx, db.FinishNotificationDeliveryParams{Status: "sent", Attempts: 1, ExternalRef: ptr("42:7"), FinishedAt: 101, ID: "ndl_1"}))
	due, err = q.ListDueNotificationDeliveries(ctx, 105)
	require.NoError(t, err)
	assert.Empty(t, due)
	byRef, err := q.GetNotificationDeliveryByRef(ctx, db.GetNotificationDeliveryByRefParams{Target: "webhook", Ref: "42:7"})
	require.NoError(t, err)
	assert.Equal(t, "ndl_1", byRef.ID)
	assert.Nil(t, byRef.NextAttemptAt)
	refs, err := q.ListDeliveryRefsByNotification(ctx, "ntf_5")
	require.NoError(t, err)
	require.Len(t, refs, 1)
	dl, err := q.ListDeliveriesByNotifications(ctx, []string{"ntf_5", "ntf_1"})
	require.NoError(t, err)
	assert.Len(t, dl, 2)
	last, err := q.GetLastDeliveryByChannel(ctx, "nch_1")
	require.NoError(t, err)
	assert.Equal(t, "sent", last.Status)
	assert.Error(t, q.InsertNotificationDelivery(ctx, db.InsertNotificationDeliveryParams{ID: "ndl_3", NotificationID: "ntf_x", Target: "webhook", NextAttemptAt: 1, CreatedAt: 1}))

	// 最近的主会话消息（倒序，跳过子 agent 文件与非消息条目）。
	for _, e := range []db.UpsertEntryParams{
		{ThreadID: th, File: "a.jsonl", EntryID: "m1", Type: "message", Payload: []byte(`1`)},
		{ThreadID: th, File: "a/sub.jsonl", EntryID: "m2", Type: "message", Payload: []byte(`2`)},
		{ThreadID: th, File: "a.jsonl", EntryID: "m3", Type: "model_change", Payload: []byte(`3`)},
		{ThreadID: th, File: "a.jsonl", EntryID: "m4", Type: "message", Payload: []byte(`4`)},
	} {
		_, err := q.UpsertEntry(ctx, e)
		require.NoError(t, err)
	}
	msgs, err := q.ListRecentMainMessages(ctx, db.ListRecentMainMessagesParams{ThreadID: th, Limit: 20})
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, "4", string(msgs[0].Payload))

	// 清理与级联：删除旧通知带走投递；删除渠道 / 订阅带走其投递。
	n, err = q.DeleteNotificationsBefore(ctx, 13)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n) // ntf_1、ntf_3
	n, err = q.DeleteNotificationChannel(ctx, "nch_1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	dl, err = q.ListDeliveriesByNotifications(ctx, []string{"ntf_5"})
	require.NoError(t, err)
	require.Len(t, dl, 1)
	assert.Equal(t, "ndl_2", dl[0].ID)
	n, err = q.DeletePushSubscription(ctx, "psub_2")
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	dl, err = q.ListDeliveriesByNotifications(ctx, []string{"ntf_5"})
	require.NoError(t, err)
	assert.Empty(t, dl)
}

func ptr[T any](v T) *T { return &v }
