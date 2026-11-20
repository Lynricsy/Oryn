/**
 * DOM 选区快照：触发上下文菜单的那一刻读出文字、所在元素与各行矩形，之后不再依赖活的 Range。
 */
import type { Anchor, DomSelection } from "./types";

/**
 * 选中的文字。公式（KaTeX）按渲染结果取会得到 MathML 与排版字形拼成的乱码，改为取它的 TeX 源码：
 * 行内 `$…$`、独立成行 `$$…$$`（选到公式的一部分也算整条）。没有公式时就是 `plain()`。
 * `plain` 由调用方传 `() => sel.toString()`：不能换成 `range.toString()`——软换行渲染成 `<br>`，
 * `Range.toString()` 只拼文本节点，会把多行粘成一行；`Selection.toString()` 才按渲染结果换行。
 */
export function selectionText(
	range: Range,
	within: Element,
	plain: () => string,
): string {
	const formulas = Array.from(within.querySelectorAll(".katex")).filter((el) =>
		range.intersectsNode(el),
	);
	if (formulas.length === 0) return plain();
	const host = document.createElement("div");
	host.append(range.cloneContents());
	// 克隆里每条被选到的公式都留有自己的 .katex 外壳（部分选中时是残缺的子树），与原文档里的顺序一一对应
	const clones = Array.from(host.querySelectorAll(".katex")).filter(
		(el) => !el.parentElement?.closest(".katex"),
	);
	if (clones.length !== formulas.length) return plain();
	clones.forEach((clone, i) => {
		const formula = formulas[i];
		const tex = formula?.querySelector(
			'annotation[encoding="application/x-tex"]',
		)?.textContent;
		if (!formula || !tex) return;
		const fence = formula.closest(".katex-display") ? "$$" : "$";
		clone.replaceWith(`${fence}${tex.trim()}${fence}`);
	});
	// innerText 才按块级元素换行：在屏外渲染一次再取
	host.style.cssText =
		"position:fixed;left:-99999px;top:0;width:800px;white-space:normal";
	document.body.append(host);
	// biome-ignore lint/performance/useDomNodeTextContent: 要的正是按渲染结果换行的文字（段落、列表项之间的换行）
	const text = host.innerText ?? host.textContent ?? "";
	host.remove();
	return text;
}

function elementOf(node: Node): Element | null {
	return node instanceof Element ? node : node.parentElement;
}

/** 压空白后取紧邻一端的 max 个码点。 */
function squeeze(raw: string, max: number, fromEnd: boolean): string {
	const chars = Array.from(raw.replace(/\s+/g, " ").trim());
	if (chars.length <= max) return chars.join("");
	return (fromEnd ? chars.slice(-max) : chars.slice(0, max)).join("");
}

export function readDomSelection(): DomSelection | null {
	const sel = document.getSelection();
	if (!sel || sel.rangeCount === 0 || sel.isCollapsed) return null;
	const range = sel.getRangeAt(0);
	const root = elementOf(range.commonAncestorContainer);
	const startEl = elementOf(range.startContainer);
	const endEl = elementOf(range.endContainer);
	if (!root || !startEl || !endEl) return null;
	const text = selectionText(range, root, () => sel.toString());
	if (!text.trim()) return null;
	const rects =
		typeof range.getClientRects === "function"
			? Array.from(range.getClientRects()).filter((r) => r.width > 0)
			: [];
	const { startContainer, startOffset, endContainer, endOffset } = range;
	return {
		text,
		root,
		startEl,
		endEl,
		rects,
		before(scope, max) {
			if (!scope.contains(startContainer)) return "";
			const r = document.createRange();
			r.selectNodeContents(scope);
			r.setEnd(startContainer, startOffset);
			return squeeze(r.toString(), max, true);
		},
		after(scope, max) {
			if (!scope.contains(endContainer)) return "";
			const r = document.createRange();
			r.selectNodeContents(scope);
			r.setStart(endContainer, endOffset);
			return squeeze(r.toString(), max, false);
		},
	};
}

/** 点 (x, y) 是否落在选区上（外扩 2px）；量不到矩形时退回「目标在选区的公共祖先里」。 */
export function hitsSelection(
	sel: DomSelection,
	x: number,
	y: number,
	target: Element,
): boolean {
	if (sel.rects.length === 0) return sel.root.contains(target);
	return sel.rects.some(
		(r) =>
			x >= r.left - 2 &&
			x <= r.right + 2 &&
			y >= r.top - 2 &&
			y <= r.bottom + 2,
	);
}

export function anchorOf(
	sel: DomSelection | null,
	x: number,
	y: number,
): Anchor {
	const rects = sel?.rects ?? [];
	if (rects.length === 0) return { left: x, top: y, right: x, bottom: y };
	return {
		left: Math.min(...rects.map((r) => r.left)),
		top: Math.min(...rects.map((r) => r.top)),
		right: Math.max(...rects.map((r) => r.right)),
		bottom: Math.max(...rects.map((r) => r.bottom)),
	};
}
