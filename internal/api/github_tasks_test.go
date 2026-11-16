package api_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// taskThreads 是 GitHub 服务的线程桩：Create 真的建出线程行（无 orb），与 p.InitTx 同一事务（同 threads.Create）；其余不做事。
type taskThreads struct {
	st      *store.Store
	mu      sync.Mutex
	creates []threads.CreateParams
}

func (t *taskThreads) Create(ctx context.Context, p threads.CreateParams) (apigen.Thread, error) {
	id := ids.New("thr")
	if err := t.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateThread(ctx, db.CreateThreadParams{
			ID: id, ProjectID: &p.ProjectID, Title: p.Title, TitleSource: "user", State: "idle", StateChangedAt: 1,
			SessionDir: "d", CreatedAt: 1, UpdatedAt: 1, Kind: "work",
		}); err != nil {
			return err
		}
		if p.InitTx != nil {
			return p.InitTx(ctx, q, id)
		}
		return nil
	}); err != nil {
		return apigen.Thread{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.creates = append(t.creates, p)
	return apigen.Thread{ID: id}, nil
}

func (t *taskThreads) created() []threads.CreateParams {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]threads.CreateParams(nil), t.creates...)
}

func (t *taskThreads) Deliver(context.Context, string, threads.AutoMessage, func(error)) (func() bool, error) {
	return func() bool { return false }, nil
}
func (t *taskThreads) Publish(context.Context, string)                                 {}
func (t *taskThreads) MarkShipped(context.Context, string, int64) error                { return nil }
func (t *taskThreads) SetModel(context.Context, string, apigen.ModelRef, string) error { return nil }
func (t *taskThreads) Abort(context.Context, string) error                             { return nil }

// newGitHubTaskEnv 建好带线程桩的环境：我的账号为 owner，项目绑定 acme/widgets（仓库 id 42）。返回项目 id。
func newGitHubTaskEnv(t *testing.T) (*h2Env, *githubtest.Server, *taskThreads, string) {
	t.Helper()
	th := &taskThreads{}
	e, fake, gh := newGitHubEnvWith(t, th)
	th.st = e.st
	connectGitHubUser(t, e, fake, gh, "owner")
	fake.AddRepo(githubtest.Repo{ID: 42, FullName: "Acme/Widgets", OwnerID: 900})
	r := e.do("POST", "/api/v1/projects", h2DeviceToken(t, e), map[string]any{"name": "w", "repoUrl": "https://github.com/acme/widgets"})
	require.Equal(t, 201, r.status, string(r.body))
	var p struct{ ID string }
	r.json(t, &p)
	return e, fake, th, p.ID
}

// addTask 直接写一个 issue 任务：线程行（updated_at = threadUpdated）+ thread_issues 行（created_at = at）。
func addTask(t *testing.T, e *h2Env, projectID string, repoID, number, at, threadUpdated int64, state string) string {
	t.Helper()
	ctx := context.Background()
	id := ids.New("thr")
	require.NoError(t, e.st.Q.CreateThread(ctx, db.CreateThreadParams{
		ID: id, ProjectID: &projectID, Title: "t", TitleSource: "user", State: "idle", StateChangedAt: 1,
		SessionDir: "d", CreatedAt: 1, UpdatedAt: threadUpdated, Kind: "work",
	}))
	require.NoError(t, e.st.Q.UpsertThreadIssue(ctx, db.UpsertThreadIssueParams{RepoID: repoID, Number: number, ThreadID: id, CreatedAt: at}))
	_, err := e.st.Q.SetThreadIssueState(ctx, db.SetThreadIssueStateParams{State: state, ThreadID: id})
	require.NoError(t, err)
	return id
}

type apiTask struct {
	Kind           string
	Repo           string
	Number         *int64
	Version        *string
	ThreadID       *string
	State          string
	Classification *string
	Rounds         *int
	UpdatedAt      int64
}

