import { describe, expect, it } from "vitest";
import { niceScale, thin } from "./scale";

describe("niceScale", () => {
	it.each([
		[97, 4, { max: 200, step: 50 }],
		[80, 4, { max: 80, step: 20 }],
		[4, 4, { max: 4, step: 1 }],
		[0.37, 4, { max: 0.4, step: 0.1 }],
		[1234, 4, { max: 2000, step: 500 }],
		[3, 5, { max: 5, step: 1 }],
		[0.0042, 4, { max: 0.008, step: 0.002 }],
	])("max=%s ticks=%s → %o（1-2-5 取整，≥ 输入）", (max, ticks, want) => {
		expect(niceScale(max, ticks)).toEqual(want);
	});

	it("0、负数与非有限数给单位刻度", () => {
		expect(niceScale(0)).toEqual({ max: 4, step: 1 });
		expect(niceScale(-3)).toEqual({ max: 4, step: 1 });
		expect(niceScale(Number.NaN)).toEqual({ max: 4, step: 1 });
	});

	it("结果总能盖住输入，且 step 的首位数字是 1 / 2 / 5", () => {
		for (const v of [1, 7, 13, 99, 101, 555, 9999, 0.03, 12.5]) {
			const s = niceScale(v);
			expect(s.max).toBeGreaterThanOrEqual(v);
			expect(String(s.step).replace(/[0.]/g, "")[0]).toMatch(/[125]/);
		}
	});
});

describe("thin", () => {
	it("数量不超过上限时全显示", () => {
		expect(thin([1, 2, 3], 5)).toEqual([true, true, true]);
	});

	it("等间隔抽稀，首个必显示，不超过上限", () => {
		const shown = thin(
			Array.from({ length: 30 }, (_, i) => i),
			7,
		);
		expect(shown[0]).toBe(true);
		expect(shown.filter(Boolean).length).toBeLessThanOrEqual(7);
		// 每 5 个取 1 个
		expect(shown.map((v, i) => (v ? i : -1)).filter((i) => i >= 0)).toEqual([
			0, 5, 10, 15, 20, 25,
		]);
	});

	it("上限小于 1 时只显示第一个", () => {
		expect(thin([1, 2, 3], 0.4)).toEqual([true, false, false]);
	});
});
