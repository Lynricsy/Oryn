package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// TestRoleAvatarsCheck 覆盖 role_avatars 的 CHECK（图标与图片恰好其一）与 upsert / 删除语义。
func TestRoleAvatarsCheck(t *testing.T) {
	ctx := context.Background()
	q := openTemp(t, t.TempDir()).Q

	assert.Error(t, q.UpsertRoleAvatar(ctx, db.UpsertRoleAvatarParams{Role: "Mare", UpdatedAt: 1}), "两列都 NULL 被拒绝")
	assert.Error(t, q.UpsertRoleAvatar(ctx, db.UpsertRoleAvatarParams{
		Role: "Mare", AvatarIcon: ptr("🌊"), AvatarImage: ptr("data:image/png;base64,AA=="), UpdatedAt: 1,
	}), "两列都有值被拒绝")
	rows, err := q.ListRoleAvatars(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows)

	require.NoError(t, q.UpsertRoleAvatar(ctx, db.UpsertRoleAvatarParams{Role: "Mare", AvatarIcon: ptr("🌊"), UpdatedAt: 1}))
	require.NoError(t, q.UpsertRoleAvatar(ctx, db.UpsertRoleAvatarParams{Role: "Mare", AvatarImage: ptr("data:image/png;base64,AA=="), UpdatedAt: 2}))
	require.NoError(t, q.UpsertRoleAvatar(ctx, db.UpsertRoleAvatarParams{Role: "mare", AvatarIcon: ptr("m"), UpdatedAt: 3}))
	rows, err = q.ListRoleAvatars(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 2, "同 role 两次 upsert 只有一行；大小写不同是两行")
	assert.Equal(t, "Mare", rows[0].Role)
	assert.Nil(t, rows[0].AvatarIcon, "后一次 upsert 覆盖前者")
	assert.Equal(t, "data:image/png;base64,AA==", *rows[0].AvatarImage)
	assert.Equal(t, "mare", rows[1].Role)

	one, err := q.GetRoleAvatar(ctx, "mare")
	require.NoError(t, err)
	assert.Equal(t, "m", *one.AvatarIcon)

	require.NoError(t, q.DeleteRoleAvatar(ctx, "Mare"))
	require.NoError(t, q.DeleteRoleAvatar(ctx, "Mare"), "删除不存在的行不报错")
	_, err = q.GetRoleAvatar(ctx, "Mare")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

// TestOwnerIdentityColumns 覆盖 agent 名字与助手身份三列的读写；无所有者行时写入影响 0 行。
func TestOwnerIdentityColumns(t *testing.T) {
	ctx := context.Background()
	q := openTemp(t, t.TempDir()).Q

	n, err := q.SetOwnerAssistantIdentity(ctx, db.SetOwnerAssistantIdentityParams{AssistantName: ptr("x")})
	require.NoError(t, err)
	assert.Zero(t, n)
	require.NoError(t, q.CreateOwner(ctx, db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))

	n, err = q.SetOwnerAgentIdentity(ctx, db.SetOwnerAgentIdentityParams{AgentName: ptr("Oryn"), AgentAvatarIcon: ptr("🦊")})
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	n, err = q.SetOwnerAssistantIdentity(ctx, db.SetOwnerAssistantIdentityParams{AssistantName: ptr("小助手")})
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)

	agent, err := q.GetOwnerAgentIdentity(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Oryn", *agent.AgentName)
	assert.Equal(t, "🦊", *agent.AgentAvatarIcon)
	assert.Nil(t, agent.AgentAvatarImage)
	asst, err := q.GetOwnerAssistantIdentity(ctx)
	require.NoError(t, err)
	assert.Equal(t, "小助手", *asst.AssistantName)
	assert.Nil(t, asst.AssistantAvatarIcon, "助手头像未设置 = NULL（跟随 agent）")
	assert.Nil(t, asst.AssistantAvatarImage)

	// 整体替换：省略即清除，且两组列互不影响。
	_, err = q.SetOwnerAssistantIdentity(ctx, db.SetOwnerAssistantIdentityParams{})
	require.NoError(t, err)
	asst, err = q.GetOwnerAssistantIdentity(ctx)
	require.NoError(t, err)
	assert.Nil(t, asst.AssistantName)
	agent, err = q.GetOwnerAgentIdentity(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Oryn", *agent.AgentName)
}

// TestShareIdentityQueries 覆盖快照分享身份行的读写删、线程删除的级联，以及来源条目查询的过滤。
func TestShareIdentityQueries(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	q := s.Q
	const th = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ac"
	require.NoError(t, q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: th, TitleSource: "auto", State: "idle", SessionDir: "/a", CreatedAt: 1, UpdatedAt: 1}))
	src := `{"type":"custom","customType":"oryn-model-source","data":{"v":1,"source":"role","role":"Mare","model":"openai/gpt-6.1-sol"}}`
	for _, e := range []db.UpsertEntryParams{
		{ThreadID: th, File: "a.jsonl", EntryID: "m0", Type: "model_change", Payload: []byte(`{"type":"model_change","model":"a/b"}`)},
		{ThreadID: th, File: "a.jsonl", EntryID: "c1", ParentID: ptr("m0"), Type: "custom", Payload: []byte(src)},
		{ThreadID: th, File: "a.jsonl", EntryID: "c2", ParentID: ptr("c1"), Type: "custom", Payload: []byte(`{"type":"custom","customType":"other"}`)},
		{ThreadID: th, File: "a.jsonl", EntryID: "u1", ParentID: ptr("c2"), Type: "message", Payload: []byte(`{"type":"message"}`)},
		{ThreadID: th, File: "a/sub.jsonl", EntryID: "s1", Type: "model_change", Payload: []byte(`{"type":"model_change"}`)},
		{ThreadID: th, File: "a/sub.jsonl", EntryID: "s2", Type: "custom", Payload: []byte(src)},
		{ThreadID: th, File: "a.jsonl", EntryID: "m1", ParentID: ptr("u1"), Type: "model_change", Payload: []byte(`{"type":"model_change","model":"c/d"}`)},
	} {
		_, err := q.UpsertEntry(ctx, e)
		require.NoError(t, err)
	}
	rows, err := q.ListModelSourceEntries(ctx, db.ListModelSourceEntriesParams{ThreadID: th, UntilSeq: 6})
	require.NoError(t, err)
	got := make([]int64, 0, len(rows))
	for _, r := range rows {
		got = append(got, r.Seq)
	}
	// 只有主文件的 model_change 与来源记录；其它 custom、message、子 agent 文件、越界（seq 7）都不在。
	assert.Equal(t, []int64{1, 2}, got)
	assert.Equal(t, "custom", rows[1].Type)
	assert.JSONEq(t, src, string(rows[1].Payload))

	require.NoError(t, q.InsertThreadShare(ctx, db.InsertThreadShareParams{ID: "shr_1", ThreadID: th, Slug: "s1", Mode: "snapshot", CreatedAt: 10}))
	require.NoError(t, q.InsertThreadShare(ctx, db.InsertThreadShareParams{ID: "shr_2", ThreadID: th, Slug: "s2", Mode: "snapshot", CreatedAt: 11}))
	assert.Error(t, q.InsertThreadShareIdentity(ctx, db.InsertThreadShareIdentityParams{ShareID: "shr_missing", Payload: `{}`}), "外键：分享须存在")
	require.NoError(t, q.InsertThreadShareIdentity(ctx, db.InsertThreadShareIdentityParams{ShareID: "shr_1", Payload: `{"roles":[]}`}))
	require.NoError(t, q.InsertThreadShareIdentity(ctx, db.InsertThreadShareIdentityParams{ShareID: "shr_2", Payload: `{"roles":[1]}`}))
	assert.Error(t, q.InsertThreadShareIdentity(ctx, db.InsertThreadShareIdentityParams{ShareID: "shr_1", Payload: `{}`}), "每个分享至多一行")
	p, err := q.GetThreadShareIdentity(ctx, "shr_1")
	require.NoError(t, err)
	assert.Equal(t, `{"roles":[]}`, p)

	require.NoError(t, q.DeleteThreadShareIdentity(ctx, "shr_1"))
	_, err = q.GetThreadShareIdentity(ctx, "shr_1")
	require.ErrorIs(t, err, sql.ErrNoRows)

	_, err = s.DB.ExecContext(ctx, `DELETE FROM threads WHERE id = ?`, th)
	require.NoError(t, err)
	var n int
	require.NoError(t, s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM thread_share_identities`).Scan(&n))
	assert.Zero(t, n, "线程删除经 thread_shares 级联删掉身份行")
}
