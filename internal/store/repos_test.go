package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// TestRepoCommitThreadLinks 覆盖 M24 阶段 ② 的提交↔线程关联：先到的正结果胜出，'none' 只占空位、可被正结果覆盖，
// 不会覆盖正结果；推送记录的待展开与过期；删除项目级联清理。
func TestRepoCommitThreadLinks(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	q := s.Q
	_, err := s.DB.ExecContext(ctx, `INSERT INTO projects (id, name, shallow_clone, created_at, updated_at) VALUES ('prj_1', 'p1', 1, 1, 1)`)
	require.NoError(t, err)
	p := "prj_1"
	insertOldThread(t, s, "thr_a", &p, nil)
	insertOldThread(t, s, "thr_b", &p, nil)
	thrA, thrB := "thr_a", "thr_b"

	// 'none' 先到，push 覆盖它；之后的 pull 与 'none' 都不改动正结果
	require.NoError(t, q.MarkRepoCommitNoThread(ctx, db.MarkRepoCommitNoThreadParams{ProjectID: p, Sha: "c1", LinkedAt: 1}))
	require.NoError(t, q.LinkRepoCommitThread(ctx, db.LinkRepoCommitThreadParams{ProjectID: p, Sha: "c1", ThreadID: &thrA, Source: "push", LinkedAt: 2}))
	require.NoError(t, q.LinkRepoCommitThread(ctx, db.LinkRepoCommitThreadParams{ProjectID: p, Sha: "c1", ThreadID: &thrB, Source: "pull", LinkedAt: 3}))
	require.NoError(t, q.MarkRepoCommitNoThread(ctx, db.MarkRepoCommitNoThreadParams{ProjectID: p, Sha: "c1", LinkedAt: 4}))
	// 'none' 重复标记会刷新时间
	require.NoError(t, q.MarkRepoCommitNoThread(ctx, db.MarkRepoCommitNoThreadParams{ProjectID: p, Sha: "c2", LinkedAt: 5}))
	require.NoError(t, q.MarkRepoCommitNoThread(ctx, db.MarkRepoCommitNoThreadParams{ProjectID: p, Sha: "c2", LinkedAt: 6}))

	rows, err := q.ListRepoCommitThreads(ctx, db.ListRepoCommitThreadsParams{ProjectID: p, Shas: []string{"c1", "c2", "c3"}})
	require.NoError(t, err)
	got := map[string]db.ListRepoCommitThreadsRow{}
	for _, r := range rows {
		got[r.Sha] = r
	}
	require.Len(t, got, 2)
	require.NotNil(t, got["c1"].ThreadID)
	assert.Equal(t, "thr_a", *got["c1"].ThreadID)
	assert.Equal(t, "push", got["c1"].Source)
	assert.Equal(t, int64(2), got["c1"].LinkedAt)
	assert.Nil(t, got["c2"].ThreadID)
	assert.Equal(t, "none", got["c2"].Source)
	assert.Equal(t, int64(6), got["c2"].LinkedAt)

	// 推送记录：待展开 → 展开；未展开的旧记录过期为 missing
	for _, sha := range []string{"n1", "n2"} {
		require.NoError(t, q.InsertRepoPush(ctx, db.InsertRepoPushParams{ProjectID: p, ThreadID: thrA, OrbID: "orb_1",
			Ref: "refs/heads/main", OldSha: "o", NewSha: sha, PushedAt: 10}))
	}
	pending, err := q.ListPendingRepoPushes(ctx, p)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	require.NoError(t, q.FinishRepoPush(ctx, db.FinishRepoPushParams{ID: pending[0].ID, ExpandedAt: 20}))
	require.NoError(t, q.ExpireRepoPushes(ctx, db.ExpireRepoPushesParams{Now: 30, Before: 11}))
	pending, err = q.ListPendingRepoPushes(ctx, p)
	require.NoError(t, err)
	assert.Empty(t, pending)
	var note string
	require.NoError(t, s.DB.QueryRowContext(ctx, `SELECT expand_note FROM repo_pushes WHERE new_sha = 'n2'`).Scan(&note))
	assert.Equal(t, "missing", note)
	require.NoError(t, q.DeleteRepoPushesBefore(ctx, 25))
	var n int
	require.NoError(t, s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM repo_pushes`).Scan(&n))
	assert.Equal(t, 1, n, "只删 25 之前展开的那条")

	// 项目仓库行：upsert 覆盖但保留 created_at
	branch := "main"
	require.NoError(t, q.UpsertProjectRepo(ctx, db.UpsertProjectRepoParams{ProjectID: p, Kind: "mirror", DefaultBranch: &branch, CreatedAt: 1, UpdatedAt: 1}))
	require.NoError(t, q.UpsertProjectRepo(ctx, db.UpsertProjectRepoParams{ProjectID: p, Kind: "mirror", SizeBytes: 42, CreatedAt: 9, UpdatedAt: 9}))
	repo, err := q.GetProjectRepo(ctx, p)
	require.NoError(t, err)
	assert.Equal(t, int64(1), repo.CreatedAt)
	assert.Equal(t, int64(42), repo.SizeBytes)
	assert.Nil(t, repo.DefaultBranch)

	// 删除项目：三张表的行都随外键级联删除
	_, err = s.DB.ExecContext(ctx, `DELETE FROM projects WHERE id = 'prj_1'`)
	require.NoError(t, err)
	for _, tbl := range []string{"project_repos", "repo_pushes", "repo_commit_threads"} {
		require.NoError(t, s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+tbl).Scan(&n))
		assert.Zero(t, n, tbl)
	}
}
