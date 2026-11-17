package threads

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
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
