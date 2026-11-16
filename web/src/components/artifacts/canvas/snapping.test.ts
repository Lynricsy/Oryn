import { describe, expect, it } from "vitest";
import { SNAP_PX, snapBox } from "./snapping";

const other = { x: 0, y: 0, width: 400, height: 300 };

describe("snapBox", () => {
	it("在阈值（6 屏幕像素）内吸附：5px 生效、7px 不生效", () => {
		for (const zoom of [1, 0.5, 2]) {
			const threshold = SNAP_PX / zoom;
			// 左边与对方右边相差 5 屏幕像素
			const near = { x: 400 + 5 / zoom, y: 1000, width: 200, height: 100 };
			const s = snapBox(near, [other], threshold);
			expect(s.dx).toBeCloseTo(-5 / zoom);
			expect(s.guides).toHaveLength(1);
			expect(s.guides[0]?.axis).toBe("x");
			expect(s.guides[0]?.pos).toBe(400);

			const far = { x: 400 + 7 / zoom, y: 1000, width: 200, height: 100 };
			const f = snapBox(far, [other], threshold);
			expect(f.dx).toBe(0);
			expect(f.guides).toHaveLength(0);
		}
	});

	it("中线也参与吸附，两个轴各取最近的一个", () => {
		const box = { x: 98, y: 145, width: 200, height: 10 };
		// 中线 x = 198 对齐对方中线 200；中线 y = 150 正好对齐
		const s = snapBox(box, [other], SNAP_PX);
		expect(s.dx).toBeCloseTo(2);
		expect(s.dy).toBeCloseTo(0);
		expect(s.guides.map((g) => g.axis).sort()).toEqual(["x", "y"]);
	});

	it("参考线覆盖对齐的全部矩形", () => {
		const box = { x: 403, y: 500, width: 100, height: 100 };
		const s = snapBox(box, [other], SNAP_PX);
		const g = s.guides[0];
		expect(g).toMatchObject({ axis: "x", pos: 400, from: 0, to: 600 });
	});

	it("缩放时只用指定的边", () => {
		const box = { x: 398, y: 1000, width: 100, height: 100 };
		// 只吸附右边（498）：离对方的线都超过阈值
		const s = snapBox(box, [other], SNAP_PX, { x: ["end"], y: [] });
		expect(s.dx).toBe(0);
		const left = snapBox(box, [other], SNAP_PX, { x: ["start"], y: [] });
		expect(left.dx).toBeCloseTo(2);
	});
});
