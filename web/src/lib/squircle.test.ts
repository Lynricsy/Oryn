/**
 * 角形降级的入口与样式表改写：特性检测分支（原生支持时不加载引擎）、半径声明改写规则（正圆与胶囊保持原样）。
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { installSquircle, needsSquircleFallback } from "./squircle";
import { patchDeclarations, scaleRadiusValue } from "./squircle-patch";

const install = vi.hoisted(() => vi.fn());
vi.mock("./squircle-fallback", () => ({ installSquircleFallback: install }));

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

	it("patchDeclarations 保留 !important，重复改写不再套一层", () => {
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
		patchDeclarations(style);
		patchDeclarations(style);
		expect(values.get("border-radius")).toEqual([
			"calc((12px) * var(--sq-s, 1))",
			"important",
		]);
		expect(values.get("border-top-left-radius")?.[0]).toBe("50%");
	});
});
