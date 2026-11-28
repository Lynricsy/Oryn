/**
 * 改动面板「推送并开 PR」/「推送更新」（M14）发给 agent 的固定提示词。服务端不做确定性的 commit / push，
 * 全由 agent 在 orb 里用已登录的 git 与 `gh` 完成。文字与 docs/plan-m14.md 步骤 5.4 逐字一致。
 * 托管项目见 buildHostedShipPrompt（M24 阶段 ③）。
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

/**
 * 托管项目（M24 阶段 ③）：直接推上基线分支；推上基线后服务端自动把线程标为已交付。
 * rebase 之后跑一次完整检查再推送，挡住「文本上没冲突、合起来行为不对」的并发改动；实质性冲突交回主人裁决；
 * 浅克隆（项目可开 shallowClone）rebase 时会冒出大量无关的 add/add 冲突，先 unshallow 再来。
 * 推送被拒后只在解决冲突改了文件时重跑检查，避免并发交付时反复全量测试互相拖住。
 */
export function buildHostedShipPrompt(base: string): string {
	return [
		`Ship the current work to \`${base}\`:`,
		"1. Commit any uncommitted changes that belong to this task, with a clear message.",
		`2. Run \`git fetch origin\` and rebase your commits onto \`origin/${base}\`. If the rebase reports add/add conflicts in many files this task never touched, the clone is shallow: run \`git rebase --abort\`, \`git fetch --unshallow origin\`, and rebase again.`,
		"3. Resolve mechanical conflicts yourself. If a conflict is substantive (both sides changed the same behavior and keeping one side drops the other's intent), run `git rebase --abort` and ask me how to combine them before going any further.",
		`4. Run the project's full test suite or checks on the rebased commits. If something fails, check whether it also fails on \`origin/${base}\` without your commits; only such pre-existing failures may be ignored, and name them in your reply.`,
		`5. Push with \`git push origin HEAD:${base}\`. If it is rejected because \`${base}\` moved, repeat steps 2–5, re-running the checks only if resolving conflicts changed files; do not force-push.`,
		"6. Reply with the commits that were pushed (short SHA and subject) and the checks you ran with their result.",
	].join("\n");
}
