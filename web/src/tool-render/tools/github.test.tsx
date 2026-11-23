/**
 * github 卡片：搜索结果从 Core 的文本解析成列表（状态、编号、标题链接、作者与标签），读数给结果数；
 * 读文件按路径高亮并带 GitHub 原文链接；盯 CI 列出作业（失败步骤、时长），读数是进度格与失败数；残缺 details 不崩。
 */
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ToolView } from "../ToolView";
import type { ToolResultLike } from "../types";

const ok = (text: string, details?: unknown): ToolResultLike => ({
	content: [{ type: "text", text }],
	details,
});

const PRS = `# GitHub pull requests search

Query: reconnect
Repository: o/r
Results: 2

- #431 rpc: resume subscriptions
  Repo: o/r
  State: open
  Author: @lyn
  Labels: realtime, needs-review
  Updated: 2026-11-22T16:40:00Z
  URL: https://github.com/o/r/pull/431

- #402 ws: backoff
  State: merged
  URL: https://github.com/o/r/pull/402`;

describe("github 卡片", () => {
	it("搜索 PR：列表行带状态、编号、标题链接与标签，读数给结果数", () => {
		const { container } = render(
			<ToolView
				name="github"
				args={{ op: "search_prs", query: "reconnect", repo: "o/r" }}
				result={ok(PRS)}
				defaultOpen
			/>,
		);
		expect(container.querySelector(".tv-sum")).toHaveTextContent("搜索 PR");
		expect(container.querySelector(".tv-meta")).toHaveTextContent("2 条结果");
		const items = container.querySelectorAll(".tv-gh-item");
		expect(items).toHaveLength(2);
		expect(items[0]).toHaveTextContent("#431");
		expect(items[0]).toHaveTextContent("@lyn");
		expect(items[0]).toHaveTextContent("needs-review");
		expect(
			screen.getByRole("link", { name: "rpc: resume subscriptions" }),
		).toHaveAttribute("href", "https://github.com/o/r/pull/431");
		expect(screen.getByRole("img", { name: "已合并" })).toBeInTheDocument();
	});

	it("没有结果：空状态，读数标黄", () => {
		const { container } = render(
			<ToolView
				name="github"
				args={{ op: "search_repos", query: "nothing" }}
				result={ok(
					"# GitHub repositories search\n\nQuery: nothing\nResults: 0\n\nNo repositories found.",
				)}
				defaultOpen
			/>,
		);
		expect(container.querySelector(".tv-empty")).toHaveTextContent(
			"没有找到结果",
		);
		expect(container.querySelector(".tv-meta .tv-meta-warn")).not.toBeNull();
	});

	it("读文件：带行号的代码与 GitHub 原文链接，读数给行数", () => {
		const url = "https://github.com/o/r/blob/main/a.ts";
		const { container } = render(
			<ToolView
				name="github"
				args={{ op: "file_read", repo: "o/r", path: "a.ts" }}
				result={ok("const a = 1;\nexport { a };\n", {
					meta: { source: { type: "url", value: url } },
				})}
				defaultOpen
			/>,
		);
		expect(container.querySelector(".tv-meta")).toHaveTextContent("2 行");
		expect(screen.getByRole("link", { name: /GitHub/ })).toHaveAttribute(
			"href",
			url,
		);
		expect(container.querySelector(".tv-sec--out")).toHaveTextContent(
			"export { a };",
		);
	});

	it("盯 CI：作业按结论列出，失败作业写出失败步骤，读数是通过数与失败数", () => {
		const watch = {
			mode: "run",
			state: "completed",
			repo: "o/r",
			run: {
				id: 9,
				workflowName: "CI",
				conclusion: "failure",
				jobs: [
					{ id: 1, name: "lint", conclusion: "success", durationSeconds: 41 },
					{
						id: 2,
						name: "e2e",
						conclusion: "failure",
						steps: [
							{ number: 3, name: "Run playwright", conclusion: "failure" },
						],
					},
					{ id: 3, name: "build", conclusion: "skipped" },
				],
			},
		};
		const { container } = render(
			<ToolView
				name="github"
				args={{ op: "run_watch", run: "9" }}
				result={ok("failed", { watch })}
				defaultOpen
			/>,
		);
		const meta = container.querySelector(".tv-meta");
		expect(meta).toHaveTextContent("2/3");
		expect(meta).toHaveTextContent("1 个失败");
		expect(container.querySelector(".tv-gh-job--err")).toHaveTextContent(
			"Run playwright",
		);
		expect(container.querySelector(".tv-gh-job--skipped")).toHaveTextContent(
			"build",
		);
		expect(container.querySelector(".tv-gh-run-head")).toHaveTextContent(
			"失败",
		);
	});

	it.each<[string, Record<string, unknown>, ToolResultLike]>([
		["没有 op", {}, ok("x")],
		["op 不认识", { op: "nope" }, ok("x")],
		["watch 不是对象", { op: "run_watch" }, ok("x", { watch: "oops" })],
		[
			"作业残缺",
			{ op: "run_watch" },
			ok("x", { watch: { run: { jobs: [null, 3, { name: 7 }] } } }),
		],
		[
			"checkouts 残缺",
			{ op: "pr_checkout", pr: 3 },
			ok("x", { checkouts: [1] }),
		],
		["搜索文本不是 Core 的格式", { op: "search_code" }, ok("just text")],
	])("残缺输入不崩（%s）", (_label, args, result) => {
		expect(() =>
			render(
				<ToolView name="github" args={args} result={result} defaultOpen />,
			),
		).not.toThrow();
	});
});
