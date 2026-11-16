package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// releaseEnv 是发布哨兵用例的环境：Acme/Widgets 上一条默认前缀的 workflow_run_completed 规则，线程服务真的建线程行。
type releaseEnv struct {
	*triggerEnv
	trig apigen.GitHubTrigger
}

func newReleaseEnv(t *testing.T) *releaseEnv {
	e := newTriggerEnv(t)
	tr := e.trigger(apigen.GitHubTriggerInput{
		Name: "release", Event: apigen.WorkflowRunCompleted, Prompt: "Repair {{repo}} v{{version}}.", Announce: new(false),
	})
	return &releaseEnv{triggerEnv: e, trig: tr}
}

func relSHA(n int) string { return fmt.Sprintf("%040x", n) }

// setRef 改假 GitHub 上 Acme/Widgets 的一个 ref（值为空时删除）。
func (e *releaseEnv) setRef(ref, sha string) {
	e.t.Helper()
	e.fake.Lock()
	defer e.fake.Unlock()
	rp := e.fake.Repos[0]
	if rp.Refs == nil {
		rp.Refs = map[string]string{}
	}
	if sha == "" {
		delete(rp.Refs, ref)
		return
	}
	rp.Refs[ref] = sha
}

// publish 让默认分支与 tag v<ver> 都指向 sha（发布提交或修复推送之后的状态）。
func (e *releaseEnv) publish(ver, sha string) {
	e.setRef("heads/main", sha)
	e.setRef("tags/v"+ver, sha)
}

// addRun 在 sha 上登记一次 run 与它的 job。
func (e *releaseEnv) addRun(sha, msg, status, conclusion string, jobs ...githubtest.WorkflowJob) *githubtest.WorkflowRun {
	run := e.fake.AddRun(githubtest.WorkflowRun{
		RepoID: 42, Name: "CI", Status: status, Conclusion: conclusion, HeadSHA: sha, HeadBranch: "main", Message: msg, Actor: "owner",
	})
	for _, j := range jobs {
		j.RunID, j.RepoID = run.ID, 42
		e.fake.AddJob(j)
	}
	return run
}

// runDone 把 run 置为已完成。
func (e *releaseEnv) runDone(run *githubtest.WorkflowRun, conclusion string) {
	e.fake.Lock()
	run.Status, run.Conclusion = "completed", conclusion
	e.fake.Unlock()
}

func runEvent(run *githubtest.WorkflowRun) map[string]any {
	var concl any
	if run.Conclusion != "" {
		concl = run.Conclusion
	}
	return map[string]any{
		"action": "completed", "repository": repoJSON(), "sender": who("owner"),
		"workflow_run": map[string]any{
			"id": run.ID, "name": run.Name, "event": run.Event, "status": run.Status, "conclusion": concl, "head_sha": run.HeadSHA,
			"head_branch": run.HeadBranch, "head_commit": map[string]any{"id": run.HeadSHA, "message": run.Message},
			"html_url": fmt.Sprintf("https://github.com/acme/widgets/actions/runs/%d", run.ID),
		},
	}
}

// fire 投递 run 的 workflow_run 事件，返回结果。
func (e *releaseEnv) fire(run *githubtest.WorkflowRun) string {
	e.t.Helper()
	return e.delivery(e.deliver("workflow_run", runEvent(run))).Outcome
}

func (e *releaseEnv) release(ver string) db.GithubRelease {
	e.t.Helper()
	r, err := e.st.Q.GetGithubRelease(e.ctx, db.GetGithubReleaseParams{RepoID: 42, Version: ver})
	require.NoError(e.t, err)
	return r
}

func (e *releaseEnv) posts() []string {
	e.n.mu.Lock()
	defer e.n.mu.Unlock()
	return append([]string(nil), e.n.posts...)
}

func failingJob(name, log string) githubtest.WorkflowJob {
	return githubtest.WorkflowJob{
		Name: name, Conclusion: "failure", Log: log,
		Steps: []githubtest.JobStep{{Number: 1, Name: "Set up job", Conclusion: "success"}, {Number: 2, Name: "Run " + name, Conclusion: "failure"}},
	}
}

const bump = "chore: bump version to 1.2.3"

func TestReleaseVersionParse(t *testing.T) {
	p := presetReleasePrefix
	for msg, want := range map[string]string{
		"chore: bump version to 1.2.3":                         "1.2.3",
		"chore: bump version to v1.2.3\n\nbody":                "1.2.3",
		"chore: bump version to 1.2.3: fix flaky test in auth": "1.2.3",
		"chore: bump version to 2.0.0-rc.1 fix build":          "2.0.0-rc.1",
		"chore: bump version to 1.2.3.":                        "1.2.3",
		"chore: bump version to next":                          "",
		"chore: bump version to ":                              "",
		"fix: chore: bump version to 1.2.3":                    "",
		"Chore: bump version to 1.2.3":                         "",
	} {
		got, ok := releaseVersion(msg, p)
		assert.Equal(t, want != "", ok, msg)
		assert.Equal(t, want, got, msg)
	}
	_, ok := releaseVersion("1.2.3", "")
	assert.False(t, ok, "空前缀不匹配")
}

func TestReleaseCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0}, {"1.10.0", "1.9.9", 1}, {"1.2", "1.2.1", -1}, {"2.0.0-rc.1", "2.0.0", -1},
		{"2.0.0-rc.2", "2.0.0-rc.10", -1}, {"2.0.0-alpha", "2.0.0-1", 1}, {"1.2.3+build.5", "1.2.3", 0},
	} {
		assert.Equal(t, c.want, compareVersions(c.a, c.b), "%s vs %s", c.a, c.b)
		assert.Equal(t, -c.want, compareVersions(c.b, c.a), "%s vs %s", c.b, c.a)
	}
}

// 日志只留末尾若干行，去掉时间戳；超过读取窗口时丢掉窗口开头的半行。
func TestReleaseTailLines(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&b, "2026-11-16T01:02:03.1234567Z line %d\r\n", i)
	}
	got, err := tailLines(strings.NewReader(b.String()), 120)
	require.NoError(t, err)
	lines := strings.Split(got, "\n")
	require.Len(t, lines, 120)
	assert.Equal(t, "line 181", lines[0])
	assert.Equal(t, "line 300", lines[119])

	big := strings.Repeat(strings.Repeat("x", 1000)+"\n", 2000) + "tail\n"
	got, err = tailLines(strings.NewReader(big), 3)
	require.NoError(t, err)
	lines = strings.Split(got, "\n")
	require.Len(t, lines, 3)
	assert.Equal(t, "tail", lines[2])
	assert.Equal(t, strings.Repeat("x", releaseLineMax)+"…", lines[0], "长行截断")
}

// tag 不指向 run 的提交（或不存在）时记 stale，不登记发布、不建线程；不带前缀的提交不是发布。
func TestReleaseStale(t *testing.T) {
	e := newReleaseEnv(t)
	e.setRef("heads/main", relSHA(1))
	run := e.addRun(relSHA(1), bump, "completed", "failure", failingJob("test", "boom"))
	assert.Equal(t, "ignored:stale", e.fire(run), "tag 不存在")
	e.setRef("tags/v1.2.3", relSHA(2))
	assert.Equal(t, "ignored:stale", e.fire(run), "tag 指向别的提交")
	_, err := e.st.Q.GetGithubRelease(e.ctx, db.GetGithubReleaseParams{RepoID: 42, Version: "1.2.3"})
	assert.True(t, isNoRows(err))
	assert.Empty(t, e.ct.created())

	other := e.addRun(relSHA(1), "feat: unrelated", "completed", "failure")
	assert.Equal(t, "ignored:not_release", e.fire(other))
}

// 阻塞结论开启线程：线程「启动」那一刻发布行已带 thread_id 且处于进行中（fixing），出站代理的 ReleaseThread 现查即为真；
// 首条提示词带固定首行与失败报告（失败步骤、日志末尾 120 行）。同一提交其余 run 的阻塞结论不另开一轮；非阻塞的 job 不进报告。
func TestReleaseBlockingStartsThread(t *testing.T) {
	e := newReleaseEnv(t)
	sha, oid := relSHA(1), relSHA(0xa1)
	e.setRef("heads/main", sha)
	e.setRef("tags/v1.2.3", oid)
	e.fake.AddTagObject(oid, sha) // 附注 tag：lease 用 tag 对象 oid
	var log strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&log, "2026-11-16T01:02:03.0000000Z step output %d\n", i)
	}
	run := e.addRun(sha, bump, "completed", "failure", failingJob("test", log.String()),
		githubtest.WorkflowJob{Name: "lint", Conclusion: "success"}, githubtest.WorkflowJob{Name: "docs", Conclusion: "cancelled"})

	var seen []db.GithubRelease
	var gate []bool
	e.ct.onStart = func(threadID string) {
		r, err := e.st.Q.GetGithubReleaseByThread(e.ctx, &threadID)
		require.NoError(t, err)
		seen = append(seen, r)
		orb, err := e.st.Q.GetThread(e.ctx, threadID)
		require.NoError(t, err)
		gate = append(gate, e.s.ReleaseThread(e.ctx, *orb.OrbID))
	}
	require.Equal(t, "handled", e.fire(run))
	require.Len(t, seen, 1)
	thr := e.ct.last()
	require.NotNil(t, seen[0].ThreadID)
	assert.Equal(t, thr, *seen[0].ThreadID)
	assert.Equal(t, releaseFixing, seen[0].State)
	assert.True(t, releaseActiveStates[seen[0].State])
	assert.EqualValues(t, 1, seen[0].Rounds)
	assert.Equal(t, []bool{true}, gate, "线程启动时出站代理已把它当作发布线程")

	created := e.ct.created()
	require.Len(t, created, 1)
	p := created[0]
	assert.True(t, p.Automation, "身份按 ResolveAutomationSlot(untrusted=false)")
	assert.False(t, p.Untrusted)
	assert.Equal(t, e.prj, p.ProjectID)
	assert.Equal(t, "Release v1.2.3", p.Title)
	head, rest, _ := strings.Cut(p.Prompt, "\n")
	assert.Equal(t, "[Oryn release sentinel on acme/widgets v1.2.3 — started automatically, not typed by the owner]", head)
	assert.True(t, strings.HasPrefix(rest, "Repair acme/widgets v1.2.3.\n"), rest)
	assert.Contains(t, p.Prompt, "round 1 of 5")
	assert.Contains(t, p.Prompt, `"tagOid":"`+oid+`"`)
	assert.Contains(t, p.Prompt, `"defaultBranch":"main"`)
	assert.Contains(t, p.Prompt, "Run test")
	assert.Contains(t, p.Prompt, "step output 200")
	assert.Contains(t, p.Prompt, "step output 81")
	assert.NotContains(t, p.Prompt, "step output 80\\n")
	assert.NotContains(t, p.Prompt, "2026-11-16T01", "时间戳已去掉")
	assert.NotContains(t, p.Prompt, `"name":"lint"`, "成功的 job 不进报告")
	assert.NotContains(t, p.Prompt, `"name":"docs"`, "cancelled 不阻塞")
	assert.Contains(t, p.Prompt, "<untrusted-github-content>")

	r := e.release("1.2.3")
	assert.Equal(t, oid, *r.TagOid)
	assert.Equal(t, sha, r.Sha)
	got, err := e.st.Q.GetGithubTrigger(e.ctx, e.trig.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, got.RunCount)

	// 同一提交的另一个 run 也失败：已在修复，不另开一轮
	run2 := e.addRun(sha, bump, "completed", "timed_out", failingJob("e2e", "slow"))
	assert.Equal(t, "ignored:fixing", e.fire(run2))
	assert.Len(t, e.ct.created(), 1)
	assert.Empty(t, e.delivered())
	assert.EqualValues(t, 1, e.release("1.2.3").Rounds)
}

