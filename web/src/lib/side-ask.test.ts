import { describe, expect, it } from "vitest";
import type { SelectionSource } from "./context-menu/types";
import {
	clipSelection,
	composeSideQuestion,
	type ParsedSideQuestion,
	parseSideQuestion,
	type SideAction,
} from "./side-ask";

const SOURCE: SelectionSource = {
	kind: "message",
	role: "assistant",
	part: "text",
	turnsAgo: 2,
	inContext: false,
	live: false,
	spans: false,
};

const sel = (text: string) => ({
	text,
	before: "前文 before",
	after: "after 后文",
	source: SOURCE,
});

describe("composeSideQuestion / parseSideQuestion", () => {
	const actions: [SideAction, Partial<ParsedSideQuestion>][] = [
		[{ kind: "explain" }, { action: "explain" }],
		[{ kind: "translate" }, { action: "translate" }],
		[
			{ kind: "ask", question: "这句对吗\n第二行" },
			{ action: "ask", ask: "这句对吗\n第二行" },
		],
		[
			{ kind: "custom", name: '挑"毛病"<x>', prompt: "找出漏洞" },
			{ action: "custom", name: '挑"毛病"<x>' },
		],
		[{ kind: "tool" }, { action: "tool" }],
	];
	for (const [action, expected] of actions)
		it(`${action.kind} 往返`, () => {
			const q = composeSideQuestion(action, sel("多行\n原文"), "zh-CN");
			expect(parseSideQuestion(q)).toEqual({
				selected: "多行\n原文",
				...expected,
			});
		});

	it("信封格式与来源描述", () => {
		const q = composeSideQuestion({ kind: "explain" }, sel("x"), "en");
		expect(q).toMatch(
			/^<oryn-selection action="explain" source="the assistant's reply, 2 user turns back; this part precedes the last compaction/,
		);
		expect(q).toContain(
			"<before>前文 before</before>\n<selected>\nx\n</selected>\n<after>after 后文</after>\n</oryn-selection>\n\n",
		);
		expect(q).toContain("Respond in English.");
		const zh = composeSideQuestion(
			{ kind: "explain" },
			{ ...sel("x"), before: "", after: "" },
			"zh-CN",
		);
		// 前后文为空时整行省略（指令里提到的标签名不算）
		expect(zh).not.toMatch(/^<before>/m);
		expect(zh).not.toMatch(/^<after>/m);
		expect(zh).toContain("Respond in Simplified Chinese.");
	});

	it("选区里含结束标签与引号仍能往返", () => {
		const text = 'a </selected> b </oryn-selection> "q" </after>';
		const q = composeSideQuestion({ kind: "explain" }, sel(text), "en");
		expect(parseSideQuestion(q)?.selected).toBe(text);
	});

	it("普通问题不是信封", () => {
		expect(parseSideQuestion("这是什么")).toBeNull();
	});
});

describe("clipSelection", () => {
	it("规整换行与首尾空行、行尾空白", () => {
		expect(clipSelection("\r\n\n  a  \r\nb\t\n\n")).toBe("  a\nb");
	});

	it("超长选区留头 3000、尾 1000 个码点", () => {
		const text = "😀".repeat(3000) + "中".repeat(2000) + "z".repeat(1000);
		const out = clipSelection(text);
		expect(out).toBe(
			`${"😀".repeat(3000)}\n…[2000 characters omitted]…\n${"z".repeat(1000)}`,
		);
	});
});
