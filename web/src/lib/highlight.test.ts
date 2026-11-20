import { describe, expect, it } from "vitest";
import {
	ensureLanguage,
	highlightLinesSync,
	normalizeLanguage,
	splitHighlightedLines,
} from "./highlight";

describe("splitHighlightedLines", () => {
	it("跨行的 span 在行尾闭合、下一行重新打开，每行标签配平", () => {
		const html = 'a<span class="x">b\n<span class="y">c\nd</span>e</span>\nf';
		expect(splitHighlightedLines(html)).toEqual([
			'a<span class="x">b</span>',
			'<span class="x"><span class="y">c</span></span>',
			'<span class="x"><span class="y">d</span>e</span>',
			"f",
		]);
	});

	it("行数与原文按换行切分一致（含空行与末尾空行）", () => {
		expect(splitHighlightedLines("a\n\nb\n")).toEqual(["a", "", "b", ""]);
	});
});

describe("整段高亮再切行", () => {
	it("多行块注释的中间行仍带注释令牌", async () => {
		await ensureLanguage("javascript");
		const lines = highlightLinesSync("/* one\n two\n*/ const x = 1;", "js");
		expect(lines).toHaveLength(3);
		expect(lines?.[1]).toContain('class="hljs-comment"');
		expect(lines?.[2]).toContain('class="hljs-keyword"');
	});

	it("别名与原型属性名：ts → typescript，constructor 不是语言", () => {
		expect(normalizeLanguage("TS")).toBe("typescript");
		expect(normalizeLanguage("asm")).toBe("x86asm");
		expect(normalizeLanguage("constructor")).toBeNull();
		expect(normalizeLanguage("toString")).toBeNull();
	});
});
