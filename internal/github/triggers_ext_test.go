package github

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 步骤 4.1 / 4.2：新事件、驱动者档位、按人限流、评审机器人、模型池与指令。

// assocIssue 是带 author_association 的 issueJSON。
func assocIssue(number int64, author, assoc, body string) map[string]any {
	is := issueJSON(number, "t", body, author)
	is["author_association"] = assoc
	return is
}

func (e *triggerEnv) threadIssue(number int64) db.GetThreadIssueRow {
	e.t.Helper()
	row, err := e.st.Q.GetThreadIssue(e.ctx, db.GetThreadIssueParams{RepoID: 42, Number: number})
	require.NoError(e.t, err)
	return row
}

// anyone 档：NONE 每小时 3 次、第 4 次被拒；OWNER 不限；issue_reopened 与 issue_opened 共用预算。
func TestTriggerAnyoneSenderRateLimit(t *testing.T) {
	e := newTriggerEnv(t)
	e.trigger(apigen.GitHubTriggerInput{Name: "open", Event: apigen.IssueOpened, Prompt: "p", Drivers: new(apigen.GitHubDriversAnyone), Announce: new(false)})
	e.trigger(apigen.GitHubTriggerInput{Name: "reopen", Event: apigen.IssueReopened, Prompt: "p", Drivers: new(apigen.GitHubDriversAnyone), Announce: new(false)})

	for n := int64(1); n <= 2; n++ {
		g := e.deliver("issues", issuesEvent("opened", assocIssue(n, "carol", "NONE", "b"), "carol", ""))
		require.Equal(t, "handled", e.delivery(g).Outcome)
	}
	g := e.deliver("issues", issuesEvent("reopened", assocIssue(3, "carol", "NONE", "b"), "carol", ""))
	require.Equal(t, "handled", e.delivery(g).Outcome, "重开同样建线程")
	g = e.deliver("issues", issuesEvent("opened", assocIssue(4, "carol", "NONE", "b"), "Carol", ""))
	assert.Equal(t, "ignored:sender_rate_limited", e.delivery(g).Outcome, "第 4 次（重开计入同一预算）")
	g = e.deliver("issues", issuesEvent("reopened", assocIssue(5, "carol", "NONE", "b"), "carol", ""))
	assert.Equal(t, "ignored:sender_rate_limited", e.delivery(g).Outcome)

	// CONTRIBUTOR 的上限是 10：第 4 次仍放行
	for n := int64(10); n < 14; n++ {
		g = e.deliver("issues", issuesEvent("opened", assocIssue(n, "dave", "CONTRIBUTOR", "b"), "dave", ""))
		require.Equal(t, "handled", e.delivery(g).Outcome, n)
	}
	// 一小时后窗口滑过，可以再跑
	e.clock.Add(time.Hour + time.Second)
	g = e.deliver("issues", issuesEvent("opened", assocIssue(20, "carol", "NONE", "b"), "carol", ""))
	assert.Equal(t, "handled", e.delivery(g).Outcome)
	// OWNER 不限（规则自身每小时的上限另计）
	for n := int64(30); n < 34; n++ {
		g = e.deliver("issues", issuesEvent("opened", assocIssue(n, "boss", "OWNER", "b"), "boss", ""))
		require.Equal(t, "handled", e.delivery(g).Outcome, n)
	}
	for _, c := range e.ct.created() {
		assert.True(t, c.Untrusted, "anyone 档只用 Agent 账号")
	}
	// 清理只删窗口外的记录
	e.s.cleanupSenderRuns(e.ctx, e.clock.Now())
	n, err := e.st.Q.CountGithubSenderRunsSince(e.ctx, db.CountGithubSenderRunsSinceParams{RuleID: "issues:" + e.prj, Sender: "carol", Since: 0})
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
}

