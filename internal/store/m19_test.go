package store

import (
	"context"
	"database/sql"
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// TestM19Queries 覆盖 0041 的查询：主线唯一、电脑 orb、条目尾部窗口与向前翻页、交给助手的判定、确认与收件。
func TestM19Queries(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	q := s.Q

	_, err := q.GetMainThread(ctx)
	assert.ErrorIs(t, err, sql.ErrNoRows)
	mk := func(id string, main *int64) error {
		return q.CreateThread(ctx, db.CreateThreadParams{ID: id, TitleSource: "auto", State: "idle", SessionDir: "d",
			CreatedAt: 1, UpdatedAt: 1, Kind: "assistant", Main: main})
	}
	require.NoError(t, mk("T-a", nil))
	require.NoError(t, mk("T-m", new(int64(1))))
	require.Error(t, mk("T-m2", new(int64(1))), "全库至多一条主线")
	m, err := q.GetMainThread(ctx)
	require.NoError(t, err)
	assert.Equal(t, "T-m", m.ID)

	// 条目：10 条，尾部 4 条从 seq 7 开始；不足 n 条时为 0。
	for i := range 10 {
		_, err := q.UpsertEntry(ctx, db.UpsertEntryParams{ThreadID: "T-a", File: "s.jsonl", EntryID: string(rune('a' + i)), Type: "message", Payload: []byte(`{}`)})
		require.NoError(t, err)
	}
	start, err := q.GetTailStartSeq(ctx, db.GetTailStartSeqParams{ThreadID: "T-a", N: 4})
	require.NoError(t, err)
	assert.EqualValues(t, 6, start)
	start, err = q.GetTailStartSeq(ctx, db.GetTailStartSeqParams{ThreadID: "T-a", N: 50})
	require.NoError(t, err)
	assert.Zero(t, start)
	page, err := q.ListEntriesBefore(ctx, db.ListEntriesBeforeParams{ThreadID: "T-a", BeforeSeq: 7, Limit: 3})
	require.NoError(t, err)
	require.Len(t, page, 3)
	assert.EqualValues(t, []int64{6, 5, 4}, []int64{page[0].Seq, page[1].Seq, page[2].Seq})

	// 电脑 orb。
	require.NoError(t, q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_d", Name: "docker", TokenHash: "hd", CreatedAt: 1}))
	require.NoError(t, q.CreateOrb(ctx, db.CreateOrbParams{ID: "orb-1", ProviderID: "prov_d", Type: "small", State: "ready", TokenHash: "t1", CreatedAt: 1}))
	_, err = q.GetComputerOrb(ctx)
	assert.ErrorIs(t, err, sql.ErrNoRows)
	require.NoError(t, q.CreateOrb(ctx, db.CreateOrbParams{ID: "orb-c", ProviderID: "prov_d", Type: "small", State: "ready", TokenHash: "tc", CreatedAt: 2, Computer: new(int64(1))}))
	c, err := q.GetComputerOrb(ctx)
	require.NoError(t, err)
	assert.Equal(t, "orb-c", c.ID)

	// 交给助手：定时开着开关时为真。
	require.NoError(t, q.CreateThread(ctx, db.CreateThreadParams{ID: "T-run", TitleSource: "auto", State: "idle", SessionDir: "d",
		CreatedAt: 1, UpdatedAt: 1, Kind: "work", ScheduleID: new("sch_1")}))
	require.NoError(t, q.InsertSchedule(ctx, db.InsertScheduleParams{ID: "sch_1", Mode: "new_thread", Name: "n", Kind: "every", Spec: "1m0s",
		Timezone: "UTC", Prompt: "p", Enabled: true, CreatedBy: "user", CreatedAt: 1, UpdatedAt: 1, Overlap: "skip", KeepLast: 5}))
	handed, err := q.IsThreadHandedToAssistant(ctx, "T-run")
	require.NoError(t, err)
	assert.False(t, handed)
	_, err = s.DB.ExecContext(ctx, `UPDATE schedules SET hand_to_assistant = 1 WHERE id = 'sch_1'`)
	require.NoError(t, err)
	handed, err = q.IsThreadHandedToAssistant(ctx, "T-run")
	require.NoError(t, err)
	assert.True(t, handed)
	// 触发规则：只看该线程自己的 thread_issues
	for _, q := range []string{
		`INSERT INTO projects (id, name, shallow_clone, created_at, updated_at) VALUES ('prj_1', 'p1', 1, 1, 1)`,
		`INSERT INTO github_triggers (id, project_id, name, event, prompt, created_at, updated_at, hand_to_assistant)
			VALUES ('ght_1', 'prj_1', 'r', 'issue_opened', 'x', 1, 1, 1)`,
		`INSERT INTO thread_issues (repo_id, number, thread_id, created_at, rule_id) VALUES (1, 7, 'T-a', 1, 'ght_1')`,
	} {
		_, err = s.DB.ExecContext(ctx, q)
		require.NoError(t, err)
	}
	handed, err = q.IsThreadHandedToAssistant(ctx, "T-a")
	require.NoError(t, err)
	assert.True(t, handed)
	handed, err = q.IsThreadHandedToAssistant(ctx, "T-m")
	require.NoError(t, err)
	assert.False(t, handed, "别的线程的触发规则不算")

	// 确认：只能从 pending 决定一次。
	require.NoError(t, q.InsertAssistantConfirmation(ctx, db.InsertAssistantConfirmationParams{ID: "cfm_1", ThreadID: "T-a",
		Action: "delete_thread", Target: "T-run", Summary: "s", Params: "{}", CreatedAt: 1}))
	n, err := q.CountPendingAssistantConfirmations(ctx, "T-a")
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	rows, err := q.DecideAssistantConfirmation(ctx, db.DecideAssistantConfirmationParams{ID: "cfm_1", State: "approved", DecidedAt: new(int64(2))})
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)
	rows, err = q.DecideAssistantConfirmation(ctx, db.DecideAssistantConfirmationParams{ID: "cfm_1", State: "rejected", DecidedAt: new(int64(3))})
	require.NoError(t, err)
	assert.Zero(t, rows)

	// 收件。
	require.NoError(t, q.InsertAssistantInbox(ctx, db.InsertAssistantInboxParams{ID: "aib_1", Kind: "schedule", SourceID: "sch_1",
		SourceName: "n", ThreadID: "T-run", Outcome: "completed", CreatedAt: 1}))
	pending, err := q.ListPendingAssistantInbox(ctx, 20)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, q.MarkAssistantInboxDelivered(ctx, db.MarkAssistantInboxDeliveredParams{ID: "aib_1", DeliveredAt: new(int64(2))}))
	pending, err = q.ListPendingAssistantInbox(ctx, 20)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

// TestM19MigrationDownUp 覆盖 0041 的 Down：同线程多条定时只留最早一条、唯一索引恢复；重新 Up 后又能多条。
func TestM19MigrationDownUp(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	sub, err := fs.Sub(migrationsFS, "migrations")
	require.NoError(t, err)
	p, err := goose.NewProvider(database.DialectSQLite3, s.DB, sub)
	require.NoError(t, err)
	exec := func(q string) {
		_, err := s.DB.ExecContext(ctx, q)
		require.NoError(t, err)
	}
	downTo(ctx, t, p, 41)
	exec(`INSERT INTO threads (id, title_source, state, state_changed_at, session_dir, created_at, updated_at, kind, main)
		VALUES ('T-m', 'auto', 'idle', 0, 'd', 1, 1, 'assistant', 1)`)
	for _, row := range []string{"('sch_b', 2)", "('sch_a', 1)", "('sch_c', 3)"} {
		exec(`INSERT INTO schedules (id, mode, thread_id, kind, timezone, prompt, created_by, created_at, updated_at)
			SELECT column1, 'thread', 'T-m', 'every', 'UTC', 'p', 'agent', column2, column2 FROM (VALUES ` + row + `)`)
	}

	downTo(ctx, t, p, 40)
	var ids []string
	rs, err := s.DB.QueryContext(ctx, `SELECT id FROM schedules ORDER BY id`)
	require.NoError(t, err)
	for rs.Next() {
		var id string
		require.NoError(t, rs.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rs.Close())
	assert.Equal(t, []string{"sch_a"}, ids, "只留最早一条")
	_, err = s.DB.ExecContext(ctx, `INSERT INTO schedules (id, mode, thread_id, kind, timezone, prompt, created_by, created_at, updated_at)
		VALUES ('sch_d', 'thread', 'T-m', 'every', 'UTC', 'p', 'agent', 4, 4)`)
	assert.Error(t, err, "唯一索引已恢复")
	const m19Tables = `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('assistant_settings', 'assistant_confirmations', 'assistant_inbox')`
	assert.Zero(t, queryInt(ctx, t, s, m19Tables))
	assert.Zero(t, queryInt(ctx, t, s, `SELECT COUNT(*) FROM pragma_table_info('threads') WHERE name = 'main'`))

	_, err = p.Up(ctx)
	require.NoError(t, err)
	exec(`INSERT INTO schedules (id, mode, thread_id, kind, timezone, prompt, created_by, created_at, updated_at)
		VALUES ('sch_d', 'thread', 'T-m', 'every', 'UTC', 'p', 'agent', 4, 4)`)
	assert.Equal(t, 3, queryInt(ctx, t, s, m19Tables))
}
