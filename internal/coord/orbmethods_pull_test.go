package coord

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// thread.pull 的目标范围：亲属，或同一项目 / 同一 Space 的平级工作线程；跨项目、全自动 orb 上的平级线程、
// runner 线程、协调对话与自己都不行。
func TestThreadPullSources(t *testing.T) {
	e := newEnv(t, true)
	alpha, beta := e.project("alpha"), e.project("beta")
	sp := e.space("S", "")
	caller := e.threadOn(e.newOrb(), "", alpha, sp.ID)
	auto := e.newOrb()
	require.NoError(t, e.st.Q.SetOrbAutomation(e.ctx, db.SetOrbAutomationParams{Automation: true, ID: auto}))
	_, runnerOrb, err := e.orbs.AddRunner("laptop", []apigen.RunnerDir{{Path: "/w/app", Name: "app"}}, nil)
	require.NoError(t, err)
	runner := ids.Thread()
	require.NoError(t, e.st.Q.CreateThread(e.ctx, db.CreateThreadParams{ID: runner, Kind: threads.KindWork, OrbID: &runnerOrb, ProjectID: &alpha,
		TitleSource: "auto", State: threads.StateIdle, SessionDir: "x", CreatedAt: 1, UpdatedAt: 1, Cwd: new("/w/app")}))

	ok := map[string]string{
		"同项目平级":         e.threadOn(e.newOrb(), "", alpha, "").ID,
		"同 Space 平级":    e.threadOn(e.newOrb(), "", beta, sp.ID).ID,
		"全自动 orb 上的子线程": e.threadOn(auto, caller.ID, alpha, "").ID,
	}
	for name, id := range ok {
		_, err := e.c.pullSource(e.ctx, caller, id)
		assert.NoError(t, err, name)
	}
	denied := map[string]string{
		"别的项目":         e.threadOn(e.newOrb(), "", beta, "").ID,
		"都没有项目":        e.threadOn(e.newOrb(), "", "", "").ID,
		"全自动 orb 上的平级": e.threadOn(auto, "", alpha, "").ID,
		"自己":           caller.ID,
		"协调对话":         e.spaceChat(sp).ID,
		"不存在":          "T-missing",
	}
	for name, id := range denied {
		_, err := e.c.pullSource(e.ctx, caller, id)
		assert.Equal(t, CodeForbidden, ctlCode(t, err), name)
	}
	_, err = e.c.pullSource(e.ctx, caller, runner)
	assert.Equal(t, threads.CodeRunnerUnsupported, ctlCode(t, err), "runner 线程")
	_, err = e.c.pullSource(e.ctx, e.threadOn(auto, "", alpha, ""), caller.ID)
	assert.Equal(t, CodeForbidden, ctlCode(t, err), "全自动 orb 上的线程不能取平级")
}

// thread.list：只列能 thread.pull 的线程并标出关系；已归档、没有 orb、隐藏、别的项目、runner 与全自动 orb 上的平级线程不列。
func TestOnThreadList(t *testing.T) {
	e := newEnv(t, true)
	alpha, beta := e.project("alpha"), e.project("beta")
	sp := e.space("S", "")
	parent := e.threadOn(e.newOrb(), "", "", "")
	x := e.newOrb()
	caller := e.threadOn(x, parent.ID, alpha, sp.ID)
	auto := e.newOrb()
	require.NoError(t, e.st.Q.SetOrbAutomation(e.ctx, db.SetOrbAutomationParams{Automation: true, ID: auto}))
	_, runnerOrb, err := e.orbs.AddRunner("laptop", []apigen.RunnerDir{{Path: "/w/app", Name: "app"}}, nil)
	require.NoError(t, err)

	want := map[string]apigen.LinkCtlThreadListItemRelation{
		parent.ID:                                       apigen.ThreadRelationParent,
		e.threadOn(auto, caller.ID, "", "").ID:          apigen.ThreadRelationChild,
		e.threadOn(e.newOrb(), "", alpha, "").ID:        apigen.ThreadRelationProject,
		e.threadOn(e.newOrb(), caller.ID, alpha, "").ID: apigen.ThreadRelationChild,
		e.threadOn(e.newOrb(), "", beta, sp.ID).ID:      apigen.ThreadRelationSpace,
	}
	e.threadOn(e.newOrb(), "", beta, "") // 别的项目
	e.threadOn(auto, "", alpha, "")      // 全自动 orb 上的平级
	e.threadOn(runnerOrb, "", alpha, "") // runner 上的线程
	archived := e.threadOn(e.newOrb(), "", alpha, "")
	hidden := e.threadOn(e.newOrb(), "", alpha, "")
	_, err = e.st.DB.ExecContext(e.ctx, `UPDATE threads SET archived_at = 1 WHERE id = ?`, archived.ID)
	require.NoError(t, err)
	_, err = e.st.DB.ExecContext(e.ctx, `UPDATE threads SET hidden = 1 WHERE id = ?`, hidden.ID)
	require.NoError(t, err)
	require.NoError(t, e.st.Q.CreateThread(e.ctx, db.CreateThreadParams{ID: ids.Thread(), Kind: threads.KindWork, ProjectID: &alpha,
		TitleSource: "auto", State: threads.StateIdle, SessionDir: "x", CreatedAt: 1, UpdatedAt: 1})) // 没有 orb

	out, err := e.c.onThreadList(e.ctx, x, raw(apigen.LinkCtlThreadListParams{ThreadID: caller.ID}))
	require.NoError(t, err)
	got := map[string]apigen.LinkCtlThreadListItemRelation{}
	for _, it := range out.(apigen.LinkCtlThreadListResult).Threads {
		got[it.ID] = it.Relation
		assert.Equal(t, "https://oryn.test/t/"+it.ID, it.URL)
	}
	assert.Equal(t, want, got)

	_, err = e.c.onThreadList(e.ctx, auto, raw(apigen.LinkCtlThreadListParams{ThreadID: caller.ID}))
	assert.Equal(t, CodeForbidden, ctlCode(t, err), "调用方不属于发起 link 的 orb")
}

