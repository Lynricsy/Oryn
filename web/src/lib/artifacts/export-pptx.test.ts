import { describe, expect, it } from "vitest";
import {
	exportPptx,
	fitContain,
	PPTX_BODY_PT,
	PPTX_FONT,
	pptxPlan,
} from "./export-pptx";
import { EIGHT_LAYOUT_DECK, tinyPng } from "./slides-fixture";

describe("pptxPlan", () => {
	const plan = pptxPlan(EIGHT_LAYOUT_DECK);

	it("八版式夹具得到 8 张，页面 10×5.625 英寸，颜色取浅色调色板", () => {
		expect(plan.slides).toHaveLength(8);
		expect(plan.slides.map((s) => s.layout)).toEqual(
			EIGHT_LAYOUT_DECK.slides.map((s) => s.layout),
		);
		expect([plan.width, plan.height]).toEqual([10, 5.625]);
		expect(plan.colors.background).toBe("ffffff");
		expect(PPTX_FONT).toBe("Arial");
	});

	it("4:3 与深色主题", () => {
		const p = pptxPlan({ ...EIGHT_LAYOUT_DECK, aspect: "4:3", theme: "dark" });
		expect([p.width, p.height]).toEqual([10, 7.5]);
		expect(p.colors.background).toBe("0f0f11");
	});

	it("bullets 带图时图片框在右半边、要点在左半边", () => {
		// 第 2 张是带图的 bullets
		const items = plan.slides[1]?.items ?? [];
		const img = items.find((i) => i.kind === "image");
		const body = items.find((i) => i.kind === "text" && i.role === "body");
		expect(img?.kind === "image" && img.path).toBe("images/launch.png");
		if (img?.kind !== "image" || body?.kind !== "text") throw new Error("缺项");
		expect(img.box.x).toBeGreaterThanOrEqual(plan.width / 2);
		expect(img.box.x + img.box.w).toBeLessThanOrEqual(plan.width - 0.6 + 1e-9);
		expect(body.box.x + body.box.w).toBeLessThanOrEqual(plan.width / 2);
		expect(body.size).toBe(PPTX_BODY_PT);
		expect(body.paragraphs).toEqual([
			{ text: "发布 2.0", bullet: 0 },
			{ text: "性能", bullet: 0 },
			{ text: "首屏 -40%", bullet: 1 },
			{ text: "内存 -25%", bullet: 1 },
		]);
	});

	it("讲者备注：第 1 张等于夹具的 notes，没写的没有", () => {
		expect(plan.slides[0]?.notes).toBe(EIGHT_LAYOUT_DECK.slides[0]?.notes);
		expect(plan.slides[0]?.notes).toBeTruthy();
		expect(plan.slides[2]?.notes).toBeUndefined();
	});

	it("每张的必填内容都摆上了，且都在页面以内", () => {
		const all = plan.slides.map((s) =>
			s.items.flatMap((i) =>
				i.kind === "text"
					? i.paragraphs.map((p) => p.text)
					: i.kind === "table"
						? [...i.columns, ...i.rows.flat()]
						: i.kind === "image"
							? [i.path]
							: [],
			),
		);
		expect(all[0]).toEqual(["2026 年第三季度回顾", "产品组 · 10 月"]);
		expect(all[3]).toEqual(
			expect.arrayContaining([
				"方案 A",
				"便宜",
				"方案 B",
				"贵一些，但快三倍。",
			]),
		);
		expect(all[4]).toEqual(["月活", "images/chart.png", "数据截至 9 月 30 日"]);
		expect(all[5]).toEqual(["“先做对，再做快。”", "— 团队守则"]);
		expect(all[6]).toEqual(["核心指标", "42%", "留存", "1.2M", "月活"]);
		expect(all[7]).toEqual([
			"排期",
			"阶段",
			"时间",
			"设计",
			"10 月",
			"开发",
			"11 月",
		]);
		for (const s of plan.slides)
			for (const i of s.items) {
				expect(i.box.x).toBeGreaterThanOrEqual(0);
				expect(i.box.y).toBeGreaterThanOrEqual(0);
				expect(i.box.x + i.box.w).toBeLessThanOrEqual(plan.width + 1e-9);
				expect(i.box.y + i.box.h).toBeLessThanOrEqual(plan.height + 1e-9);
			}
	});

	it("fitContain 等比缩放并居中", () => {
		expect(fitContain({ x: 1, y: 1, w: 4, h: 4 }, 200, 100)).toEqual({
			x: 1,
			y: 2,
			w: 4,
			h: 2,
		});
	});
});

describe("exportPptx", () => {
	it("生成非空的 .pptx；取不到的图片记进 missing", async () => {
		const { blob, missing } = await exportPptx({
			title: "季度回顾",
			deckText: JSON.stringify(EIGHT_LAYOUT_DECK),
			readFile: async (path) => {
				if (path === "images/launch.png") return tinyPng(800, 600);
				throw new Error("not found");
			},
		});
		expect(blob.size).toBeGreaterThan(0);
		expect(missing).toEqual(["images/chart.png"]);
	});

	it("slides.json 解析失败时抛错", async () => {
		await expect(
			exportPptx({
				title: "x",
				deckText: "{",
				readFile: async () => new Uint8Array(),
			}),
		).rejects.toThrow();
	});
});
