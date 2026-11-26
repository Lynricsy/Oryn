package ownermem_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// thread 建一个线程：n 个主人回合（每个回合一条主人消息 + 一条回复），最后一条消息在 lastAgo 之前。
func (e *env) thread(t *testing.T, id, kind, state string, n int, lastAgo time.Duration) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, e.st.Q.CreateThread(ctx, db.CreateThreadParams{
		ID: id, Title: "标题" + id, TitleSource: "auto", State: state, StateChangedAt: 1, SessionDir: "d",
		CreatedAt: 1, UpdatedAt: 1, Kind: kind,
	}))
	e.turns(t, id, 0, n)
	e.touch(t, id, lastAgo)
}

// turns 追加第 from..from+n-1 个回合（条目 id 连成一条链）。
func (e *env) turns(t *testing.T, id string, from, n int) {
	t.Helper()
	ctx := context.Background()
	for i := from; i < from+n; i++ {
		for j, m := range []struct{ role, text string }{
			{"user", fmt.Sprintf("第 %d 个问题：%s", i, strings.Repeat("问", 600))},
			{"assistant", fmt.Sprintf("第 %d 个回答", i)},
		} {
			eid := fmt.Sprintf("e%d", 2*i+j)
			var parent *string
			if 2*i+j > 0 {
				p := fmt.Sprintf("e%d", 2*i+j-1)
				parent = &p
			}
			payload, _ := json.Marshal(map[string]any{"type": "message", "message": map[string]any{
				"role": m.role, "content": []map[string]any{{"type": "text", "text": m.text}},
			}})
			ts := e.now.UnixMilli()
			_, err := e.st.Q.UpsertEntry(ctx, db.UpsertEntryParams{
				ThreadID: id, File: "s.jsonl", EntryID: eid, ParentID: parent, Type: "message", Ts: &ts, Payload: payload,
			})
			require.NoError(t, err)
		}
	}
}

func (e *env) touch(t *testing.T, id string, ago time.Duration) {
	t.Helper()
	_, err := e.st.DB.Exec(`UPDATE threads SET last_message_at = ? WHERE id = ?`, e.now.Add(-ago).UnixMilli(), id)
	require.NoError(t, err)
}

func (e *env) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	_, err := e.st.DB.Exec(q, args...)
	require.NoError(t, err)
}

func (e *env) scan(t *testing.T) int {
	t.Helper()
	n, err := e.svc.Scan(context.Background())
	require.NoError(t, err)
	return n
}

func (e *env) retainCount() int {
	e.hs.mu.Lock()
	defer e.hs.mu.Unlock()
	return len(e.hs.retains)
}

// TestWriterIdleAndArchive:空闲满 30 分钟才写入；归档立即写入；写入带 replace 与来源标签。
func TestWriterIdleAndArchive(t *testing.T) {
	e := newEnv(t, true)
	e.enable(t)
	e.thread(t, "c1", "assistant", "idle", 1, 10*time.Minute)
	e.thread(t, "w1", "work", "working", 1, time.Minute)
	assert.Equal(t, 0, e.scan(t))
	assert.Equal(t, 0, e.retainCount())

	e.now = e.now.Add(25 * time.Minute)
	assert.Equal(t, 1, e.scan(t), "对话空闲满 30 分钟")
	assert.Equal(t, []string{"conv:c1:0"}, e.hs.retainedDocs(0))
	assert.Len(t, e.hs.retains[0], 2, "主人消息与末尾助手回复各一个 item，同属一个 document")
	assert.True(t, e.hs.retainSync[0], "一块多个 item 时同步写入（异步批次不允许重复 document_id）")
	it := e.hs.retains[0][0]
	assert.Equal(t, "replace", it["update_mode"])
	assert.Contains(t, it["tags"], "src:assistant")
	assert.Equal(t, 0, e.scan(t), "没有新消息不重写")

	e.exec(t, `UPDATE threads SET archived_at = ? WHERE id = 'w1'`, e.now.UnixMilli())
	assert.Equal(t, 1, e.scan(t), "归档的线程即使在运行也立即写入")
	assert.Equal(t, []string{"thread:w1:0"}, e.hs.retainedDocs(1))
	assert.False(t, e.hs.retainSync[1], "每块一个 item 的工作线程照常异步写入")
}

