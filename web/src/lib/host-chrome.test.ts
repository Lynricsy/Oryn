/** 系统栏配色：颜色换算、亮度判定与页面底色变化的订阅。 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { relativeLuminance, toHexColor, watchChrome } from "./host-chrome";

describe("toHexColor", () => {
	it("rgb / rgba / 短长十六进制直接换算；没有可用画布时其它写法为 null", () => {
		expect(toHexColor("rgb(238, 240, 244)")).toBe("#eef0f4");
		expect(toHexColor("rgba(16, 17, 18, 0.5)")).toBe("#101112");
		expect(toHexColor("#ABC")).toBe("#aabbcc");
		expect(toHexColor("#0a0b0c")).toBe("#0a0b0c");
		// jsdom 是否带画布取决于是否装了可选的 @napi-rs/canvas（pdfjs-dist 的可选依赖），这里显式给一个没有 2d 上下文的文档
		const noCanvas = {
			createElement: () => ({ getContext: () => null }),
		} as unknown as Document;
		expect(toHexColor("oklch(0.2 0.01 250)", noCanvas)).toBeNull();
	});
});

describe("relativeLuminance", () => {
	it("白 1、黑 0、中灰 < 0.5", () => {
		expect(relativeLuminance("#ffffff")).toBeCloseTo(1);
		expect(relativeLuminance("#000000")).toBe(0);
		expect(relativeLuminance("#777777")).toBeLessThan(0.5);
	});
});

describe("watchChrome", () => {
	afterEach(() => {
		document.body.style.backgroundColor = "";
		document.documentElement.className = "";
		vi.restoreAllMocks();
	});

	it("先回调当前底色；<html> 属性变化后底色变了才再回调；取消后不再回调", async () => {
		document.body.style.backgroundColor = "rgb(238, 240, 244)";
		const cb = vi.fn();
		const off = watchChrome(window, cb);
		expect(cb).toHaveBeenLastCalledWith({ dark: false, color: "#eef0f4" });

		// 属性变了但底色没变：不回调
		document.documentElement.dataset.x = "1";
		await Promise.resolve();
		expect(cb).toHaveBeenCalledTimes(1);

		document.body.style.backgroundColor = "rgb(20, 22, 26)";
		document.documentElement.className = "dark";
		await vi.waitFor(() =>
			expect(cb).toHaveBeenLastCalledWith({ dark: true, color: "#14161a" }),
		);
		expect(cb).toHaveBeenCalledTimes(2);

		off();
		document.body.style.backgroundColor = "rgb(255, 255, 255)";
		document.documentElement.className = "";
		await Promise.resolve();
		expect(cb).toHaveBeenCalledTimes(2);
		delete document.documentElement.dataset.x;
	});
});