// issue_reopened 与 issue_opened 共用规则自身的每小时上限（triggerMaxPerHour）。
func TestTriggerReopenedSharesRuleBudget(t *testing.T) {
	e := newTriggerEnv(t)
	e.trigger(apigen.GitHubTriggerInput{Name: "open", Event: apigen.IssueOpened, Prompt: "p", Announce: new(false)})
	e.trigger(apigen.GitHubTriggerInput{Name: "reopen", Event: apigen.IssueReopened, Prompt: "p", Announce: new(false)})
	for n := int64(1); n <= triggerMaxPerHour; n++ {
		action := "opened"
		if n%2 == 0 {
			action = "reopened"
		}
		g := e.deliver("issues", issuesEvent(action, assocIssue(n, "alice", "MEMBER", "b"), "alice", ""))
		require.Equal(t, "handled", e.delivery(g).Outcome, n)
	}
	g := e.deliver("issues", issuesEvent("reopened", assocIssue(99, "alice", "MEMBER", "b"), "alice", ""))
	assert.Equal(t, "ignored:rate_limited", e.delivery(g).Outcome)
	assert.Equal(t, triggerMaxPerHour, len(e.ct.created()))
	// 重开只认 sender 是作者时的 author_association：别人重开时按权限接口判定
	g = e.deliver("issues", issuesEvent("reopened", assocIssue(100, "dave", "OWNER", "b"), "carol", ""))
	assert.Equal(t, "ignored:no_write_access", e.delivery(g).Outcome)
}

// Q6：anyone 档与 pull_request_opened 规则在 Agent 账号不可用时不建线程，记 ignored:agent_required 并通知；
// 其余规则回退到我的账号。
func TestTriggerUntrustedAgentRequired(t *testing.T) {
	e := newTriggerEnv(t)
	tr := e.trigger(apigen.GitHubTriggerInput{Name: "open", Event: apigen.IssueOpened, Prompt: "p", Drivers: new(apigen.GitHubDriversAnyone)})
	require.NoError(t, e.s.Disconnect(e.ctx, slotAgent))

	g := e.deliver("issues", issuesEvent("opened", assocIssue(1, "carol", "NONE", "b"), "carol", ""))
	assert.Equal(t, "ignored:agent_required", e.delivery(g).Outcome)
	assert.Empty(t, e.ct.created())
	sys := e.n.systemPosts()
	require.Len(t, sys, 1)
	assert.True(t, strings.HasPrefix(sys[0], "error\nGitHub trigger skipped\n"), sys[0])
	assert.Contains(t, sys[0], `"open" on acme/widgets#1`)
	assert.True(t, strings.HasSuffix(sys[0], "\n"+githubSettingsPath))
	assert.Empty(t, e.fake.CallsTo("POST", "/repos/acme/widgets/issues/1/reactions"))

	// 同一规则改回 writers_and_author：作者是写权限者时照常建线程（不是不可信规则，由线程服务回退）
	_, err := e.s.UpdateTrigger(e.ctx, tr.ID, apigen.GitHubTriggerPatch{Drivers: new(apigen.GitHubDriversWritersAndAuthor)})
	require.NoError(t, err)
	g = e.deliver("issues", issuesEvent("opened", assocIssue(2, "alice", "MEMBER", "b"), "alice", ""))
	require.Equal(t, "handled", e.delivery(g).Outcome)
	require.Len(t, e.ct.created(), 1)
	assert.False(t, e.ct.created()[0].Untrusted)

	// 外部 PR 评审规则同样要求 Agent 账号
	e.trigger(apigen.GitHubTriggerInput{Name: "review", Event: apigen.PullRequestOpened, Prompt: "p", Drivers: new(apigen.GitHubDriversWriters)})
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 50, Title: "Ext", Author: "alice"}, HeadRef: "f", HeadSHA: "s50", HeadRepoID: 999})
	g = e.deliver("pull_request", pullOpenedEvent("opened", 50, "alice", "MEMBER", "alice", false))
	assert.Equal(t, "ignored:agent_required", e.delivery(g).Outcome)
	assert.Len(t, e.n.systemPosts(), 2)
}

