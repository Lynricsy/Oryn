/**
 * 改动面板「推送并开 PR」/「推送更新」（M14）发给 agent 的固定提示词。服务端不做确定性的 commit / push，
 * 全由 agent 在 orb 里用已登录的 git 与 `gh` 完成。文字与 docs/plan-m14.md 步骤 5.4 逐字一致。
 */
export function buildShipPrompt(o: { base?: string; update: boolean }): string {
	if (o.update)
		return "Update the open pull request for this work: commit any uncommitted changes that belong to this task, push the branch to origin, and reply with what was pushed.";
	const base = o.base || "the base branch";
	return [
		"Ship the current work as a pull request:",
		"1. Commit any uncommitted changes that belong to this task, with a clear message.",
		`2. If HEAD is on \`${base}\`, create a new branch with a short descriptive name first.`,
		"3. Push the branch to origin.",
		`4. Open a pull request against \`${base}\` with \`gh pr create\`; the body should summarize what changed and how it was verified.`,
		"5. Reply with the pull request URL.",
	].join("\n");
}
