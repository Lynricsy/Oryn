/**
 * grep / ast_grep / glob：分组输出的解析（`#` 目录头、hashline 文件头、`*` 命中行、`│` 行号栏、旧的
 * `path:12:code`），逐文件分组的卡片、空状态与截断读数。
 */
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { parseGrouped } from "../parts";
import { ToolView } from "../ToolView";
import type { ToolResultLike } from "../types";

function ok(text: string, details?: unknown): ToolResultLike {
	return { content: [{ type: "text", text }], details };
}

describe("parseGrouped", () => {
	it("目录头拼出完整路径，快照标签去掉，命中 / 上下文 / 间隔分开", () => {
		const { groups, notes } = parseGrouped(
			"# src/\n## a.ts#7F3A\n 11:ctx\n*12:hit\n...\n*40:again\n## lib/\n### b.ts#C0DE\n*3:x\n\nShowing files 1-2 of 5. Use skip=2 for the next page.",
		);
		expect(groups.map((g) => g.path)).toEqual(["src/a.ts", "src/lib/b.ts"]);
		expect(groups[0]?.rows.map((r) => r.kind)).toEqual([
			"ctx",
			"hit",
			"gap",
			"hit",
		]);
		expect(groups[0]?.rows[1]?.no).toBe(12);
		expect(notes).toEqual([
			"Showing files 1-2 of 5. Use skip=2 for the next page.",
		]);
	});

	it("displayContent 的 `│` 行号栏与 ast_edit 的增删行、文件头附注", () => {
		const { groups } = parseGrouped(
			"# src/\n## a.ts (2 replacements)\n  -12│old()\n  +12│new()\n    │...",
		);
		expect(groups[0]?.path).toBe("src/a.ts");
		expect(groups[0]?.label).toBe("2 replacements");
		expect(groups[0]?.rows.map((r) => r.kind)).toEqual(["del", "add", "gap"]);
	});

	it("旧格式 `path:12:code` 每行都是命中，按路径分组", () => {
		const { groups } = parseGrouped(
			"a/x.ts:3:one\na/x.ts:9:two\nb/y.ts:1:three",
		);
		expect(groups.map((g) => [g.path, g.rows.length])).toEqual([
			["a/x.ts", 2],
			["b/y.ts", 1],
		]);
		expect(groups.every((g) => g.rows.every((r) => r.kind === "hit"))).toBe(
			true,
		);
	});
});

describe("grep / glob 卡片", () => {
	it("按文件分组：多个文件时文件头条带各自的命中数，截断时头部警示", () => {
		const { container } = render(
			<ToolView
				name="grep"
				args={{ pattern: "foo", path: "src" }}
				result={ok("# src/\n## a.ts\n*1:foo\n*5:foo()\n## b.ts\n*2:foo", {
					matchCount: 3,
					fileCount: 2,
					truncated: true,
				})}
				defaultOpen
			/>,
		);
		const heads = [...container.querySelectorAll(".tv-fh")].map(
			(h) => h.textContent,
		);
		expect(heads).toEqual(["src/a.ts2 处", "src/b.ts1 处"]);
		expect(container.querySelectorAll(".tv-cl-row--hit")).toHaveLength(3);
		expect(container.querySelector(".tv-meta .tv-meta-warn")?.textContent).toBe(
			"已截断",
		);
	});

	it("没有命中：空状态写工具的原话，读数是 0 处（警示）", () => {
		const { container } = render(
			<ToolView
				name="grep"
				args={{ pattern: "nope" }}
				result={ok("No matches found", { matchCount: 0, fileCount: 0 })}
				defaultOpen
			/>,
		);
		expect(container.querySelector(".tv-empty")?.textContent).toBe(
			"No matches found",
		);
		expect(container.querySelector(".tv-meta-warn")?.textContent).toBe("0 处");
	});

	it("glob 的结果按目录分组，保持工具给的顺序", () => {
		const { container } = render(
			<ToolView
				name="glob"
				args={{ path: "**/*.ts" }}
				result={ok("# src/\nb.ts\na.ts\n\nroot.ts", {
					fileCount: 3,
					files: ["src/b.ts", "src/a.ts", "root.ts"],
				})}
				defaultOpen
			/>,
		);
		expect(
			[...container.querySelectorAll(".tv-ptree-dirname")].map(
				(d) => d.textContent,
			),
		).toEqual(["src/"]);
		expect(
			[...container.querySelectorAll(".tv-ptree-file")].map(
				(f) => f.textContent,
			),
		).toEqual(["b.ts", "a.ts", "root.ts"]);
	});
});