func pullOpenedEvent(action string, number int64, author, assoc, sender string, draft bool) map[string]any {
	return map[string]any{
		"action": action, "number": number, "sender": who(sender),
		"repository": map[string]any{"id": 42, "full_name": "acme/widgets", "node_id": "R_42"},
		"pull_request": map[string]any{
			"number": number, "node_id": "PR_" + itoa(number), "title": "Add feature", "body": "please review", "state": "open",
			"draft": draft, "html_url": "https://github.com/acme/widgets/pull/" + itoa(number), "user": who(author),
			"author_association": assoc,
			"head":               map[string]any{"ref": "f", "sha": "s" + itoa(number), "repo": map[string]any{"id": 999, "full_name": "ext/widgets"}},
			"base":               map[string]any{"ref": "main", "repo": map[string]any{"id": 42, "full_name": "acme/widgets"}},
		},
	}
}

// pull_request_opened：非草稿、open、作者不是 bot；线程只读（review_only）、身份只用 Agent 账号；同一 PR 不重复触发。
func TestTriggerPullRequestOpened(t *testing.T) {
	e := newTriggerEnv(t)
	tr := e.trigger(apigen.GitHubTriggerInput{
		Name: "review", Event: apigen.PullRequestOpened, Prompt: "Review {{kind}} {{url}}", Drivers: new(apigen.GitHubDriversAnyone),
		Announce: new(false),
	})
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 50, Title: "Add feature", Author: "carol"}, HeadRef: "f", HeadSHA: "s50", HeadRepoID: 999})

	g := e.deliver("pull_request", pullOpenedEvent("opened", 50, "carol", "NONE", "carol", true))
	assert.Equal(t, "ignored:draft", e.delivery(g).Outcome)
	bot := pullOpenedEvent("opened", 51, "renovate[bot]", "NONE", "renovate[bot]", false)
	bot["pull_request"].(map[string]any)["user"] = map[string]any{"login": "renovate[bot]", "type": "Bot"}
	g = e.deliver("pull_request", bot)
	assert.Equal(t, "ignored:bot_author", e.delivery(g).Outcome)
	g = e.deliver("pull_request", pullOpenedEvent("synchronize", 50, "carol", "NONE", "carol", false))
	assert.Equal(t, "handled", e.delivery(g).Outcome, "其它动作只入库")

	g = e.deliver("pull_request", pullOpenedEvent("ready_for_review", 50, "carol", "NONE", "carol", false))
	require.Equal(t, "handled", e.delivery(g).Outcome)
	created := e.ct.created()
	require.Len(t, created, 1)
	assert.True(t, created[0].Untrusted)
	assert.Contains(t, created[0].Prompt, "Review pull request https://github.com/acme/widgets/pull/50")
	assert.Contains(t, created[0].Prompt, `"kind":"pull request","title":"Add feature","body":"please review","author":"carol"`)
	row := e.threadIssue(50)
	assert.True(t, row.ReviewOnly)
	require.NotNil(t, row.RuleID)
	assert.Equal(t, tr.ID, *row.RuleID)
	assert.Len(t, e.fake.CallsTo("POST", "/repos/acme/widgets/issues/50/reactions"), 1)

	g = e.deliver("pull_request", pullOpenedEvent("reopened", 50, "carol", "NONE", "carol", false))
	assert.Equal(t, "ignored:already_linked", e.delivery(g).Outcome)

	// 跟进评论按来源规则（anyone）判定：只读权限的 carol 也能驱动
	g = e.deliver("issue_comment", issueCommentEvent(50, true, "carol", "also check the docs"))
	assert.Equal(t, "handled", e.delivery(g).Outcome)

	// 没有 pull_request_opened 规则的仓库：PR 事件照旧记 handled
	require.NoError(t, e.s.DeleteTrigger(e.ctx, tr.ID))
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 52, Title: "x", Author: "carol"}, HeadRef: "g", HeadSHA: "s52", HeadRepoID: 999})
	g = e.deliver("pull_request", pullOpenedEvent("opened", 52, "carol", "NONE", "carol", false))
	assert.Equal(t, "handled", e.delivery(g).Outcome)
}

