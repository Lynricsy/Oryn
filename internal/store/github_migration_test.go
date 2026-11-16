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

const (
	// githubTables 统计 GitHub 相关的表：0028 建 10 张，0033 再建 11 张。
	githubTables = `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND (name LIKE 'github_%' OR name IN ('thread_pulls', 'thread_issues'))`
	// projectGithubCols 统计 projects 上的 github_* 列：0028 加 7 列，0033 再加 2 列。
	projectGithubCols = `SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name LIKE 'github_%'`
)

func githubProvider(t *testing.T, s *Store) *goose.Provider {
	t.Helper()
	sub, err := fs.Sub(migrationsFS, "migrations")
	require.NoError(t, err)
	p, err := goose.NewProvider(database.DialectSQLite3, s.DB, sub)
	require.NoError(t, err)
	return p
}

func queryInt(ctx context.Context, t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, s.DB.QueryRowContext(ctx, q, args...).Scan(&n))
	return n
}

func columnDefault(ctx context.Context, t *testing.T, s *Store, table, column string) string {
	t.Helper()
	var v string
	require.NoError(t, s.DB.QueryRowContext(ctx, `SELECT dflt_value FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&v))
	return v
}

// TestGitHubMigrationDownUp 覆盖 0028（连同其后的迁移）：回滚到 27 后新表与新列都消失，重新应用后恢复且默认值正确。
func TestGitHubMigrationDownUp(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	p := githubProvider(t, s)
	assert.Equal(t, 21, queryInt(ctx, t, s, githubTables))
	assert.Equal(t, 9, queryInt(ctx, t, s, projectGithubCols))

	_, err := p.DownTo(ctx, 27)
	require.NoError(t, err)
	assert.Zero(t, queryInt(ctx, t, s, githubTables), "0028 回滚后表都消失")
	assert.Zero(t, queryInt(ctx, t, s, projectGithubCols), "0028 回滚后项目列都消失")

	_, err = p.Up(ctx)
	require.NoError(t, err)
	assert.Equal(t, 21, queryInt(ctx, t, s, githubTables))
	assert.Equal(t, 9, queryInt(ctx, t, s, projectGithubCols))
	assert.Equal(t, "'user'", columnDefault(ctx, t, s, "orbs", "github_identity"))
	assert.Equal(t, "'agent'", columnDefault(ctx, t, s, "owner", "github_automation_identity"))
}

// TestGitHubOAuthMigrationDownUp 覆盖 0033：新表新列、两张重建 CHECK 的表在 Down / Up 中保留数据，
// 自动化身份的默认值与现有值都变为 agent。
func TestGitHubOAuthMigrationDownUp(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	p := githubProvider(t, s)
	cols := func(table string) int {
		return queryInt(ctx, t, s, `SELECT COUNT(*) FROM pragma_table_info(?)`, table)
	}
	exec := func(q string, args ...any) error {
		_, err := s.DB.ExecContext(ctx, q, args...)
		return err
	}

	assert.Equal(t, 21, queryInt(ctx, t, s, githubTables))
	assert.Equal(t, 9, queryInt(ctx, t, s, projectGithubCols))
	assert.Equal(t, 12, cols("thread_issues"))
	assert.Equal(t, 8, cols("thread_pulls"))
	assert.Equal(t, 23, cols("github_triggers"))
	assert.Equal(t, 1, queryInt(ctx, t, s, `SELECT COUNT(*) FROM pragma_table_info('github_identities') WHERE name = 'scopes'`))
	assert.Equal(t, "'agent'", columnDefault(ctx, t, s, "owner", "github_automation_identity"))
	assert.Equal(t, "'writers_and_author'", columnDefault(ctx, t, s, "github_triggers", "drivers"))
	assert.Equal(t, "'new'", columnDefault(ctx, t, s, "thread_issues", "state"))

	// 新 CHECK 放行新值；FinishGithubDelivery 保留载荷。
	require.NoError(t, exec(`INSERT INTO projects (id, name, shallow_clone, created_at, updated_at) VALUES ('prj_1', 'p1', 1, 1, 1)`))
	require.NoError(t, exec(`INSERT INTO github_triggers (id, project_id, name, event, prompt, created_at, updated_at)
		VALUES ('ght_old', 'prj_1', 'old', 'issue_opened', 'x', 1, 1), ('ght_new', 'prj_1', 'new', 'pull_request_opened', 'x', 2, 2)`))
	require.NoError(t, exec(`INSERT INTO github_deliveries (guid, event, source, payload, received_at)
		VALUES ('d-hook', 'issues', 'webhook', x'7b7d', 1), ('d-rest', 'issues', 'rest', x'7b7d', 2)`))
	require.NoError(t, s.Q.FinishGithubDelivery(ctx, db.FinishGithubDeliveryParams{Outcome: "handled", Guid: "d-hook"}))
	d, err := s.Q.GetGithubDelivery(ctx, "d-hook")
	require.NoError(t, err)
	assert.Equal(t, []byte("{}"), d.Payload, "处理完仍保留载荷")
	require.Error(t, exec(`INSERT INTO github_hooks (repo_id, full_name, status, updated_at) VALUES (1, 'o/r', 'bogus', 1)`))

	// 回滚 0033：恢复 0028 的形状，新枚举值的行被删掉，其余数据保留。
	_, err = p.DownTo(ctx, 32)
	require.NoError(t, err)
	assert.Equal(t, 10, queryInt(ctx, t, s, githubTables))
	assert.Equal(t, 7, queryInt(ctx, t, s, projectGithubCols))
	assert.Equal(t, 5, cols("thread_issues"))
	assert.Equal(t, 7, cols("thread_pulls"))
	assert.Equal(t, 12, cols("github_triggers"))
	assert.Equal(t, "'user'", columnDefault(ctx, t, s, "owner", "github_automation_identity"))
	assert.Equal(t, 1, queryInt(ctx, t, s, `SELECT COUNT(*) FROM github_triggers`))
	assert.Equal(t, 1, queryInt(ctx, t, s, `SELECT COUNT(*) FROM github_deliveries WHERE guid = 'd-hook' AND payload IS NOT NULL`))
	assert.Zero(t, queryInt(ctx, t, s, `SELECT COUNT(*) FROM github_deliveries WHERE source = 'rest'`))
	require.Error(t, exec(`INSERT INTO github_deliveries (guid, event, source, received_at) VALUES ('d-x', 'issues', 'rest', 3)`))
	require.Error(t, exec(`INSERT INTO github_triggers (id, project_id, name, event, prompt, created_at, updated_at)
		VALUES ('ght_x', 'prj_1', 'x', 'issue_reopened', 'x', 3, 3)`))
	const rebuiltIndexes = `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name IN ('github_triggers_project', 'github_deliveries_received')`
	assert.Equal(t, 2, queryInt(ctx, t, s, rebuiltIndexes), "重建后索引仍在")
	assert.Zero(t, queryInt(ctx, t, s, `SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%\_keep' ESCAPE '\'`), "暂存表已删除")

	// 回滚后建的 owner 取旧默认值 user；重新应用 0033 后现有值改为 agent。
	require.NoError(t, s.Q.CreateOwner(ctx, db.CreateOwnerParams{DisplayName: "me", UserHandle: []byte{1}, CreatedAt: 1}))
	ident, err := s.Q.GetOwnerGithubAutomation(ctx)
	require.NoError(t, err)
	assert.Equal(t, "user", ident)

	_, err = p.Up(ctx)
	require.NoError(t, err)
	assert.Equal(t, 21, queryInt(ctx, t, s, githubTables))
	assert.Equal(t, 23, cols("github_triggers"))
	assert.Equal(t, 2, queryInt(ctx, t, s, rebuiltIndexes))
	ident, err = s.Q.GetOwnerGithubAutomation(ctx)
	require.NoError(t, err)
	assert.Equal(t, "agent", ident)
	tr, err := s.Q.GetGithubTrigger(ctx, "ght_old")
	require.NoError(t, err)
	assert.Equal(t, "issue_opened", tr.Event)
	assert.Equal(t, "writers_and_author", tr.Drivers)
	assert.Equal(t, "[]", tr.Maintainers)
	require.NoError(t, exec(`INSERT INTO github_deliveries (guid, event, source, received_at) VALUES ('d-x', 'issues', 'rest', 3)`))
}
