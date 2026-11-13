import { describe, expect, it } from "vitest";
import { isNerdGlyph, parseTitleCard, withTitleCard } from "./title";

const NF = String.fromCodePoint(0xf0668);

describe("parseTitleCard", () => {
	it("拆出 emoji 图标、代码与标题", () => {
		expect(parseTitleCard("🧪 FLAKY: 修复不稳定的重连测试")).toEqual({
			icon: "🧪",
			code: "FLAKY",
			text: "修复不稳定的重连测试",
		});
	});

	it("拆出 Nerd Font 私有区图标（星形平面码点）", () => {
		expect(parseTitleCard(`${NF} WS: 重构 WebSocket 重连`)).toEqual({
			icon: NF,
			code: "WS",
			text: "重构 WebSocket 重连",
		});
	});

	it.each([
		"重构 WebSocket 重连与 afterSeq 补拉",
		"Fix BUG: crash on start",
		"🧪 flaky: 小写代码不算卡片",
		"🧪🧪 FLAKY: 两个图标不算卡片",
		"🧪 TOOLONG7: 代码超过 6 位",
	])("非卡片标题原样返回：%s", (raw) => {
		expect(parseTitleCard(raw)).toEqual({ text: raw });
	});
});

describe("withTitleCard", () => {
	it("改名时保留原卡片前缀", () => {
		expect(withTitleCard("🧪 FLAKY: 旧标题", "新标题")).toBe(
			"🧪 FLAKY: 新标题",
		);
	});
	it("原标题无卡片时只用新文字", () => {
		expect(withTitleCard("旧标题", "新标题")).toBe("新标题");
	});
});

describe("isNerdGlyph", () => {
	it("区分私有区字形与 emoji", () => {
		expect(isNerdGlyph(NF)).toBe(true);
		expect(isNerdGlyph("\ue0a0")).toBe(true);
		expect(isNerdGlyph("🧪")).toBe(false);
	});
});