// R7：我的账号经出站代理开的 PR（预登记 pr.create，目标为仓库 node_id 与 /repos/o/r/pulls）不触发评审规则。
func TestSelfWritePullOpened(t *testing.T) {
	e := newTriggerEnv(t)
	e.trigger(apigen.GitHubTriggerInput{Name: "review", Event: apigen.PullRequestOpened, Prompt: "p", Drivers: new(apigen.GitHubDriversAnyone)})
	for _, target := range []string{"R_42", "/repos/acme/widgets/pulls"} {
		require.NoError(t, e.st.Q.InsertGithubSelfWrite(e.ctx, db.InsertGithubSelfWriteParams{
			Slot: slotUser, Category: egress.CatPRCreate, Target: target, BodySha256: new(egress.BodyHash("please review")),
			CreatedAt: e.clock.Now().UnixMilli(),
		}))
	}
	g := e.deliver("pull_request", pullOpenedEvent("opened", 60, "owner", "OWNER", "owner", false))
	assert.Equal(t, "ignored:self", e.delivery(g).Outcome)
	// 补充键 node:<PR node_id> 同样命中（出站代理记下的 createPullRequest 响应）
	e.s.rememberSelfKeys(e.ctx, []string{"node:PR_61"})
	g = e.deliver("pull_request", pullOpenedEvent("opened", 61, "alice", "MEMBER", "alice", false))
	assert.Equal(t, "ignored:self", e.delivery(g).Outcome)
	// 正文不同、不是自己的 PR：照常触发
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 62, Title: "x", Author: "alice"}, HeadRef: "g", HeadSHA: "s62", HeadRepoID: 999})
	g = e.deliver("pull_request", pullOpenedEvent("opened", 62, "alice", "MEMBER", "alice", false))
	assert.Equal(t, "handled", e.delivery(g).Outcome)

	p := pullProbe(hookEvent{Repository: repoPayload{ID: 42, FullName: "Acme/Widgets", NodeID: "R_42"},
		PullRequest: &hookIssue{Number: 9, NodeID: "PR_9"}}, "ready_for_review")
	assert.Equal(t, egress.CatPRUpdate, p.category)
	assert.Equal(t, []string{"PR_9", "/repos/acme/widgets/pulls/9", "/repositories/42/pulls/9"}, p.targets)
}

// 评审机器人：提及规则里的 bot 发言不需要 @；名单外的 bot、没 @ 的人都不触发。
func TestTriggerReviewerBotMention(t *testing.T) {
	e := newTriggerEnv(t)
	e.trigger(apigen.GitHubTriggerInput{Name: "m", Event: apigen.Mention, Prompt: "p", ReviewerBots: []string{"coderabbitai"}, Announce: new(false)})
	botComment := func(number int64, login string) map[string]any {
		ev := issueCommentEvent(number, false, login, "Found a bug in foo()")
		ev["sender"] = map[string]any{"login": login, "type": "Bot"}
		return ev
	}
	g := e.deliver("issue_comment", botComment(5, "coderabbitai[bot]"))
	require.Equal(t, "handled", e.delivery(g).Outcome)
	g = e.deliver("issue_comment", botComment(6, "dependabot[bot]"))
	assert.Equal(t, "ignored:no_thread", e.delivery(g).Outcome)
	g = e.deliver("issue_comment", issueCommentEvent(7, false, "alice", "no mention here"))
	assert.Equal(t, "ignored:no_thread", e.delivery(g).Outcome)
	assert.Len(t, e.ct.created(), 1)
}

