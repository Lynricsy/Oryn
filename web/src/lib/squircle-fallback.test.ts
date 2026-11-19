/**
 * 降级引擎：装上后改写现有样式表，之后加入或热更新的样式表也被改写；正圆与胶囊的规则不受影响；--sq-s 注册为不继承的包络圆系数。
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { installSquircleFallback } from "./squircle-fallback";
import { ROUND_SCALE } from "./squircle-geometry";

const SCALED = "calc((12px) * var(--sq-s, 1))";

function addStyle(parent: Element, css: string): HTMLStyleElement {
	const style = document.createElement("style");
	style.textContent = css;
	parent.append(style);
	return style;
}

/** 某个 <style> 里第 index 条顶层样式规则的某个属性值。 */
function ruleValue(style: HTMLStyleElement, prop: string, index = 0): string {
	const rule = style.sheet?.cssRules[index] as CSSStyleRule | undefined;
	return rule?.style.getPropertyValue(prop).trim() ?? "";
}

afterEach(() => {
	document.documentElement.removeAttribute("data-squircle");
	document.head.innerHTML = "";
	document.body.innerHTML = "";
	document.adoptedStyleSheets = [];
	vi.unstubAllGlobals();
});

describe("installSquircleFallback", () => {
	it("注册不继承的 --sq-s（初始值为包络圆系数），根元素打开关；同一文档只装一次", () => {
		const registerProperty = vi.fn();
		vi.stubGlobal("CSS", { supports: () => false, registerProperty });
		installSquircleFallback(document);
		installSquircleFallback(document);
		expect(document.documentElement.getAttribute("data-squircle")).toBe(
			"fallback",
		);
		expect(registerProperty).toHaveBeenCalledTimes(1);
		expect(registerProperty).toHaveBeenCalledWith({
			name: "--sq-s",
			syntax: "*",
			inherits: false,
			initialValue: ROUND_SCALE.toFixed(5),
		});
	});

	it("改写已有样式表里的半径（含 @media 内的规则）", () => {
		const style = addStyle(
			document.head,
			".a { border-radius: 12px; } @media (min-width: 1px) { .b { border-top-left-radius: 12px; } }",
		);
		installSquircleFallback(document);
		expect(ruleValue(style, "border-radius")).toBe(SCALED);
		const media = style.sheet?.cssRules[1] as CSSMediaRule;
		expect(
			(media.cssRules[0] as CSSStyleRule).style.getPropertyValue(
				"border-top-left-radius",
			),
		).toBe(SCALED);
	});

	it("正圆与胶囊：index.css 的 --sq-s: 1 规则与 infinity / 50% 半径保持原样", () => {
		const style = addStyle(
			document.head,
			':root[data-squircle] [class*="rounded-full"] { --sq-s: 1; } .pill { border-radius: calc(infinity * 1px); } .dot { border-radius: 50%; }',
		);
		installSquircleFallback(document);
		expect(ruleValue(style, "--sq-s", 0)).toBe("1");
		expect(ruleValue(style, "border-radius", 1)).toBe("calc(infinity * 1px)");
		expect(ruleValue(style, "border-radius", 2)).toBe("50%");
	});

	it("之后加入 <head> 的样式表、热更新换掉的样式文本、<body> 直接子节点里的 <style> 都被改写", async () => {
		installSquircleFallback(document);
		// MutationObserver 的回调排在变更时的微任务里：变更后 await 一个已决的 Promise，回调已经跑完
		const lazy = addStyle(document.head, ".c { border-radius: 12px; }");
		await Promise.resolve();
		expect(ruleValue(lazy, "border-radius")).toBe(SCALED);

		// Vite 热更新：同一个 <style> 换文本，得到一张新表
		lazy.textContent = ".c { border-bottom-right-radius: 12px; }";
		await Promise.resolve();
		expect(ruleValue(lazy, "border-bottom-right-radius")).toBe(SCALED);

		const portal = addStyle(document.body, ".d { border-radius: 12px; }");
		await Promise.resolve();
		expect(ruleValue(portal, "border-radius")).toBe(SCALED);
	});
});
