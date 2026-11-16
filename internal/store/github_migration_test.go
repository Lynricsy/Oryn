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

// TestGitHubMigrationDownUp 覆盖 0028：回滚后新表与新列都消失，重新应用后恢复且默认值正确。
func TestGitHubMigrationDownUp(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	sub, err := fs.Sub(migrationsFS, "migrations")
	require.NoError(t, err)
	p, err := goose.NewProvider(database.DialectSQLite3, s.DB, sub)
	require.NoError(t, err)
	const tables = `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND (name LIKE 'github_%' OR name IN ('thread_pulls', 'thread_issues'))`
	const cols = `SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name LIKE 'github_%'`
	count := func(q string) int {
		var n int
		require.NoError(t, s.DB.QueryRowContext(ctx, q).Scan(&n))
		return n
	}
	assert.Equal(t, 10, count(tables))
	assert.Equal(t, 7, count(cols))

	_, err = p.DownTo(ctx, 27)
	require.NoError(t, err)
	assert.Zero(t, count(tables), "0028 回滚后表都消失")
	assert.Zero(t, count(cols), "0028 回滚后项目列都消失")

	_, err = p.Up(ctx)
	require.NoError(t, err)
	assert.Equal(t, 10, count(tables))
	var ident string
	require.NoError(t, s.DB.QueryRowContext(ctx, `SELECT dflt_value FROM pragma_table_info('orbs') WHERE name = 'github_identity'`).Scan(&ident))
	assert.Equal(t, "'user'", ident)
}