// workflow_run 的路由：没有启用的发布哨兵规则时不处理；有规则时交给发布状态机（head 提交不带前缀的 run 不是发布）。
func TestTriggerWorkflowRunRouted(t *testing.T) {
	e := newTriggerEnv(t)
	run := map[string]any{"action": "completed", "repository": repoJSON(), "sender": who("owner"), "workflow_run": map[string]any{"id": 1}}
	g := e.deliver("workflow_run", run)
	assert.Equal(t, "ignored:no_trigger", e.delivery(g).Outcome)
	tr := e.trigger(apigen.GitHubTriggerInput{Name: "rel", Event: apigen.WorkflowRunCompleted, Prompt: "p", ReleasePrefix: new("chore: release ")})
	assert.Equal(t, "chore: release ", *tr.ReleasePrefix)
	g = e.deliver("workflow_run", run)
	assert.Equal(t, "ignored:not_release", e.delivery(g).Outcome)
	run["action"] = "requested"
	g = e.deliver("workflow_run", run)
	assert.Equal(t, "ignored:action", e.delivery(g).Outcome)
}

// 规则选项的增改查与校验。
func TestTriggerOptionsCRUD(t *testing.T) {
	e := newTriggerEnv(t)
	tr := e.trigger(apigen.GitHubTriggerInput{Name: "o", Event: apigen.IssueOpened, Prompt: "p", ReleasePrefix: new("x")})
	assert.Equal(t, apigen.GitHubDriversWritersAndAuthor, *tr.Drivers, "默认档位")
	assert.Nil(t, tr.ReleasePrefix, "非发布哨兵规则不保留前缀")
	assert.Nil(t, tr.Thinking)

	up, err := e.s.UpdateTrigger(e.ctx, tr.ID, apigen.GitHubTriggerPatch{
		Drivers: new(apigen.GitHubDriversAnyone), Maintainers: []string{" Bob ", "bob", ""}, ReviewerBots: []string{"coderabbitai[bot]"},
		Models: []apigen.ModelRef{{Provider: "anthropic", ID: "claude-opus"}}, Thinking: new("hi"), ImplAuth: new(true),
		CommentOnlyReviews: new(true), AutocloseHours: new(6), MaxDurationSec: new(3600), CompletionReminders: new(true),
	})
	require.NoError(t, err)
	assert.Equal(t, apigen.GitHubDriversAnyone, *up.Drivers)
	assert.Equal(t, []string{"Bob"}, up.Maintainers)
	assert.Equal(t, []string{"coderabbitai[bot]"}, up.ReviewerBots)
	assert.Equal(t, []apigen.ModelRef{{Provider: "anthropic", ID: "claude-opus"}}, up.Models)
	assert.Equal(t, "high", *up.Thinking)
	assert.True(t, *up.ImplAuth)
	assert.True(t, *up.CommentOnlyReviews)
	assert.True(t, *up.CompletionReminders)
	assert.Equal(t, 6, *up.AutocloseHours)
	assert.Equal(t, 3600, *up.MaxDurationSec)
	list, err := e.s.ListTriggers(e.ctx, e.prj)
	require.NoError(t, err)
	assert.Equal(t, up, list[0])

	// 清除：空串思考级别、0 时限、空列表
	up, err = e.s.UpdateTrigger(e.ctx, tr.ID, apigen.GitHubTriggerPatch{Thinking: new(""), MaxDurationSec: new(0), Maintainers: []string{}})
	require.NoError(t, err)
	assert.Nil(t, up.Thinking)
	assert.Nil(t, up.MaxDurationSec)
	assert.Empty(t, up.Maintainers)
	assert.Equal(t, []string{"coderabbitai[bot]"}, up.ReviewerBots, "未出现的字段不变")

	for _, p := range []apigen.GitHubTriggerPatch{
		{Drivers: new(apigen.GitHubTriggerDrivers("everyone"))},
		{Thinking: new("ultra")},
		{AutocloseHours: new(0)},
		{MaxDurationSec: new(5)},
		{Models: []apigen.ModelRef{{Provider: "", ID: "x"}}},
		{Maintainers: []string{"two words"}},
	} {
		_, err := e.s.UpdateTrigger(e.ctx, tr.ID, p)
		assert.Equal(t, "invalid_request", codeOf(err), "%+v", p)
	}
}

