import { describe, expect, it } from "vitest";
import {
	checkBudget,
	fitWithin,
	MAX_LONG_SIDE,
	MESSAGE_IMAGE_BUDGET,
} from "./images";

describe("图片压缩预算", () => {
	it("长边压到 2000px 并保持比例", () => {
		expect(fitWithin(4000, 3000)).toEqual({
			width: MAX_LONG_SIDE,
			height: 1500,
		});
		expect(fitWithin(1200, 5000)).toEqual({
			width: 480,
			height: MAX_LONG_SIDE,
		});
		expect(fitWithin(800, 600)).toEqual({ width: 800, height: 600 });
	});

	it("整条消息内联图片总量 ≤ 900KB", () => {
		expect(MESSAGE_IMAGE_BUDGET).toBe(900 * 1024);
		const half = MESSAGE_IMAGE_BUDGET / 2;
		expect(checkBudget([], half)).toEqual({
			ok: true,
			total: half,
			remaining: MESSAGE_IMAGE_BUDGET,
		});
		expect(checkBudget([half], half).ok).toBe(true);
		const over = checkBudget([half, 100_000], half);
		expect(over.ok).toBe(false);
		expect(over.remaining).toBe(MESSAGE_IMAGE_BUDGET - half - 100_000);
		expect(checkBudget([MESSAGE_IMAGE_BUDGET], 1)).toMatchObject({
			ok: false,
			remaining: 0,
		});
	});
});
