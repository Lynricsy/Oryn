package store

import (
	"context"
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// TestSearchMigrationDownUp 覆盖 0027：回滚后视图、表、全文表与触发器都消失，条目照常入库；重新应用后触发器恢复工作。
func TestSearchMigrationDownUp(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	sub, err := fs.Sub(migrationsFS, "migrations")
	require.NoError(t, err)
	p, err := goose.NewProvider(database.DialectSQLite3, s.DB, sub)
	require.NoError(t, err)
	const objects = `SELECT COUNT(*) FROM sqlite_master
		WHERE name IN ('search_source', 'search_docs', 'search_fts', 'search_state', 'search_docs_ad', 'search_entries_ai', 'search_entries_au')`
	var n int
	require.NoError(t, s.DB.QueryRowContext(ctx, objects).Scan(&n))
	assert.Equal(t, 7, n)

	_, err = p.DownTo(ctx, 26)
	require.NoError(t, err)
	require.NoError(t, s.DB.QueryRowContext(ctx, objects).Scan(&n))
	assert.Zero(t, n, "0027 回滚后对象都消失")
	// 回滚后的表结构比当前代码旧：直接写 SQL，不经随新列变化的 CreateThread。
	_, err = s.DB.ExecContext(ctx, `INSERT INTO threads (id, title_source, state, state_changed_at, session_dir, created_at, updated_at, kind)
		VALUES ('T-1', 'auto', 'idle', 0, 'd', 1, 1, 'work')`)
	require.NoError(t, err)
	upsert := func(id, text string) {
		_, err := s.Q.UpsertEntry(ctx, db.UpsertEntryParams{ThreadID: "T-1", File: "s.jsonl", EntryID: id, Type: "message",
			Payload: []byte(`{"type":"message","id":"` + id + `","message":{"role":"user","content":"` + text + `"}}`)})
		require.NoError(t, err)
	}
	upsert("e1", "before upgrade")

	_, err = p.Up(ctx)
	require.NoError(t, err)
	upsert("e2", "after upgrade")
	var docs int
	require.NoError(t, s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH '"upgrade"'`).Scan(&docs))
	assert.Equal(t, 1, docs, "升级前的条目留给后台回填，升级后的条目由触发器入索引")
}
