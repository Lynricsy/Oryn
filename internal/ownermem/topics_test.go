package ownermem_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ownermem"
)

func (e *env) setFake(fn func(f *fakeHS)) {
	e.hs.mu.Lock()
	defer e.hs.mu.Unlock()
	fn(e.hs)
}

func topic(name string, keep ...string) map[string]any {
	return map[string]any{"name": name, "scope": name + "的范围", "keep_existing_ids": keep}
}

// activeTopics 返回活跃主题的 名称 -> id。
func (e *env) activeTopics(t *testing.T) map[string]string {
	t.Helper()
	list, err := e.svc.Topics(context.Background())
	require.NoError(t, err)
	out := map[string]string{}
	for _, it := range list.Items {
		if it.Status == apigen.OwnerMemoryTopicActive {
			out[it.Name] = it.ID
		}
	}
	return out
}

// TestTopicCurate:整理新建主题，延续的保留，不再出现的归档；owner-profile 始终在且不归档；结果可在列表里看到。
func TestTopicCurate(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true)
	e.enable(t)
	e.setFake(func(f *fakeHS) { f.curated = []map[string]any{topic("Oryn"), topic("面试")} })
	cur, err := e.svc.Curate(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"Oryn", "面试"}, cur.Created)
	active := e.activeTopics(t)
	require.Len(t, active, 3)
	assert.Equal(t, ownermem.ProfileTopicID, active["主人画像"])
	oryn := active["Oryn"]
	assert.Regexp(t, `^owner-topic-[0-9a-f]{10}$`, oryn)

	// 「Oryn」延续（改名也算），「面试」消失 -> 归档；新增「论文」
	e.setFake(func(f *fakeHS) { f.curated = []map[string]any{topic("Oryn 项目", oryn), topic("论文")} })
	e.now = e.now.Add(time.Hour)
	cur, err = e.svc.Curate(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"Oryn"}, cur.Kept)
	assert.Equal(t, []string{"论文"}, cur.Created)
	assert.Equal(t, []string{"面试"}, cur.Archived)
	active = e.activeTopics(t)
	assert.Len(t, active, 3)
	assert.Equal(t, oryn, active["Oryn"], "延续的主题保持 id 与名称")

	list, err := e.svc.Topics(ctx)
	require.NoError(t, err)
	require.NotNil(t, list.LastCuration)
	assert.Equal(t, []string{"面试"}, list.LastCuration.Archived)
	assert.True(t, list.Items[0].Seed)
	require.NotNil(t, list.Items[0].Content)
	assert.Equal(t, "内容:主人画像", *list.Items[0].Content)

	// 整理结果为空：动态主题全部归档，owner-profile 不动
	e.setFake(func(f *fakeHS) { f.curated = nil })
	_, err = e.svc.Curate(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"主人画像": ownermem.ProfileTopicID}, e.activeTopics(t))
}

// TestTopicCap:活跃主题（含 owner-profile）不超过 12 个。
func TestTopicCap(t *testing.T) {
	e := newEnv(t, true)
	e.enable(t)
	var many []map[string]any
	for i := range 15 {
		many = append(many, topic(fmt.Sprintf("主题%d", i)))
	}
	e.setFake(func(f *fakeHS) { f.curated = many })
	cur, err := e.svc.Curate(context.Background())
	require.NoError(t, err)
	assert.Len(t, cur.Created, 11)
	assert.Len(t, e.activeTopics(t), 12)
}

// TestTopicRefresh:同一主题 20 小时内不刷新；没有新事实不刷新，有上次刷新之后提到的事实才刷新；owner-profile 每周至少一次。
func TestTopicRefresh(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true)
	e.enable(t)
	e.setFake(func(f *fakeHS) { f.curated = []map[string]any{topic("Oryn")} })
	_, err := e.svc.Curate(ctx)
	require.NoError(t, err)
	oryn := e.activeTopics(t)["Oryn"]

	// owner-profile 从未刷新过 -> 刷新；Oryn 刚创建（算作刷新）-> 不刷新
	n, err := e.svc.RefreshDue(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{ownermem.ProfileTopicID}, e.hs.refreshes)

	// 10 小时后有新事实也不刷新（间隔不足 20 小时）
	e.now = e.now.Add(10 * time.Hour)
	e.setFake(func(f *fakeHS) { f.mentioned = e.now.UTC().Format(time.RFC3339) })
	n, err = e.svc.RefreshDue(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	// 21 小时后：事实是 10 小时前提到的（晚于上次刷新）-> 两个主题都刷新
	e.now = e.now.Add(11 * time.Hour)
	n, err = e.svc.RefreshDue(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.ElementsMatch(t, []string{ownermem.ProfileTopicID, ownermem.ProfileTopicID, oryn}, e.hs.refreshes)

	// 再过 2 天没有新事实（最新事实早于上次刷新）-> 不刷新
	e.now = e.now.Add(48 * time.Hour)
	n, err = e.svc.RefreshDue(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	// owner-profile 距上次刷新满 7 天 -> 即使没有新事实也刷新；Oryn 不刷新
	e.now = e.now.Add(5 * 24 * time.Hour)
	e.setFake(func(f *fakeHS) { f.refreshes = nil })
	n, err = e.svc.RefreshDue(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{ownermem.ProfileTopicID}, e.hs.refreshes)

	// 手动刷新不受间隔限制；不存在的主题 404
	require.NoError(t, e.svc.RefreshTopic(ctx, oryn))
	assert.Equal(t, []string{ownermem.ProfileTopicID, oryn}, e.hs.refreshes)
	assert.Error(t, e.svc.RefreshTopic(ctx, "owner-topic-missing"))
}
