package github

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// orbTools 是 orb 内 omp 注册的 GitHub 工具（R17）；预设提示词只能引用这些名字。
var orbTools = []string{
	"github_classify_issue", "github_task_state", "github_prepare",
	"github_autoclose", "github_release_status", "github_abort_task",
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

func TestPresetPromptsUseKnownToolsAndNoHeader(t *testing.T) {
	toolRe := regexp.MustCompile(`github_[a-z_]+`)
	varRe := regexp.MustCompile(`{{[a-z_]+}}`)
	allowedVars := []string{"{{repo}}", "{{number}}", "{{kind}}", "{{url}}", "{{autoclose_hours}}"}
	for _, p := range Presets() {
		prompt := p.Trigger.Prompt
		assert.False(t, strings.HasPrefix(prompt, "<!--"), "%s：出处注释应被去掉", p.ID)
		assert.LessOrEqual(t, len(prompt), 20000, "%s：提示词不能超过规则上限", p.ID)
		for _, name := range toolRe.FindAllString(prompt, -1) {
			assert.Contains(t, orbTools, name, "%s 引用了未注册的工具", p.ID)
		}
		for _, v := range varRe.FindAllString(prompt, -1) {
			assert.True(t, slices.Contains(allowedVars, v), "%s 使用了未知模板变量 %s", p.ID, v)
		}
	}
}

func TestPresetFinalizedAndTriageComposition(t *testing.T) {
	reply := finalizedReply()
	assert.NotEmpty(t, reply)
	assert.NotContains(t, reply, "<!--")
	assert.NotContains(t, reply, "移植自", "回帖正文不能带出处注释")

	p := triagePrompt()
	assert.Contains(t, p, "Question auto-close", "分诊提示词接上自动关闭说明")
	assert.Contains(t, p, "{{autoclose_hours}}", "小时数在触发时渲染")
	assert.NotContains(t, p, "移植自")
}

func TestStripLeadingComment(t *testing.T) {
	assert.Equal(t, "body", stripLeadingComment("  <!--\nx\n-->\n\nbody"))
	assert.Equal(t, "no comment", stripLeadingComment("no comment"))
	assert.Equal(t, "<!-- unterminated", stripLeadingComment("<!-- unterminated"))
}
