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

// TestM11ProjectSnapshots 覆盖 0021 的项目快照查询：同键至多一次构建（ON CONFLICT DO NOTHING）、
// 先退役同键旧 ready 再发布、按项目退役、命中计数、配额求和与 LRU 顺序、项目删除后行保留（project_id 置空）。
func TestM11ProjectSnapshots(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	q := s.Q
	require.NoError(t, q.CreateProject(ctx, db.CreateProjectParams{ID: "prj_1", Name: "p1", CreatedAt: 1, UpdatedAt: 1}))
	require.NoError(t, q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))

	prj, err := q.GetProject(ctx, "prj_1")
	require.NoError(t, err)
	assert.True(t, prj.Snapshots, "缺省开启项目快照")
	assert.Zero(t, prj.WarmOrbs)

	build := func(id string, at int64) int64 {
		n, err := q.InsertBuildingProjectSnapshot(ctx, db.InsertBuildingProjectSnapshotParams{
			ID: id, ProjectID: "prj_1", ProviderID: "prov_1", Type: "small", ConfigHash: "h1", LineageAt: at, CreatedAt: at,
		})
		require.NoError(t, err)
		return n
	}
	publish := func(id string, at int64, size int64) {
		require.NoError(t, s.Tx(ctx, func(q *db.Queries) error {
			if err := q.RetireOtherReadySnapshots(ctx, db.RetireOtherReadySnapshotsParams{RetiredAt: at, RetireReason: "superseded", ID: id}); err != nil {
				return err
			}
			n, err := q.PublishProjectSnapshot(ctx, db.PublishProjectSnapshotParams{ReadyAt: at, SizeBytes: &size, BaseCommit: "c-" + id, ID: id})
			require.EqualValues(t, 1, n)
			return err
		}))
	}

	assert.EqualValues(t, 1, build("oryn-snap-a", 10))
	assert.EqualValues(t, 0, build("oryn-snap-b", 11), "同键已有 building：第二个构建者让路")
	publish("oryn-snap-a", 12, 100)
	assert.EqualValues(t, 1, build("oryn-snap-c", 20), "ready 不占 building 的键")
	publish("oryn-snap-c", 21, 300)

	ready, err := q.GetReadyProjectSnapshot(ctx, db.GetReadyProjectSnapshotParams{ProjectID: "prj_1", ProviderID: "prov_1", Type: "small"})
	require.NoError(t, err)
	assert.Equal(t, "oryn-snap-c", ready.ID)
	assert.Equal(t, "c-oryn-snap-c", ready.BaseCommit)
	old, err := q.GetProjectSnapshot(ctx, "oryn-snap-a")
	require.NoError(t, err)
	assert.Equal(t, "retired", old.State)
	assert.Equal(t, "superseded", *old.RetireReason)

	n, err := q.TouchProjectSnapshotUse(ctx, db.TouchProjectSnapshotUseParams{LastUsedAt: 30, ID: "oryn-snap-c"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	n, err = q.TouchProjectSnapshotUse(ctx, db.TouchProjectSnapshotUseParams{LastUsedAt: 30, ID: "oryn-snap-a"})
	require.NoError(t, err)
	assert.Zero(t, n, "已退役的快照不再命中")

	sum, err := q.SumProjectSnapshotBytes(ctx, "prov_1")
	require.NoError(t, err)
	assert.EqualValues(t, 300, sum, "只有 ready 计入配额（快照缓存）；退役的不算")
	lru, err := q.ListReadySnapshotsLRU(ctx, "prov_1")
	require.NoError(t, err)
	require.Len(t, lru, 1)
	assert.EqualValues(t, 1, lru[0].UseCount)

	list, err := q.ListProjectSnapshotsByProject(ctx, "prj_1")
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "oryn-snap-c", list[0].ProjectSnapshot.ID, "按 createdAt 倒序")
	assert.Equal(t, "local", list[0].ProviderName)

	require.NoError(t, q.RetireProjectSnapshotsByProject(ctx, db.RetireProjectSnapshotsByProjectParams{RetiredAt: 40, RetireReason: "manual", ProjectID: "prj_1", Type: new("large")}))
	_, err = q.GetReadyProjectSnapshot(ctx, db.GetReadyProjectSnapshotParams{ProjectID: "prj_1", ProviderID: "prov_1", Type: "small"})
	require.NoError(t, err, "type 不匹配的不退役")
	require.NoError(t, q.RetireProjectSnapshotsByProject(ctx, db.RetireProjectSnapshotsByProjectParams{RetiredAt: 40, RetireReason: "manual", ProjectID: "prj_1"}))
	_, err = q.GetReadyProjectSnapshot(ctx, db.GetReadyProjectSnapshotParams{ProjectID: "prj_1", ProviderID: "prov_1", Type: "small"})
	assert.ErrorIs(t, err, sql.ErrNoRows)

	_, err = q.DeleteProject(ctx, "prj_1")
	require.NoError(t, err)
	retired, err := q.ListProjectSnapshotsByState(ctx, "retired")
	require.NoError(t, err)
	require.Len(t, retired, 2, "项目删除后快照行留给回收器")
	assert.Nil(t, retired[0].ProjectID)
}