// thread.pull：在对方 orb 上 /sync/prepare（have 原样转交），有 bundle 时把字节原样写到
// 调用方 orb 的 bundlePath（不覆盖）；对象已齐时不写文件。
func TestOnThreadPull(t *testing.T) {
	e := newEnv(t, true)
	x, y := e.newOrb(), e.newOrb()
	parent := e.threadOn(x, "", "", "")
	child := e.threadOn(y, parent.ID, "", "")
	stranger := e.threadOn(y, "", "", "")

	m := newFSMachines()
	var prepared apigen.OrbSyncPrepareRequest
	bundle := &apigen.OrbSyncBundle{ID: "0123456789abcdef", Size: 11}
	m.mux.HandleFunc("POST /sync/prepare", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&prepared))
		branch := "feat"
		_ = json.NewEncoder(w).Encode(apigen.OrbSyncState{Root: "/home/u/repo", Branch: &branch,
			Head: "1111111111111111111111111111111111111111", Snapshot: "2222222222222222222222222222222222222222", Dirty: true, Bundle: bundle})
	})
	m.mux.HandleFunc("GET /sync/bundle", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, bundle.ID, r.URL.Query().Get("id"))
		_, _ = w.Write([]byte("bundle-data"))
	})
	e.c.SetMachines(m)

	pull := func(orb, from, to, path string) (any, error) {
		return e.c.onThreadPull(e.ctx, orb, raw(apigen.LinkCtlThreadPullParams{ThreadID: from, Target: to,
			Have: []string{"3333333333333333333333333333333333333333"}, BundlePath: path}))
	}
	_, err := pull(x, parent.ID, stranger.ID, "/home/u/b")
	assert.Equal(t, CodeForbidden, ctlCode(t, err), "非亲属")
	_, err = pull(y, parent.ID, child.ID, "/home/u/b")
	assert.Equal(t, CodeForbidden, ctlCode(t, err), "调用方不属于发起 link 的 orb")
	_, err = pull(x, parent.ID, child.ID, "b")
	assert.Equal(t, "invalid_params", ctlCode(t, err), "bundlePath 须是绝对路径")

	out, err := pull(x, parent.ID, child.ID, "/home/u/repo/.git/oryn-pull-1/changes.bundle")
	require.NoError(t, err)
	res := out.(apigen.LinkCtlThreadPullResult)
	assert.True(t, res.Bundled)
	assert.True(t, res.Dirty)
	assert.Equal(t, "feat", deref(res.Branch))
	assert.Equal(t, []string{"3333333333333333333333333333333333333333"}, prepared.Have, "have 原样转交")
	put := m.last()
	assert.Equal(t, x, put.orb, "bundle 写进调用方的 orb")
	assert.Equal(t, http.MethodPut, put.method)
	assert.Equal(t, "/fs/file", put.path)
	assert.Equal(t, "false", put.query["overwrite"])
	assert.Equal(t, "bundle-data", put.body)
	assert.Contains(t, m.features, orbs.FeatureSync)

	// 对象已齐（没有 bundle）：不往调用方写文件
	bundle = nil
	n := len(m.reqs)
	out, err = pull(y, child.ID, parent.ID, "/home/u/b")
	require.NoError(t, err, "子线程也能取父线程的改动")
	assert.False(t, out.(apigen.LinkCtlThreadPullResult).Bundled)
	assert.Len(t, m.reqs, n+1, "只有一次 /sync/prepare")
}
