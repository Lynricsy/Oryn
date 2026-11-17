package ownermem_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/memory"
	"github.com/Lynricsy/Oryn/internal/ownermem"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func (e *env) addFact(id, typ, text string, extra map[string]any) {
	e.setFake(func(f *fakeHS) {
		if f.facts == nil {
			f.facts = map[string]map[string]any{}
		}
		m := map[string]any{"id": id, "type": typ, "text": text, "state": "valid"}
		for k, v := range extra {
			m[k] = v
		}
		f.facts[id] = m
		f.factOrder = append(f.factOrder, id)
	})
}

// staleTopics 返回遗忘后等待刷新的主题 id。
func (e *env) staleTopics(t *testing.T) []string {
	t.Helper()
	list, err := e.svc.Topics(context.Background())
	require.NoError(t, err)
	var out []string
	for _, it := range list.Items {
		if it.Stale {
			out = append(out, it.ID)
		}
	}
	return out
}

func memErr(t *testing.T, err error) *memory.Error {
	t.Helper()
	var me *memory.Error
	require.ErrorAs(t, err, &me)
	return me
}

// TestOwnerMemoryFactsProvenance:出处——只含主人消息为 owner，含助手消息为 mixed（带线程与 seq 范围），
// 明确记忆为 explicit，observation 沿来源事实回溯，回溯不到为 unknown。
func TestOwnerMemoryFactsProvenance(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true)
	e.enable(t)
	e.thread(t, "c1", "assistant", "idle", 1, time.Hour)
	e.addFact("f1", "world", "主人住在新加坡", map[string]any{
		"document_id": "conv:c1:0", "metadata": map[string]any{"seqs": "3", "roles": "owner"}})
	e.addFact("f2", "world", "主人决定用 OAuth App", map[string]any{
		"document_id": "conv:c1:0", "metadata": map[string]any{"seqs": "4,5", "roles": "owner,assistant"}})
	e.addFact("f3", "world", "主人喜欢狐狸", map[string]any{"document_id": "explicit:ome_X"})
	e.addFact("o1", "observation", "主人常驻新加坡", map[string]any{"source_memory_ids": []string{"f1"}})
	e.addFact("o2", "observation", "无来源", nil)

	list, err := e.svc.Facts(ctx, apigen.ListOwnerMemoryFactsParams{})
	require.NoError(t, err)
	require.Len(t, list.Items, 5)
	byID := map[string]apigen.OwnerMemoryProvenance{}
	for _, it := range list.Items {
		byID[it.ID] = it.Provenance
	}
	p := byID["f1"]
	assert.Equal(t, apigen.OwnerMemoryProvenanceOwner, p.Kind)
	assert.Equal(t, "c1", *p.ThreadID)
	assert.Equal(t, "标题c1", *p.ThreadTitle)
	assert.Equal(t, "assistant", *p.ThreadKind)
	assert.Equal(t, int64(3), *p.SeqFrom)
	p = byID["f2"]
	assert.Equal(t, apigen.OwnerMemoryProvenanceMixed, p.Kind)
	assert.Equal(t, int64(4), *p.SeqFrom)
	assert.Equal(t, int64(5), *p.SeqTo)
	assert.Equal(t, apigen.OwnerMemoryProvenanceExplicit, byID["f3"].Kind)
	assert.Equal(t, apigen.OwnerMemoryProvenanceOwner, byID["o1"].Kind, "observation 沿来源事实回溯")
	assert.Equal(t, "conv:c1:0", *byID["o1"].DocumentID)
	assert.Equal(t, apigen.OwnerMemoryProvenanceUnknown, byID["o2"].Kind)

	world := apigen.World
	lim := 2
	list, err = e.svc.Facts(ctx, apigen.ListOwnerMemoryFactsParams{Type: &world, Limit: &lim})
	require.NoError(t, err)
	assert.Len(t, list.Items, 2)
	assert.True(t, list.HasMore)

	// 召回：来源事实随结果给出
	e.setFake(func(f *fakeHS) {
		f.recallHits = []map[string]any{{"id": "o1", "text": "主人常驻新加坡", "type": "observation", "source_fact_ids": []string{"f2"}}}
	})
	q := "住在哪"
	list, err = e.svc.Facts(ctx, apigen.ListOwnerMemoryFactsParams{Q: &q})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.Equal(t, apigen.OwnerMemoryProvenanceMixed, list.Items[0].Provenance.Kind)
}

