package store

import (
	"context"
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestM26MigrationDownUp 覆盖 0054：收件表重建时保留旧行、研究员对话唯一；Down 删掉研究数据后回到 0053 的结构，
// 旧收件原样保留，Up 可以重来。
func TestM26MigrationDownUp(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	sub, err := fs.Sub(migrationsFS, "migrations")
	require.NoError(t, err)
	p, err := goose.NewProvider(database.DialectSQLite3, s.DB, sub)
	require.NoError(t, err)
	exec := func(q string) {
		t.Helper()
		_, err := s.DB.ExecContext(ctx, q)
		require.NoError(t, err)
	}

	downTo(ctx, t, p, 53)
	exec(`INSERT INTO assistant_inbox (id, kind, source_id, source_name, thread_id, outcome, created_at)
		VALUES ('in_old', 'schedule', 'sch_1', 'n', 'T-x', 'completed', 1)`)
	_, err = p.Up(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, queryInt(ctx, t, s, `SELECT COUNT(*) FROM assistant_inbox WHERE id = 'in_old' AND restricted = 0`))

	exec(`INSERT INTO threads (id, title_source, state, state_changed_at, session_dir, created_at, updated_at, kind, hidden, research)
		VALUES ('T-r', 'auto', 'idle', 0, 'd', 1, 1, 'assistant', 1, 'web')`)
	_, err = s.DB.ExecContext(ctx, `INSERT INTO threads (id, title_source, state, state_changed_at, session_dir, created_at, updated_at, kind, hidden, research)
		VALUES ('T-r2', 'auto', 'idle', 0, 'd', 1, 1, 'assistant', 1, 'web')`)
	assert.Error(t, err, "每种研究员至多一个")
	exec(`INSERT INTO assistant_inbox (id, kind, source_id, source_name, thread_id, outcome, created_at, restricted)
		VALUES ('in_r', 'research', 'rsr_1', 't', '', 'finding', 2, 1)`)
	exec(`INSERT INTO research_topics (id, kind, title, question, origin, state, interval_s, created_at, updated_at)
		VALUES ('rtp_1', 'question', 't', 'q', 'manual', 'active', 86400, 1, 1)`)

	downTo(ctx, t, p, 53)
	assert.Zero(t, queryInt(ctx, t, s, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name LIKE 'research_%'`))
	assert.Zero(t, queryInt(ctx, t, s, `SELECT COUNT(*) FROM threads WHERE id = 'T-r'`))
	assert.Equal(t, 1, queryInt(ctx, t, s, `SELECT COUNT(*) FROM assistant_inbox`), "只留旧收件")
	assert.Zero(t, queryInt(ctx, t, s, `SELECT COUNT(*) FROM pragma_table_info('assistant_inbox') WHERE name = 'restricted'`))

	_, err = p.Up(ctx)
	require.NoError(t, err)
	assert.Equal(t, 4, queryInt(ctx, t, s, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name LIKE 'research_%'`))
}
