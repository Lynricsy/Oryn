/**
 * 角形降级的入口与样式表改写：特性检测分支（原生支持时不加载引擎）、半径声明改写规则（正圆与胶囊保持原样）。
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { installSquircle, needsSquircleFallback } from "./squircle";
import type * as Fallback from "./squircle-fallback";
import { colorAlpha, styleChangeCascades } from "./squircle-fallback";
import { patchDeclarations, scaleRadiusValue } from "./squircle-patch";

const install = vi.hoisted(() => vi.fn());
vi.mock("./squircle-fallback", async (importOriginal) => ({
	...(await importOriginal<typeof Fallback>()),
	installSquircleFallback: install,
}));

afterEach(() => {
	vi.unstubAllGlobals();
	install.mockReset();
});

describe("特性检测", () => {
	it("按 CSS.supports('corner-shape', 'superellipse(1.6)') 判定", () => {
		const supports = vi.fn(() => true);
		expect(needsSquircleFallback({ supports })).toBe(false);
		expect(supports).toHaveBeenCalledWith("corner-shape", "superellipse(1.6)");
		expect(needsSquircleFallback({ supports: () => false })).toBe(true);
		// 没有 CSS 全局对象的环境也走降级
		vi.stubGlobal("CSS", undefined);
		expect(needsSquircleFallback()).toBe(true);
	});

	it("原生支持：不加载引擎", async () => {
		vi.stubGlobal("CSS", { supports: () => true });
		await installSquircle(document);
		expect(install).not.toHaveBeenCalled();
	});

	it("不支持：动态加载引擎并装到当前文档", async () => {
		vi.stubGlobal("CSS", { supports: () => false });
		await installSquircle(document);
		expect(install).toHaveBeenCalledWith(document);
	});

	it("引擎出错只打日志，不阻断启动", async () => {
		vi.stubGlobal("CSS", { supports: () => false });
		install.mockImplementationOnce(() => {
			throw new Error("boom");
		});
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
		await expect(installSquircle(document)).resolves.toBeUndefined();
		expect(error).toHaveBeenCalled();
		error.mockRestore();
	});
});

describe("scaleRadiusValue", () => {
	it("长度乘上逐元素系数，椭圆半径的两段各自改写", () => {
		expect(scaleRadiusValue("12px")).toBe("calc((12px) * var(--sq-s, 1))");
		expect(scaleRadiusValue("var(--r-md)")).toBe(
			"calc((var(--r-md)) * var(--sq-s, 1))",
		);
		expect(scaleRadiusValue("1rem 8px / 4px")).toBe(
			"calc((1rem) * var(--sq-s, 1)) calc((8px) * var(--sq-s, 1)) / calc((4px) * var(--sq-s, 1))",
		);
		expect(scaleRadiusValue("calc(var(--r-xl) - 1px)")).toBe(
			"calc((calc(var(--r-xl) - 1px)) * var(--sq-s, 1))",
		);
	});

	it("正圆（百分比）、胶囊（infinity）、0、全局关键字与已改写的值保持原样", () => {
		expect(scaleRadiusValue("50%")).toBeNull();
		expect(scaleRadiusValue("calc(infinity * 1px)")).toBeNull();
		expect(scaleRadiusValue("0")).toBeNull();
		expect(scaleRadiusValue("0px")).toBeNull();
		expect(scaleRadiusValue("inherit")).toBeNull();
		expect(scaleRadiusValue("revert-layer")).toBeNull();
		expect(scaleRadiusValue("calc((12px) * var(--sq-s, 1))")).toBeNull();
		// 混合：只改长度那一段
		expect(scaleRadiusValue("50% 12px")).toBe(
			"50% calc((12px) * var(--sq-s, 1))",
		);
	});

	it("patchDeclarations 保留 !important，并报告是否改动", () => {
		const values = new Map<string, [string, string]>([
			["border-radius", ["12px", "important"]],
			["border-top-left-radius", ["50%", ""]],
		]);
		const style = {
			getPropertyValue: (p: string) => values.get(p)?.[0] ?? "",
			getPropertyPriority: (p: string) => values.get(p)?.[1] ?? "",
			setProperty: (p: string, v: string, prio: string) =>
				values.set(p, [v, prio]),
		} as unknown as CSSStyleDeclaration;
		expect(patchDeclarations(style)).toBe(true);
		expect(values.get("border-radius")).toEqual([
			"calc((12px) * var(--sq-s, 1))",
			"important",
		]);
		expect(values.get("border-top-left-radius")?.[0]).toBe("50%");
		expect(patchDeclarations(style)).toBe(false);
	});
});

describe("colorAlpha", () => {
	it("读出计算值颜色的不透明度", () => {
		expect(colorAlpha("transparent")).toBe(0);
		expect(colorAlpha("rgb(1, 2, 3)")).toBe(1);
		expect(colorAlpha("rgba(0, 0, 0, 0)")).toBe(0);
		expect(colorAlpha("rgba(0, 0, 0, 0.5)")).toBe(0.5);
		expect(colorAlpha("oklch(0.99 0.003 235)")).toBe(1);
		expect(colorAlpha("oklch(0.99 0.003 235 / 0.4)")).toBe(0.4);
		expect(colorAlpha("color(srgb 1 1 1 / 40%)")).toBe(0.4);
	});
});

describe("styleChangeCascades：行内样式变化是否可能波及后代", () => {
	const check = (old: string | null, now: string) => {
		const el = document.createElement("div");
		el.style.cssText = now;
		return styleChangeCascades(
			old,
			el.style,
			document.createElement("div").style,
		);
	};

	it("只改不继承的属性（虚拟列表的 transform / top / 宽高）不波及", () => {
		expect(
			check(
				"transform: translateY(10px); top: 0px; --gap: 4px",
				"transform: translateY(80px); top: 3px; width: 10px; --gap: 4px",
			),
		).toBe(false);
		expect(check(null, "height: 20px")).toBe(false);
	});

	it("自定义属性、color、font-size 变了，或增删了它们，都算波及", () => {
		expect(check("--r: 8px", "--r: 12px")).toBe(true);
		expect(check("--r: 8px", "")).toBe(true);
		expect(check(null, "color: red")).toBe(true);
		expect(check("font-size: 12px", "font-size: 14px")).toBe(true);
	});
});