// TestWriterChangedChunksOnly:追加消息只重写变了的块；转录回退后块数变少时删除多出的 document。
func TestWriterChangedChunksOnly(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true)
	e.enable(t)
	e.thread(t, "w", "work", "idle", 16, time.Hour)
	require.Equal(t, 1, e.scan(t))
	first := e.hs.retainedDocs(0)
	require.Greater(t, len(first), 2)
	chunks, err := e.st.Q.ListOwnerMemoryChunks(ctx, "w")
	require.NoError(t, err)
	require.Len(t, chunks, len(first))

	e.now = e.now.Add(2 * time.Hour)
	e.turns(t, "w", 16, 1)
	e.touch(t, "w", time.Hour)
	require.Equal(t, 1, e.scan(t))
	second := e.hs.retainedDocs(1)
	assert.NotEmpty(t, second)
	assert.Less(t, len(second), len(first), "前面的块不变，不重写")
	assert.NotContains(t, second, "thread:w:0")

	// 回到第 1 个回合（活动分支变短），转录版本加一
	e.exec(t, `UPDATE threads SET leaf_entry_id = 'e1', transcript_rev = transcript_rev + 1 WHERE id = 'w'`)
	require.Equal(t, 1, e.scan(t))
	chunks, err = e.st.Q.ListOwnerMemoryChunks(ctx, "w")
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	assert.Equal(t, "thread:w:0", chunks[0].DocumentID)
	assert.Contains(t, e.hs.deletes, "thread:w:1")
	assert.NotContains(t, e.hs.deletes, "thread:w:0")
	src, err := e.st.Q.GetOwnerMemorySource(ctx, "w")
	require.NoError(t, err)
	assert.Equal(t, int64(1), src.Chunks)
}

// TestWriterExclusionForgets:排除对话或所在项目、删除线程后，已写入的 document 被删除，来源记录清掉。
func TestWriterExclusionForgets(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true)
	e.enable(t)
	require.NoError(t, e.st.Q.CreateProject(ctx, db.CreateProjectParams{ID: "p", Name: "P", CreatedAt: 1, UpdatedAt: 1}))
	e.thread(t, "a", "assistant", "idle", 1, time.Hour)
	e.thread(t, "b", "work", "idle", 1, time.Hour)
	e.thread(t, "c", "work", "idle", 1, time.Hour)
	e.exec(t, `UPDATE threads SET project_id = 'p' WHERE id = 'b'`)
	require.Equal(t, 3, e.scan(t))

	require.NoError(t, e.st.Q.InsertOwnerMemoryExclusion(ctx, db.InsertOwnerMemoryExclusionParams{Kind: "thread", TargetID: "a", CreatedAt: 1}))
	require.NoError(t, e.st.Q.InsertOwnerMemoryExclusion(ctx, db.InsertOwnerMemoryExclusionParams{Kind: "project", TargetID: "p", CreatedAt: 1}))
	e.exec(t, `DELETE FROM threads WHERE id = 'c'`)
	e.scan(t)
	assert.ElementsMatch(t, []string{"conv:a:0", "thread:b:0", "thread:c:0"}, e.hs.deletes)
	for _, id := range []string{"a", "b", "c"} {
		_, err := e.st.Q.GetOwnerMemorySource(ctx, id)
		require.Error(t, err, id)
		chunks, err := e.st.Q.ListOwnerMemoryChunks(ctx, id)
		require.NoError(t, err)
		assert.Empty(t, chunks)
	}
	// 被排除的不再写入
	e.touch(t, "a", 40*time.Minute)
	assert.Equal(t, 0, e.scan(t))
}

// TestWriterRetry:写入失败记下错误并退避 30 秒，期间不重试；恢复后写入成功并清掉错误。
func TestWriterRetry(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true)
	e.enable(t)
	e.thread(t, "c", "assistant", "idle", 1, time.Hour)
	e.hs.mu.Lock()
	e.hs.failRetain = true
	e.hs.mu.Unlock()
	assert.Equal(t, 0, e.scan(t))
	src, err := e.st.Q.GetOwnerMemorySource(ctx, "c")
	require.NoError(t, err)
	require.NotNil(t, src.LastError)
	assert.Contains(t, *src.LastError, "boom")
	assert.Equal(t, e.now.Add(30*time.Second).UnixMilli(), *src.NextAttemptAt)
	st, err := e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, st.FailingSources)

	e.hs.mu.Lock()
	e.hs.failRetain = false
	e.hs.mu.Unlock()
	e.now = e.now.Add(10 * time.Second)
	assert.Equal(t, 0, e.scan(t), "退避期间不重试")
	e.now = e.now.Add(30 * time.Second)
	assert.Equal(t, 1, e.scan(t))
	src, err = e.st.Q.GetOwnerMemorySource(ctx, "c")
	require.NoError(t, err)
	assert.Nil(t, src.LastError)
	assert.Equal(t, int64(0), src.Attempts)
}

