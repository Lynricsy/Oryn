package github

import (
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// presetFiles 是 presets/ 下的全部预设文件；每个都必须带 MIT 出处注释。
var presetFiles = []string{"triage.md", "review.md", "release.md", "finalized.md", "autoclose_suffix.md"}

// releaseStatusFields 是 release.md 点名的 github_release_status 结果字段（Details 的键）。toolReleaseStatus 的 Details
// 是现拼的 map，没有类型可反射，从提示词里自动挑字段名又分不清字段与状态值、命令片段，所以把名单固定在这里：
// 一边断言 release.md 确实点名了每一个，一边经真实调用断言 Details 里确实有这些键。
var releaseStatusFields = []string{
	"state", "version", "sha", "defaultBranch", "tagOid", "round", "maxRounds",
	"stale", "defaultBranchAdvanced", "newerVersion", "runs", "blockingJobs", "overflow", "errors",
}

// releaseJobFields 是 release.md 点名的阻塞 job 字段（releaseJobView 的 JSON 名）。
var releaseJobFields = []string{"failedSteps", "logTail"}

// rawPreset 读取预设文件的原文（含出处注释）。
func rawPreset(t *testing.T, name string) string {
	t.Helper()
	b, err := presetFS.ReadFile("presets/" + name)
	require.NoError(t, err)
	return string(b)
}

// registeredOrbTools 从 oryn-orb.ts 里取出 omp 注册的 GitHub 工具名（`name: "github_…"`）。
func registeredOrbTools(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("../orbdrt/assets/oryn-orb.ts")
	require.NoError(t, err)
	var names []string
	for _, m := range regexp.MustCompile(`name:\s*"(github_[a-z_]+)"`).FindAllStringSubmatch(string(src), -1) {
		names = append(names, m[1])
	}
	require.Contains(t, names, "github_prepare", "oryn-orb.ts 里应能解析出 GitHub 工具")
	return names
}

func TestPresetFields(t *testing.T) {
	ps := Presets()
	require.Len(t, ps, 3)
	byID := map[apigen.GitHubPresetID]apigen.GitHubPreset{}
	for _, p := range ps {
		byID[p.ID] = p
	}

	triage := byID[apigen.GitHubPresetRobompTriage]
	assert.Equal(t, apigen.IssueOpened, triage.Trigger.Event)
	assert.Equal(t, apigen.GitHubDriversWritersAndAuthor, *triage.Trigger.Drivers)
	assert.True(t, *triage.Trigger.ImplAuth)
	assert.True(t, *triage.Trigger.CompletionReminders)
	assert.Equal(t, defaultAutocloseHours, *triage.Trigger.AutocloseHours)
	require.NotNil(t, triage.Gates)
	assert.True(t, triage.Gates.Push && triage.Gates.Pr)
	assert.Equal(t, []string{"Repro", "Cause", "Fix", "Verification"}, triage.Gates.PrHeadings)
	assert.True(t, *triage.Gates.RequireClosingRef)

	review := byID[apigen.GitHubPresetRobompReview]
	assert.Equal(t, apigen.PullRequestOpened, review.Trigger.Event)
	assert.True(t, *review.Trigger.CommentOnlyReviews, "评审预设只发 COMMENT")
	assert.Nil(t, review.Gates, "评审线程是只读工作区，不需要推送门禁")

	release := byID[apigen.GitHubPresetRobompRelease]
	assert.Equal(t, apigen.WorkflowRunCompleted, release.Trigger.Event)
	assert.Equal(t, presetReleasePrefix, *release.Trigger.ReleasePrefix)
	require.NotNil(t, release.Gates)
	assert.True(t, release.Gates.Push)
	assert.False(t, release.Gates.Pr)
}

// 每个预设文件（含分诊之后接上的自动关闭说明）引用的 github_* 工具都必须是 oryn-orb.ts 注册过的；出处注释被去掉。
func TestPresetPromptsUseRegisteredTools(t *testing.T) {
	tools := registeredOrbTools(t)
	toolRe := regexp.MustCompile(`github_[a-z_]+`)
	for _, name := range presetFiles {
		raw := rawPreset(t, name)
		assert.True(t, strings.HasPrefix(raw, "<!--"), "%s：文件头应有出处注释", name)
		assert.Contains(t, raw, "can1357/oh-my-pi python/robomp，MIT", "%s：出处注释应注明 MIT", name)
		body := presetText(name)
		assert.False(t, strings.HasPrefix(body, "<!--"), "%s：出处注释应被去掉", name)
		for _, ref := range toolRe.FindAllString(body, -1) {
			assert.Contains(t, tools, ref, "%s 引用了未注册的工具", name)
		}
	}
	for _, p := range Presets() {
		assert.LessOrEqual(t, len(p.Trigger.Prompt), 20000, "%s：提示词不能超过规则上限", p.ID)
	}
}

// 每个预设用到的 {{var}} 都必须被它的渲染器替换：分诊与评审经 triggerPrompt，发布经 releasePrompt；渲染后不剩任何占位符。
func TestPresetTemplateVariablesRendered(t *testing.T) {
	leftover := regexp.MustCompile(`{{[^{}]*}}`)
	hours := int64(9)
	ev := triggerEvent{
		event: evIssueOpened, repo: "acme/widgets",
		issue: hookIssue{Number: 7, Title: "t", HTMLURL: "https://github.com/acme/widgets/issues/7"},
	}
	for _, p := range Presets() {
		trig := db.GithubTrigger{Name: p.Name, Prompt: p.Trigger.Prompt, AutocloseHours: &hours}
		var out string
		switch p.ID {
		case apigen.GitHubPresetRobompRelease:
			out = releasePrompt(trig, "acme/widgets", releaseInfo{
				Version: "1.2.3", Tag: "v1.2.3", SHA: relSHA(1), TagOid: relSHA(1), DefaultBranch: "main", Round: 1, MaxRounds: maxReleaseRounds,
				releaseDossier: releaseDossier{Runs: []releaseRunView{{ID: 1, Name: "CI", Conclusion: "failure", URL: "https://github.com/acme/widgets/actions/runs/1"}}},
			})
			assert.Contains(t, out, "v1.2.3")
			assert.Contains(t, out, "https://github.com/acme/widgets/actions/runs/1")
		default:
			out = triggerPrompt(trig, ev)
			assert.Contains(t, out, "#7")
		}
		assert.Contains(t, out, "acme/widgets")
		assert.Empty(t, leftover.FindAllString(out, -1), "%s：有渲染器不替换的模板变量", p.ID)
	}
	assert.Contains(t, triggerPrompt(db.GithubTrigger{Prompt: triagePrompt(), AutocloseHours: &hours}, ev), "auto-close in 9 hours",
		"自动关闭说明里的小时数取规则配置")
	assert.NotContains(t, finalizedReply(), "{{", "终态回帖原样发出，不能有模板变量")
}

func TestPresetFinalizedAndTriageComposition(t *testing.T) {
	reply := finalizedReply()
	assert.NotEmpty(t, reply)
	assert.NotContains(t, reply, "<!--")
	assert.NotContains(t, reply, "移植自", "回帖正文不能带出处注释")
	for _, internal := range []string{"Oryn", "github_", "egress", "orb"} {
		assert.NotContains(t, reply, internal, "回帖原样发到 GitHub，不能带 Oryn 内部信息")
	}

	p := triagePrompt()
	assert.Contains(t, p, "Question auto-close", "分诊提示词接上自动关闭说明")
	assert.Contains(t, p, "{{autoclose_hours}}", "小时数在触发时渲染")
	assert.NotContains(t, p, "移植自")
}

// 分诊提示词里的实现授权与 tasks.go 一致：标签名、go ahead 用语、能授权的人（R16）。
func TestPresetTriageImplAuth(t *testing.T) {
	p := presetText("triage.md")
	assert.Contains(t, p, "`"+implAuthLabel+"` label")
	assert.True(t, goAhead.MatchString(p), "提示词应写出授权用语")
	assert.Contains(t, p, "association is `OWNER`")
	assert.Contains(t, p, "MEMBER and COLLABORATOR accounts included", "MEMBER / COLLABORATOR 不能授权")
	for _, s := range toolTaskStates {
		assert.Contains(t, p, `state="`+s+`"`, "提示词应覆盖工具可设的状态 %s", s)
	}
	assert.Contains(t, p, "`"+needsInfoLabel+"` label")
}

// release.md 点名的结果字段确实出现在 github_release_status 的 Details 里；状态名与提交标题规则和 release.go 一致。
func TestPresetReleaseStatusFields(t *testing.T) {
	doc := presetText("release.md")
	for _, f := range append(slices.Clone(releaseStatusFields), releaseJobFields...) {
		assert.Contains(t, doc, "`"+f+"`", "release.md 应点名字段 %s", f)
	}
	for _, s := range []string{releaseAwaitingCI, releaseFixing, releaseGreen, releaseFailed, releaseSuperseded} {
		assert.Contains(t, doc, "`"+s+"`", "release.md 应点名状态 %s", s)
	}
	// 修复提交标题：版本之后紧跟空格或冒号；示例取预设前缀
	assert.Contains(t, doc, "`"+presetReleasePrefix+"{{version}}`")
	for _, subject := range []string{"1.2.3: fix flaky timeout", "1.2.3 fix flaky timeout"} {
		v, ok := releaseVersion(presetReleasePrefix+subject, presetReleasePrefix)
		assert.True(t, ok && v == "1.2.3", "%q 应识别为同一版本", subject)
	}
	for _, subject := range []string{"1.2.3-fix", "1.2.3/fix"} {
		v, _ := releaseVersion(presetReleasePrefix+subject, presetReleasePrefix)
		assert.NotEqual(t, "1.2.3", v, "%q 不是同一版本", subject)
	}

	var jobTags []string
	for f := range reflect.TypeFor[releaseJobView]().Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		jobTags = append(jobTags, name)
	}
	for _, f := range releaseJobFields {
		assert.Contains(t, jobTags, f, "releaseJobView 应有字段 %s", f)
	}

	e := newReleaseEnv(t)
	sha := relSHA(1)
	e.publish("1.2.3", sha)
	require.Equal(t, "handled", e.fire(e.addRun(sha, bump, "completed", "failure", failingJob("build", "boom"))))
	require.NoError(t, e.s.st.Q.UpsertGithubRelease(e.ctx, db.UpsertGithubReleaseParams{
		RepoID: 42, Version: "1.2.4", Sha: relSHA(9), State: releaseAwaitingCI, UpdatedAt: e.s.nowMs(),
	}))
	thr := e.ct.last()
	orb := threadOrb(t, e, thr)
	keys := map[string]bool{}
	call := func() {
		t.Helper()
		res, err := e.s.Call(e.ctx, orb, thr, apigen.LinkCtlGithubCallParams{ThreadID: thr, Tool: "github_release_status", Params: map[string]any{}})
		require.NoError(t, err)
		for k := range res.Details {
			keys[k] = true
		}
	}
	call()
	// 默认分支读不到：errors 出现
	e.setRef("heads/main", "")
	call()
	for _, f := range releaseStatusFields {
		assert.True(t, keys[f], "github_release_status 的 Details 没有 release.md 点名的字段 %s", f)
	}
}

func TestStripLeadingComment(t *testing.T) {
	assert.Equal(t, "body", stripLeadingComment("  <!--\nx\n-->\n\nbody"))
	assert.Equal(t, "no comment", stripLeadingComment("no comment"))
	assert.Equal(t, "<!-- unterminated", stripLeadingComment("<!-- unterminated"))
}
