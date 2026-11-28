package coord

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// thread.pull：只能取父子线程的改动；在对方 orb 上 /sync/prepare（have 原样转交），有 bundle 时把字节原样写到
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