// TestOwnerMemoryFactState:world 事实失效与恢复；失效后主题在刷新完成前不可注入；observation 不能直接失效。
func TestOwnerMemoryFactState(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true)
	e.enable(t)
	e.addFact("f1", "world", "主人住在上海", nil)
	e.addFact("o1", "observation", "x", nil)
	assert.Empty(t, e.staleTopics(t))

	reason := "搬家了"
	require.NoError(t, e.svc.SetFactState(ctx, "f1", apigen.OwnerMemoryFactStateUpdate{State: apigen.Invalidated, Reason: &reason}))
	require.Len(t, e.hs.factPatches, 1)
	assert.Equal(t, "invalidated", e.hs.factPatches[0]["state"])
	assert.Equal(t, "搬家了", e.hs.factPatches[0]["reason"])
	assert.Equal(t, []string{ownermem.ProfileTopicID}, e.staleTopics(t), "刷新完成前主题不可注入")
	assert.Contains(t, e.hs.refreshes, ownermem.ProfileTopicID, "失效后提交刷新")

	list, err := e.svc.Facts(ctx, apigen.ListOwnerMemoryFactsParams{})
	require.NoError(t, err)
	assert.Equal(t, apigen.OwnerMemoryFactInvalidated, list.Items[0].State)

	require.NoError(t, e.svc.SetFactState(ctx, "f1", apigen.OwnerMemoryFactStateUpdate{State: apigen.Valid}))
	assert.Equal(t, "valid", e.hs.factPatches[1]["state"])

	assert.Equal(t, 400, memErr(t, e.svc.SetFactState(ctx, "o1", apigen.OwnerMemoryFactStateUpdate{State: apigen.Invalidated})).Status)
	assert.Equal(t, 404, memErr(t, e.svc.SetFactState(ctx, "nope", apigen.OwnerMemoryFactStateUpdate{State: apigen.Invalidated})).Status)
}

// TestOwnerMemoryForgetSource:按来源遗忘删除全部 document，主题在刷新完成前不可注入；没有新内容时不会被写回，
// 有新内容时重新写入。
func TestOwnerMemoryForgetSource(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true)
	e.enable(t)
	e.thread(t, "c", "assistant", "idle", 1, time.Hour)
	require.Equal(t, 1, e.scan(t))

	out, err := e.svc.ForgetSource(ctx, "c")
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryForgetResult{Deleted: 1}, out)
	assert.Equal(t, []string{"conv:c:0"}, e.hs.deletes)
	assert.Equal(t, []string{ownermem.ProfileTopicID}, e.staleTopics(t))

	srcs, err := e.svc.Sources(ctx, apigen.ListOwnerMemorySourcesParams{})
	require.NoError(t, err)
	require.Len(t, srcs.Items, 1)
	assert.Equal(t, 0, srcs.Items[0].Chunks)
	assert.Equal(t, "标题c", *srcs.Items[0].ThreadTitle)

	assert.Equal(t, 0, e.scan(t), "没有新内容不写回")
	e.now = e.now.Add(2 * time.Hour)
	e.touch(t, "c", time.Hour)
	assert.Equal(t, 1, e.scan(t), "有新内容时重新写入")

	assert.Equal(t, 404, memErr(t, func() error { _, err := e.svc.ForgetSource(ctx, "none"); return err }()).Status)
}

