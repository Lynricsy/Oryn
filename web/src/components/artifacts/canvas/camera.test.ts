import { describe, expect, it } from "vitest";
import {
	clampZoom,
	fitBox,
	MAX_ZOOM,
	MIN_ZOOM,
	panBy,
	reveal,
	stepZoom,
	toScreen,
	toWorld,
	wheelCamera,
	zoomAt,
} from "./camera";

describe("camera", () => {
	it("zoomAt 之后指针下的世界坐标不变", () => {
		const cam = { x: 37, y: -12, zoom: 0.8 };
		for (const [px, py, z] of [
			[100, 200, 2],
			[0, 0, 0.25],
			[640, 400, 4],
			[321.5, 17.25, 0.05],
		] as const) {
			const before = toWorld(cam, px, py);
			const after = toWorld(zoomAt(cam, z, px, py), px, py);
			expect(after.x).toBeCloseTo(before.x, 9);
			expect(after.y).toBeCloseTo(before.y, 9);
		}
	});

	it("缩放夹在 0.05–4 之间", () => {
		expect(clampZoom(10)).toBe(MAX_ZOOM);
		expect(clampZoom(0.001)).toBe(MIN_ZOOM);
		expect(zoomAt({ x: 0, y: 0, zoom: 1 }, 100, 0, 0).zoom).toBe(MAX_ZOOM);
	});

	it("toScreen 与 toWorld 互逆", () => {
		const cam = { x: 10, y: 20, zoom: 0.5 };
		const s = toScreen(cam, 300, -40);
		expect(toWorld(cam, s.x, s.y)).toEqual({ x: 300, y: -40 });
	});

	it("stepZoom 在缩放档之间跳，两档之间时跳到相邻的一档", () => {
		const at1 = { x: 0, y: 0, zoom: 1 };
		expect(stepZoom(at1, 1, 800, 600).zoom).toBe(2);
		expect(stepZoom(at1, -1, 800, 600).zoom).toBe(0.5);
		expect(stepZoom({ x: 0, y: 0, zoom: 0.7 }, 1, 800, 600).zoom).toBe(1);
		expect(stepZoom({ x: 0, y: 0, zoom: 0.7 }, -1, 800, 600).zoom).toBe(0.5);
		expect(stepZoom({ x: 0, y: 0, zoom: 4 }, 1, 800, 600).zoom).toBe(4);
	});

	it("fitBox 把盒子居中放进画布区，不超过 maxZoom", () => {
		const box = { x: 100, y: 100, width: 1000, height: 500 };
		const cam = fitBox(box, 1200, 800);
		expect(cam.zoom).toBe(1);
		const tl = toScreen(cam, box.x, box.y);
		const br = toScreen(cam, box.x + box.width, box.y + box.height);
		expect(tl.x + br.x).toBeCloseTo(1200);
		expect(tl.y + br.y).toBeCloseTo(800);
		const small = fitBox({ x: 0, y: 0, width: 4000, height: 1000 }, 1200, 800);
		expect(small.zoom).toBeCloseTo((1200 - 128) / 4000);
	});

	it("reveal：已可见不动，否则平移最少距离", () => {
		const cam = { x: 0, y: 0, zoom: 1 };
		expect(
			reveal(cam, { x: 100, y: 100, width: 100, height: 100 }, 800, 600),
		).toBe(cam);
		const moved = reveal(
			cam,
			{ x: 900, y: 100, width: 100, height: 100 },
			800,
			600,
		);
		expect(moved.zoom).toBe(1);
		expect(toScreen(moved, 1000, 100).x).toBe(800 - 64);
	});

	it("滚轮平移，Ctrl / ⌘ 滚轮以指针为中心缩放", () => {
		const cam = { x: 0, y: 0, zoom: 1 };
		const wheel = {
			deltaX: 10,
			deltaY: 20,
			deltaMode: 0,
			ctrlKey: false,
			metaKey: false,
		};
		expect(wheelCamera(cam, wheel, 0, 0)).toEqual(panBy(cam, -10, -20));
		expect(wheelCamera(cam, { ...wheel, deltaMode: 1 }, 0, 0)).toEqual(
			panBy(cam, -160, -320),
		);
		const zoomed = wheelCamera(cam, { ...wheel, ctrlKey: true }, 200, 100);
		expect(zoomed.zoom).toBeLessThan(1);
		const w = toWorld(zoomed, 200, 100);
		expect(w.x).toBeCloseTo(200);
		expect(w.y).toBeCloseTo(100);
	});
});
