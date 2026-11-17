package threads

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 主线对话（M19 步骤 3.1）：只能是助手对话；API 带 main；不能归档、标记已交付、删除；其余字段照常可改。
func TestMainConversationRules(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")

	var te *Error
	_, err := f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindSpace, OrbID: orb, Main: true})
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "invalid", te.Code)

	main, err := f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindAssistant, OrbID: orb, Main: true, Title: "Main"})
	require.NoError(t, err)
	require.NotNil(t, main.Main)
	assert.True(t, *main.Main)
	row, err := f.st.Q.GetThread(f.ctx, main.ID)
	require.NoError(t, err)
	assert.Equal(t, "user", row.TitleSource)

	plain, err := f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindAssistant, OrbID: orb})
	require.NoError(t, err)
	assert.Nil(t, plain.Main, "普通助手对话不带 main")

	// 全库至多一条主线：唯一索引兜底
	_, err = f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindAssistant, OrbID: orb, Main: true})
	require.Error(t, err)

	yes := true
	_, err = f.s.Patch(f.ctx, main.ID, apigen.ThreadPatch{Archived: &yes})
	require.ErrorAs(t, err, &te)
	assert.Equal(t, CodeMainConversation, te.Code)
	_, err = f.s.Patch(f.ctx, main.ID, apigen.ThreadPatch{Shipped: &yes})
	require.ErrorAs(t, err, &te)
	assert.Equal(t, CodeMainConversation, te.Code)
	err = f.s.Delete(f.ctx, main.ID)
	require.ErrorAs(t, err, &te)
	assert.Equal(t, CodeMainConversation, te.Code)

	got, err := f.s.Patch(f.ctx, main.ID, apigen.ThreadPatch{Pinned: &yes})
	require.NoError(t, err)
	assert.True(t, got.Pinned)
	assert.Nil(t, got.ArchivedAt)
}

// 主线可有多条提醒（M19 步骤 3.4）：线程 API 的单数 schedule 摘要对主线省略。
func TestMainScheduleSummaryOmitted(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	main, err := f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindAssistant, OrbID: orb, Main: true, Title: "Main"})
	require.NoError(t, err)
	next := int64(1_900_000_000_000)
	for _, name := range []string{"a", "b"} {
		require.NoError(t, f.st.Q.InsertSchedule(f.ctx, db.InsertScheduleParams{
			ID: ids.New("sch"), Mode: "thread", ThreadID: &main.ID, Name: name, Kind: "every", Spec: "1h0m0s", Timezone: "UTC",
			Prompt: "p", Enabled: true, NextRunAt: &next, CreatedBy: "user", CreatedAt: 1, UpdatedAt: 1, Overlap: "skip", KeepLast: 5,
		}))
	}
	got, err := f.s.Get(f.ctx, main.ID)
	require.NoError(t, err)
	assert.Nil(t, got.Schedule)
	list, err := f.s.List(f.ctx, ListFilter{Kind: "all"})
	require.NoError(t, err)
	found := false
	for _, th := range list {
		if th.ID == main.ID {
			found = true
			assert.Nil(t, th.Schedule)
		}
	}
	assert.True(t, found, "kind=all 含主线")
}
