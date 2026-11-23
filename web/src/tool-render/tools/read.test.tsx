/**
 * read 卡片：带行号栏的代码（行号来自前缀 / displayContent，所选范围外的上下文弱化）、
 * 头部读数（只读了一段时 `已读 / 总行数`）、目录树、续读提示与出错。
 */
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ToolView } from "../ToolView";
import type { ToolResultLike } from "../types";

function ok(text: string, details?: unknown): ToolResultLike {
	return { content: [{ type: "text", text }], details };
}

function view(args: Record<string, unknown>, result?: ToolResultLike) {
	return render(
		<ToolView name="read" args={args} result={result} defaultOpen />,
	).container;
}

const rowNos = (c: HTMLElement) =>
	[...c.querySelectorAll(".tv-cl-row")].map(
		(r) => r.querySelector(".tv-cl-no")?.textContent ?? "",
	);

describe("read 卡片", () => {
	it("hashline 前缀给出行号，所选范围外 read 补的上下文弱化", () => {
		const c = view(
			{ path: "src/a.ts:2-3" },
			ok("[src/a.ts#7F3A]\n1:one\n2:two\n3:three\n4:four", {
				rowPrefixes: true,
				totalLines: 40,
			}),
		);
		expect(rowNos(c)).toEqual(["1", "2", "3", "4"]);
		const dim = [...c.querySelectorAll(".tv-cl-row--dim .tv-cl-no")].map(
			(n) => n.textContent,
		);
		expect(dim).toEqual(["1", "4"]);
		// 读数只数所选范围内的行，并给出总行数
		expect(c.querySelector(".tv-meta")?.textContent).toBe("2 / 40 行");
	});

	it("displayContent 的 null 行号是省略行", () => {
		const c = view(
			{ path: "src/a.ts" },
			ok("ignored", {
				displayContent: {
					text: "a\nb\nc",
					startLine: 1,
					lineNumbers: [1, null, 30],
				},
			}),
		);
		expect(rowNos(c)).toEqual(["1", "⋯", "30"]);
		expect(c.querySelector(".tv-cl-row--gap")).not.toBeNull();
	});

	it("没有前缀的纯文本从所选起始行顺序编号；续读提示收进附注", () => {
		const c = view(
			{ path: "notes.txt:10-11" },
			ok(
				"alpha\nbeta\n\n[30 more lines in file. Use notes.txt:12- to continue]",
				{
					rowPrefixes: false,
				},
			),
		);
		expect(rowNos(c)).toEqual(["10", "11"]);
		expect(c.querySelector(".tv-fnotes")?.textContent).toContain(
			"30 more lines in file",
		);
	});

	it("目录读成树：项数读数、目录加粗、`… N more` 弱化", () => {
		const c = view(
			{ path: "src" },
			ok(
				"src\n  - lib/                1m ago\n    - a.ts        1.2KB  1m ago\n  - … 3 more\n  - b.ts          96B  2d ago",
				{ isDirectory: true },
			),
		);
		expect(c.querySelectorAll(".tv-dir-row")).toHaveLength(4);
		expect(c.querySelector(".tv-dir-row--dir .tv-dir-name")?.textContent).toBe(
			"lib/",
		);
		expect(c.querySelector(".tv-dir-row--more")).not.toBeNull();
		expect(c.querySelector(".tv-dir-size")?.textContent).toBe("1.2KB");
		expect(c.querySelector(".tv-meta")?.textContent).toBe("3 项");
	});

	it("出错时文件头条之后是醒目的错误首行，没有读数", () => {
		const c = view(
			{ path: "src/missing.ts" },
			{
				content: [{ type: "text", text: "Path not found\nDid you mean x" }],
				isError: true,
			},
		);
		expect(c.querySelector(".tv-fh-path")?.textContent).toBe("src/missing.ts");
		expect(c.querySelector(".tv-errblock-head")?.textContent).toContain(
			"Path not found",
		);
		expect(c.querySelector(".tv-meta")?.textContent ?? "").toBe("");
	});

	it("残缺参数（流式生成中）不报错", () => {
		const c = view({ path: 42 } as unknown as Record<string, unknown>);
		expect(c.querySelector(".tv-card")).not.toBeNull();
	});
});
