import { describe, expect, it } from "vitest";
import {
	type Crop,
	clampCrop,
	cropAtZoom,
	initialCrop,
	MAX_ZOOM,
	minCropSize,
	moveCrop,
	resizeFromCorner,
	zoomCrop,
	zoomOf,
} from "./avatar-crop";

const WIDE = { w: 400, h: 200 };
const TALL = { w: 150, h: 600 };
const SQUARE = { w: 300, h: 300 };

describe("initialCrop", () => {
	it.each([
		["横图", WIDE, { x: 100, y: 0, size: 200 }],
		["竖图", TALL, { x: 0, y: 225, size: 150 }],
		["方图", SQUARE, { x: 0, y: 0, size: 300 }],
	])("%s：居中的最大正方形", (_, d, want) => {
		expect(initialCrop(d)).toEqual(want);
	});
});

describe("minCropSize", () => {
	it("短边 / MAX_ZOOM，但不小于 32", () => {
		expect(minCropSize({ w: 1600, h: 800 })).toBe(800 / MAX_ZOOM);
		expect(minCropSize(WIDE)).toBe(32);
	});
	it("短边不足 32px 的图：等于短边", () => {
		expect(minCropSize({ w: 40, h: 20 })).toBe(20);
		expect(clampCrop({ x: 0, y: 0, size: 5 }, { w: 40, h: 20 }).size).toBe(20);
	});
});

describe("clampCrop", () => {
	it.each<[string, Crop, Crop]>([
		["左越界", { x: -10, y: 0, size: 100 }, { x: 0, y: 0, size: 100 }],
		["右越界", { x: 350, y: 0, size: 100 }, { x: 300, y: 0, size: 100 }],
		["上越界", { x: 10, y: -5, size: 100 }, { x: 10, y: 0, size: 100 }],
		["下越界", { x: 10, y: 150, size: 100 }, { x: 10, y: 100, size: 100 }],
		["过大", { x: 50, y: 0, size: 900 }, { x: 50, y: 0, size: 200 }],
		["过小", { x: 50, y: 10, size: 1 }, { x: 50, y: 10, size: 32 }],
	])("%s", (_, c, want) => {
		expect(clampCrop(c, WIDE)).toEqual(want);
	});
	it("范围内原样返回", () => {
		expect(clampCrop({ x: 20, y: 30, size: 64 }, WIDE)).toEqual({
			x: 20,
			y: 30,
			size: 64,
		});
	});
});

describe("moveCrop", () => {
	it("平移后夹回图内", () => {
		expect(moveCrop({ x: 100, y: 0, size: 200 }, 500, 9, WIDE)).toEqual({
			x: 200,
			y: 0,
			size: 200,
		});
		expect(moveCrop({ x: 100, y: 50, size: 100 }, -20, 10, WIDE)).toEqual({
			x: 80,
			y: 60,
			size: 100,
		});
	});
});

describe("zoomCrop", () => {
	it("不动点在缩放前后映射到同一相对位置", () => {
		const c: Crop = { x: 100, y: 0, size: 200 };
		const ax = 150;
		const ay = 120;
		const z = zoomCrop(c, 0.5, ax, ay, WIDE);
		expect(z.size).toBe(100);
		expect((ax - z.x) / z.size).toBeCloseTo((ax - c.x) / c.size);
		expect((ay - z.y) / z.size).toBeCloseTo((ay - c.y) / c.size);
		// 反向放大回去也回到原处
		const back = zoomCrop(z, 2, ax, ay, WIDE);
		expect(back.x).toBeCloseTo(c.x);
		expect(back.y).toBeCloseTo(c.y);
		expect(back.size).toBeCloseTo(c.size);
	});
	it("边长夹在 [minCropSize, 短边]", () => {
		const c = initialCrop(WIDE);
		expect(zoomCrop(c, 100, 0, 0, WIDE).size).toBe(200);
		expect(zoomCrop(c, 0.0001, 200, 100, WIDE).size).toBe(32);
	});
});

describe("zoomOf / cropAtZoom", () => {
	it("互逆，且以取景框中心为不动点", () => {
		const d = { w: 1600, h: 1000 };
		const c: Crop = { x: 300, y: 0, size: 1000 };
		for (const zoom of [1, 1.5, 2.25, 4, MAX_ZOOM]) {
			const z = cropAtZoom(c, zoom, d);
			expect(zoomOf(z, d)).toBeCloseTo(zoom);
			expect(z.x + z.size / 2).toBeCloseTo(c.x + c.size / 2);
			expect(z.y + z.size / 2).toBeCloseTo(c.y + c.size / 2);
		}
	});
	it("倍数夹到 [1, MAX_ZOOM]", () => {
		const d = { w: 1600, h: 1000 };
		const c = initialCrop(d);
		expect(zoomOf(cropAtZoom(c, 0.2, d), d)).toBe(1);
		expect(zoomOf(cropAtZoom(c, 99, d), d)).toBeCloseTo(MAX_ZOOM);
	});
});

describe("resizeFromCorner", () => {
	it("拖右下角：左上角不动，边长取较大的那一维", () => {
		const c: Crop = { x: 50, y: 20, size: 100 };
		expect(resizeFromCorner(c, "se", 130, 160, WIDE)).toEqual({
			x: 50,
			y: 20,
			size: 140,
		});
	});
	it("拖左上角：右下角不动，受图边限制", () => {
		const c: Crop = { x: 50, y: 20, size: 100 };
		// 右下角在 (150, 120)；往左上最多还剩 min(150, 120)
		expect(resizeFromCorner(c, "nw", -100, -100, WIDE)).toEqual({
			x: 30,
			y: 0,
			size: 120,
		});
	});
	it("拖过对角也不会小于最小边长", () => {
		const c: Crop = { x: 50, y: 20, size: 100 };
		expect(resizeFromCorner(c, "ne", 0, 500, WIDE)).toEqual({
			x: 50,
			y: 88,
			size: 32,
		});
	});
});
