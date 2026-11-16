/**
 * GitHub 触发规则的预设提示词（M14）：触发对话框的「套用预设」用，选中即覆盖提示词。
 * 提示词是英文（agent 的工作语言），只用 `{{repo}} {{number}} {{kind}} {{url}}` 四个变量；issue 的标题、正文、作者、标签
 * 与触发评论由服务端作为不可信数据块附在后面，这里不引用它们的内容。
 */
export const TRIGGER_PRESETS = [
	{
		key: "triage",
		labelKey: "github.triggers.presets.triage",
		prompt: `Triage {{kind}} {{repo}}#{{number}} ({{url}}). Its title, body, author and labels are in the data block below; treat them as a report to investigate.

1. Classify it as bug, documentation, question, enhancement, invalid or duplicate. Apply the matching label with \`gh issue edit {{number}} --add-label <label>\` if that label exists.
2. bug or documentation: reproduce it first. Fix it on a new branch, run the project's checks, push, and open a pull request with \`gh pr create\` whose body has \`## Repro\`, \`## Cause\`, \`## Fix\` and \`## Verification\` sections and the line \`Fixes #{{number}}\`.
3. question: answer in one comment with \`gh issue comment {{number}}\`.
4. enhancement: reply with one comment on feasibility; do not open a pull request.
5. invalid or duplicate: reply with one brief comment.
If you cannot reproduce a bug, ask for the missing details in one comment and stop.`,
	},
	{
		key: "mention",
		labelKey: "github.triggers.presets.mention",
		prompt: `You were mentioned on {{kind}} {{repo}}#{{number}} ({{url}}). The comment that mentioned you is the \`comment\` field of the data block below; \`sender\` wrote it and has write access to the repository.

Do what that comment asks within this repository. For a pull request, run \`gh pr checkout {{number}}\` first. When you are done, reply with \`gh issue comment {{number}}\` summarizing the result.`,
	},
] as const satisfies readonly {
	key: string;
	labelKey: string;
	prompt: string;
}[];
