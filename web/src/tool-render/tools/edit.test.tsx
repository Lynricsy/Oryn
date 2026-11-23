/**
 * edit / write / ast_edit / resolve：带号 diff 的解析（跳过的未改动行补成省略行）、逐文件小节、
 * 补丁输入的分段、预览横幅与结局横幅。
 */
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { parseDiagnostics, parseDiff } from "../parts";
import { ToolView } from "../ToolView";
import type { ToolResultLike } from "../types";

function ok(text: string, details?: unknown): ToolResultLike {
	return { content: [{ type: "text", text }], details };
}

describe("parseDiff", () => {
	it("omp 的带号 diff：删除行用旧行号，行号跳跃处补上省略行及其行数", () => {
		const rows = parseDiff(" 1|a\n-2|b\n+2|B\n 3|c\n 40|x\n+41|y");
		expect(rows.map((r) => [r.kind, r.no ?? r.count])).toEqual([
			["ctx", 1],
			["del", 2],
			["add", 2],
			["ctx", 3],
			["gap", 36],
			["ctx", 40],
			["add", 41],
		]);
	});

	it("统一 diff 按 `@@` 头推算行号，分段头显示所在上下文", () => {
		const rows = parseDiff("@@ -10,2 +10,3 @@ class A {\n x\n-y\n+Y\n+Z");
		expect(rows[0]).toEqual({ kind: "hunk", text: "class A {" });
		expect(rows.slice(1).map((r) => [r.kind, r.no])).toEqual([
			["ctx", 10],
			["del", 11],
			["add", 11],
			["add", 12],
		]);
	});
});

describe("parseDiagnostics", () => {
	it("平铺与分组两种格式，来源与代码拆出", () => {
		const { diags, status } = parseDiagnostics(
			"1 error(s):\n# src/\n## a.ts\n  12:5 [error] [ts] Cannot find name 'x' (2304)\nsrc/b.ts:3:1 [warning] unused\n✓ src/c.ts: no issues",
		);
		expect(diags).toEqual([
			{
				file: "src/a.ts",
				line: 12,
				col: 5,
				severity: "error",
				message: "Cannot find name 'x'",
				source: "ts",
				code: "2304",
			},
			{
				file: "src/b.ts",
				line: 3,
				col: 1,
				severity: "warning",
				message: "unused",
				source: null,
				code: null,
			},
		]);
		expect(status).toEqual([
			{ file: "src/c.ts", tone: "ok", text: "no issues" },
		]);
	});
});

describe("edit / write / ast_edit / resolve 卡片", () => {
	it("hashline 补丁输入按文件分段，操作行是分段头；结果里的绝对路径按补丁写法显示", () => {
		const { container } = render(
			<ToolView
				name="edit"
				args={{ input: "[src/a.ts#AB12]\nPUT 3.=3:\n+x" }}
				result={ok("[src/a.ts#CD34]\n3:x", {
					diff: " 2|w\n-3|old\n+3|x",
					path: "/repo/src/a.ts",
					firstChangedLine: 3,
				})}
				defaultOpen
			/>,
		);
		const input = container.querySelector(".tv-sec--in");
		expect(input?.querySelector(".tv-cl-row--hunk")?.textContent).toContain(
			"PUT 3.=3:",
		);
		const out = container.querySelector(".tv-sec--out");
		expect(out?.querySelector(".tv-fh-path")?.textContent).toBe("src/a.ts:3");
		expect(container.querySelector(".tv-meta")?.textContent).toBe("+1−1");
	});

	it("write 覆盖：结局写「覆盖原文件」与字节数，附上 diff", () => {
		const { container } = render(
			<ToolView
				name="write"
				args={{ path: "a.txt", content: "A\nB\n" }}
				result={ok("Successfully wrote 4 bytes to a.txt", {
					overwritten: true,
					diff: " 1|A\n-2|b\n+2|B",
				})}
				defaultOpen
			/>,
		);
		expect(container.querySelector(".tv-wstat")?.textContent).toBe(
			"覆盖原文件4 B",
		);
		expect(
			container.querySelectorAll(".tv-sec--out .tv-cl-row--add"),
		).toHaveLength(1);
	});

	it("ast_edit 预览：标明文件尚未修改，逐文件列出改动", () => {
		const { container } = render(
			<ToolView
				name="ast_edit"
				args={{ ops: [{ pat: "a($X)", out: "b($X)" }], path: "src" }}
				result={ok(
					"Staged as a proposal — files NOT modified yet.\n\n# src/\n## a.ts#7F3A (1 replacement)\n-3:a(1)\n+3:b(1)",
					{
						totalReplacements: 1,
						filesTouched: 1,
						applied: false,
						fileReplacements: [{ path: "src/a.ts", count: 1 }],
					},
				)}
				defaultOpen
			/>,
		);
		expect(container.querySelector(".tv-stage")).not.toBeNull();
		expect(
			container.querySelector(".tv-sec--out .tv-fh-path")?.textContent,
		).toBe("src/a.ts");
		expect(container.querySelector(".tv-meta")?.textContent).toContain("预览");
	});

	it("resolve 应用 / reject 放弃两种结局的横幅与来源工具", () => {
		const apply = render(
			<ToolView
				name="resolve"
				args={{ reason: "ok" }}
				result={ok("Applied 1 replacement in 1 file.", {
					action: "apply",
					reason: "ok",
					sourceToolName: "ast_edit",
					label: "AST Edit: 1 replacement in 1 file",
				})}
				defaultOpen
			/>,
		).container;
		expect(
			apply.querySelector(".tv-outcome--apply .tv-outcome-title")?.textContent,
		).toBe("已应用 ast_edit 的预览");
		expect(apply.querySelector(".tv-outcome-sub")?.textContent).toBe(
			"1 replacement in 1 file",
		);
		const discard = render(
			<ToolView
				name="reject"
				args={{ reason: "no" }}
				result={ok("Discarded.", {
					action: "discard",
					reason: "no",
					sourceToolName: "ast_edit",
				})}
				defaultOpen
			/>,
		).container;
		expect(discard.querySelector(".tv-outcome--discard")).not.toBeNull();
		expect(discard.querySelector(".tv-meta")?.textContent).toBe("已放弃");
	});
});