// stubCatalog 是模型目录桩。
type stubCatalog []apigen.ModelRef

func (c stubCatalog) Models(context.Context) ([]apigen.ModelRef, error) { return c, nil }

// R14：模型池随机取一个、规则的思考级别；维护者正文里的 /model、/thinking 覆盖，非维护者的忽略；
// 跟进评论里的指令经 SetModel 应用；{{autoclose_hours}} 取规则值或默认值。
func TestTriggerPragmaOverride(t *testing.T) {
	e := newTriggerEnv(t)
	e.s.intn = func(n int) int { return n - 1 }
	pool := []apigen.ModelRef{{Provider: "anthropic", ID: "claude-opus"}, {Provider: "openai", ID: "gpt-5"}}
	tr := e.trigger(apigen.GitHubTriggerInput{
		Name: "o", Event: apigen.IssueOpened, Prompt: "close after {{autoclose_hours}}h", Models: pool, Thinking: new("med"),
		Maintainers: []string{"dave"}, Announce: new(false),
	})

	// 非维护者（作者 alice 是写权限者，但 association 是 NONE）：指令忽略，取池里的最后一个与规则级别
	g := e.deliver("issues", issuesEvent("opened", assocIssue(1, "alice", "NONE", "/model opus\n/thinking hi"), "alice", ""))
	require.Equal(t, "handled", e.delivery(g).Outcome)
	c := e.ct.created()[0]
	require.NotNil(t, c.Model)
	assert.Equal(t, pool[1], *c.Model)
	assert.Equal(t, "medium", c.ThinkingLevel)
	assert.Contains(t, c.Prompt, "close after 4h", "未配置时取 defaultAutocloseHours")

	// 维护者（OWNER）：没有模型目录时在池里按短名查找
	g = e.deliver("issues", issuesEvent("opened", assocIssue(2, "alice", "OWNER", "please\n/model claude-opus\n/thinking hi\n"), "alice", ""))
	require.Equal(t, "handled", e.delivery(g).Outcome)
	c = e.ct.created()[1]
	assert.Equal(t, pool[0], *c.Model)
	assert.Equal(t, "high", c.ThinkingLevel)

	// 规则维护者名单；有模型目录时在目录里查（子串）；查不到的别名忽略
	e.s.SetModelCatalog(stubCatalog{{Provider: "google", ID: "gemini-3-pro"}, {Provider: "anthropic", ID: "claude-opus"}})
	_, err := e.s.UpdateTrigger(e.ctx, tr.ID, apigen.GitHubTriggerPatch{AutocloseHours: new(8)})
	require.NoError(t, err)
	g = e.deliver("issues", issuesEvent("opened", assocIssue(3, "dave", "CONTRIBUTOR", "/model gemini\n/thinking off"), "dave", ""))
	require.Equal(t, "handled", e.delivery(g).Outcome)
	c = e.ct.created()[2]
	assert.Equal(t, apigen.ModelRef{Provider: "google", ID: "gemini-3-pro"}, *c.Model)
	assert.Equal(t, "off", c.ThinkingLevel)
	assert.Contains(t, c.Prompt, "close after 8h")
	g = e.deliver("issues", issuesEvent("opened", assocIssue(4, "dave", "NONE", "/model nosuch"), "dave", ""))
	require.Equal(t, "handled", e.delivery(g).Outcome)
	assert.Equal(t, pool[1], *e.ct.created()[3].Model)

	// 跟进评论：MEMBER 的指令经 SetModel 应用；写权限者但 association 为 NONE 的不应用（评论照常回流）
	thr := e.threadIssue(1).ThreadID
	member := issueCommentEvent(1, false, "bob", "try again\n/thinking xhi")
	member["comment"].(map[string]any)["author_association"] = "MEMBER"
	g = e.deliver("issue_comment", member)
	require.Equal(t, "handled", e.delivery(g).Outcome)
	g = e.deliver("issue_comment", issueCommentEvent(1, false, "alice", "/model gemini"))
	require.Equal(t, "handled", e.delivery(g).Outcome)
	assert.Equal(t, []string{thr + " / xhigh"}, e.ct.setModels())
}

