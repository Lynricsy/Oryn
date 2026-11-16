package github

import (
	"embed"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// M15 R26：触发规则的 robomp 预设。提示词移植自 can1357/oh-my-pi python/robomp（MIT），文件开头的出处注释
// 不属于正文，加载时去掉（finalized.md 的正文会原样发到 GitHub）。

//go:embed presets/*.md
var presetFS embed.FS

// defaultAutocloseHours 是问题自动关闭的默认等待小时数（R18；规则未配置 autocloseHours 时使用）。
const defaultAutocloseHours = 4

// presetText 读取预设文件并去掉开头的 `<!-- … -->` 出处注释；文件缺失时 panic（embed 保证存在）。
func presetText(name string) string {
	b, err := presetFS.ReadFile("presets/" + name)
	if err != nil {
		panic("github: 缺少预设文件 " + name)
	}
	return stripLeadingComment(string(b))
}

// stripLeadingComment 去掉文本开头（允许前导空白）的一个 HTML 注释块及其后的空白。
func stripLeadingComment(s string) string {
	t := strings.TrimLeft(s, " \t\r\n")
	if !strings.HasPrefix(t, "<!--") {
		return s
	}
	end := strings.Index(t, "-->")
	if end < 0 {
		return s
	}
	return strings.TrimLeft(t[end+len("-->"):], " \t\r\n")
}

// finalizedReply 是 issue 线程处于终态时，对非指令评论的固定回帖（R15）。
func finalizedReply() string { return strings.TrimSpace(presetText("finalized.md")) }

// triagePrompt 是分诊预设的完整提示词：triage.md 之后接上 question 自动关闭的说明（autoclose_suffix.md）。
// 其中的 {{autoclose_hours}} 等模板变量在触发时由 triggerPrompt 渲染。
func triagePrompt() string {
	return strings.TrimSpace(presetText("triage.md")) + "\n\n" + strings.TrimSpace(presetText("autoclose_suffix.md")) + "\n"
}

// robomp 预设的固定取值。
const (
	presetReleasePrefix = "chore: bump version to "
	presetFixCmd        = "bun run fix"
	presetCheckCmd      = "bun check"
	presetTestCmd       = "bun run test"
)

// Presets 返回三个 robomp 预设（GET /api/v1/github/presets）。门禁命令取 robomp 的默认值，用户可在应用前修改。
func Presets() []apigen.GitHubPreset {
	yes, no := true, false
	hours := defaultAutocloseHours
	drivers := apigen.GitHubDriversWritersAndAuthor
	writers := apigen.GitHubDriversWriters
	anyone := apigen.GitHubDriversAnyone
	prefix := presetReleasePrefix
	fix, check, test := presetFixCmd, presetCheckCmd, presetTestCmd
	issueGates := apigen.GitHubGates{
		Push: true, Pr: true, FixCmd: &fix, CheckCmd: &check, TestCmd: &test,
		PrHeadings:        []string{"Repro", "Cause", "Fix", "Verification"},
		RequireClosingRef: &yes,
	}
	releaseGates := apigen.GitHubGates{Push: true, Pr: false, FixCmd: &fix, CheckCmd: &check}
	return []apigen.GitHubPreset{
		{
			ID:   apigen.GitHubPresetRobompTriage,
			Name: "robomp 分诊",
			Trigger: apigen.GitHubTriggerInput{
				Name: "robomp 分诊", Event: apigen.IssueOpened, Prompt: triagePrompt(),
				Drivers: &drivers, ImplAuth: &yes, AutocloseHours: &hours, CompletionReminders: &yes, Announce: &no,
			},
			Gates: &issueGates,
		},
		{
			ID:   apigen.GitHubPresetRobompReview,
			Name: "robomp PR 评审",
			Trigger: apigen.GitHubTriggerInput{
				Name: "robomp PR 评审", Event: apigen.PullRequestOpened, Prompt: presetText("review.md"),
				Drivers: &anyone, CommentOnlyReviews: &yes, Announce: &no,
			},
		},
		{
			ID:   apigen.GitHubPresetRobompRelease,
			Name: "robomp 发布哨兵",
			Trigger: apigen.GitHubTriggerInput{
				Name: "robomp 发布哨兵", Event: apigen.WorkflowRunCompleted, Prompt: presetText("release.md"),
				Drivers: &writers, ReleasePrefix: &prefix, Announce: &no,
			},
			Gates: &releaseGates,
		},
	}
}