// TestWriterPartialRetainThenExclude:多块对话逐块同步写入，首块成功、次块失败时首块已记下哈希、其余块登记为待写；
// 重试只重写没成功的块；随后排除对话时，全部块（含落库后未确认的）都被删除。
func TestWriterPartialRetainThenExclude(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true)
	e.enable(t)
	e.thread(t, "c", "assistant", "idle", 16, time.Hour)
	e.hs.mu.Lock()
	e.hs.failRetainFrom = 2
	e.hs.mu.Unlock()
	assert.Equal(t, 0, e.scan(t))

	chunks, err := e.st.Q.ListOwnerMemoryChunks(ctx, "c")
	require.NoError(t, err)
	require.Greater(t, len(chunks), 2, "需要多块才能覆盖逐块写入")
	require.Equal(t, 1, e.retainCount())
	assert.True(t, e.hs.retainSync[0])
	assert.Equal(t, []string{"conv:c:0"}, e.hs.retainedDocs(0))
	assert.NotEmpty(t, chunks[0].Hash, "成功写入的首块记下哈希")
	var all []string
	for i, c := range chunks {
		all = append(all, c.DocumentID)
		if i > 0 {
			assert.Empty(t, c.Hash, "未确认的块登记为待写：%s", c.DocumentID)
		}
	}

	// 恢复后重试：首块不再重写，失败与未写的块逐块写入
	e.hs.mu.Lock()
	e.hs.failRetainFrom = 0
	e.hs.mu.Unlock()
	e.now = e.now.Add(31 * time.Second)
	assert.Equal(t, 1, e.scan(t))
	var retried []string
	for i := 1; i < e.retainCount(); i++ {
		retried = append(retried, e.hs.retainedDocs(i)...)
	}
	assert.Equal(t, all[1:], retried)

	// 再写入一次后失败，然后排除：所有块都被删除
	e.hs.mu.Lock()
	e.hs.failRetainFrom = e.hs.retainCalls + 1
	e.hs.mu.Unlock()
	e.now = e.now.Add(2 * time.Hour)
	e.turns(t, "c", 16, 1)
	e.touch(t, "c", time.Hour)
	assert.Equal(t, 0, e.scan(t))
	require.NoError(t, e.st.Q.InsertOwnerMemoryExclusion(ctx, db.InsertOwnerMemoryExclusionParams{Kind: "thread", TargetID: "c", CreatedAt: 1}))
	e.scan(t)
	chunks, err = e.st.Q.ListOwnerMemoryChunks(ctx, "c")
	require.NoError(t, err)
	assert.Empty(t, chunks)
	for _, id := range all {
		assert.Contains(t, e.hs.deletes, id)
	}
}

// TestWriterBackfillWindow:回填只取开启前 30 天内有活动的来源；进度按来源数计算。
func TestWriterBackfillWindow(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true)
	e.thread(t, "old", "assistant", "idle", 1, 31*24*time.Hour)
	e.thread(t, "new", "assistant", "idle", 1, 29*24*time.Hour)
	e.enable(t)
	st, err := e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, st.BackfillTotal)
	assert.Equal(t, 0, st.BackfillDone)

	assert.Equal(t, 1, e.scan(t))
	assert.Equal(t, []string{"conv:new:0"}, e.hs.retainedDocs(0))
	st, err = e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, st.BackfillDone)
}

// TestWriterDisabled:助手记忆关闭时不写入。
func TestWriterDisabled(t *testing.T) {
	e := newEnv(t, true)
	e.thread(t, "c", "assistant", "idle", 1, time.Hour)
	assert.Equal(t, 0, e.scan(t))
	assert.Equal(t, 0, e.retainCount())
}
