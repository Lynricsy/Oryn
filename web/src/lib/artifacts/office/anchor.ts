// 评论点选与图钉共用的小工具（步骤 7.4）：Office 视图、沙箱里的 Markdown 文档与 JSON 幻灯片查看器都用。
// 锚点字段的上限与帧运行时的校验一致（sandbox/runtime/messages.ts 的 parseViewerAnchor）。

import type { MouseEvent } from "react";
import {
	type AnchorPin,
	SELECTION_TEXT_CHARS,
	type ViewerAnchor,
} from "../../../../sandbox/runtime/messages";

export interface Point {
	x: number;
	y: number;
}

/** 没有图钉时共用的空数组（引用稳定，免得 memo 失效）。 */
export const NO_PINS: readonly AnchorPin[] = Object.freeze([]);

/** 折叠空白并截到前 120 个码点：锚点的 `text` 与 `page.name` 都按这个上限校验。 */
export function clipText(s: string): string {
	const t = s.replace(/\s+/g, " ").trim();
	let out = "";
	let n = 0;
	for (const ch of t) {
		if (n === SELECTION_TEXT_CHARS) break;
		out += ch;
		n++;
	}
	return out;
}

/** 元素内的偏移换成归一化坐标，夹到 [0, 1]；元素没有尺寸时取中心。 */
function fraction(offset: number, size: number): number {
	return size > 0 ? Math.min(1, Math.max(0, offset / size)) : 0.5;
}

/**
 * 点在元素上的位置：`point` 是元素内的归一化坐标（0–1），`at` 是视口坐标（父页面据此摆评论框）。
 * 键盘触发的点击没有指针坐标（`detail` 为 0），取元素中心。
 */
export function pickPoint(e: MouseEvent<HTMLElement>): {
	point: Point;
	at: Point;
} {
	const r = e.currentTarget.getBoundingClientRect();
	const keyboard = e.detail === 0 && e.clientX === 0 && e.clientY === 0;
	const at = keyboard
		? { x: r.left + r.width / 2, y: r.top + r.height / 2 }
		: { x: e.clientX, y: e.clientY };
	return {
		point: {
			x: fraction(at.x - r.left, r.width),
			y: fraction(at.y - r.top, r.height),
		},
		at,
	};
}

/** 页内一点的锚点（Word、PDF、幻灯片）：不带区域与块序号。 */
export function pointAnchor(
	page: { number: number; name: string },
	point: Point,
	text: string,
): ViewerAnchor {
	return {
		page: { number: page.number, name: clipText(page.name) },
		x: point.x,
		y: point.y,
		range: null,
		block: null,
		text: clipText(text),
	};
}

/** 按页号（从 1 起）分组；没有页的图钉不进任何一组。 */
export function pinsByPage(
	pins: readonly AnchorPin[],
): ReadonlyMap<number, readonly AnchorPin[]> {
	const out = new Map<number, AnchorPin[]>();
	for (const p of pins) {
		const n = p.anchor.page?.number;
		if (n === undefined) continue;
		const list = out.get(n);
		if (list) list.push(p);
		else out.set(n, [p]);
	}
	return out;
}

/**
 * 视口一点下面的文字：从最上层往下找第一个满足 `accept` 且有文字的元素（跳过点选按钮与图钉）。
 * 用于 DOM 里本来就有文字的表面（PDF 的文本层、JSON 幻灯片）；没有 `elementsFromPoint` 的环境回空串。
 */
export function textAtPoint(
	at: Point,
	accept: (el: Element) => boolean,
): string {
	if (typeof document.elementsFromPoint !== "function") return "";
	for (const el of document.elementsFromPoint(at.x, at.y)) {
		if (el.closest(".oryn-pick, .oryn-pins")) continue;
		if (!accept(el)) continue;
		const text = clipText(el.textContent ?? "");
		if (text) return text;
	}
	return "";
}

/**
 * 把滚动容器滚到某页（可带页内纵向位置，归一化）：有位置时让它落在视口中间，否则页顶对齐（留 `pad` 的边）。
 * 页元素的 offsetParent 必须是滚动容器。
 */
export function scrollToPage(
	scroller: HTMLElement,
	page: HTMLElement,
	y: number | null,
	pad: number,
): void {
	const top =
		y === null
			? page.offsetTop - pad
			: page.offsetTop + y * page.offsetHeight - scroller.clientHeight / 2;
	scroller.scrollTop = Math.max(0, top);
}
