package threads

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// M15 R25：Abort 只在线程工作中时下发 abort 帧；空闲或 agent 未就绪时什么也不发、不唤醒。
func TestAbortOnlyWhenWorking(t *testing.T) {
	f := newFixture(t, Config{})
	idle := f.thread(f.orb("ready"), StateIdle)
	a := fakeAgent(8)
	attach(idle, a, apigen.AgentStatusReady)
	require.NoError(t, f.s.Abort(f.ctx, idle.id))
	assert.Empty(t, sent(a))

	working := f.thread(f.orb("ready"), StateWorking)
	b := fakeAgent(8)
	attach(working, b, apigen.AgentStatusReady)
	working.mu.Lock()
	working.state = StateWorking
	working.mu.Unlock()
	require.NoError(t, f.s.Abort(f.ctx, working.id))
	assert.Equal(t, []string{"abort"}, typesOf(sent(b)))

	noAgent := f.thread(f.orb("ready"), StateWorking)
	require.NoError(t, f.s.Abort(f.ctx, noAgent.id))
	assert.Empty(t, f.orbs.wakes, "不为中止唤醒 orb")
}

// M15 R15：线程带 issue 任务时，Get 与 List 都给出页头徽标（仓库取项目的 repo_url，状态取最新任务）。
func TestThreadGithubTask(t *testing.T) {
	f := newFixture(t, Config{})
	prj := ids.New("prj")
	require.NoError(t, f.st.Q.CreateProject(f.ctx, db.CreateProjectParams{ID: prj, Name: "w", RepoUrl: new("https://github.com/Acme/Widgets.git"), CreatedAt: 1, UpdatedAt: 1}))
	id := ids.Thread()
	require.NoError(t, f.st.Q.CreateThread(f.ctx, db.CreateThreadParams{Kind: "work", ID: id, ProjectID: &prj, TitleSource: "auto", State: StateIdle, SessionDir: sessionDirOf(id), CreatedAt: 1, UpdatedAt: 1}))
	plain := f.thread(f.orb("ready"), StateIdle)
	require.NoError(t, f.st.Q.UpsertThreadIssue(f.ctx, db.UpsertThreadIssueParams{RepoID: 42, Number: 7, ThreadID: id, CreatedAt: 1}))
	_, err := f.st.Q.SetThreadIssueState(f.ctx, db.SetThreadIssueStateParams{State: "fixing", ThreadID: id})
	require.NoError(t, err)

	want := &apigen.GitHubThreadTask{Kind: apigen.GitHubThreadTaskKindIssue, Repo: "Acme/Widgets", Number: new(int64(7)), State: "fixing"}
	got, err := f.s.Get(f.ctx, id)
	require.NoError(t, err)
	assert.Equal(t, want, got.GithubTask)
	list, err := f.s.List(f.ctx, ListFilter{})
	require.NoError(t, err)
	for _, th := range list {
		switch th.ID {
		case id:
			assert.Equal(t, want, th.GithubTask)
		case plain.id:
			assert.Nil(t, th.GithubTask)
		}
	}
	assert.Equal(t, "", repoPathOf("not a url/x/y/z"))
}

// M15 步骤 6.1 / R15：只挂发布哨兵行的线程，Get 与 List 都给出 release 徽标（版本与状态取最新一行，仓库取项目的 repo_url）；
// 同时挂 issue 任务与发布行时 issue 任务优先；什么都不挂的线程没有徽标。
func TestThreadGithubReleaseTask(t *testing.T) {
	f := newFixture(t, Config{})
	prj := ids.New("prj")
	require.NoError(t, f.st.Q.CreateProject(f.ctx, db.CreateProjectParams{ID: prj, Name: "w", RepoUrl: new("https://github.com/Acme/Widgets.git"), CreatedAt: 1, UpdatedAt: 1}))
	thread := func() string {
		id := ids.Thread()
		require.NoError(t, f.st.Q.CreateThread(f.ctx, db.CreateThreadParams{Kind: "work", ID: id, ProjectID: &prj, TitleSource: "auto", State: StateIdle, SessionDir: sessionDirOf(id), CreatedAt: 1, UpdatedAt: 1}))
		return id
	}
	release := func(id, version, state string, at int64) {
		require.NoError(t, f.st.Q.UpsertGithubRelease(f.ctx, db.UpsertGithubReleaseParams{RepoID: 42, Version: version, Sha: "abc", State: "awaiting_ci", UpdatedAt: at}))
		require.NoError(t, f.st.Q.SetGithubReleaseThread(f.ctx, db.SetGithubReleaseThreadParams{ThreadID: &id, UpdatedAt: at, RepoID: 42, Version: version}))
		require.NoError(t, f.st.Q.SetGithubReleaseState(f.ctx, db.SetGithubReleaseStateParams{State: state, Rounds: 1, UpdatedAt: at, RepoID: 42, Version: version}))
	}
	rel := thread()
	release(rel, "1.0.0", "green", 5)
	release(rel, "1.1.0", "fixing", 9)
	both := thread()
	release(both, "2.0.0", "fixing", 20)
	require.NoError(t, f.st.Q.UpsertThreadIssue(f.ctx, db.UpsertThreadIssueParams{RepoID: 42, Number: 7, ThreadID: both, CreatedAt: 1}))
	plain := thread()

	want := map[string]*apigen.GitHubThreadTask{
		rel:   {Kind: apigen.GitHubThreadTaskKindRelease, Repo: "Acme/Widgets", Version: new("1.1.0"), State: "fixing"},
		both:  {Kind: apigen.GitHubThreadTaskKindIssue, Repo: "Acme/Widgets", Number: new(int64(7)), State: "new"},
		plain: nil,
	}
	for id, w := range want {
		got, err := f.s.Get(f.ctx, id)
		require.NoError(t, err)
		assert.Equal(t, w, got.GithubTask, "Get %s", id)
	}
	list, err := f.s.List(f.ctx, ListFilter{})
	require.NoError(t, err)
	seen := 0
	for _, th := range list {
		if w, ok := want[th.ID]; ok {
			seen++
			assert.Equal(t, w, th.GithubTask, "List %s", th.ID)
		}
	}
	assert.Equal(t, len(want), seen)
}
