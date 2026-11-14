package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// dumpRows 把查询结果逐行格式化成字符串（NULL 记为 <nil>），用于逐列比较。
func dumpRows(t *testing.T, conn *sql.DB, query string) []string {
	t.Helper()
	rows, err := conn.QueryContext(context.Background(), query)
	require.NoError(t, err)
	defer rows.Close()
	cols, err := rows.Columns()
	require.NoError(t, err)
	var out []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		require.NoError(t, rows.Scan(ptrs...))
		parts := make([]string, len(vals))
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			parts[i] = fmt.Sprint(v)
		}
		out = append(out, strings.Join(parts, "|"))
	}
	require.NoError(t, rows.Err())
	return out
}

func scalarString(t *testing.T, conn *sql.DB, query string, args ...any) string {
	t.Helper()
	var s string
	require.NoError(t, conn.QueryRowContext(context.Background(), query, args...).Scan(&s))
	return s
}

// TestM10NotificationsRebuild 覆盖 0018 对 notifications / notification_deliveries 的重建：
// 数据与投递逐列保留、无线程通知回填 link、索引与外键完好、kind 接受 budget、渠道 events 补 budget；Down 复原。
func TestM10NotificationsRebuild(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	sub, err := fs.Sub(migrationsFS, "migrations")
	require.NoError(t, err)
	p, err := goose.NewProvider(database.DialectSQLite3, s.DB, sub)
	require.NoError(t, err)
	r, err := p.Down(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 18, r.Source.Version)

	// 0017 的结构上造数据（notifications / deliveries 用原生 SQL：生成的查询已带 link 列）。
	q := s.Q
	const th = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"
	require.NoError(t, q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: th, TitleSource: "auto", State: "idle", SessionDir: "/a", CreatedAt: 1, UpdatedAt: 1}))
	require.NoError(t, q.InsertNotificationChannel(ctx, db.InsertNotificationChannelParams{
		ID: "nch_a", Kind: "webhook", Name: "all", Enabled: true, Events: `["done","input","error"]`, Locale: "en", ConfigEnc: []byte{1}, State: "{}", CreatedAt: 1, UpdatedAt: 1,
	}))
	require.NoError(t, q.InsertNotificationChannel(ctx, db.InsertNotificationChannelParams{
		ID: "nch_b", Kind: "webhook", Name: "input", Enabled: true, Events: `["input"]`, Locale: "en", ConfigEnc: []byte{1}, State: "{}", CreatedAt: 1, UpdatedAt: 1,
	}))
	_, err = q.UpsertPushSubscription(ctx, db.UpsertPushSubscriptionParams{ID: "psub_1", Kind: "webpush", Endpoint: "https://p/1", P256dh: "k", Auth: "a", CreatedAt: 1})
	require.NoError(t, err)
	_, err = s.DB.ExecContext(ctx, `INSERT INTO notifications (id, thread_id, kind, title, body, request_id, request, created_at, read_at, resolved_at) VALUES
		('ntf_done', ?, 'done', 'Done', 'b1', NULL, NULL, 10, 15, NULL),
		('ntf_err', NULL, 'error', 'Schedule failed', 'b2', NULL, NULL, 11, NULL, NULL),
		('ntf_in', ?, 'input', 'Ask', 'b3', 'r1', '{"x":1}', 12, NULL, NULL)`, th, th)
	require.NoError(t, err)
	_, err = s.DB.ExecContext(ctx, `INSERT INTO notification_deliveries
		(id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at) VALUES
		('ndl_1', 'ntf_done', 'nch_a', NULL, 'webhook', 'sent', 1, NULL, NULL, 'ref1', 10, 11),
		('ndl_2', 'ntf_err', NULL, 'psub_1', 'webpush', 'pending', 2, 99, 'later', NULL, 11, NULL),
		('ndl_3', 'ntf_in', 'nch_b', NULL, 'webhook', 'failed', 5, NULL, 'boom', NULL, 12, 20)`)
	require.NoError(t, err)

	const notifCols = `SELECT id, thread_id, kind, title, body, request_id, request, created_at, read_at, resolved_at FROM notifications ORDER BY id`
	const delivCols = `SELECT id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at
		FROM notification_deliveries ORDER BY id`
	beforeN := dumpRows(t, s.DB, notifCols)
	beforeD := dumpRows(t, s.DB, delivCols)
	require.Len(t, beforeN, 3)
	require.Len(t, beforeD, 3)

	_, err = p.Up(ctx)
	require.NoError(t, err)

	assert.Equal(t, beforeN, dumpRows(t, s.DB, notifCols), "三条通知逐列相等")
	assert.Equal(t, beforeD, dumpRows(t, s.DB, delivCols), "三行投递逐列相等")
	assert.Equal(t, []string{"ntf_done|<nil>", "ntf_err|/settings/automations", "ntf_in|<nil>"},
		dumpRows(t, s.DB, `SELECT id, link FROM notifications ORDER BY id`))
	assert.Equal(t, []string{
		"notification_deliveries_channel", "notification_deliveries_due", "notification_deliveries_notification", "notification_deliveries_ref",
		"notifications_created", "notifications_open_request", "notifications_thread",
	}, dumpRows(t, s.DB, `SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name IN ('notifications', 'notification_deliveries')
		AND name NOT LIKE 'sqlite_autoindex_%' ORDER BY name`))
	assert.Empty(t, dumpRows(t, s.DB, `PRAGMA foreign_key_check`))
	assert.Empty(t, dumpRows(t, s.DB, `SELECT name FROM sqlite_master WHERE name LIKE '%\_keep' ESCAPE '\'`), "暂存表已删")

	link := "/stats/usage"
	n, err := q.InsertNotification(ctx, db.InsertNotificationParams{ID: "ntf_bud", Kind: "budget", Title: "Daily budget exceeded", Link: &link, CreatedAt: 30})
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	got, err := q.GetNotification(ctx, "ntf_bud")
	require.NoError(t, err)
	assert.Nil(t, got.ThreadID)
	assert.Equal(t, "/stats/usage", *got.Link)
	_, err = q.InsertNotification(ctx, db.InsertNotificationParams{ID: "ntf_bad", Kind: "bogus", Title: "x", CreatedAt: 30})
	assert.Error(t, err, "CHECK 拒绝未知 kind")
	req := "r1"
	n, err = q.InsertNotification(ctx, db.InsertNotificationParams{ID: "ntf_dup", ThreadID: ptr(th), Kind: "input", Title: "Ask", RequestID: &req, CreatedAt: 31})
	require.NoError(t, err)
	assert.Zero(t, n, "同一未决请求不重复插入")

	assert.Equal(t, []string{`nch_a|["done","input","error","budget"]`, `nch_b|["input"]`},
		dumpRows(t, s.DB, `SELECT id, events FROM notification_channels ORDER BY id`))

	// 级联：删通知 → 投递随之删除。
	_, err = s.DB.ExecContext(ctx, `DELETE FROM notifications WHERE id = 'ntf_done'`)
	require.NoError(t, err)
	assert.Equal(t, "0", scalarString(t, s.DB, `SELECT COUNT(*) FROM notification_deliveries WHERE notification_id = 'ntf_done'`))

	// 按 id 已读。
	n, err = q.MarkNotificationRead(ctx, db.MarkNotificationReadParams{Now: 40, ID: "ntf_bud"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	n, err = q.MarkNotificationRead(ctx, db.MarkNotificationReadParams{Now: 41, ID: "ntf_bud"})
	require.NoError(t, err)
	assert.Zero(t, n, "已读不再更新")
	assert.Equal(t, "<nil>", dumpRows(t, s.DB, `SELECT read_at FROM notifications WHERE id = 'ntf_err'`)[0], "别的通知不受影响")

	// 预算通知的投递 → Down：预算通知与其投递消失，其余保留，link 列消失，渠道 events 复原。
	require.NoError(t, q.InsertNotificationDelivery(ctx, db.InsertNotificationDeliveryParams{ID: "ndl_4", NotificationID: "ntf_bud", ChannelID: ptr("nch_a"), Target: "webhook", NextAttemptAt: 50, CreatedAt: 30}))
	keepN := dumpRows(t, s.DB, `SELECT id, thread_id, kind, title, body, request_id, request, created_at, read_at, resolved_at FROM notifications WHERE kind <> 'budget' ORDER BY id`)
	keepD := dumpRows(t, s.DB, `SELECT id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at
		FROM notification_deliveries WHERE id <> 'ndl_4' ORDER BY id`)
	r, err = p.Down(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 18, r.Source.Version)
	assert.Equal(t, keepN, dumpRows(t, s.DB, notifCols))
	assert.Equal(t, keepD, dumpRows(t, s.DB, delivCols))
	assert.Equal(t, []string{"ntf_err", "ntf_in"}, dumpRows(t, s.DB, `SELECT id FROM notifications ORDER BY id`))
	assert.Equal(t, "0", scalarString(t, s.DB, `SELECT COUNT(*) FROM pragma_table_info('notifications') WHERE name = 'link'`))
	assert.Equal(t, []string{`nch_a|["done","input","error"]`, `nch_b|["input"]`},
		dumpRows(t, s.DB, `SELECT id, events FROM notification_channels ORDER BY id`))
	assert.Empty(t, dumpRows(t, s.DB, `PRAGMA foreign_key_check`))
	_, err = s.DB.ExecContext(ctx, `INSERT INTO notifications (id, kind, title, created_at) VALUES ('ntf_x', 'budget', 't', 1)`)
	assert.Error(t, err, "回滚后 CHECK 回到三值")
}

// TestM10StatsQueries 覆盖 M10 的事实行写入 / 去重、聚合开关与筛选（含已删线程的归属快照回落）、
// 孤儿清理、归属快照、orb 统计的 state_at_from、预算账本去重与明细键集分页。
func TestM10StatsQueries(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	q := s.Q
	const day = int64(86400000)
	for _, id := range []string{"prj_1", "prj_2"} {
		require.NoError(t, q.CreateProject(ctx, db.CreateProjectParams{ID: id, Name: id, ShallowClone: true, CreatedAt: 1, UpdatedAt: 1}))
	}
	thread := func(id string, project, space *string) {
		require.NoError(t, q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: id, ProjectID: project, SpaceID: space, TitleSource: "auto", State: "idle", SessionDir: "/a", CreatedAt: 1, UpdatedAt: 1}))
	}
	thread("T-1", ptr("prj_1"), ptr("spc_1"))
	thread("T-2", ptr("prj_2"), nil)
	thread("T-3", ptr("prj_1"), ptr("spc_1")) // 稍后删除：靠归属快照
	usage := func(p db.InsertUsageRecordParams) int64 {
		n, err := q.InsertUsageRecord(ctx, p)
		require.NoError(t, err)
		return n
	}

	// ---- 写入与 ukey 去重
	assert.EqualValues(t, 1, usage(db.InsertUsageRecordParams{ThreadID: ptr("T-1"), EntrySeq: 1, Ts: 1000, Provider: "anthropic", Model: "opus", Ukey: ptr("e1@1000"),
		StopReason: "stop", InputTokens: 10, OutputTokens: 200, CostUsd: 1.0, DurationMs: ptr(int64(2000)), TtftMs: ptr(int64(100))}))
	assert.Zero(t, usage(db.InsertUsageRecordParams{ThreadID: ptr("T-2"), EntrySeq: 7, Ts: 1000, Provider: "anthropic", Model: "opus", Ukey: ptr("e1@1000"), CostUsd: 1.0}),
		"别的线程（fork 拷贝）占用同一 ukey 被忽略")
	assert.Zero(t, usage(db.InsertUsageRecordParams{ThreadID: ptr("T-1"), EntrySeq: 1, Ts: 1000, Provider: "x", Model: "y", Ukey: ptr("other@1")}),
		"同一 (thread_id, entry_seq) 被忽略")
	assert.EqualValues(t, 1, usage(db.InsertUsageRecordParams{ThreadID: ptr("T-1"), EntrySeq: 2, Ts: 2000, Provider: "anthropic", Model: "opus", Ukey: ptr("e2@2000"),
		Purpose: "title", StopReason: "error", CostUsd: 0.5, IsSubagent: true}))
	assert.EqualValues(t, 1, usage(db.InsertUsageRecordParams{ThreadID: ptr("T-2"), EntrySeq: 1, Ts: day + 5, Provider: "openai", Model: "gpt", Ukey: ptr("e3@x"), CostUsd: 0.25}))
	assert.EqualValues(t, 1, usage(db.InsertUsageRecordParams{ThreadID: ptr("T-3"), EntrySeq: 1, Ts: 3000, Provider: "anthropic", Model: "opus", Ukey: ptr("e4@3000"), CostUsd: 2.0}))
	assert.EqualValues(t, 1, usage(db.InsertUsageRecordParams{ThreadID: ptr("T-1"), EntrySeq: 9, Ts: 10 * day, Provider: "openai", Model: "gpt", CostUsd: 100})) // 窗口外；ukey NULL 允许多个
	n, err := q.InsertToolCall(ctx, db.InsertToolCallParams{ThreadID: ptr("T-1"), EntrySeq: 3, Ts: 1500, Tool: "bash", Ukey: ptr("t1@1500")})
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	n, err = q.InsertToolCall(ctx, db.InsertToolCallParams{ThreadID: ptr("T-2"), EntrySeq: 3, Ts: 1500, Tool: "bash", Ukey: ptr("t1@1500")})
	require.NoError(t, err)
	assert.Zero(t, n)
	for _, c := range []db.InsertToolCallParams{
		{ThreadID: ptr("T-1"), EntrySeq: 4, Ts: 1600, Tool: "bash", IsError: true, Ukey: ptr("t2@1600")},
		{ThreadID: ptr("T-3"), EntrySeq: 2, Ts: 3100, Tool: "read", Ukey: ptr("t3@3100")},
	} {
		_, err := q.InsertToolCall(ctx, c)
		require.NoError(t, err)
	}
	for _, r := range []db.InsertTurnParams{
		{ThreadID: ptr("T-1"), EntrySeq: 0, Ts: 900, Ukey: ptr("u1@900")},
		{ThreadID: ptr("T-3"), EntrySeq: 0, Ts: 2900, Ukey: ptr("u2@2900")},
	} {
		n, err := q.InsertTurn(ctx, r)
		require.NoError(t, err)
		assert.EqualValues(t, 1, n)
	}
	n, err = q.InsertTurn(ctx, db.InsertTurnParams{ThreadID: ptr("T-2"), EntrySeq: 0, Ts: 900, Ukey: ptr("u1@900")})
	require.NoError(t, err)
	assert.Zero(t, n)

	// ---- 删线程：先写归属快照，再删行（thread_id 经 ON DELETE SET NULL 置空）
	for _, f := range []func(context.Context, string) error{
		func(ctx context.Context, id string) error {
			return q.TombstoneUsage(ctx, db.TombstoneUsageParams{ProjectID: ptr("prj_1"), SpaceID: ptr("spc_1"), ThreadID: id})
		},
		func(ctx context.Context, id string) error {
			return q.TombstoneToolCalls(ctx, db.TombstoneToolCallsParams{ProjectID: ptr("prj_1"), SpaceID: ptr("spc_1"), ThreadID: id})
		},
		func(ctx context.Context, id string) error {
			return q.TombstoneTurns(ctx, db.TombstoneTurnsParams{ProjectID: ptr("prj_1"), SpaceID: ptr("spc_1"), ThreadID: id})
		},
	} {
		require.NoError(t, f(ctx, "T-3"))
	}
	_, err = q.DeleteThread(ctx, "T-3")
	require.NoError(t, err)
	for _, tbl := range []string{"usage_records", "tool_calls", "turns"} {
		assert.Equal(t, []string{"<nil>|prj_1|spc_1"}, dumpRows(t, s.DB,
			`SELECT thread_id, project_id, space_id FROM `+tbl+` WHERE ukey LIKE '%@3%' OR ukey = 'u2@2900'`), tbl)
	}
	assert.Zero(t, usage(db.InsertUsageRecordParams{ThreadID: ptr("T-2"), EntrySeq: 8, Ts: 3000, Ukey: ptr("e4@3000"), CostUsd: 2.0}),
		"已删线程的 ukey 仍然占用")

	// ---- StatsUsageAgg：开关与筛选
	agg := func(p db.StatsUsageAggParams) []db.StatsUsageAggRow {
		if p.ToTs == 0 {
			p.FromTs, p.ToTs = 0, 5*day
		}
		if p.BucketMs == 0 {
			p.BucketMs = day
		}
		rows, err := q.StatsUsageAgg(ctx, p)
		require.NoError(t, err)
		return rows
	}
	costBy := func(rows []db.StatsUsageAggRow, key func(db.StatsUsageAggRow) string) map[string]float64 {
		m := map[string]float64{}
		for _, r := range rows {
			m[key(r)] += r.CostUsd
		}
		return m
	}
	all := agg(db.StatsUsageAggParams{})
	require.Len(t, all, 1)
	tot := all[0]
	assert.EqualValues(t, 4, tot.Requests)
	assert.EqualValues(t, 1, tot.RequestErrors)
	assert.InDelta(t, 3.75, tot.CostUsd, 1e-9)
	assert.InDelta(t, 0.5, tot.SubagentCostUsd, 1e-9)
	assert.EqualValues(t, 100, tot.TtftSumMs)
	assert.EqualValues(t, 1, tot.TtftN)
	assert.EqualValues(t, 200, tot.TimedOutputTokens)
	assert.EqualValues(t, 2000, tot.DurationMs)
	assert.Equal(t, "", tot.ThreadID+tot.ProjectID+tot.SpaceID+tot.Model+tot.Purpose, "关掉的分组键为空串")

	assert.Equal(t, map[string]float64{"prj_1": 3.5, "prj_2": 0.25},
		costBy(agg(db.StatsUsageAggParams{ByProject: true}), func(r db.StatsUsageAggRow) string { return r.ProjectID }), "已删线程按快照归到 prj_1")
	assert.Equal(t, map[string]float64{"spc_1": 3.5, "": 0.25},
		costBy(agg(db.StatsUsageAggParams{BySpace: true}), func(r db.StatsUsageAggRow) string { return r.SpaceID }))
	assert.Equal(t, map[string]float64{"T-1": 1.5, "T-2": 0.25, "": 2.0},
		costBy(agg(db.StatsUsageAggParams{ByThread: true}), func(r db.StatsUsageAggRow) string { return r.ThreadID }))
	assert.Equal(t, map[string]float64{"anthropic/opus|": 3.0, "anthropic/opus|title": 0.5, "openai/gpt|": 0.25},
		costBy(agg(db.StatsUsageAggParams{ByModel: true, ByPurpose: true}), func(r db.StatsUsageAggRow) string { return r.Model + "|" + r.Purpose }))
	assert.Equal(t, map[string]float64{"0": 3.5, "1": 0.25},
		costBy(agg(db.StatsUsageAggParams{ByBucket: true}), func(r db.StatsUsageAggRow) string { return fmt.Sprint(r.Bucket) }))
	assert.Equal(t, map[string]float64{"0": 1.0, "1": 2.75},
		costBy(agg(db.StatsUsageAggParams{ByBucket: true, OffsetMs: day - 1500}), func(r db.StatsUsageAggRow) string { return fmt.Sprint(r.Bucket) }), "偏移移动日界")

	sum := func(rows []db.StatsUsageAggRow) float64 {
		var c float64
		for _, r := range rows {
			c += r.CostUsd
		}
		return c
	}
	assert.InDelta(t, 3.5, sum(agg(db.StatsUsageAggParams{Project: ptr("prj_1")})), 1e-9, "project 筛选含已删线程的快照")
	assert.InDelta(t, 0.25, sum(agg(db.StatsUsageAggParams{Space: ptr("")})), 1e-9, "space = none")
	assert.InDelta(t, 1.5, sum(agg(db.StatsUsageAggParams{Thread: ptr("T-1")})), 1e-9)
	assert.InDelta(t, 0.25, sum(agg(db.StatsUsageAggParams{Model: ptr("openai/gpt")})), 1e-9)
	assert.InDelta(t, 103.75, sum(agg(db.StatsUsageAggParams{FromTs: 0, ToTs: 11 * day})), 1e-9)

	// ---- StatsToolAgg / StatsTurnAgg
	tools, err := q.StatsToolAgg(ctx, db.StatsToolAggParams{ByTool: true, BucketMs: day, FromTs: 0, ToTs: day})
	require.NoError(t, err)
	toolMap := map[string][2]int64{}
	for _, r := range tools {
		toolMap[r.Tool] = [2]int64{r.Calls, r.Errors}
	}
	assert.Equal(t, map[string][2]int64{"bash": {2, 1}, "read": {1, 0}}, toolMap)
	tools, err = q.StatsToolAgg(ctx, db.StatsToolAggParams{ByProject: true, BucketMs: day, FromTs: 0, ToTs: day, Project: ptr("prj_1")})
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.EqualValues(t, 3, tools[0].Calls)
	turns, err := q.StatsTurnAgg(ctx, db.StatsTurnAggParams{BySpace: true, BucketMs: day, FromTs: 0, ToTs: day})
	require.NoError(t, err)
	require.Len(t, turns, 1)
	assert.Equal(t, "spc_1", turns[0].SpaceID)
	assert.EqualValues(t, 2, turns[0].Turns)

	// ---- 其它用量查询
	minTs, err := q.MinUsageTs(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1000, minTs)
	cost, err := q.SumUsageCost(ctx, db.SumUsageCostParams{FromTs: 0, ToTs: day})
	require.NoError(t, err)
	assert.InDelta(t, 3.5, cost, 1e-9)
	cnt, err := q.CountUsageRecords(ctx, db.CountUsageRecordsParams{FromTs: 0, ToTs: 11 * day, Project: ptr("prj_1")})
	require.NoError(t, err)
	assert.EqualValues(t, 4, cnt)

	// ---- 明细键集分页（同一 ts 的行按 id 续翻）
	for i := range 3 {
		usage(db.InsertUsageRecordParams{ThreadID: ptr("T-2"), EntrySeq: int64(20 + i), Ts: 2000, Provider: "p", Model: "m"})
	}
	var pages [][]int64
	var seen []int64
	afterTs, afterID := int64(0), int64(0)
	for {
		rows, err := q.ListUsageRecordsPage(ctx, db.ListUsageRecordsPageParams{FromTs: 0, ToTs: day, AfterTs: afterTs, AfterID: afterID, Limit: 2})
		require.NoError(t, err)
		if len(rows) == 0 {
			break
		}
		var ids []int64
		for _, r := range rows {
			ids = append(ids, r.ID)
			seen = append(seen, r.Ts)
		}
		pages = append(pages, ids)
		afterTs, afterID = rows[len(rows)-1].Ts, rows[len(rows)-1].ID
	}
	assert.Len(t, pages, 3, "6 行、每页 2 行")
	assert.Equal(t, []int64{1000, 2000, 2000, 2000, 2000, 3000}, seen, "按 ts 升序且同 ts 不丢不重")
	rows, err := q.ListUsageRecordsPage(ctx, db.ListUsageRecordsPageParams{FromTs: 0, ToTs: day, Limit: 10, Thread: ptr("T-1")})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "prj_1", rows[0].ProjectID)
	assert.Equal(t, "spc_1", rows[0].SpaceID)
	rows, err = q.ListUsageRecordsPage(ctx, db.ListUsageRecordsPageParams{FromTs: 0, ToTs: day, Limit: 10, Space: ptr("spc_1")})
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Equal(t, "", rows[2].ThreadID, "已删线程")
	assert.Equal(t, "prj_1", rows[2].ProjectID)

	// ---- 孤儿：条目不存在的事实行被删，存在的保留
	thread("T-4", nil, nil)
	seq, err := q.UpsertEntry(ctx, db.UpsertEntryParams{ThreadID: "T-4", File: "a.jsonl", EntryID: "aa", Type: "message", Payload: []byte(`{}`)})
	require.NoError(t, err)
	usage(db.InsertUsageRecordParams{ThreadID: ptr("T-4"), EntrySeq: seq, Ts: 1})
	usage(db.InsertUsageRecordParams{ThreadID: ptr("T-4"), EntrySeq: seq + 100, Ts: 1})
	_, err = q.InsertToolCall(ctx, db.InsertToolCallParams{ThreadID: ptr("T-4"), EntrySeq: seq + 100, Ts: 1, Tool: "x"})
	require.NoError(t, err)
	_, err = q.InsertTurn(ctx, db.InsertTurnParams{ThreadID: ptr("T-4"), EntrySeq: seq, Ts: 1})
	require.NoError(t, err)
	n, err = q.DeleteOrphanUsage(ctx, "T-4")
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	n, err = q.DeleteOrphanToolCalls(ctx, "T-4")
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	n, err = q.DeleteOrphanTurns(ctx, "T-4")
	require.NoError(t, err)
	assert.Zero(t, n)
	n, err = q.DeleteOrphanUsage(ctx, "T-1")
	require.NoError(t, err)
	assert.EqualValues(t, 3, n, "只动给定线程（T-1 没有条目）")
	assert.Equal(t, "1", scalarString(t, s.DB, `SELECT COUNT(*) FROM usage_records WHERE thread_id = 'T-4'`))
	require.NoError(t, q.DeleteUsageAt(ctx, db.DeleteUsageAtParams{ThreadID: "T-4", EntrySeq: seq}))
	assert.Equal(t, "0", scalarString(t, s.DB, `SELECT COUNT(*) FROM usage_records WHERE thread_id = 'T-4'`))

	// ---- orb：state_at_from 与窗口
	require.NoError(t, q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "p", TokenHash: "h", CreatedAt: 1}))
	orb := func(id string, project *string, created int64, snapshot *string) {
		require.NoError(t, q.CreateOrb(ctx, db.CreateOrbParams{ID: id, ProviderID: "prov_1", Type: "small", Cpus: 2, MemoryMb: 4096, Image: "i",
			ProjectID: project, State: "ready", TokenHash: "t-" + id, SnapshotOf: snapshot, CreatedAt: created}))
	}
	orb("orb-a", ptr("prj_1"), 5, nil)
	orb("orb-b", nil, 25, ptr("oryn-snap-1"))
	orb("orb-late", nil, 500, nil)
	orb("orb-dead", nil, 1, nil)
	_, err = s.DB.ExecContext(ctx, `UPDATE orbs SET deleted_at = 8 WHERE id = 'orb-dead'`)
	require.NoError(t, err)
	ev := func(orb string, ts int64, from, to string) {
		require.NoError(t, q.InsertOrbEvent(ctx, db.InsertOrbEventParams{OrbID: orb, Ts: ts, FromState: from, ToState: to, Detail: "{}"}))
	}
	ev("orb-a", 10, "", "requested")
	ev("orb-a", 20, "requested", "spawning")
	ev("orb-a", 20, "spawning", "connecting") // 同一 ts：按 id 取最后一条
	ev("orb-a", 50, "connecting", "ready")
	ev("orb-b", 30, "", "requested")
	ev("orb-dead", 2, "", "requested")
	orbs, err := q.ListOrbsForStats(ctx, db.ListOrbsForStatsParams{FromTs: 30, ToTs: 100})
	require.NoError(t, err)
	got := map[string]string{}
	for _, o := range orbs {
		got[o.ID] = o.StateAtFrom
	}
	assert.Equal(t, map[string]string{"orb-a": "connecting", "orb-b": ""}, got, "orb-late 在窗口后创建、orb-dead 在窗口前删除")
	orbs, err = q.ListOrbsForStats(ctx, db.ListOrbsForStatsParams{FromTs: 30, ToTs: 100, Project: ptr("prj_1")})
	require.NoError(t, err)
	require.Len(t, orbs, 1)
	assert.Equal(t, "orb-a", orbs[0].ID)
	orbs, err = q.ListOrbsForStats(ctx, db.ListOrbsForStatsParams{FromTs: 30, ToTs: 100, Orb: ptr("orb-b")})
	require.NoError(t, err)
	require.Len(t, orbs, 1)
	assert.Equal(t, "oryn-snap-1", *orbs[0].SnapshotOf)
	evs, err := q.ListOrbEventsInWindow(ctx, db.ListOrbEventsInWindowParams{FromTs: 20, ToTs: 50})
	require.NoError(t, err)
	assert.Len(t, evs, 3)
	tl, err := q.ListOrbEventsForTimeline(ctx, db.ListOrbEventsForTimelineParams{OrbID: "orb-a", FromTs: 30, ToTs: 100})
	require.NoError(t, err)
	require.Len(t, tl, 3, "从窗口前最后一条事件的 ts 起")
	assert.Equal(t, "spawning", tl[0].ToState)

	// ---- 预算账本：同一 (period, period_key, level) 第二次返回 0 行
	alert := db.InsertBudgetAlertParams{Period: "day", PeriodKey: "2026-11-14", Level: "warn", SpentUsd: 8, LimitUsd: 10, CreatedAt: 1}
	n, err = q.InsertBudgetAlert(ctx, alert)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	n, err = q.InsertBudgetAlert(ctx, alert)
	require.NoError(t, err)
	assert.Zero(t, n)
	alert.Level = "over"
	n, err = q.InsertBudgetAlert(ctx, alert)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)

	// ---- 预算与状态键
	_, err = q.GetBudget(ctx)
	assert.ErrorIs(t, err, sql.ErrNoRows)
	require.NoError(t, q.UpsertBudget(ctx, db.UpsertBudgetParams{DailyUsd: ptr(1.5), WarnPct: 80, Timezone: "UTC", Enabled: true, UpdatedAt: 1}))
	require.NoError(t, q.UpsertBudget(ctx, db.UpsertBudgetParams{MonthlyUsd: ptr(30.0), WarnPct: 0, Timezone: "Asia/Shanghai", UpdatedAt: 2}))
	b, err := q.GetBudget(ctx)
	require.NoError(t, err)
	assert.Nil(t, b.DailyUsd)
	assert.Equal(t, 30.0, *b.MonthlyUsd)
	assert.Equal(t, "Asia/Shanghai", b.Timezone)
	require.NoError(t, q.SetStatsState(ctx, db.SetStatsStateParams{Key: "facts_version", Value: "1"}))
	require.NoError(t, q.SetStatsState(ctx, db.SetStatsStateParams{Key: "facts_version", Value: "2"}))
	v, err := q.GetStatsState(ctx, "facts_version")
	require.NoError(t, err)
	assert.Equal(t, "2", v)
	require.NoError(t, q.DeleteStatsState(ctx, "facts_version"))
	_, err = q.GetStatsState(ctx, "facts_version")
	assert.ErrorIs(t, err, sql.ErrNoRows)

	// ---- 保留期：只删已删除 orb 的事件
	n, err = q.DeleteDeadOrbEventsBefore(ctx, 100)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	assert.Equal(t, "5", scalarString(t, s.DB, `SELECT COUNT(*) FROM orb_events`))
}
