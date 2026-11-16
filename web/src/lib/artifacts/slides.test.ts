import { describe, expect, it } from "vitest";
import {
	DECK_PALETTES,
	type Deck,
	parseDeck,
	slideSize,
	textAt,
	textPaths,
	withText,
} from "./slides";
import { EIGHT_LAYOUT_DECK } from "./slides-fixture";

const text = (d: unknown) => JSON.stringify(d);

describe("parseDeck", () => {
	it("八种版式的夹具通过", () => {
		const r = parseDeck(text(EIGHT_LAYOUT_DECK));
		expect(r.ok).toBe(true);
	});

	it("报告位置与原因，与服务端同口径", () => {
		const bad = {
			theme: "blue",
			slides: [
				{ layout: "title" },
				{ layout: "stats", stats: [{ value: 42 }] },
				{ layout: "table", columns: ["a", "b"], rows: [["1"]] },
				{ layout: "quote", quote: "x", color: "red" },
				{ layout: "nope" },
				{ layout: "two-column", left: {}, right: { text: "y" } },
				{
					layout: "bullets",
					title: "t",
					bullets: [{ text: "a", bullets: [{ text: "deep" }] }],
				},
			],
		};
		const r = parseDeck(text(bad));
		expect(r.ok).toBe(false);
		if (r.ok) return;
		expect(r.problems).toEqual(
			expect.arrayContaining([
				{ where: "theme", issue: "enum" },
				{ where: "slides[0].title", issue: "required" },
				{ where: "slides[1].stats[0].value", issue: "string" },
				{ where: "slides[2].rows[0]", issue: "cells", limit: 2 },
				{ where: "slides[3].color", issue: "unknown" },
				{ where: "slides[4].layout", issue: "layout" },
				{ where: "slides[5].left", issue: "column_empty" },
				{ where: "slides[6].bullets[0].bullets[0]", issue: "string" },
			]),
		);
	});

	it("上限按码点计", () => {
		const r = parseDeck(
			text({ slides: [{ layout: "title", title: "字".repeat(201) }] }),
		);
		expect(r).toEqual({
			ok: false,
			problems: [{ where: "slides[0].title", issue: "too_long", limit: 200 }],
		});
		expect(parseDeck("{").ok).toBe(false);
		expect(parseDeck(text({ slides: [] }))).toEqual({
			ok: false,
			problems: [{ where: "slides", issue: "too_few", limit: 1 }],
		});
	});
});

describe("尺寸与调色板", () => {
	it("16:9 与 4:3", () => {
		expect(slideSize("16:9")).toEqual({ width: 1280, height: 720 });
		expect(slideSize(undefined)).toEqual({ width: 1280, height: 720 });
		expect(slideSize("4:3")).toEqual({ width: 960, height: 720 });
		expect(DECK_PALETTES.dark.background).toBe("#0f0f11");
		expect(DECK_PALETTES.light.accent).toBe("#2563eb");
	});
});

describe("textPaths 与 withText", () => {
	it("列出各版式可编辑的文字位置", () => {
		const [title, bullets, , two, image, quote, stats, table] =
			EIGHT_LAYOUT_DECK.slides;
		if (!title || !bullets || !two || !image || !quote || !stats || !table)
			throw new Error("fixture");
		expect(textPaths(title)).toEqual(["title", "subtitle"]);
		expect(textPaths(bullets)).toEqual([
			"title",
			"bullets[0]",
			"bullets[1].text",
			"bullets[1].bullets[0]",
			"bullets[1].bullets[1]",
		]);
		expect(textPaths(two)).toEqual([
			"title",
			"left.heading",
			"left.bullets[0]",
			"left.bullets[1]",
			"right.heading",
			"right.text",
		]);
		expect(textPaths(image)).toEqual(["title", "caption"]);
		expect(textPaths(quote)).toEqual(["quote", "attribution"]);
		expect(textPaths(stats)).toEqual([
			"title",
			"stats[0].value",
			"stats[0].label",
			"stats[1].value",
			"stats[1].label",
		]);
		expect(textPaths(table)).toEqual([
			"title",
			"columns[0]",
			"columns[1]",
			"rows[0][0]",
			"rows[0][1]",
			"rows[1][0]",
			"rows[1][1]",
		]);
		// 每个位置都能读到文字
		EIGHT_LAYOUT_DECK.slides.forEach((s, i) => {
			for (const p of textPaths(s))
				expect(textAt(EIGHT_LAYOUT_DECK, `slides[${i}].${p}`)).toBeTypeOf(
					"string",
				);
		});
	});

	it("改 slides[1].bullets[0] 后其它字段不变", () => {
		const before = structuredClone(EIGHT_LAYOUT_DECK);
		const next = withText(
			EIGHT_LAYOUT_DECK,
			"slides[1].bullets[0]",
			"发布 2.1",
		);
		expect(EIGHT_LAYOUT_DECK).toEqual(before);
		expect(textAt(next, "slides[1].bullets[0]")).toBe("发布 2.1");
		const expected: Deck = structuredClone(before);
		const slide = expected.slides[1];
		if (slide?.layout !== "bullets") throw new Error("fixture");
		slide.bullets[0] = "发布 2.1";
		expect(next).toEqual(expected);
		// 没改到的分支原样共用
		expect(next.slides[0]).toBe(EIGHT_LAYOUT_DECK.slides[0]);
	});

	it("位置不存在、不是字符串或文字没变时原样返回", () => {
		const d = EIGHT_LAYOUT_DECK;
		expect(withText(d, "slides[1].bullets[9]", "x")).toBe(d);
		expect(withText(d, "slides[1].bullets[1]", "x")).toBe(d);
		expect(withText(d, "slides[0].title", "2026 年第三季度回顾")).toBe(d);
		expect(withText(d, "slides[0]..title", "x")).toBe(d);
		expect(withText(d, "slides[7].rows[1][0]", "测试").slides[7]).toEqual({
			...d.slides[7],
			rows: [
				["设计", "10 月"],
				["测试", "11 月"],
			],
		});
	});
});
