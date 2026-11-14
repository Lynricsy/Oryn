/**
 * 角形降级的样式表改写（纯 CSSOM 操作，无布局读取）：把所有 border-*-radius 声明改写成
 * `calc((原值) * var(--sq-s, 1))`。
 *
 * 级联、:hover、主题变量、过渡都照原样生效，只是最终半径多乘一个逐元素的系数 --sq-s：
 * 超椭圆表面由 index.css「角形」段落设为包络圆系数（ROUND_SCALE），正圆与胶囊为 1。
 * 这样 JS 只需从计算值反推原半径（计算值 ÷ --sq-s），不必覆盖任何作者样式。
 */

/** 逐元素的半径系数（index.css 设默认值，被等比缩小的盒子由 JS 写在行内）。 */
export const SCALE_VAR = "--sq-s";

/** 改写的属性：简写 + 物理与逻辑长手。 */
export const RADIUS_PROPERTIES = [
	"border-radius",
	"border-top-left-radius",
	"border-top-right-radius",
	"border-bottom-right-radius",
	"border-bottom-left-radius",
	"border-start-start-radius",
	"border-start-end-radius",
	"border-end-start-radius",
	"border-end-end-radius",
] as const;

/** 全局关键字：inherit 继承来的计算值已经缩放过，其余取初始值 0。 */
const KEYWORD = /^(inherit|initial|unset|revert|revert-layer)$/i;

/** 按顶层（不在括号里的）空白与「/」切分，「/」单独成段。 */
function splitTopLevel(value: string): string[] {
	const out: string[] = [];
	let depth = 0;
	let cur = "";
	for (const ch of value) {
		if (ch === "(") depth++;
		else if (ch === ")") depth--;
		if (
			depth === 0 &&
			(ch === " " || ch === "\t" || ch === "\n" || ch === "/")
		) {
			if (cur) out.push(cur);
			cur = "";
			if (ch === "/") out.push("/");
			continue;
		}
		cur += ch;
	}
	if (cur) out.push(cur);
	return out;
}

/**
 * 改写一个半径声明值；不需要改写时返回 null：全局关键字（inherit 等，继承来的计算值已经缩放过）、
 * 已经改写过、只有 0 / 百分比（百分比用来画正圆与椭圆，保持原样）、含 infinity（胶囊）。
 * 顶层的 var() 必须展开成单个长度，否则 calc 无效（本站的半径变量都是单值）。
 */
export function scaleRadiusValue(value: string): string | null {
	const v = value.trim();
	if (!v || KEYWORD.test(v) || v.includes(SCALE_VAR)) return null;
	let changed = false;
	const parts = splitTopLevel(v).map((tok) => {
		if (
			tok === "/" ||
			/^[+-]?(\d+\.?\d*|\.\d+)%$/.test(tok) ||
			/^[+-]?0+(\.0*)?[a-z]*$/i.test(tok) ||
			/infinity/i.test(tok)
		) {
			return tok;
		}
		changed = true;
		return `calc((${tok}) * var(${SCALE_VAR}, 1))`;
	});
	return changed ? parts.join(" ") : null;
}

/** 改写一组声明（样式规则、关键帧或元素的行内样式）；返回是否有改动。 */
export function patchDeclarations(style: CSSStyleDeclaration): boolean {
	let changed = false;
	// 先改简写：其长手随之更新，下面读到的长手已带 --sq-s，会被跳过
	for (const prop of RADIUS_PROPERTIES) {
		const value = style.getPropertyValue(prop);
		if (!value) continue;
		const scaled = scaleRadiusValue(value);
		if (scaled === null) continue;
		style.setProperty(prop, scaled, style.getPropertyPriority(prop));
		changed = true;
	}
	return changed;
}

/** 递归改写规则列表（@layer、@media、@supports、@container、嵌套规则、@keyframes 都下钻）；返回改动的声明块数。 */
export function patchRules(rules: CSSRuleList): number {
	let count = 0;
	for (const rule of Array.from(rules)) {
		if ("style" in rule && rule.style instanceof CSSStyleDeclaration) {
			if (patchDeclarations(rule.style)) count++;
		}
		if ("cssRules" in rule && rule.cssRules instanceof CSSRuleList) {
			count += patchRules(rule.cssRules);
		}
	}
	return count;
}

/**
 * 改写一张样式表；跨源样式表读不到规则时跳过。返回该表当前的顶层规则数（-1 表示读不到）与本次改写的声明块数
 * （0 表示这张表没有要改写的半径：调用方不必为它重扫整篇文档）。
 */
export function patchSheet(sheet: CSSStyleSheet): {
	ruleCount: number;
	patched: number;
} {
	let rules: CSSRuleList;
	try {
		rules = sheet.cssRules;
	} catch {
		return { ruleCount: -1, patched: 0 };
	}
	return { ruleCount: rules.length, patched: patchRules(rules) };
}