// 安全不变式：PR 评审线程「启动」的那一刻（InitTx 已提交、Create 返回之前），thread_issues 里 review_only 与 rule_id
// 已经在——出站代理按线程任务现查门禁，没有零限制的窗口。
func TestTriggerTaskWrittenBeforeStart(t *testing.T) {
	e := newTriggerEnv(t)
	tr := e.trigger(apigen.GitHubTriggerInput{Name: "review", Event: apigen.PullRequestOpened, Prompt: "p", Drivers: new(apigen.GitHubDriversAnyone), Announce: new(false)})
	var seen []db.ThreadIssue
	e.ct.onStart = func(threadID string) {
		ti, err := e.st.Q.GetThreadIssueByThread(e.ctx, threadID)
		require.NoError(t, err)
		seen = append(seen, ti)
	}
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 50, Title: "x", Author: "carol"}, HeadRef: "f", HeadSHA: "s50", HeadRepoID: 999})
	g := e.deliver("pull_request", pullOpenedEvent("opened", 50, "carol", "NONE", "carol", false))
	require.Equal(t, "handled", e.delivery(g).Outcome)
	require.Len(t, seen, 1)
	assert.True(t, seen[0].ReviewOnly)
	require.NotNil(t, seen[0].RuleID)
	assert.Equal(t, tr.ID, *seen[0].RuleID)
	assert.Equal(t, "new", seen[0].State)
}

// 重新触发的 issue（旧线程已归档）开始新任务：状态、分类、实现授权、提醒次数重置。
func TestTriggerRetriggerResetsTask(t *testing.T) {
	e := newTriggerEnv(t)
	e.trigger(apigen.GitHubTriggerInput{Name: "open", Event: apigen.IssueReopened, Prompt: "p", Announce: new(false)})
	old := e.workThread(e.prj)
	require.NoError(t, e.st.Q.UpsertThreadIssue(e.ctx, db.UpsertThreadIssueParams{RepoID: 42, Number: 7, ThreadID: old, CreatedAt: 1}))
	_, err := e.st.Q.SetThreadIssueState(e.ctx, db.SetThreadIssueStateParams{State: "closed", ThreadID: old})
	require.NoError(t, err)
	_, err = e.st.Q.SetThreadIssueClassification(e.ctx, db.SetThreadIssueClassificationParams{Classification: new("bug"), ThreadID: old})
	require.NoError(t, err)
	_, err = e.st.Q.SetThreadIssueImplAuthorized(e.ctx, db.SetThreadIssueImplAuthorizedParams{ImplAuthorized: true, RepoID: 42, Number: 7})
	require.NoError(t, err)
	_, err = e.st.Q.BumpThreadIssueReminders(e.ctx, old)
	require.NoError(t, err)
	_, err = e.st.Q.UpdateThreadMeta(e.ctx, db.UpdateThreadMetaParams{Title: "t", TitleSource: "auto", ArchivedAt: new(int64(1)), UpdatedAt: 1, ID: old})
	require.NoError(t, err)

	g := e.deliver("issues", issuesEvent("reopened", assocIssue(7, "alice", "MEMBER", "b"), "alice", ""))
	require.Equal(t, "handled", e.delivery(g).Outcome)
	row := e.threadIssue(7)
	assert.Equal(t, e.ct.last(), row.ThreadID)
	assert.Equal(t, "new", row.State)
	assert.Nil(t, row.Classification)
	assert.False(t, row.ImplAuthorized)
	assert.Zero(t, row.Reminders)
}
