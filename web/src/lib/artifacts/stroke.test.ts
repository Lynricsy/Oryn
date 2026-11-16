import { describe, expect, it } from "vitest";
import { strokePath } from "./stroke";

describe("strokePath", () => {
	it("没有点时为空串，一个点画成零长度的线", () => {
		expect(strokePath([])).toBe("");
		expect(strokePath([5, 6, 0.5])).toBe("M5 6L5 6");
	});

	it("两个点直接连线", () => {
		expect(strokePath([0, 0, 1, 10, 20, 1])).toBe("M0 0L10 20");
	});

	it("三个点以上用中点二次贝塞尔平滑，首尾落在原始端点上", () => {
		const d = strokePath([0, 0, 1, 10, 0, 1, 10, 10, 1, 0, 10, 1]);
		expect(d).toBe("M0 0L5 0Q10 0 10 5Q10 10 5 10L0 10");
	});

	it("坐标保留两位小数，忽略不完整的最后一组", () => {
		expect(strokePath([0.123, 1.005, 1, 2.3456, 3, 1, 9])).toBe(
			"M0.12 1L2.35 3",
		);
	});
});