// 非阻塞：run 没全部完成时继续等；全部完成但 Release 还是草稿时不置 green；有非草稿 Release 后 green，且不建线程、不发言。
func TestReleaseGreenNeedsPublishedRelease(t *testing.T) {
	e := newReleaseEnv(t)
	sha := relSHA(1)
	e.publish("1.2.3", sha)
	ci := e.addRun(sha, bump, "completed", "success")
	pub := e.addRun(sha, bump, "in_progress", "")
	skipped := e.addRun(sha, bump, "completed", "skipped")
	require.Equal(t, "handled", e.fire(ci))
	assert.Equal(t, releaseAwaitingCI, e.release("1.2.3").State)
	require.Equal(t, "handled", e.fire(skipped))
	assert.Equal(t, releaseAwaitingCI, e.release("1.2.3").State, "还有 run 没完成")

	e.runDone(pub, "neutral")
	rel := e.fake.AddRelease(githubtest.Release{RepoID: 42, TagName: "v1.2.3", Draft: true})
	assert.Equal(t, "handled", e.fire(pub))
	assert.Equal(t, releaseAwaitingCI, e.release("1.2.3").State, "草稿 Release 不算")

	e.fake.Lock()
	rel.Draft = false
	e.fake.Unlock()
	require.Equal(t, "handled", e.fire(pub))
	assert.Equal(t, releaseGreen, e.release("1.2.3").State)
	assert.Empty(t, e.ct.created())
	assert.Empty(t, e.fake.CallsTo("POST", "/repos/acme/widgets/*"), "终态不在 GitHub 上发言")
	assert.Equal(t, "ignored:release_green", e.fire(ci), "终态之后不再处理")
}

