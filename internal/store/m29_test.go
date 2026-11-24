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

// TestM29MigrationDownUp 覆盖 0059：Up 给 artifact_versions 加 turn_id / title / check_json 与回合索引，建 artifact_designs
// （auto_notes 缺省 1，随 artifact 级联删除）；Down 全部退掉且不丢已有版本，再 Up 可重放。
func TestM29MigrationDownUp(t *testing.T) {
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
	count := func(q string) int { return queryInt(ctx, t, s, q) }

	downTo(ctx, t, p, 58)
	exec(`INSERT INTO artifacts (id, origin, kind, title, head, size, pages, created_at, updated_at) VALUES ('art_1', 'agent', 'canvas', 't', 1, 1, 0, 1, 1)`)
	exec(`INSERT INTO artifact_versions (artifact_id, n, size, pages, author_label, capabilities, files_key, created_at) VALUES ('art_1', 1, 1, 0, '', '[]', 'k', 1)`)

	_, err = p.UpTo(ctx, 59)
	require.NoError(t, err)
	assert.Equal(t, 1, count(`SELECT COUNT(*) FROM artifact_versions WHERE artifact_id = 'art_1' AND turn_id IS NULL AND title IS NULL AND check_json IS NULL`))
	assert.Equal(t, 1, count(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'artifact_versions_turn'`))
	exec(`UPDATE artifact_versions SET turn_id = 'turn-1', title = '首页', check_json = '{"status":"clean","issues":[]}' WHERE artifact_id = 'art_1'`)
	exec(`INSERT INTO artifact_designs (artifact_id) VALUES ('art_1')`)
	assert.Equal(t, 1, count(`SELECT COUNT(*) FROM artifact_designs WHERE artifact_id = 'art_1' AND auto_notes = 1 AND dealt = '[]' AND delivered = '[]'`))
	exec(`DELETE FROM artifacts WHERE id = 'art_1'`)
	assert.Zero(t, count(`SELECT COUNT(*) FROM artifact_designs`), "随 artifact 级联删除")
	exec(`INSERT INTO artifacts (id, origin, kind, title, head, size, pages, created_at, updated_at) VALUES ('art_1', 'agent', 'canvas', 't', 1, 1, 0, 1, 1)`)
	exec(`INSERT INTO artifact_versions (artifact_id, n, size, pages, author_label, capabilities, files_key, created_at, turn_id, title) VALUES ('art_1', 1, 1, 0, '', '[]', 'k', 1, 'turn-1', '首页')`)
	exec(`INSERT INTO artifact_designs (artifact_id, auto_notes) VALUES ('art_1', 0)`)

	downTo(ctx, t, p, 58)
	assert.Equal(t, 1, count(`SELECT COUNT(*) FROM artifact_versions WHERE artifact_id = 'art_1'`), "Down 不丢版本")
	assert.Zero(t, count(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('artifact_designs', 'artifact_versions_turn')`))
	assert.Zero(t, count(`SELECT COUNT(*) FROM pragma_table_info('artifact_versions') WHERE name IN ('turn_id', 'title', 'check_json')`))

	_, err = p.UpTo(ctx, latestVersion(t, p))
	require.NoError(t, err)
	assert.Equal(t, 3, count(`SELECT COUNT(*) FROM pragma_table_info('artifact_versions') WHERE name IN ('turn_id', 'title', 'check_json')`))
}