func TestGitHubTasksAPI(t *testing.T) {
	e, _, _, prj := newGitHubTaskEnv(t)
	dev := h2DeviceToken(t, e)
	ctx := context.Background()

	r := e.do("GET", "/api/v1/github/tasks", dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, `{"tasks":[]}`, string(r.body))

	// issue 任务：#7 创建于 100、线程更新于 500；#8 创建于 300、线程更新于 50（取较晚者）
	t7 := addTask(t, e, prj, 42, 7, 100, 500, "fixing")
	_, err := e.st.Q.SetThreadIssueClassification(ctx, db.SetThreadIssueClassificationParams{Classification: new("bug"), ThreadID: t7})
	require.NoError(t, err)
	t8 := addTask(t, e, prj, 42, 8, 300, 50, "needs_info")
	// 发布哨兵：仓库 42 有项目；仓库 43 没有项目，名字退回 hook 表
	require.NoError(t, e.st.Q.UpsertGithubRelease(ctx, db.UpsertGithubReleaseParams{RepoID: 42, Version: "1.2.0", Sha: "s", State: "fixing", UpdatedAt: 400}))
	require.NoError(t, e.st.Q.SetGithubReleaseThread(ctx, db.SetGithubReleaseThreadParams{ThreadID: &t8, UpdatedAt: 400, RepoID: 42, Version: "1.2.0"}))
	require.NoError(t, e.st.Q.SetGithubReleaseState(ctx, db.SetGithubReleaseStateParams{State: "fixing", Rounds: 2, UpdatedAt: 400, RepoID: 42, Version: "1.2.0"}))
	require.NoError(t, e.st.Q.UpsertGithubHook(ctx, db.UpsertGithubHookParams{RepoID: 43, FullName: "acme/lib", Status: "active", UpdatedAt: 1}))
	require.NoError(t, e.st.Q.UpsertGithubRelease(ctx, db.UpsertGithubReleaseParams{RepoID: 43, Version: "0.1.0", Sha: "s", State: "green", UpdatedAt: 200}))

	r = e.do("GET", "/api/v1/github/tasks", dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var list struct{ Tasks []apiTask }
	r.json(t, &list)
	require.Len(t, list.Tasks, 4)
	// 按 updatedAt 倒序：#7(500) > 发布 1.2.0(400) > #8(300) > 发布 0.1.0(200)
	got := []string{}
	for _, tk := range list.Tasks {
		got = append(got, tk.Kind+" "+tk.Repo+" "+tk.State)
	}
	assert.Equal(t, []string{
		"issue acme/widgets fixing", "release acme/widgets fixing", "issue acme/widgets needs_info", "release acme/lib green",
	}, got)
	i7 := list.Tasks[0]
	assert.EqualValues(t, 7, *i7.Number)
	assert.Equal(t, t7, *i7.ThreadID)
	assert.Equal(t, "bug", *i7.Classification)
	assert.EqualValues(t, 500, i7.UpdatedAt)
	assert.Nil(t, i7.Version)
	assert.Nil(t, i7.Rounds)
	rel := list.Tasks[1]
	assert.Equal(t, "1.2.0", *rel.Version)
	assert.Equal(t, 2, *rel.Rounds)
	assert.Equal(t, t8, *rel.ThreadID)
	assert.Nil(t, rel.Number)
	assert.EqualValues(t, 300, list.Tasks[2].UpdatedAt)
	assert.Nil(t, list.Tasks[2].Classification)
	assert.Nil(t, list.Tasks[3].ThreadID)

	assert.Equal(t, 401, e.do("GET", "/api/v1/github/tasks", h2Cred{}, nil).status)
}

func TestGitHubCleanupAPI(t *testing.T) {
	e, _, _, prj := newGitHubTaskEnv(t)
	dev := h2DeviceToken(t, e)
	ctx := context.Background()
	id := addTask(t, e, prj, 42, 7, 100, 100, "fixing")

	// 仓库名不区分大小写
	r := e.do("POST", "/api/v1/github/tasks/cleanup", dev, map[string]any{"repo": "ACME/widgets", "number": 7})
	require.Equal(t, 200, r.status, string(r.body))
	var tk apiTask
	r.json(t, &tk)
	assert.Equal(t, "issue", tk.Kind)
	assert.Equal(t, "acme/widgets", tk.Repo)
	assert.EqualValues(t, 7, *tk.Number)
	assert.Equal(t, id, *tk.ThreadID)
	assert.Equal(t, "abandoned", tk.State)
	row, err := e.st.Q.GetThreadIssueByThread(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "abandoned", row.State)

	r = e.do("POST", "/api/v1/github/tasks/cleanup", dev, map[string]any{"repo": "acme/widgets", "number": 8})
	assert.Equal(t, 404, r.status)
	assert.Equal(t, "not_found", r.code(t))
	r = e.do("POST", "/api/v1/github/tasks/cleanup", dev, map[string]any{"repo": "acme/other", "number": 7})
	assert.Equal(t, 404, r.status)
	assert.Equal(t, "not_found", r.code(t))
	r = e.do("POST", "/api/v1/github/tasks/cleanup", dev, map[string]any{"repo": "nope", "number": 7})
	assert.Equal(t, 400, r.status)
	assert.Equal(t, 401, e.do("POST", "/api/v1/github/tasks/cleanup", h2Cred{}, map[string]any{"repo": "acme/widgets", "number": 7}).status)
}

func TestGitHubTriageAPI(t *testing.T) {
	e, fake, th, prj := newGitHubTaskEnv(t)
	dev := h2DeviceToken(t, e)
	ctx := context.Background()
	fake.AddIssue(githubtest.Issue{RepoID: 42, Number: 7, Title: "Crash on start", Body: "boom", State: "open", Author: "alice"})
	fake.AddIssue(githubtest.Issue{RepoID: 42, Number: 9, Title: "Other", Body: "x", State: "open", Author: "bob"})
	triage := func(body map[string]any) h2Resp {
		t.Helper()
		return e.do("POST", "/api/v1/github/triage", dev, body)
	}

	// 未绑定的仓库 → github_not_bound
	r := triage(map[string]any{"repo": "acme/none", "number": 7})
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "github_not_bound", r.code(t))
	// 绑定了但没有启用的 issue_opened 规则 → conflict
	r = triage(map[string]any{"repo": "acme/widgets", "number": 7})
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "conflict", r.code(t))

	base := "/api/v1/projects/" + prj + "/github/triggers"
	r = e.do("POST", base, dev, map[string]any{"name": "Triage", "event": "issue_opened", "prompt": "Triage {{repo}}#{{number}}", "announce": false})
	require.Equal(t, 201, r.status, string(r.body))
	var rule struct{ ID string }
	r.json(t, &rule)

	// 线上没有的 issue → not_found
	r = triage(map[string]any{"repo": "acme/widgets", "number": 99})
	assert.Equal(t, 404, r.status)
	assert.Equal(t, "not_found", r.code(t))
	require.Empty(t, th.created())

	// 建线程：任务行与线程同事务写入，带规则
	r = triage(map[string]any{"repo": "Acme/Widgets", "number": 7})
	require.Equal(t, 200, r.status, string(r.body))
	var res struct{ ThreadID string }
	r.json(t, &res)
	require.NotEmpty(t, res.ThreadID)
	cr := th.created()
	require.Len(t, cr, 1)
	assert.Equal(t, prj, cr[0].ProjectID)
	assert.Equal(t, "#7 Crash on start", cr[0].Title)
	assert.True(t, strings.Contains(cr[0].Prompt, "Triage acme/widgets#7"), cr[0].Prompt)
	assert.Contains(t, cr[0].Prompt, `"author":"alice"`)
	assert.Contains(t, cr[0].Prompt, `"sender":"owner"`)
	assert.NotNil(t, cr[0].InitTx)
	ti, err := e.st.Q.GetThreadIssue(ctx, db.GetThreadIssueParams{RepoID: 42, Number: 7})
	require.NoError(t, err)
	assert.Equal(t, res.ThreadID, ti.ThreadID)
	assert.Equal(t, "new", ti.State)
	require.NotNil(t, ti.RuleID)
	assert.Equal(t, rule.ID, *ti.RuleID)
	assert.False(t, ti.ReviewOnly)

	// 已关联：回同一线程，不再建
	r = triage(map[string]any{"repo": "acme/widgets", "number": 7})
	require.Equal(t, 200, r.status, string(r.body))
	var again struct{ ThreadID string }
	r.json(t, &again)
	assert.Equal(t, res.ThreadID, again.ThreadID)
	assert.Len(t, th.created(), 1)

	// 指定规则：「任何人」档要求 Agent 账号，未连接 → github_agent_required，不建线程
	r = e.do("POST", base, dev, map[string]any{"name": "Anyone", "event": "issue_opened", "drivers": "anyone", "prompt": "p", "announce": false})
	require.Equal(t, 201, r.status, string(r.body))
	var anyone struct{ ID string }
	r.json(t, &anyone)
	r = triage(map[string]any{"repo": "acme/widgets", "number": 9, "ruleId": anyone.ID})
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "github_agent_required", r.code(t))
	assert.Len(t, th.created(), 1)
	_, err = e.st.Q.GetThreadIssue(ctx, db.GetThreadIssueParams{RepoID: 42, Number: 9})
	assert.Error(t, err, "没有建任务")

	// 规则不存在 → not_found；发布哨兵规则 → invalid_request
	r = triage(map[string]any{"repo": "acme/widgets", "number": 9, "ruleId": "ght_" + strings.Repeat("0", 26)})
	assert.Equal(t, 404, r.status)
	assert.Equal(t, "not_found", r.code(t))
	r = e.do("POST", base, dev, map[string]any{"name": "Release", "event": "workflow_run_completed", "prompt": "p"})
	require.Equal(t, 201, r.status, string(r.body))
	var rel struct{ ID string }
	r.json(t, &rel)
	r = triage(map[string]any{"repo": "acme/widgets", "number": 9, "ruleId": rel.ID})
	assert.Equal(t, 400, r.status)
	assert.Equal(t, "invalid_request", r.code(t))

	// 指定停用的规则也可分诊
	r = e.do("PATCH", "/api/v1/github/triggers/"+rule.ID, dev, map[string]any{"enabled": false})
	require.Equal(t, 200, r.status, string(r.body))
	r = triage(map[string]any{"repo": "acme/widgets", "number": 9, "ruleId": rule.ID})
	require.Equal(t, 200, r.status, string(r.body))
	assert.Len(t, th.created(), 2)

	r = triage(map[string]any{"repo": "acme/widgets", "number": 0})
	assert.Equal(t, 400, r.status)
	assert.Equal(t, 401, e.do("POST", "/api/v1/github/triage", h2Cred{}, map[string]any{"repo": "acme/widgets", "number": 7}).status)
}