// TestM11PoolOrbs 覆盖预热池的 CAS 查询：认领 / 状态迁移只在 pool_state 等于调用方所见值时生效。
func TestM11PoolOrbs(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	q := s.Q
	require.NoError(t, q.CreateProject(ctx, db.CreateProjectParams{ID: "prj_1", Name: "p1", CreatedAt: 1, UpdatedAt: 1, WarmOrbs: new(int64(2))}))
	require.NoError(t, q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "p", TokenHash: "h", CreatedAt: 1}))
	require.NoError(t, q.CreateOrb(ctx, db.CreateOrbParams{ID: "orb-1", ProviderID: "prov_1", Type: "small", ProjectID: new("prj_1"),
		State: "requested", TokenHash: "t1", CreatedAt: 1, PoolState: new("warming"), ConfigHash: new("h1")}))
	require.NoError(t, q.CreateOrb(ctx, db.CreateOrbParams{ID: "orb-2", ProviderID: "prov_1", Type: "small", ProjectID: new("prj_1"),
		State: "ready", TokenHash: "t2", CreatedAt: 2}))

	prj, err := q.GetProject(ctx, "prj_1")
	require.NoError(t, err)
	assert.EqualValues(t, 2, prj.WarmOrbs)

	pool, err := q.ListPoolOrbs(ctx, new("prj_1"))
	require.NoError(t, err)
	require.Len(t, pool, 1)
	assert.Equal(t, "orb-1", pool[0].ID)

	n, err := q.SetOrbPoolState(ctx, db.SetOrbPoolStateParams{PoolState: new("idle"), ID: "orb-1", Seen: "warming"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	n, err = q.ClaimPoolOrb(ctx, db.ClaimPoolOrbParams{PoolState: new("claimed"), ClaimedAt: 5, ID: "orb-1", Seen: "warming"})
	require.NoError(t, err)
	assert.Zero(t, n, "所见状态已过期：认领失败")
	n, err = q.ClaimPoolOrb(ctx, db.ClaimPoolOrbParams{PoolState: new("claimed"), ClaimedAt: 5, ID: "orb-1", Seen: "idle"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	n, err = q.SetOrbPoolState(ctx, db.SetOrbPoolStateParams{ID: "orb-1", Seen: "claimed"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	pool, err = q.ListPoolOrbs(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, pool, "认领完成后离开预热池")
	o, err := q.GetOrb(ctx, "orb-1")
	require.NoError(t, err)
	assert.EqualValues(t, 5, *o.ClaimedAt)

	require.NoError(t, q.SetOrbConfigHash(ctx, db.SetOrbConfigHashParams{ConfigHash: "h2", ID: "orb-2"}))
	o, err = q.GetOrb(ctx, "orb-2")
	require.NoError(t, err)
	assert.Equal(t, "h2", *o.ConfigHash)
}

// TestM11MigrationDownUp 先回滚 0023，再依次回滚 0022、0021 后重新应用：新表、新列与索引消失后复原。
func TestM11MigrationDownUp(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	sub, err := fs.Sub(migrationsFS, "migrations")
	require.NoError(t, err)
	p, err := goose.NewProvider(database.DialectSQLite3, s.DB, sub)
	require.NoError(t, err)
	m11 := `SELECT (SELECT COUNT(*) FROM sqlite_master WHERE name IN ('project_snapshots', 'project_snapshots_building',
			'project_snapshots_ready', 'project_snapshots_state', 'orbs_pool'))
		+ (SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name IN ('snapshots', 'warm_orbs'))
		+ (SELECT COUNT(*) FROM pragma_table_info('orbs') WHERE name IN ('pool_state', 'config_hash', 'claimed_at'))
		+ (SELECT COUNT(*) FROM pragma_table_info('providers') WHERE name = 'base_image_id')
		+ (SELECT COUNT(*) FROM pragma_table_info('jobs') WHERE name = 'result')
		+ (SELECT COUNT(*) FROM pragma_table_info('orb_setup_runs') WHERE name IN ('prepared', 'base_commit'))`
	m11fix := `SELECT COUNT(*) FROM pragma_table_info('orbs') WHERE name = 'base_image_id'`
	var n, nf int
	require.NoError(t, s.DB.QueryRowContext(ctx, m11).Scan(&n))
	assert.Equal(t, 14, n)
	require.NoError(t, s.DB.QueryRowContext(ctx, m11fix).Scan(&nf))
	assert.Equal(t, 1, nf)

	r, err := p.Down(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 25, r.Source.Version)
	r, err = p.Down(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 24, r.Source.Version)
	r, err = p.Down(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 23, r.Source.Version)
	r, err = p.Down(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 22, r.Source.Version)
	require.NoError(t, s.DB.QueryRowContext(ctx, m11fix).Scan(&nf))
	assert.Zero(t, nf, "0022 回滚后 orbs.base_image_id 消失")
	r, err = p.Down(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 21, r.Source.Version)
	require.NoError(t, s.DB.QueryRowContext(ctx, m11).Scan(&n))
	assert.Zero(t, n, "0021 回滚后新表、索引与列都消失")

	_, err = p.Up(ctx)
	require.NoError(t, err)
	require.NoError(t, s.DB.QueryRowContext(ctx, m11).Scan(&n))
	assert.Equal(t, 14, n)
	require.NoError(t, s.DB.QueryRowContext(ctx, m11fix).Scan(&nf))
	assert.Equal(t, 1, nf)
}
