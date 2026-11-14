/**
 * 角形降级的共享规则表：同声明共用一条规则、引用归零后延迟删除、删除中间的规则不打乱其余规则。
 */
import { describe, expect, it } from "vitest";
import { RULE_ATTR, ShapeRules } from "./squircle-rules";

function sheet(): CSSStyleSheet {
	const style = document.createElement("style");
	document.head.appendChild(style);
	if (!style.sheet) throw new Error("jsdom 没有给 <style> 建样式表");
	return style.sheet;
}

const texts = (s: CSSStyleSheet) =>
	Array.from(s.cssRules, (r) => r.cssText.replace(/\s+/g, ""));

describe("ShapeRules", () => {
	it("同一组声明在同一伪元素上共用一条规则；不同伪元素各一条", () => {
		const s = sheet();
		const rules = new ShapeRules(s);
		const a = rules.acquire("--sq-s:0.5!important;", "");
		expect(rules.acquire("--sq-s:0.5!important;", "")).toBe(a);
		const b = rules.acquire("--sq-s:0.5!important;", "::before");
		expect(b).not.toBe(a);
		expect(a).toMatch(RULE_ATTR);
		expect(b).toMatch(RULE_ATTR);
		expect(texts(s)).toEqual([
			`[${a}]{--sq-s:0.5!important;}`,
			`[${b}]::before{--sq-s:0.5!important;}`,
		]);
		// 根元素上的开关属性与其它 data-sq 前缀的属性不会被当成规则属性
		expect(RULE_ATTR.test("data-squircle")).toBe(false);
	});

	it("引用归零的规则留到 sweep 才删；其间再被取用就不删，也不重复插入", () => {
		const s = sheet();
		const rules = new ShapeRules(s);
		const a = rules.acquire("--sq-s:0.1;", "");
		rules.release(a);
		expect(s.cssRules).toHaveLength(1);
		expect(rules.acquire("--sq-s:0.1;", "")).toBe(a);
		expect(s.cssRules).toHaveLength(1);
		rules.sweep();
		expect(s.cssRules).toHaveLength(1);
	});

	it("多余的 release（重复归还、未知属性）不会让引用变负，仍被引用的规则不会被删", () => {
		const s = sheet();
		const rules = new ShapeRules(s);
		const a = rules.acquire("--sq-s:0.2;", "");
		rules.release(a);
		rules.release(a);
		rules.release("data-sq999");
		expect(rules.acquire("--sq-s:0.2;", "")).toBe(a);
		rules.sweep();
		expect(texts(s)).toEqual([`[${a}]{--sq-s:0.2;}`]);
		rules.release(a);
		rules.sweep();
		expect(s.cssRules).toHaveLength(0);
	});

	it("sweep 删掉中间的规则后，其余规则的内容与顺序不变，之后插入的新规则也正常", () => {
		const s = sheet();
		const rules = new ShapeRules(s);
		const [a, b, c, d] = ["0.1", "0.2", "0.3", "0.4"].map((v) =>
			rules.acquire(`--sq-s:${v};`, ""),
		);
		rules.release(b ?? "");
		rules.release(d ?? "");
		rules.sweep();
		expect(texts(s)).toEqual([`[${a}]{--sq-s:0.1;}`, `[${c}]{--sq-s:0.3;}`]);
		// 被删掉的声明再出现时换一个新属性名（旧元素上残留的旧属性不会意外套上它）
		const b2 = rules.acquire("--sq-s:0.2;", "");
		expect(b2).not.toBe(b);
		expect(texts(s).at(-1)).toBe(`[${b2}]{--sq-s:0.2;}`);
	});

	it("无引用的规则攒满 64 条时自动清理", () => {
		const s = sheet();
		const rules = new ShapeRules(s);
		const keep = rules.acquire("--sq-s:1;", "");
		for (let i = 0; i < 63; i++)
			rules.release(rules.acquire(`--sq-s:0.${i + 10};`, ""));
		expect(s.cssRules).toHaveLength(64);
		rules.release(rules.acquire("--sq-s:0.99;", ""));
		expect(texts(s)).toEqual([`[${keep}]{--sq-s:1;}`]);
	});
});