// 轮数上限：每次修复推送（tag 与默认分支移到新提交）后再失败续跑同一线程、轮数 +1；第 5 轮之后仍失败置 failed 并通知。
// 旧一轮提交的晚到结论是 stale。
func TestReleaseRoundLimit(t *testing.T) {
	e := newReleaseEnv(t)
	e.publish("1.2.3", relSHA(1))
	require.Equal(t, "handled", e.fire(e.addRun(relSHA(1), bump, "completed", "failure", failingJob("test", "boom 1"))))
	thr := e.ct.last()
	for round := 2; round <= maxReleaseRounds; round++ {
		sha := relSHA(round)
		e.publish("1.2.3", sha)
		ok := e.addRun(sha, bump+": fix attempt", "completed", "success")
		require.Equal(t, "handled", e.fire(ok))
		assert.Equal(t, releaseAwaitingCI, e.release("1.2.3").State, "修复提交先等 CI")
		require.Equal(t, "handled", e.fire(e.addRun(sha, bump+": fix attempt", "completed", "failure", failingJob("test", fmt.Sprintf("boom %d", round)))))
		r := e.release("1.2.3")
		assert.Equal(t, releaseFixing, r.State)
		assert.EqualValues(t, round, r.Rounds)
		assert.Equal(t, sha, r.Sha)
	}
	d := e.delivered()
	require.Len(t, d, maxReleaseRounds-1, "续跑同一线程")
	for i, msg := range d {
		assert.True(t, strings.HasPrefix(msg, thr+"\n[Oryn release sentinel on acme/widgets v1.2.3"), msg)
		assert.Contains(t, msg, fmt.Sprintf("round %d of 5", i+2))
		assert.Contains(t, msg, fmt.Sprintf("boom %d", i+2))
	}
	assert.Len(t, e.ct.created(), 1)

	// 旧提交的晚到结论
	assert.Equal(t, "ignored:stale", e.fire(e.addRun(relSHA(1), bump, "completed", "failure")))

	sha := relSHA(9)
	e.publish("1.2.3", sha)
	require.Equal(t, "handled", e.fire(e.addRun(sha, bump+": fix", "completed", "failure", failingJob("test", "boom 6"))))
	r := e.release("1.2.3")
	assert.Equal(t, releaseFailed, r.State)
	assert.EqualValues(t, maxReleaseRounds, r.Rounds)
	assert.Len(t, e.delivered(), maxReleaseRounds-1, "失败后不再投递")
	posts := e.posts()
	require.Len(t, posts, 1)
	assert.True(t, strings.HasPrefix(posts[0], thr+"\nerror\nRelease acme/widgets v1.2.3 is failed: CI is still blocked after 5 repair rounds"), posts[0])
	assert.Empty(t, e.fake.CallsTo("POST", "/repos/acme/widgets/*"), "终态不在 GitHub 上发言")
}

// 默认分支被别人推进（不再指向发布提交）时遇到阻塞结论置 failed，不开线程。
func TestReleaseDefaultBranchAdvanced(t *testing.T) {
	e := newReleaseEnv(t)
	e.publish("1.2.3", relSHA(1))
	e.setRef("heads/main", relSHA(7))
	require.Equal(t, "handled", e.fire(e.addRun(relSHA(1), bump, "completed", "startup_failure")))
	assert.Equal(t, releaseFailed, e.release("1.2.3").State)
	assert.Empty(t, e.ct.created())
	assert.Empty(t, e.posts(), "没有线程时不通知")
}

// 同仓库出现新版本：旧版本置 superseded（中止它的线程并通知）；之后晚到的旧版本事件一出现就是 superseded。
func TestReleaseSuperseded(t *testing.T) {
	e := newReleaseEnv(t)
	e.publish("1.2.3", relSHA(1))
	require.Equal(t, "handled", e.fire(e.addRun(relSHA(1), bump, "completed", "failure", failingJob("test", "boom"))))
	old := e.ct.last()

	e.publish("1.3.0", relSHA(2))
	require.Equal(t, "handled", e.fire(e.addRun(relSHA(2), "chore: bump version to 1.3.0", "completed", "success")))
	assert.Equal(t, releaseSuperseded, e.release("1.2.3").State)
	assert.Equal(t, releaseAwaitingCI, e.release("1.3.0").State)
	e.th.mu.Lock()
	aborted := append([]string(nil), e.th.aborted...)
	e.th.mu.Unlock()
	assert.Equal(t, []string{old}, aborted)
	posts := e.posts()
	require.Len(t, posts, 1)
	assert.Contains(t, posts[0], "superseded by v1.3.0")
	assert.False(t, e.s.ReleaseThread(e.ctx, threadOrb(t, e, old)), "被取代的线程不再享有默认分支例外")

	e.publish("1.2.2", relSHA(3))
	assert.Equal(t, "ignored:superseded", e.fire(e.addRun(relSHA(3), "chore: bump version to 1.2.2", "completed", "failure")))
	assert.Equal(t, releaseSuperseded, e.release("1.2.2").State)
	assert.Equal(t, "ignored:release_superseded", e.fire(e.addRun(relSHA(1), bump, "completed", "failure")))
	assert.Len(t, e.ct.created(), 1)
}

func threadOrb(t *testing.T, e *releaseEnv, threadID string) string {
	t.Helper()
	th, err := e.st.Q.GetThread(e.ctx, threadID)
	require.NoError(t, err)
	require.NotNil(t, th.OrbID)
	return *th.OrbID
}

// 线程已归档：续跑投递失败，该版本置 failed。
func TestReleaseThreadGone(t *testing.T) {
	e := newReleaseEnv(t)
	e.publish("1.2.3", relSHA(1))
	require.Equal(t, "handled", e.fire(e.addRun(relSHA(1), bump, "completed", "failure")))
	ft := &failingThreads{stubThreads: e.th, err: &threads.Error{Code: "archived", Message: "thread is archived"}}
	e.s.th = ft
	e.publish("1.2.3", relSHA(2))
	assert.Equal(t, "ignored:thread_gone", e.fire(e.addRun(relSHA(2), bump+" fix", "completed", "failure")))
	assert.Equal(t, releaseFailed, e.release("1.2.3").State)
}

