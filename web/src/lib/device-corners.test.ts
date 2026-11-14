/** 屏幕圆角同心：视口角半径、贴边公式、CSS 变量注入与移动壳下的安装。 */
import { describe, expect, it, vi } from "vitest";
import {
	applyDeviceCorners,
	concentricRadius,
	installDeviceCorners,
	viewportCornerRadii,
} from "./device-corners";
import type { DeviceGeometry, Host, MobileHost, MobileInfo } from "./host";

const R40 = { tl: 40, tr: 40, bl: 40, br: 40 };
const NO_INSETS = { top: 0, right: 0, bottom: 0, left: 0 };

describe("viewportCornerRadii", () => {
	it("屏幕半径减去相邻两边 inset 的较大者，不小于 0", () => {
		expect(
			viewportCornerRadii({
				cornerRadii: R40,
				insets: { top: 24, right: 0, bottom: 16, left: 30 },
			}),
		).toEqual({ tl: 10, tr: 16, bl: 10, br: 24 });
		// 软键盘弹出：底边 inset 远大于圆角 → 下两角为 0
		expect(
			viewportCornerRadii({
				cornerRadii: R40,
				insets: { top: 0, right: 0, bottom: 300, left: 0 },
			}),
		).toEqual({ tl: 40, tr: 40, bl: 0, br: 0 });
		expect(
			viewportCornerRadii({
				cornerRadii: { tl: 0, tr: 0, bl: 0, br: 0 },
				insets: NO_INSETS,
			}),
		).toEqual({ tl: 0, tr: 0, bl: 0, br: 0 });
	});
});

describe("concentricRadius", () => {
	it("max(令牌, 视口角半径 − 间距)：设备为 0 时就是令牌", () => {
		expect(concentricRadius(36, 0, 8)).toBe(36);
		expect(concentricRadius(36, 40, 8)).toBe(36);
		expect(concentricRadius(32, 56, 12)).toBe(44);
		expect(concentricRadius(0, 40, 0)).toBe(40);
	});
});

describe("applyDeviceCorners / installDeviceCorners", () => {
	const vars = (el: HTMLElement) =>
		["tl", "tr", "bl", "br"].map((k) =>
			el.style.getPropertyValue(`--device-r-${k}`),
		);

	it("写入四个 CSS 变量（整数 px）", () => {
		const el = document.createElement("div");
		applyDeviceCorners(el, {
			cornerRadii: { tl: 40.4, tr: 40, bl: 40, br: 40 },
			insets: { top: 0, right: 0, bottom: 16, left: 0 },
		});
		expect(vars(el)).toEqual(["40px", "40px", "24px", "24px"]);
	});

	function fakeHost(info: Partial<MobileInfo> | null) {
		let cb: ((g: DeviceGeometry) => void) | null = null;
		const h = {
			mobile: info
				? ({
						info: vi.fn(async () => info as MobileInfo),
					} as unknown as MobileHost)
				: null,
			onInsets: (f: (g: DeviceGeometry) => void) => {
				cb = f;
				return () => {
					cb = null;
				};
			},
		} as unknown as Host;
		return { h, emit: (g: DeviceGeometry) => cb?.(g) };
	}

	it("非移动宿主不做任何事", () => {
		const el = document.createElement("div");
		const { h } = fakeHost(null);
		installDeviceCorners(h, el)();
		expect(vars(el)).toEqual(["", "", "", ""]);
	});

	it("先用 MobileInfo 的几何，之后跟随 insets 事件；取消后不再更新", async () => {
		const el = document.createElement("div");
		const { h, emit } = fakeHost({
			cornerRadii: R40,
			insets: { top: 24, right: 0, bottom: 0, left: 0 },
		});
		const off = installDeviceCorners(h, el);
		await vi.waitFor(() =>
			expect(vars(el)).toEqual(["16px", "16px", "40px", "40px"]),
		);
		emit({ cornerRadii: R40, insets: NO_INSETS });
		expect(vars(el)).toEqual(["40px", "40px", "40px", "40px"]);
		off();
		emit({ cornerRadii: R40, insets: { ...NO_INSETS, top: 40 } });
		expect(vars(el)).toEqual(["40px", "40px", "40px", "40px"]);
	});

	it("事件先于初值到达时不被旧初值覆盖", async () => {
		const el = document.createElement("div");
		let resolve: (i: MobileInfo) => void = () => {};
		const { h, emit } = fakeHost({});
		(h.mobile as unknown as { info: () => Promise<MobileInfo> }).info = () =>
			new Promise((r) => {
				resolve = r;
			});
		installDeviceCorners(h, el);
		emit({ cornerRadii: R40, insets: NO_INSETS });
		resolve({
			cornerRadii: R40,
			insets: { ...NO_INSETS, top: 40 },
		} as MobileInfo);
		await Promise.resolve();
		await Promise.resolve();
		expect(vars(el)).toEqual(["40px", "40px", "40px", "40px"]);
	});
});
