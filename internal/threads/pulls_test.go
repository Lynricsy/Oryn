package threads

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 关联的 GitHub PR（M14）：Get 与 List 结果一致；没有关联时 JSON 里没有 pulls 键。
func TestThreadPulls(t *testing.T) {
	f := newFixture(t, Config{})
	w := f.thread(f.orb("ready"), "idle")
	plain := f.thread(f.orb("ready"), "idle")

	failure := "failure"
	require.NoError(t, f.st.Q.UpsertGithubPull(f.ctx, db.UpsertGithubPullParams{
		RepoID: 42, Number: 12, RepoFullName: "acme/widgets", Title: "Fix login", State: "open", SameRepo: true,
		HeadRef: "fix", HeadSha: "abc", BaseRef: "main", AuthorLogin: "alice", HtmlUrl: "https://github.com/acme/widgets/pull/12", UpdatedAt: 1,
	}))
	require.NoError(t, f.st.Q.SetGithubPullChecks(f.ctx, db.SetGithubPullChecksParams{Checks: &failure, ChecksSha: new("abc"), RepoID: 42, Number: 12}))
	n, err := f.st.Q.LinkThreadPull(f.ctx, db.LinkThreadPullParams{ThreadID: w.id, RepoID: 42, Number: 12, LinkedAt: 1})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	got, err := f.s.Get(f.ctx, w.id)
	require.NoError(t, err)
	checks := apigen.ThreadPullChecksFailure
	want := []apigen.ThreadPull{{
		Repo: "acme/widgets", Number: 12, Title: "Fix login", State: apigen.ThreadPullStateOpen, Draft: false,
		URL: "https://github.com/acme/widgets/pull/12", Checks: &checks, Listening: true,
	}}
	assert.Equal(t, want, got.Pulls)

	list, err := f.s.List(f.ctx, ListFilter{})
	require.NoError(t, err)
	byID := map[string]apigen.Thread{}
	for _, th := range list {
		byID[th.ID] = th
	}
	assert.Equal(t, want, byID[w.id].Pulls, "列表与单线程结果一致")

	other, err := f.s.Get(f.ctx, plain.id)
	require.NoError(t, err)
	b, err := json.Marshal(other)
	require.NoError(t, err)
	assert.NotContains(t, string(b), `"pulls"`)
	assert.Nil(t, byID[plain.id].Pulls)

	// 取消关联后 pulls 消失
	_, err = f.st.Q.UnlinkThreadPull(f.ctx, db.UnlinkThreadPullParams{ThreadID: w.id, RepoID: 42, Number: 12})
	require.NoError(t, err)
	got, err = f.s.Get(f.ctx, w.id)
	require.NoError(t, err)
	assert.Nil(t, got.Pulls)
}