// github_release_status：发布线程得到版本、轮数、tagOid、run 与阻塞 job（至多 5 个附日志，其余 overflow）；
// stale 与默认分支被推进现查；brief 只回控制面里的状态；不是发布线程回 not_release_thread。
func TestReleaseStatusTool(t *testing.T) {
	e := newReleaseEnv(t)
	sha := relSHA(1)
	e.publish("1.2.3", sha)
	jobs := make([]githubtest.WorkflowJob, 7)
	for i := range jobs {
		jobs[i] = failingJob(fmt.Sprintf("job%d", i), fmt.Sprintf("log of job%d", i))
	}
	require.Equal(t, "handled", e.fire(e.addRun(sha, bump, "completed", "failure", jobs...)))
	thr := e.ct.last()
	orb := threadOrb(t, e, thr)
	call := func(params map[string]any) (apigen.LinkCtlGithubCallResult, error) {
		return e.s.Call(e.ctx, orb, thr, apigen.LinkCtlGithubCallParams{ThreadID: thr, Tool: "github_release_status", Params: params})
	}

	res, err := call(map[string]any{})
	require.NoError(t, err)
	d := res.Details
	assert.Equal(t, releaseFixing, d["state"])
	assert.Equal(t, "1.2.3", d["version"])
	assert.Equal(t, sha, d["sha"])
	assert.Equal(t, sha, d["tagOid"], "轻量 tag 的 oid 即提交")
	assert.EqualValues(t, 1, d["round"])
	assert.EqualValues(t, maxReleaseRounds, d["maxRounds"])
	assert.Equal(t, false, d["stale"])
	assert.Equal(t, false, d["defaultBranchAdvanced"])
	assert.Equal(t, "main", d["defaultBranch"])
	blocking := d["blockingJobs"].([]releaseJobView)
	require.Len(t, blocking, releaseMaxJobs)
	assert.Equal(t, "log of job0", blocking[0].LogTail)
	assert.Equal(t, []string{"Run job0"}, blocking[0].FailedSteps)
	overflow := d["overflow"].([]releaseJobView)
	require.Len(t, overflow, 2)
	assert.Empty(t, overflow[0].LogTail)
	assert.Contains(t, res.Text, "tagOid "+sha)
	assert.Contains(t, res.Text, "log of job4")
	assert.NotContains(t, res.Text, "log of job5")
	assert.Contains(t, res.Text, "job6")
	assert.Nil(t, d["errors"])

	// tag 被移走、默认分支被推进：现查得出
	e.setRef("tags/v1.2.3", relSHA(5))
	e.setRef("heads/main", relSHA(6))
	res, err = call(map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, true, res.Details["stale"])
	assert.Equal(t, true, res.Details["defaultBranchAdvanced"])
	assert.Contains(t, res.Text, "STALE")

	// brief 不读 GitHub
	e.fake.ResetCalls()
	res, err = call(map[string]any{"brief": true})
	require.NoError(t, err)
	assert.Equal(t, releaseFixing, res.Details["state"])
	e.fake.Lock()
	assert.Empty(t, e.fake.Calls)
	e.fake.Unlock()

	_, err = call(map[string]any{"nope": 1})
	assert.Equal(t, codeInvalidParams, codeOf(err))
	other := e.workThread(e.prj)
	_, err = e.s.Call(e.ctx, orb, other, apigen.LinkCtlGithubCallParams{ThreadID: other, Tool: "github_release_status", Params: map[string]any{}})
	assert.Equal(t, codeNotRelease, codeOf(err))
}

