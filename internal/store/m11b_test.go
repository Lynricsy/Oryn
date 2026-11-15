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

// insertOldThread 在回滚到 0023 之前的结构上建一个 idle 的 work 线程（生成的 CreateThread 已带 cwd / worktree 列）。
func insertOldThread(t *testing.T, s *Store, id string, project, space *string) {
	t.Helper()
	_, err := s.DB.ExecContext(context.Background(), `INSERT INTO threads (id, project_id, space_id, title_source, state, state_changed_at,
		session_dir, created_at, updated_at, kind) VALUES (?, ?, ?, 'auto', 'idle', 0, '/a', 1, 1, 'work')`, id, project, space)
	require.NoError(t, err)
}

// TestM11bRunners 覆盖 0023 的 runner 查询：建 runner（provider + kind=runner 的 orb + runners 行）、
// 按 provider / orb 查、hello 与 touch、列表（排除已吊销）、ListProviders 不含 runner、令牌哈希可查到 runner、吊销。
func TestM11bRunners(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	q := s.Q
	require.NoError(t, q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_d", Name: "docker", TokenHash: "hd", CreatedAt: 1}))
	require.NoError(t, q.CreateOrb(ctx, db.CreateOrbParams{ID: "orb-d", ProviderID: "prov_d", Type: "small", State: "ready", TokenHash: "td", CreatedAt: 1}))

	mk := func(prov, orb, name string, at int64) {
		require.NoError(t, s.Tx(ctx, func(q *db.Queries) error {
			if err := q.CreateRunnerProvider(ctx, db.CreateRunnerProviderParams{ID: prov, Name: name, TokenHash: "rt-" + prov, CreatedAt: at}); err != nil {
				return err
			}
			if err := q.CreateOrb(ctx, db.CreateOrbParams{ID: orb, ProviderID: prov, Type: "runner", State: "stopped",
				TokenHash: "ot-" + orb, CreatedAt: at, Kind: new("runner")}); err != nil {
				return err
			}
			return q.CreateRunner(ctx, db.CreateRunnerParams{ProviderID: prov, OrbID: orb})
		}))
	}
	mk("prov_r2", "orb-r2", "zed", 3)
	mk("prov_r1", "orb-r1", "laptop", 2)

	d, err := q.GetOrb(ctx, "orb-d")
	require.NoError(t, err)
	assert.Equal(t, "orb", d.Kind, "CreateOrb 不给 kind 时为 orb")
	o, err := q.GetOrb(ctx, "orb-r1")
	require.NoError(t, err)
	assert.Equal(t, "runner", o.Kind)

	p, err := q.GetProviderByTokenHash(ctx, "rt-prov_r1")
	require.NoError(t, err)
	assert.Equal(t, "prov_r1", p.ID)
	assert.Equal(t, "runner", p.Driver)
	assert.Nil(t, p.DisabledAt)

	err = q.CreateRunnerProvider(ctx, db.CreateRunnerProviderParams{ID: "prov_x", Name: "docker", TokenHash: "rt-x", CreatedAt: 4})
	require.Error(t, err, "runner 名与 provider 名共用唯一约束")

	provs, err := q.ListProviders(ctx)
	require.NoError(t, err)
	require.Len(t, provs, 1, "ListProviders 不含 runner 的 provider 行")
	assert.Equal(t, "prov_d", provs[0].ID)

	r, err := q.GetRunnerByProvider(ctx, "prov_r1")
	require.NoError(t, err)
	assert.Equal(t, db.Runner{ProviderID: "prov_r1", OrbID: "orb-r1", Dirs: "[]", Features: "[]"}, r, "新 runner 的默认列")
	r, err = q.GetRunnerByOrb(ctx, "orb-r2")
	require.NoError(t, err)
	assert.Equal(t, "prov_r2", r.ProviderID)
	_, err = q.GetRunnerByOrb(ctx, "orb-d")
	require.ErrorIs(t, err, sql.ErrNoRows)

	require.NoError(t, q.UpdateRunnerHello(ctx, db.UpdateRunnerHelloParams{Hostname: "box", Os: "linux", Arch: "amd64",
		Dirs: `[{"path":"/w","name":"w","git":true}]`, Features: `["fs","git"]`, LastSeenAt: 10, OrbID: "orb-r1"}))
	require.NoError(t, q.SetOrbVersions(ctx, db.SetOrbVersionsParams{OrbdVersion: new("1.2"), AgentVersion: new("0.9"), ID: "orb-r1"}))
	require.NoError(t, q.TouchRunner(ctx, db.TouchRunnerParams{LastSeenAt: 20, OrbID: "orb-r2"}))

	list, err := q.ListRunners(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, db.ListRunnersRow{ProviderID: "prov_r1", OrbID: "orb-r1", Hostname: "box", Os: "linux", Arch: "amd64",
		Dirs: `[{"path":"/w","name":"w","git":true}]`, Features: `["fs","git"]`, LastSeenAt: new(int64(10)),
		Name: "laptop", CreatedAt: 2, OrbState: "stopped", OrbdVersion: new("1.2"), AgentVersion: new("0.9")}, list[0], "按名称排序")
	assert.Equal(t, "zed", list[1].Name)
	assert.Equal(t, new(int64(20)), list[1].LastSeenAt)
	assert.Empty(t, list[1].Hostname, "touch 不改 hello 信息")

	n, err := q.RevokeRunnerProvider(ctx, db.RevokeRunnerProviderParams{DisabledAt: new(int64(30)), TokenHash: "rand", ID: "prov_r2"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	n, err = q.RevokeRunnerProvider(ctx, db.RevokeRunnerProviderParams{DisabledAt: new(int64(31)), TokenHash: "rand2", ID: "prov_r2"})
	require.NoError(t, err)
	assert.Zero(t, n, "已吊销的不再命中")
	n, err = q.RevokeRunnerProvider(ctx, db.RevokeRunnerProviderParams{DisabledAt: new(int64(31)), TokenHash: "rand3", ID: "prov_d"})
	require.NoError(t, err)
	assert.Zero(t, n, "只作用于 driver = runner 的行")
	_, err = q.GetProviderByTokenHash(ctx, "rt-prov_r2")
	require.ErrorIs(t, err, sql.ErrNoRows, "吊销后旧令牌哈希查不到")
	list, err = q.ListRunners(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1, "ListRunners 不含已吊销的 runner")
	assert.Equal(t, "laptop", list[0].Name)
}

// TestM11bThreadCwdWorktree 覆盖线程的 cwd / worktree 列：CreateThread 写入、ListThreadsByOrbCwd（只算未归档、精确匹配）、SetThreadWorktree 写入与清空。
func TestM11bThreadCwdWorktree(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	q := s.Q
	require.NoError(t, q.CreateRunnerProvider(ctx, db.CreateRunnerProviderParams{ID: "prov_r", Name: "r", TokenHash: "rt", CreatedAt: 1}))
	require.NoError(t, q.CreateOrb(ctx, db.CreateOrbParams{ID: "orb-r", ProviderID: "prov_r", Type: "runner", State: "ready",
		TokenHash: "ot", CreatedAt: 1, Kind: new("runner")}))
	thread := func(id string, orb, cwd, wt *string) {
		require.NoError(t, q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: id, OrbID: orb, TitleSource: "auto", State: "idle",
			SessionDir: "/" + id, CreatedAt: 1, UpdatedAt: 1, Cwd: cwd, Worktree: wt}))
	}
	wt := `{"repo":"/w/app","dir":"/w/app-x","branch":"x"}`
	thread("T-1", new("orb-r"), new("/w/app"), nil)
	thread("T-2", new("orb-r"), new("/w/app"), nil)
	thread("T-3", new("orb-r"), new("/w/app-x"), new(wt))
	thread("T-4", new("orb-r"), new("/w/app/sub"), nil)
	thread("T-5", nil, nil, nil)
	_, err := s.DB.ExecContext(ctx, `UPDATE threads SET archived_at = 5 WHERE id = 'T-2'`)
	require.NoError(t, err)

	got, err := q.ListThreadsByOrbCwd(ctx, db.ListThreadsByOrbCwdParams{OrbID: "orb-r", Cwd: "/w/app"})
	require.NoError(t, err)
	assert.Equal(t, []string{"T-1"}, got, "已归档与子目录都不算")

	th, err := q.GetThread(ctx, "T-3")
	require.NoError(t, err)
	assert.Equal(t, "/w/app-x", *th.Cwd)
	assert.Equal(t, wt, *th.Worktree)
	th, err = q.GetThread(ctx, "T-5")
	require.NoError(t, err)
	assert.Nil(t, th.Cwd)
	assert.Nil(t, th.Worktree)

	require.NoError(t, q.SetThreadWorktree(ctx, db.SetThreadWorktreeParams{ID: "T-3"}))
	th, err = q.GetThread(ctx, "T-3")
	require.NoError(t, err)
	assert.Nil(t, th.Worktree, "NULL 清空 worktree")
	assert.Equal(t, "/w/app-x", *th.Cwd, "cwd 不受影响")
	require.NoError(t, q.SetThreadWorktree(ctx, db.SetThreadWorktreeParams{Worktree: new(wt), ID: "T-1"}))
	th, err = q.GetThread(ctx, "T-1")
	require.NoError(t, err)
	assert.Equal(t, wt, *th.Worktree)
}

// TestM11bMigrationDownUp 回滚 0023 再重新应用：runners 表与新列消失后复原，已有 orb 的 kind 回填为 orb。
func TestM11bMigrationDownUp(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	sub, err := fs.Sub(migrationsFS, "migrations")
	require.NoError(t, err)
	p, err := goose.NewProvider(database.DialectSQLite3, s.DB, sub)
	require.NoError(t, err)
	m11b := `SELECT (SELECT COUNT(*) FROM sqlite_master WHERE name = 'runners')
		+ (SELECT COUNT(*) FROM pragma_table_info('orbs') WHERE name = 'kind')
		+ (SELECT COUNT(*) FROM pragma_table_info('threads') WHERE name IN ('cwd', 'worktree'))`
	var n int
	require.NoError(t, s.DB.QueryRowContext(ctx, m11b).Scan(&n))
	assert.Equal(t, 4, n)

	r, err := p.Down(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 23, r.Source.Version)
	require.NoError(t, s.DB.QueryRowContext(ctx, m11b).Scan(&n))
	assert.Zero(t, n, "0023 回滚后表与列都消失")
	_, err = s.DB.ExecContext(ctx, `INSERT INTO providers (id, name, token_hash, created_at) VALUES ('prov_1', 'p', 'h', 1)`)
	require.NoError(t, err)
	_, err = s.DB.ExecContext(ctx, `INSERT INTO orbs (id, provider_id, type, cpus, memory_mb, image, state, state_changed_at, token_hash, created_at)
		VALUES ('orb-old', 'prov_1', 'small', 1, 1, '', 'ready', 1, 't', 1)`)
	require.NoError(t, err)

	_, err = p.Up(ctx)
	require.NoError(t, err)
	require.NoError(t, s.DB.QueryRowContext(ctx, m11b).Scan(&n))
	assert.Equal(t, 4, n)
	o, err := s.Q.GetOrb(ctx, "orb-old")
	require.NoError(t, err)
	assert.Equal(t, "orb", o.Kind)
}
