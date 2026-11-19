/**
 * 角形降级的样式表改写（纯 CSSOM 操作，无布局读取）：把所有 border-*-radius 声明改写成
 * `calc((原值) * var(--sq-s, 1))`。
 *
 * 级联、:hover、主题变量、过渡、@keyframes 都照原样生效，只是最终半径多乘一个逐元素的系数 --sq-s：
 * 超椭圆表面取注册的初始值（包络圆系数 ROUND_SCALE），正圆与胶囊由 index.css「角形」段落设为 1。
 * 不覆盖任何作者样式，也不需要逐元素的 JS。
 */

/** 逐元素的半径系数（squircle-fallback.ts 注册，index.css 给正圆与胶囊设 1）。 */
export const SCALE_VAR = "--sq-s";

/** 改写的属性：简写 + 物理与逻辑长手。 */
const RADIUS_PROPERTIES = [
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

/** 改写一组声明（样式规则或关键帧）。 */
export function patchDeclarations(style: CSSStyleDeclaration): void {
	// 先改简写：其长手随之更新，下面读到的长手已带 --sq-s，会被跳过
	for (const prop of RADIUS_PROPERTIES) {
		const value = style.getPropertyValue(prop);
		if (!value) continue;
		const scaled = scaleRadiusValue(value);
		if (scaled === null) continue;
		style.setProperty(prop, scaled, style.getPropertyPriority(prop));
	}
}

/** 递归改写规则列表（@layer、@media、@supports、@container、嵌套规则、@keyframes 都下钻）。 */
export function patchRules(rules: CSSRuleList): void {
	for (const rule of Array.from(rules)) {
		if ("style" in rule && rule.style instanceof CSSStyleDeclaration) {
			patchDeclarations(rule.style);
		}
		if ("cssRules" in rule && rule.cssRules instanceof CSSRuleList) {
			patchRules(rule.cssRules);
		}
	}
}