// TestOwnerMemoryExclusions:排除对话或项目时已写入的立即遗忘；列表带名称；取消排除。
func TestOwnerMemoryExclusions(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true)
	e.enable(t)
	require.NoError(t, e.st.Q.CreateProject(ctx, db.CreateProjectParams{ID: "p", Name: "项目P", CreatedAt: 1, UpdatedAt: 1}))
	e.thread(t, "a", "assistant", "idle", 1, time.Hour)
	e.thread(t, "b", "work", "idle", 1, time.Hour)
	e.exec(t, `UPDATE threads SET project_id = 'p' WHERE id = 'b'`)
	require.Equal(t, 2, e.scan(t))

	out, err := e.svc.PutExclusion(ctx, apigen.OwnerMemoryExclusionThread, "a")
	require.NoError(t, err)
	assert.Equal(t, 1, out.Deleted)
	out, err = e.svc.PutExclusion(ctx, apigen.OwnerMemoryExclusionProject, "p")
	require.NoError(t, err)
	assert.Equal(t, 1, out.Deleted)
	assert.ElementsMatch(t, []string{"conv:a:0", "thread:b:0"}, e.hs.deletes)

	list, err := e.svc.Exclusions(ctx)
	require.NoError(t, err)
	require.Len(t, list.Items, 2)
	names := []string{*list.Items[0].Name, *list.Items[1].Name}
	assert.ElementsMatch(t, []string{"项目P", "标题a"}, names)

	_, err = e.svc.PutExclusion(ctx, apigen.OwnerMemoryExclusionThread, "missing")
	assert.Equal(t, 404, memErr(t, err).Status)

	require.NoError(t, e.svc.DeleteExclusion(ctx, apigen.OwnerMemoryExclusionThread, "a"))
	assert.Equal(t, 404, memErr(t, e.svc.DeleteExclusion(ctx, apigen.OwnerMemoryExclusionThread, "a")).Status)
	assert.Equal(t, 1, e.scan(t), "取消排除后重新写入")
}

// TestOwnerMemoryExplicit:管理页新建直接写入 explicit: document；remember 的原话出现在主人消息里才直接生效，
// 否则待确认，批准（可改写）后写入；修改已生效的重写 document；删除已生效的删除 document。
func TestOwnerMemoryExplicit(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true)
	e.enable(t)
	e.thread(t, "c", "assistant", "idle", 1, time.Hour)

	created, err := e.svc.CreateExplicit(ctx, "  主人的猫叫团子 ")
	require.NoError(t, err)
	assert.Equal(t, apigen.OwnerMemoryExplicitActive, created.Status)
	assert.Equal(t, "主人的猫叫团子", created.Content)
	require.Equal(t, 1, e.retainCount())
	it := e.hs.retains[0][0]
	assert.Equal(t, "explicit:"+created.ID, it["document_id"])
	assert.Contains(t, it["tags"], "src:explicit")
	assert.Contains(t, it["content"], "主人的猫叫团子")

	// 原话出现在主人消息里（e.turns 写的是「第 0 个问题：…」）
	quote := "第 0 个问题"
	direct, err := e.svc.Remember(ctx, "c", "主人问过第 0 个问题", &quote)
	require.NoError(t, err)
	assert.Equal(t, apigen.OwnerMemoryExplicitActive, direct.Status)
	assert.Equal(t, 2, e.retainCount())

	// 原话只出现在助手回复里 -> 待确认，不写入
	fake := "第 0 个回答"
	pending, err := e.svc.Remember(ctx, "c", "助手编的", &fake)
	require.NoError(t, err)
	assert.Equal(t, apigen.OwnerMemoryExplicitPending, pending.Status)
	assert.Equal(t, "标题c", *pending.SourceThreadTitle)
	assert.Equal(t, 2, e.retainCount())
	st, err := e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, st.PendingExplicit)

	list, err := e.svc.ListExplicit(ctx)
	require.NoError(t, err)
	require.Len(t, list.Items, 3)
	assert.Equal(t, pending.ID, list.Items[0].ID, "待确认的在前")

	fixed := "改写后的内容"
	approved, err := e.svc.ApproveExplicit(ctx, pending.ID, &fixed)
	require.NoError(t, err)
	assert.Equal(t, apigen.OwnerMemoryExplicitActive, approved.Status)
	require.Equal(t, 3, e.retainCount())
	assert.Equal(t, "explicit:"+pending.ID, e.hs.retains[2][0]["document_id"])
	assert.Contains(t, e.hs.retains[2][0]["content"], "改写后的内容")
	_, err = e.svc.ApproveExplicit(ctx, pending.ID, nil)
	assert.Equal(t, 409, memErr(t, err).Status)

	updated, err := e.svc.UpdateExplicit(ctx, created.ID, "主人的猫叫年糕")
	require.NoError(t, err)
	assert.Equal(t, "主人的猫叫年糕", updated.Content)
	require.Equal(t, 4, e.retainCount())
	assert.Equal(t, "replace", e.hs.retains[3][0]["update_mode"])

	require.NoError(t, e.svc.DeleteExplicit(ctx, created.ID))
	assert.Contains(t, e.hs.deletes, "explicit:"+created.ID)
	_, err = e.svc.UpdateExplicit(ctx, created.ID, "x")
	assert.Equal(t, 404, memErr(t, err).Status)
}
