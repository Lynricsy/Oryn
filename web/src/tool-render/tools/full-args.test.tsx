import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ToolView } from "../ToolView";
import type { ToolResultLike } from "../types";

/**
 * 折叠行摘要会被 CSS 省略号截断（窄屏下几十个字符就放不下），
 * 所以展开体必须给出完整参数，不能因为「头部已经写过」而省掉。
 */
function bodyText(
	name: string,
	args: Record<string, unknown>,
	result?: ToolResultLike,
): string {
	const { container } = render(
		<ToolView name={name} args={args} result={result} defaultOpen />,
	);
	const body = container.querySelector(".tv-body");
	expect(body).not.toBeNull();
	return body?.textContent ?? "";
}

const ok = (text = "done", details?: unknown): ToolResultLike => ({
	content: [{ type: "text", text }],
	details,
});

// 55 个字符：旧逻辑认为「折叠行放得下」而在展开体里省掉，但 390 宽的手机上摘要早已被省略号截断
const CMD = "cd web && bun run test -- src/realtime/socket.test.ts -u";
const SHORT = "reconnect backoff with jitter";

describe("展开体保留折叠行里可能被截断的完整参数", () => {
	it.each<[string, string, Record<string, unknown>, string[]]>([
		["bash 命令", "bash", { command: CMD }, [CMD]],
		["terminal 命令", "terminal", { action: "run", command: CMD }, [CMD]],
		[
			"grep 模式与范围",
			"grep",
			{ pattern: "afterSeq", path: "web/src/realtime" },
			["/afterSeq/", "web/src/realtime"],
		],
		[
			"ast_grep 模式与路径",
			"ast_grep",
			{ pat: "useEffect($$$)", path: "web/src/components" },
			["useEffect($$$)", "web/src/components"],
		],
		["recall 查询", "recall", { query: SHORT }, [SHORT]],
		["reflect 查询", "reflect", { query: SHORT }, [SHORT]],
		[
			"report_tool_issue 报告",
			"report_tool_issue",
			{ tool: "grep", report: SHORT },
			[SHORT],
		],
		["resolve 理由", "resolve", { action: "apply", reason: SHORT }, [SHORT]],
		["propose 计划标题", "propose", { title: SHORT }, [SHORT]],
		[
			"lsp 符号",
			"lsp",
			{ action: "references", file: "a.ts", symbol: "ReconnectingSocket" },
			["ReconnectingSocket"],
		],
		[
			"fetch 地址",
			"fetch",
			{ url: "https://example.com/docs/realtime/reconnect" },
			["https://example.com/docs/realtime/reconnect"],
		],
		["web_search 查询", "web_search", { query: SHORT }, [SHORT]],
		["goal 目标", "goal", { op: "create", objective: SHORT }, [SHORT]],
		[
			"generate_image 主体",
			"generate_image",
			{ subject: SHORT, aspect_ratio: "16:9" },
			[SHORT],
		],
		[
			"debug 程序",
			"debug",
			{ action: "launch", program: "bin/orynd", file: "cmd/main.go", line: 3 },
			["bin/orynd", "cmd/main.go"],
		],
	])("%s", (_label, name, args, expected) => {
		const text = bodyText(name, args, ok());
		for (const s of expected) expect(text).toContain(s);
	});
});

describe("github 展开体只省掉折叠行无损写出的参数", () => {
	it("PR 超过 3 个（折叠行写成 +N more）时展开体列出全部", () => {
		const pr = ["11", "12", "13", "14", "15"];
		const text = bodyText("github", { op: "pr_checkout", pr }, ok());
		expect(text).toContain("11, 12, 13, 14, 15");
	});

	it("超过 40 字符的分支式 PR 参数（折叠行被截断）展开体给出全文", () => {
		const pr = "feature/reconnect-with-exponential-backoff-and-replay";
		const text = bodyText("github", { op: "pr_push", pr }, ok());
		expect(text).toContain(pr);
	});

	it("搜索查询与仓库在展开体里完整给出", () => {
		const text = bodyText(
			"github",
			{ op: "search_issues", query: SHORT, repo: "can1357/oh-my-pi" },
			ok(),
		);
		expect(text).toContain(SHORT);
		expect(text).toContain("can1357/oh-my-pi");
	});
});

/** 展开体里以路径样式（`.tv-path`）给出的路径文字，按出现顺序。 */
function bodyPaths(
	name: string,
	args: Record<string, unknown>,
	result?: ToolResultLike,
): string[] {
	const { container } = render(
		<ToolView name={name} args={args} result={result} defaultOpen />,
	);
	return [...container.querySelectorAll(".tv-body .tv-path")].map(
		(el) => el.textContent ?? "",
	);
}

describe("read / edit / write 的展开体顶部给出完整文件路径", () => {
	const FILE = "web/src/components/transcript/MessageView.tsx";

	it("read：路径连同行范围", () => {
		expect(
			bodyPaths("read", { path: FILE, offset: 40, limit: 8 }, ok("x")),
		).toEqual([`${FILE}:40-47`]);
	});

	it("write：写入的文件路径", () => {
		expect(bodyPaths("write", { path: FILE, content: "x" }, ok())).toEqual([
			FILE,
		]);
	});

	it("edit：结果里没有逐文件 diff 时按文件列出路径", () => {
		expect(
			bodyPaths(
				"edit",
				{ path: FILE, edits: [{ op: "replace", sel: "42" }] },
				ok(),
			),
		).toEqual([FILE]);
		// 多文件补丁失败（没有 details）：每个文件一行
		const input =
			"[web/src/a.ts#AB12]\nreplace 1\n+x\n[web/src/b.ts]\nreplace 2\n+y";
		expect(
			bodyPaths(
				"edit",
				{ input },
				{ content: [{ type: "text", text: "no match" }], isError: true },
			),
		).toEqual(["web/src/a.ts", "web/src/b.ts"]);
	});

	it("edit：逐文件结果已带路径时不在顶部重复", () => {
		const diff = "@@ -1 +1 @@\n-a\n+b";
		expect(
			bodyPaths(
				"edit",
				{
					input: "[web/src/a.ts]\nreplace 1\n+b\n[web/src/b.ts]\nreplace 1\n+b",
				},
				ok("done", {
					perFileResults: [
						{ path: "web/src/a.ts", diff },
						{ path: "web/src/b.ts", diff },
					],
				}),
			),
		).toEqual(["web/src/a.ts", "web/src/b.ts"]);
	});
});
