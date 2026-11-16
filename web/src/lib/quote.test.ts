import { describe, expect, it } from "vitest";
import {
	composeQuoted,
	normalizeQuote,
	QUOTE_MAX,
	quoteBlock,
	splitLeadingQuote,
} from "./quote";

describe("normalizeQuote", () => {
	it("统一换行，去掉行尾空白与首尾空行，保留行首缩进与中间空行", () => {
		expect(normalizeQuote("\r\n\n  a  \r\n\tb\t\n\n  c \u00a0\n \n")).toBe(
			"  a\n\tb\n\n  c",
		);
	});

	it("只有空白时为空串", () => {
		expect(normalizeQuote(" \n\t\n")).toBe("");
	});

	it("恰好到上限不截；超出按码点截断并以 … 收尾，不切开 emoji", () => {
		const exact = "x".repeat(QUOTE_MAX);
		expect(normalizeQuote(exact)).toBe(exact);
		const long = `${"x".repeat(QUOTE_MAX - 1)}😀😀`;
		expect(normalizeQuote(long)).toBe(`${"x".repeat(QUOTE_MAX - 1)}😀…`);
		// 截断点前的空白不留在省略号前面；再规整一遍结果不变
		const spaced = `${"y".repeat(QUOTE_MAX - 2)}  tail`;
		const once = normalizeQuote(spaced);
		expect(once).toBe(`${"y".repeat(QUOTE_MAX - 2)}…`);
		expect(normalizeQuote(once)).toBe(once);
	});
});

describe("quoteBlock / composeQuoted", () => {
	it("每行加 `> `，空行只留 `>`，原有的 `>` 成为嵌套引用", () => {
		expect(quoteBlock("a\n\n> b\nc")).toBe("> a\n>\n> > b\n> c");
	});

	it("引用块、空行、回复；回复为空只有引用块", () => {
		expect(composeQuoted("one\ntwo", "why?")).toBe("> one\n> two\n\nwhy?");
		expect(composeQuoted("one", "")).toBe("> one");
	});
});

describe("splitLeadingQuote", () => {
	it("与 composeQuoted 互逆（多行、空行、嵌套）", () => {
		const quote = "line 1\n\n> inner\n  code";
		expect(splitLeadingQuote(composeQuoted(quote, "reply\nmore"))).toEqual({
			quote,
			rest: "reply\nmore",
		});
	});

	it("只认开头的引用；正文中间的 `>` 不拆", () => {
		expect(splitLeadingQuote("hi\n> not leading")).toBeNull();
		expect(splitLeadingQuote("a > b")).toBeNull();
	});

	it("没有回复时 rest 为空；引用里只有空白时不拆", () => {
		expect(splitLeadingQuote("> only\n\n")).toEqual({
			quote: "only",
			rest: "",
		});
		expect(splitLeadingQuote(">\n>  \nreply")).toBeNull();
	});

	it("兼容 `>` 后不带空格、至多 3 个空格缩进与 CRLF", () => {
		expect(splitLeadingQuote("  >a\r\n>b\r\n\r\nc")).toEqual({
			quote: "a\nb",
			rest: "c",
		});
	});
});
