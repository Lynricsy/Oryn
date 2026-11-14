package stats

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// fixture 是统计测试的数据夹具：两个项目、一个 Space、几条线程。
type fixture struct {
	t   *testing.T
	ctx context.Context
	st  *store.Store
	seq int64
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, ctx: context.Background(), st: openStore(t)}
	q := f.st.Q
	require.NoError(t, q.CreateProject(f.ctx, db.CreateProjectParams{ID: "prj_a", Name: "alpha", CreatedAt: 1, UpdatedAt: 1}))
	require.NoError(t, q.CreateProject(f.ctx, db.CreateProjectParams{ID: "prj_b", Name: "beta", CreatedAt: 1, UpdatedAt: 1}))
	f.thread("T-chat", "", "", "space")
	require.NoError(t, q.CreateSpace(f.ctx, db.CreateSpaceParams{ID: "sp_1", Name: "S1", ThreadID: "T-chat", CreatedAt: 1, UpdatedAt: 1}))
	return f
}

// thread 建一条线程；project / space 为空 = 无。
func (f *fixture) thread(id, project, space, kind string) {
	f.t.Helper()
	p := db.CreateThreadParams{ID: id, Title: "title " + id, TitleSource: "auto", State: "idle", StateChangedAt: 1, SessionDir: "d", CreatedAt: 1, UpdatedAt: 1, Kind: kind}
	if project != "" {
		p.ProjectID = new(project)
	}
	if space != "" {
		p.SpaceID = new(space)
	}
	require.NoError(f.t, f.st.Q.CreateThread(f.ctx, p))
}

// usage 写一行用量（entry_seq 自增）。
func (f *fixture) usage(thread string, ts int64, model string, cost float64, mod ...func(*db.InsertUsageRecordParams)) {
	f.t.Helper()
	f.seq++
	p := db.InsertUsageRecordParams{EntrySeq: f.seq, Ts: ts, Provider: "p", Model: model, InputTokens: 10, OutputTokens: 20, CostUsd: cost}
	if thread != "" {
		p.ThreadID = new(thread)
	}
	for _, m := range mod {
		m(&p)
	}
	n, err := f.st.Q.InsertUsageRecord(f.ctx, p)
	require.NoError(f.t, err)
	require.EqualValues(f.t, 1, n)
}

func (f *fixture) tool(thread string, ts int64, tool string, isErr bool) {
	f.t.Helper()
	f.seq++
	_, err := f.st.Q.InsertToolCall(f.ctx, db.InsertToolCallParams{ThreadID: new(thread), EntrySeq: f.seq, Ts: ts, Tool: tool, IsError: isErr})
	require.NoError(f.t, err)
}

func (f *fixture) turn(thread string, ts int64) {
	f.t.Helper()
	f.seq++
	_, err := f.st.Q.InsertTurn(f.ctx, db.InsertTurnParams{ThreadID: new(thread), EntrySeq: f.seq, Ts: ts})
	require.NoError(f.t, err)
}

// deleteThread 模拟线程删除：先写归属快照，再删线程行（事实行 thread_id 置空）。
func (f *fixture) deleteThread(id string) {
	f.t.Helper()
	th, err := f.st.Q.GetThread(f.ctx, id)
	require.NoError(f.t, err)
	require.NoError(f.t, f.st.Q.TombstoneUsage(f.ctx, db.TombstoneUsageParams{ProjectID: th.ProjectID, SpaceID: th.SpaceID, ThreadID: id}))
	require.NoError(f.t, f.st.Q.TombstoneToolCalls(f.ctx, db.TombstoneToolCallsParams{ProjectID: th.ProjectID, SpaceID: th.SpaceID, ThreadID: id}))
	require.NoError(f.t, f.st.Q.TombstoneTurns(f.ctx, db.TombstoneTurnsParams{ProjectID: th.ProjectID, SpaceID: th.SpaceID, ThreadID: id}))
	_, err = f.st.Q.DeleteThread(f.ctx, id)
	require.NoError(f.t, err)
}

func (f *fixture) exec(query string, args ...any) {
	f.t.Helper()
	_, err := f.st.DB.ExecContext(f.ctx, query, args...)
	require.NoError(f.t, err)
}
