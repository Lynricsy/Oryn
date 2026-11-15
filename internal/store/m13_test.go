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

// TestAgentAvatarsCheck 覆盖 agent_avatars 的 CHECK（图标与图片恰好其一）与 upsert / 删除语义。
func TestAgentAvatarsCheck(t *testing.T) {
	ctx := context.Background()
	q := openTemp(t, t.TempDir()).Q

	assert.Error(t, q.UpsertAgentAvatar(ctx, db.UpsertAgentAvatarParams{Name: "scout", UpdatedAt: 1}), "两列都 NULL 被拒绝")
	assert.Error(t, q.UpsertAgentAvatar(ctx, db.UpsertAgentAvatarParams{
		Name: "scout", AvatarIcon: ptr("🔭"), AvatarImage: ptr("data:image/png;base64,AA=="), UpdatedAt: 1,
	}), "两列都有值被拒绝")
	rows, err := q.ListAgentAvatars(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows)

	require.NoError(t, q.UpsertAgentAvatar(ctx, db.UpsertAgentAvatarParams{Name: "scout", AvatarIcon: ptr("🔭"), UpdatedAt: 1}))
	require.NoError(t, q.UpsertAgentAvatar(ctx, db.UpsertAgentAvatarParams{Name: "scout", AvatarImage: ptr("data:image/png;base64,AA=="), UpdatedAt: 2}))
	require.NoError(t, q.UpsertAgentAvatar(ctx, db.UpsertAgentAvatarParams{Name: "reviewer", AvatarIcon: ptr("r"), UpdatedAt: 3}))
	rows, err = q.ListAgentAvatars(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 2, "同名两次 upsert 只有一行")
	assert.Equal(t, "reviewer", rows[0].Name, "按名字排序")
	assert.Equal(t, "scout", rows[1].Name)
	assert.Nil(t, rows[1].AvatarIcon, "后一次 upsert 覆盖前者")
	assert.Equal(t, "data:image/png;base64,AA==", *rows[1].AvatarImage)

	one, err := q.GetAgentAvatar(ctx, "reviewer")
	require.NoError(t, err)
	assert.Equal(t, "r", *one.AvatarIcon)

	require.NoError(t, q.DeleteAgentAvatar(ctx, "scout"))
	require.NoError(t, q.DeleteAgentAvatar(ctx, "scout"), "删除不存在的行不报错")
	_, err = q.GetAgentAvatar(ctx, "scout")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

// TestOwnerNewThreadTarget 覆盖 owner.new_thread_target：新库缺省 role，可写 assistant，CHECK 拒绝其他值。
func TestOwnerNewThreadTarget(t *testing.T) {
	ctx := context.Background()
	q := openTemp(t, t.TempDir()).Q

	n, err := q.SetOwnerNewThreadTarget(ctx, "assistant")
	require.NoError(t, err)
	assert.Zero(t, n, "无所有者行时写入影响 0 行")
	require.NoError(t, q.CreateOwner(ctx, db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))

	v, err := q.GetOwnerNewThreadTarget(ctx)
	require.NoError(t, err)
	assert.Equal(t, "role", v, "缺省为 role")

	n, err = q.SetOwnerNewThreadTarget(ctx, "assistant")
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	v, err = q.GetOwnerNewThreadTarget(ctx)
	require.NoError(t, err)
	assert.Equal(t, "assistant", v)

	_, err = q.SetOwnerNewThreadTarget(ctx, "space")
	require.Error(t, err, "CHECK 拒绝 role / assistant 以外的值")
	o, err := q.GetOwner(ctx)
	require.NoError(t, err)
	assert.Equal(t, "assistant", o.NewThreadTarget)
}

// TestM13MigrationDownUp 回滚 0025、0024 再重新应用：已有所有者行在升级后取缺省 role，Down 后表与列都消失。
func TestM13MigrationDownUp(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	sub, err := fs.Sub(migrationsFS, "migrations")
	require.NoError(t, err)
	p, err := goose.NewProvider(database.DialectSQLite3, s.DB, sub)
	require.NoError(t, err)
	m13 := `SELECT (SELECT COUNT(*) FROM sqlite_master WHERE name = 'agent_avatars')
		+ (SELECT COUNT(*) FROM pragma_table_info('owner') WHERE name = 'new_thread_target')`
	var n int
	require.NoError(t, s.DB.QueryRowContext(ctx, m13).Scan(&n))
	assert.Equal(t, 2, n)

	r, err := p.Down(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 25, r.Source.Version)
	r, err = p.Down(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 24, r.Source.Version)
	require.NoError(t, s.DB.QueryRowContext(ctx, m13).Scan(&n))
	assert.Zero(t, n, "0024 回滚后表与列都消失")
	_, err = s.DB.ExecContext(ctx, `INSERT INTO owner (id, display_name, user_handle, created_at) VALUES (1, 'o', x'00', 1)`)
	require.NoError(t, err)

	_, err = p.Up(ctx)
	require.NoError(t, err)
	require.NoError(t, s.DB.QueryRowContext(ctx, m13).Scan(&n))
	assert.Equal(t, 2, n)
	v, err := s.Q.GetOwnerNewThreadTarget(ctx)
	require.NoError(t, err)
	assert.Equal(t, "role", v, "存量所有者行升级后取缺省 role")
}

// TestAssistantFilesMigration 覆盖 0025：CHECK 约束、线程硬删除时 thread_id 置 NULL 而行保留，以及回滚再应用。
func TestAssistantFilesMigration(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	q := s.Q
	require.NoError(t, q.CreateThread(ctx, db.CreateThreadParams{
		Kind: "work", ID: "T-1", TitleSource: "auto", State: "idle", SessionDir: "~/.oryn/threads/T-1",
	}))
	row := db.InsertAssistantFileParams{
		ID: "afl_1", Name: "a.txt", Mime: "text/plain", Size: 1, Sha256: "x", Scope: "conversation",
		ThreadID: ptr("T-1"), Source: "upload", CreatedAt: 1,
	}
	require.NoError(t, q.InsertAssistantFile(ctx, row))
	bad := row
	bad.ID, bad.Scope = "afl_2", "public"
	assert.Error(t, q.InsertAssistantFile(ctx, bad), "CHECK 拒绝未知 scope")
	bad.Scope, bad.Source = "shared", "web"
	assert.Error(t, q.InsertAssistantFile(ctx, bad), "CHECK 拒绝未知 source")
	bad.Source, bad.ThreadID = "orb", ptr("T-404")
	assert.Error(t, q.InsertAssistantFile(ctx, bad), "thread_id 受外键约束")

	n, err := q.DeleteThread(ctx, "T-1")
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	got, err := q.GetAssistantFile(ctx, "afl_1")
	require.NoError(t, err, "线程删除后文件行保留")
	assert.Nil(t, got.ThreadID, "thread_id 置 NULL")

	sub, err := fs.Sub(migrationsFS, "migrations")
	require.NoError(t, err)
	p, err := goose.NewProvider(database.DialectSQLite3, s.DB, sub)
	require.NoError(t, err)
	const exists = `SELECT COUNT(*) FROM sqlite_master WHERE name IN ('assistant_files', 'assistant_files_thread')`
	var c int
	r, err := p.Down(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 25, r.Source.Version)
	require.NoError(t, s.DB.QueryRowContext(ctx, exists).Scan(&c))
	assert.Zero(t, c, "0025 回滚后表与索引都消失")
	_, err = p.Up(ctx)
	require.NoError(t, err)
	require.NoError(t, s.DB.QueryRowContext(ctx, exists).Scan(&c))
	assert.Equal(t, 2, c)
}
