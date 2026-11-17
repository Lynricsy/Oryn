package ownermem_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ownermem"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func (e *env) brief(t *testing.T, threadID string) string {
	t.Helper()
	out, err := e.svc.Brief(context.Background(), threadID)
	require.NoError(t, err)
	return out
}

// TestBriefTopicsBudget:块一按固定顺序拼接，总长不超过 8,000 字；超出时保留 owner-profile 与最近有新事实的主题；
// 遗忘后待刷新的主题不进入；带外层说明。
func TestBriefTopicsBudget(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true)
	e.enable(t)
	e.thread(t, "c", "assistant", "idle", 0, time.Minute)
	e.setFake(func(f *fakeHS) { f.curated = []map[string]any{topic("旧主题"), topic("新主题")} })
	_, err := e.svc.Curate(ctx)
	require.NoError(t, err)
	active := e.activeTopics(t)
	old, fresh := active["旧主题"], active["新主题"]
	e.exec(t, `UPDATE owner_memory_topics SET last_hit_at = 1 WHERE model_id = ?`, old)
	e.exec(t, `UPDATE owner_memory_topics SET last_hit_at = 2 WHERE model_id = ?`, fresh)
	e.setFake(func(f *fakeHS) {
		f.contents = map[string]string{
			ownermem.ProfileTopicID: "画像" + strings.Repeat("甲", 2998),
			old:                     "旧" + strings.Repeat("乙", 3999),
			fresh:                   "新" + strings.Repeat("丙", 3999),
		}
	})

	out := e.brief(t, "c")
	assert.True(t, strings.HasPrefix(out, "Memory（记忆）：\n以下是关于主人的背景资料"))
	assert.Contains(t, out, "只用明确写成主人本人的条目")
	assert.Contains(t, out, "### 主人画像\n画像")
	assert.Contains(t, out, "### 新主题\n新")
	assert.NotContains(t, out, "### 旧主题", "超出预算时丢掉最久没有新事实的主题")
	about := out[strings.Index(out, "## 关于主人"):]
	assert.LessOrEqual(t, utf8.RuneCountInString(about), 8000+30)

	// 遗忘后：全部主题待刷新，块一不进入（新对话没有快照）
	e.thread(t, "c2", "assistant", "idle", 0, time.Minute)
	e.addFact("f1", "world", "x", nil)
	require.NoError(t, e.svc.SetFactState(ctx, "f1", apigen.OwnerMemoryFactStateUpdate{State: apigen.Invalidated}))
	out = e.brief(t, "c2")
	assert.NotContains(t, out, "## 关于主人")
}

// TestBriefRecent:块二按项目分组列出最近 14 天的助手对话与工作线程（状态、交付、PR），不超过 30 行，
// 不含当前对话、更早的线程与被排除的线程。
func TestBriefRecent(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true)
	e.enable(t)
	e.setFake(func(f *fakeHS) { f.down = true }) // Hindsight 不可达：只有块二
	require.NoError(t, e.st.Q.CreateProject(ctx, db.CreateProjectParams{ID: "p", Name: "Oryn", CreatedAt: 1, UpdatedAt: 1}))
	e.thread(t, "cur", "assistant", "idle", 0, time.Minute)
	e.thread(t, "chat", "assistant", "idle", 0, time.Hour)
	e.thread(t, "w1", "work", "working", 0, 2*time.Hour)
	e.thread(t, "w2", "work", "idle", 0, 3*time.Hour)
	e.thread(t, "old", "work", "idle", 0, 15*24*time.Hour)
	e.thread(t, "hidden", "work", "idle", 0, time.Hour)
	e.exec(t, `UPDATE threads SET project_id = 'p' WHERE id IN ('w1','w2')`)
	e.exec(t, `UPDATE threads SET shipped_at = 1 WHERE id = 'w2'`)
	require.NoError(t, e.st.Q.InsertOwnerMemoryExclusion(ctx, db.InsertOwnerMemoryExclusionParams{Kind: "thread", TargetID: "hidden", CreatedAt: 1}))

	out := e.brief(t, "cur")
	assert.NotContains(t, out, "## 关于主人")
	day := e.now.Format("2006-01-02")
	assert.Contains(t, out, "助手对话：\n- 「标题chat」（"+day+"）")
	assert.Contains(t, out, "项目「Oryn」：\n- 「标题w1」（进行中 · 最后活动 "+day+"）\n- 「标题w2」（已交付 · 最后活动 "+day+"）")
	for _, id := range []string{"标题cur", "标题old", "标题hidden"} {
		assert.NotContains(t, out, id)
	}

	for i := range 40 {
		e.thread(t, fmt.Sprintf("n%02d", i), "work", "idle", 0, time.Duration(i)*time.Minute)
	}
	e.now = e.now.Add(2 * time.Hour) // 快照过期
	out = e.brief(t, "cur")
	recent := out[strings.Index(out, "## 最近 14 天"):]
	assert.LessOrEqual(t, strings.Count(recent, "\n"), 30)
}

// TestBriefSnapshot:快照 1 小时内沿用，过期后重新生成；助手记忆关闭或 Space 线程时为空。
func TestBriefSnapshot(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true)
	e.enable(t)
	e.thread(t, "c", "assistant", "idle", 0, time.Minute)
	first := e.brief(t, "c")
	require.Contains(t, first, "内容:主人画像")

	e.setFake(func(f *fakeHS) { f.contents = map[string]string{ownermem.ProfileTopicID: "新画像"} })
	e.now = e.now.Add(59 * time.Minute)
	assert.Equal(t, first, e.brief(t, "c"), "1 小时内沿用快照")
	prev, err := e.svc.Preview(ctx, new("c"))
	require.NoError(t, err)
	assert.True(t, prev.Cached)
	assert.Equal(t, first, prev.Content)

	e.now = e.now.Add(2 * time.Minute)
	assert.Contains(t, e.brief(t, "c"), "新画像")

	e.thread(t, "s", "space", "idle", 0, time.Minute)
	assert.Equal(t, "", e.brief(t, "s"))

	_, err = e.svc.PutSettings(ctx, apigen.OwnerMemorySettingsUpdate{Enabled: false})
	require.NoError(t, err)
	assert.Equal(t, "", e.brief(t, "c"))
	prev, err = e.svc.Preview(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "", prev.Content)
}
