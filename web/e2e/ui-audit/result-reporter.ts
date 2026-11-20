/**
 * 结果终审：Playwright 在用例「完全结束」（含 fixture 清理）后才给出最终状态。
 * audit.ts 在用例体末尾写的是 `recorded`（未终审）；这里在 onTestEnd 里按最终状态改写：
 * 通过 → `done`；failed / timedOut / interrupted → `failed` 并记下原因（清理超时这类用例体之后的失败也算）。
 * 进程被直接杀掉、onTestEnd 没来得及跑的，留在 `recorded`，总清单不计为通过。
 */
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import type { Reporter, TestCase, TestResult } from "@playwright/test/reporter";

const PHASE = process.env.UI_AUDIT_PHASE === "before" ? "before" : "after";
const SHOTS = new URL("../../../docs/ui-audit/shots/", import.meta.url)
	.pathname;

export default class ResultReporter implements Reporter {
	onTestEnd(test: TestCase, result: TestResult) {
		if (result.status === "skipped") return;
		const project = test.parent.project()?.name ?? "";
		const dir = `${SHOTS}${test.title}/`;
		const file = `${dir}${project}-${PHASE}.json`;
		const prev: unknown = existsSync(file)
			? JSON.parse(readFileSync(file, "utf8"))
			: undefined;
		const issues =
			prev &&
			typeof prev === "object" &&
			"issues" in prev &&
			Array.isArray(prev.issues)
				? prev.issues
				: [];
		const recorded =
			prev &&
			typeof prev === "object" &&
			"status" in prev &&
			prev.status === "recorded";
		// 通过但用例体没写到 recorded（不该发生）也不升为 done
		const final =
			result.status === "passed" && recorded
				? { status: "done", issues }
				: {
						status: "failed",
						outcome: result.status,
						errors: result.errors.map((e) => (e.message ?? "").split("\n")[0]),
						issues,
					};
		mkdirSync(dir, { recursive: true });
		writeFileSync(
			file,
			`${JSON.stringify({ id: test.title, project, phase: PHASE, ...final }, null, "\t")}\n`,
		);
	}
}