// 发布线程上的 github_abort_task：版本置 failed、记审计、通知所有者，不在 GitHub 上发言；终态之后再调回 task_finished。
func TestReleaseAbortTask(t *testing.T) {
	e := newReleaseEnv(t)
	e.publish("1.2.3", relSHA(1))
	require.Equal(t, "handled", e.fire(e.addRun(relSHA(1), bump, "completed", "failure")))
	thr := e.ct.last()
	orb := threadOrb(t, e, thr)
	abort := func() error {
		_, err := e.s.Call(e.ctx, orb, thr, apigen.LinkCtlGithubCallParams{
			ThreadID: thr, Tool: "github_abort_task", Params: map[string]any{"reason": "runner secrets are missing"},
		})
		return err
	}
	require.NoError(t, abort())
	assert.Equal(t, releaseFailed, e.release("1.2.3").State)
	assert.False(t, e.s.ReleaseThread(e.ctx, orb))
	rows, err := e.st.Q.ListGithubAudit(e.ctx, db.ListGithubAuditParams{ThreadID: &thr, Before: 1 << 62, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Contains(t, rows[0].Op, "runner secrets are missing")
	posts := e.posts()
	require.Len(t, posts, 1)
	assert.Contains(t, posts[0], "aborted by the release thread")
	assert.Equal(t, "task_finished", codeOf(abort()))
	assert.Empty(t, e.fake.CallsTo("POST", "/repos/acme/widgets/*"))
}

// awaitingWithThread 走到「有发布线程、修复提交 CI 全部通过、Release 仍是草稿」：版本停在 awaiting_ci，线程仍享有例外。
func (e *releaseEnv) awaitingWithThread() (thr, orb string, rel *githubtest.Release) {
	e.t.Helper()
	e.publish("1.2.3", relSHA(1))
	require.Equal(e.t, "handled", e.fire(e.addRun(relSHA(1), bump, "completed", "failure", failingJob("test", "boom"))))
	thr = e.ct.last()
	orb = threadOrb(e.t, e, thr)
	rel = e.fake.AddRelease(githubtest.Release{RepoID: 42, TagName: "v1.2.3", Draft: true})
	e.publish("1.2.3", relSHA(2))
	require.Equal(e.t, "handled", e.fire(e.addRun(relSHA(2), bump+": fix", "completed", "success")))
	require.Equal(e.t, releaseAwaitingCI, e.release("1.2.3").State)
	require.True(e.t, e.s.ReleaseThread(e.ctx, orb))
	return thr, orb, rel
}

func (e *releaseEnv) systemPosts() []string { return e.n.systemPosts() }

// scan 一轮：Release 仍是草稿时只读（只发 GET、发布行不变）；Release 被手动发布后 scan 置 green，线程失去发布例外。
func TestReleaseScanPublishedGoesGreen(t *testing.T) {
	e := newReleaseEnv(t)
	thr, orb, rel := e.awaitingWithThread()
	before := e.release("1.2.3")
	e.clock.Add(time.Hour)
	e.fake.ResetCalls()
	e.s.scan(e.ctx)
	assert.Equal(t, before, e.release("1.2.3"), "未到期且没有结论：发布行不变")
	e.fake.Lock()
	calls := append([]githubtest.Call(nil), e.fake.Calls...)
	e.fake.Unlock()
	require.NotEmpty(t, calls)
	for _, c := range calls {
		assert.Equal(t, "GET", c.Method, c.Path)
	}
	assert.True(t, e.s.ReleaseThread(e.ctx, orb))
	assert.Empty(t, e.posts())

	e.fake.Lock()
	rel.Draft = false
	e.fake.Unlock()
	e.s.scan(e.ctx)
	assert.Equal(t, releaseGreen, e.release("1.2.3").State)
	assert.False(t, e.s.ReleaseThread(e.ctx, orb), "green 之后不再享有默认分支例外")
	posts := e.posts()
	require.Len(t, posts, 1)
	assert.True(t, strings.HasPrefix(posts[0], thr+"\ndone\n"), posts[0])
	assert.Empty(t, e.fake.CallsTo("POST", "/repos/acme/widgets/*"), "终态不在 GitHub 上发言")
}

// 一直是草稿：同一提交上不断到来的 run 结论不续长等待期；越过 releaseAwaitMax 后 scan 置 failed、通知所有者，例外消失。
func TestReleaseScanAwaitExpires(t *testing.T) {
	e := newReleaseEnv(t)
	thr, orb, _ := e.awaitingWithThread()
	e.clock.Add(releaseAwaitMax - time.Hour)
	require.Equal(t, "handled", e.fire(e.addRun(relSHA(2), bump+": fix", "completed", "skipped")))
	e.s.scan(e.ctx)
	assert.Equal(t, releaseAwaitingCI, e.release("1.2.3").State)
	assert.True(t, e.s.ReleaseThread(e.ctx, orb))

	e.clock.Add(2 * time.Hour)
	e.s.scan(e.ctx)
	assert.Equal(t, releaseFailed, e.release("1.2.3").State)
	assert.False(t, e.s.ReleaseThread(e.ctx, orb), "failed 之后不再享有默认分支例外")
	posts := e.posts()
	require.Len(t, posts, 1)
	assert.True(t, strings.HasPrefix(posts[0], thr+"\nerror\n"), posts[0])
	assert.Contains(t, posts[0], "no published (non-draft) GitHub Release")
	e.s.scan(e.ctx)
	assert.Len(t, e.posts(), 1, "终态之后不再处理")

	// 没有线程的版本（从未阻塞）到期：发一条无线程通知
	e.publish("1.3.0", relSHA(3))
	require.Equal(t, "handled", e.fire(e.addRun(relSHA(3), "chore: bump version to 1.3.0", "completed", "success")))
	e.clock.Add(releaseAwaitMax + time.Minute)
	e.s.scan(e.ctx)
	assert.Equal(t, releaseFailed, e.release("1.3.0").State)
	sys := e.systemPosts()
	require.Len(t, sys, 1)
	assert.Contains(t, sys[0], "Release acme/widgets v1.3.0 is failed")
}

// 阻塞 job 在 job 列表第二页（前 100 个成功）：报告里有它的失败步骤与日志末尾，没有截断标记。
func TestReleaseJobsPaged(t *testing.T) {
	e := newReleaseEnv(t)
	e.publish("1.2.3", relSHA(1))
	jobs := make([]githubtest.WorkflowJob, 0, 101)
	for i := range 100 {
		jobs = append(jobs, githubtest.WorkflowJob{Name: fmt.Sprintf("ok%d", i), Conclusion: "success"})
	}
	jobs = append(jobs, failingJob("matrix-101", "tail of job 101"))
	require.Equal(t, "handled", e.fire(e.addRun(relSHA(1), bump, "completed", "failure", jobs...)))
	p := e.ct.created()[0].Prompt
	assert.Contains(t, p, `"name":"matrix-101"`)
	assert.Contains(t, p, "Run matrix-101")
	assert.Contains(t, p, "tail of job 101")
	assert.NotContains(t, p, "truncated")
}

// job 列表超过 releaseJobPages 页：报告标明截断（prompt 与 github_release_status 都能看到），run 本身仍列为阻塞。
func TestReleaseJobsTruncated(t *testing.T) {
	e := newReleaseEnv(t)
	e.publish("1.2.3", relSHA(1))
	jobs := make([]githubtest.WorkflowJob, 0, releaseJobPages*100+1)
	for i := range releaseJobPages * 100 {
		jobs = append(jobs, githubtest.WorkflowJob{Name: fmt.Sprintf("ok%d", i), Conclusion: "success"})
	}
	jobs = append(jobs, failingJob("hidden", "never read"))
	run := e.addRun(relSHA(1), bump, "completed", "failure", jobs...)
	require.Equal(t, "handled", e.fire(run))
	thr := e.ct.last()
	p := e.ct.created()[0].Prompt
	assert.Contains(t, p, `"truncated":[`)
	assert.Contains(t, p, fmt.Sprintf("the job list of run %d (CI) stopped after %d pages", run.ID, releaseJobPages))
	assert.NotContains(t, p, "never read")
	assert.Contains(t, p, fmt.Sprintf(`"runId":%d`, run.ID), "run 级阻塞项仍在")

	res, err := e.s.Call(e.ctx, threadOrb(t, e, thr), thr, apigen.LinkCtlGithubCallParams{ThreadID: thr, Tool: "github_release_status", Params: map[string]any{}})
	require.NoError(t, err)
	assert.Contains(t, res.Text, "TRUNCATED: the job list of run")
	assert.NotEmpty(t, res.Details["truncated"])
}

// run 列表超过 releaseRunPages 页、失败的 run 在最后一页：事件与 scan 都不判 green，保持 awaiting_ci。
func TestReleaseRunsTruncatedNotGreen(t *testing.T) {
	e := newReleaseEnv(t)
	sha := relSHA(1)
	e.publish("1.2.3", sha)
	e.fake.AddRelease(githubtest.Release{RepoID: 42, TagName: "v1.2.3"})
	e.fake.AddRun(githubtest.WorkflowRun{
		RepoID: 42, Name: "old", Conclusion: "failure", HeadSHA: sha, Message: bump, UpdatedAt: time.Now().Add(-time.Hour).UTC().Truncate(time.Second),
	})
	var last *githubtest.WorkflowRun
	for range releaseRunPages * 100 {
		last = e.addRun(sha, bump, "completed", "success")
	}
	require.Equal(t, "handled", e.fire(last))
	assert.Equal(t, releaseAwaitingCI, e.release("1.2.3").State)
	e.s.scan(e.ctx)
	assert.Equal(t, releaseAwaitingCI, e.release("1.2.3").State)
	assert.Empty(t, e.ct.created())
}

// cleanup 只删保留期之外的终态行；进行中的版本（awaiting_ci / fixing）再旧也保留。
func TestReleaseCleanup(t *testing.T) {
	e := newEnv(t)
	now := e.clock.Now()
	old := now.Add(-releaseRetention - time.Hour).UnixMilli()
	recent := now.Add(-releaseRetention + time.Hour).UnixMilli()
	rows := []struct {
		ver, state string
		at         int64
		keep       bool
	}{
		{"1.0.0", releaseGreen, old, false}, {"1.0.1", releaseFailed, old, false}, {"1.0.2", releaseSuperseded, old, false},
		{"1.0.3", releaseGreen, recent, true}, {"1.0.4", releaseAwaitingCI, old, true}, {"1.0.5", releaseFixing, old, true},
	}
	for _, r := range rows {
		require.NoError(t, e.st.Q.UpsertGithubRelease(e.ctx, db.UpsertGithubReleaseParams{
			RepoID: 42, Version: r.ver, Sha: relSHA(1), State: r.state, UpdatedAt: r.at,
		}))
	}
	e.s.cleanup(e.ctx)
	for _, r := range rows {
		_, err := e.st.Q.GetGithubRelease(e.ctx, db.GetGithubReleaseParams{RepoID: 42, Version: r.ver})
		if r.keep {
			assert.NoError(t, err, r.ver)
		} else {
			assert.True(t, isNoRows(err), r.ver)
		}
	}
}

// hookThreads 在 Deliver 转给内层之前先调用 hook；hook 返回错误时 Deliver 原样返回它。
type hookThreads struct {
	Threads
	hook func(threadID string) error
}

func (t *hookThreads) Deliver(ctx context.Context, threadID string, m threads.AutoMessage, ack func(error)) (func() bool, error) {
	if err := t.hook(threadID); err != nil {
		return nil, err
	}
	return t.Threads.Deliver(ctx, threadID, m, ack)
}

// 续跑一轮：投递（唤醒线程）的那一刻，github_release_status 已看到 fixing 与新一轮的轮数。
func TestReleaseContinueFixingBeforeDeliver(t *testing.T) {
	e := newReleaseEnv(t)
	e.publish("1.2.3", relSHA(1))
	require.Equal(t, "handled", e.fire(e.addRun(relSHA(1), bump, "completed", "failure")))
	var seen []map[string]any
	e.s.th = &hookThreads{Threads: e.ct, hook: func(threadID string) error {
		res, err := e.s.toolReleaseStatus(e.ctx, threadID, map[string]any{"brief": true})
		require.NoError(t, err)
		seen = append(seen, res.Details)
		return nil
	}}
	e.publish("1.2.3", relSHA(2))
	require.Equal(t, "handled", e.fire(e.addRun(relSHA(2), bump+": fix", "completed", "failure")))
	require.Len(t, seen, 1)
	assert.Equal(t, releaseFixing, seen[0]["state"])
	assert.EqualValues(t, 2, seen[0]["round"])
	assert.Len(t, e.delivered(), 1)
}

// 投递返回普通错误：状态、轮数与 updated_at 回滚到调用前；重试成功后轮数只加 1。
func TestReleaseContinueDeliverErrorRollsBack(t *testing.T) {
	e := newReleaseEnv(t)
	e.publish("1.2.3", relSHA(1))
	require.Equal(t, "handled", e.fire(e.addRun(relSHA(1), bump, "completed", "failure")))
	fail := errors.New("link down")
	e.s.th = &hookThreads{Threads: e.ct, hook: func(string) error { return fail }}
	e.publish("1.2.3", relSHA(2))
	require.Equal(t, "handled", e.fire(e.addRun(relSHA(2), bump+": fix", "completed", "success")))
	run := e.addRun(relSHA(2), bump+": fix", "completed", "failure")
	before := e.release("1.2.3")
	require.Equal(t, releaseAwaitingCI, before.State)
	e.clock.Add(time.Minute)
	assert.Contains(t, e.fire(run), "link down")
	assert.Equal(t, before, e.release("1.2.3"), "回滚到调用前")
	assert.Empty(t, e.delivered())

	fail = nil
	require.Equal(t, "handled", e.fire(run))
	r := e.release("1.2.3")
	assert.Equal(t, releaseFixing, r.State)
	assert.EqualValues(t, 2, r.Rounds)
	assert.Len(t, e.delivered(), 1)
}

// fixing 只判超时：未到期时 scan 不发任何请求；自 updated_at 起越过 releaseAwaitMax 置 failed、通知线程，例外消失。
func TestReleaseScanFixingTimeout(t *testing.T) {
	e := newReleaseEnv(t)
	e.publish("1.2.3", relSHA(1))
	require.Equal(t, "handled", e.fire(e.addRun(relSHA(1), bump, "completed", "failure")))
	thr := e.ct.last()
	orb := threadOrb(t, e, thr)
	e.clock.Add(releaseAwaitMax - time.Hour)
	e.fake.ResetCalls()
	e.s.scan(e.ctx)
	e.fake.Lock()
	assert.Empty(t, e.fake.Calls, "fixing 未到期不发请求")
	e.fake.Unlock()
	assert.Equal(t, releaseFixing, e.release("1.2.3").State)
	assert.True(t, e.s.ReleaseThread(e.ctx, orb))

	e.clock.Add(2 * time.Hour)
	e.s.scan(e.ctx)
	assert.Equal(t, releaseFailed, e.release("1.2.3").State)
	assert.False(t, e.s.ReleaseThread(e.ctx, orb))
	posts := e.posts()
	require.Len(t, posts, 1)
	assert.Equal(t, thr+"\nerror\nRelease acme/widgets v1.2.3 is failed: no fix was pushed within 24h.", posts[0])
}

// 正常的修复推送：新提交的 run 结论把 fixing 写成 awaiting_ci 并刷新 updated_at，fixing 的超时不会触发。
func TestReleaseFixPushResetsTimeout(t *testing.T) {
	e := newReleaseEnv(t)
	e.publish("1.2.3", relSHA(1))
	require.Equal(t, "handled", e.fire(e.addRun(relSHA(1), bump, "completed", "failure")))
	e.clock.Add(releaseAwaitMax - time.Hour)
	e.publish("1.2.3", relSHA(2))
	require.Equal(t, "handled", e.fire(e.addRun(relSHA(2), bump+": fix", "completed", "success")))
	r := e.release("1.2.3")
	assert.Equal(t, releaseAwaitingCI, r.State)
	assert.Equal(t, e.clock.Now().UnixMilli(), r.UpdatedAt)
	e.clock.Add(2 * time.Hour)
	e.s.scan(e.ctx)
	assert.Equal(t, releaseAwaitingCI, e.release("1.2.3").State, "等 Release，未到 awaiting_ci 的期限")
	assert.Empty(t, e.posts())
}
