package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// TestM31ScheduleWebhooksMigration 覆盖 0066：kind = manual 只用于 new_thread、运行接受 trigger = webhook 与 payload、
// Webhook 随定时级联删除；回滚到 65 后 manual 定时与 webhook 运行被删、原 CHECK 恢复，其余数据保留。
func TestM31ScheduleWebhooksMigration(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	p := githubProvider(t, s)
	exec := func(q string, args ...any) error {
		_, err := s.DB.ExecContext(ctx, q, args...)
		return err
	}
	schedule := func(id, kind string) error {
		return exec(`INSERT INTO schedules (id, mode, name, kind, timezone, prompt, created_by, created_at, updated_at)
			VALUES (?, 'new_thread', ?, ?, 'UTC', 'p', 'user', 1, 1)`, id, id, kind)
	}
	run := func(id, sch, trigger string) error {
		return exec(`INSERT INTO schedule_runs (id, schedule_id, trigger, scheduled_for, started_at, status, payload)
			VALUES (?, ?, ?, 1, 1, 'completed', '{"body":"x"}')`, id, sch, trigger)
	}
	require.NoError(t, schedule("sch-cron", "cron"))
	require.NoError(t, schedule("sch-manual", "manual"))
	require.NoError(t, run("srn-1", "sch-cron", "schedule"))
	require.NoError(t, run("srn-2", "sch-manual", "webhook"))
	require.NoError(t, run("srn-3", "sch-cron", "webhook"))
	assert.Error(t, run("srn-bad", "sch-cron", "email"))
	require.NoError(t, exec(`INSERT INTO schedule_webhooks (schedule_id, key_hash, created_at, rotated_at) VALUES ('sch-manual', 'h1', 1, 1)`))
	require.NoError(t, s.Q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: "T-1", Title: "t", TitleSource: "user",
		State: "idle", SessionDir: "d", CreatedAt: 1, UpdatedAt: 1}))
	assert.Error(t, exec(`INSERT INTO schedules (id, mode, thread_id, kind, timezone, prompt, created_by, created_at, updated_at)
		VALUES ('sch-thread', 'thread', 'T-1', 'manual', 'UTC', 'p', 'user', 1, 1)`), "manual 只用于 new_thread")

	require.NoError(t, exec(`DELETE FROM schedules WHERE id = 'sch-manual'`))
	assert.Zero(t, queryInt(ctx, t, s, `SELECT COUNT(*) FROM schedule_webhooks`), "Webhook 随定时删除")
	require.NoError(t, schedule("sch-manual2", "manual"))

	_, err := p.DownTo(ctx, 65)
	require.NoError(t, err)
	assert.Equal(t, 1, queryInt(ctx, t, s, `SELECT COUNT(*) FROM schedules`), "回滚删掉 manual 定时")
	assert.Equal(t, 1, queryInt(ctx, t, s, `SELECT COUNT(*) FROM schedule_runs`), "回滚删掉 webhook 运行，保留其余")
	assert.Error(t, schedule("sch-manual3", "manual"))

	_, err = p.Up(ctx)
	require.NoError(t, err)
	require.NoError(t, schedule("sch-manual4", "manual"))
	require.NoError(t, run("srn-4", "sch-manual4", "webhook"))
	assert.Equal(t, 1, queryInt(ctx, t, s, `SELECT COUNT(*) FROM schedule_runs WHERE id = 'srn-1' AND payload IS NULL`), "旧运行的 payload 为 NULL")
}
