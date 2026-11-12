/**
 * 模态层：打开的模态按栈管理，栈顶以外的 body 子树一律 `inert`（不可聚焦、不可点击、对辅助技术隐藏），
 * 并提供 Tab / Shift+Tab 的焦点圈定。纯 DOM 逻辑，供 `Dialog` 使用。
 */
import type { KeyboardEvent as ReactKeyboardEvent } from "react";

const stack: HTMLElement[] = [];
/** 由本模块设上 inert 的元素（不碰别处本来就 inert 的节点）。 */
const inerted = new Set<Element>();

function applyInert() {
	for (const el of inerted) el.removeAttribute("inert");
	inerted.clear();
	const top = stack[stack.length - 1];
	if (!top || typeof document === "undefined") return;
	for (const child of Array.from(document.body.children)) {
		if (child === top || child.contains(top) || child.hasAttribute("inert"))
			continue;
		child.setAttribute("inert", "");
		inerted.add(child);
	}
}

/** 登记一个打开的模态宿主（body 的直接子节点）；返回撤销函数。 */
export function pushModal(host: HTMLElement): () => void {
	stack.push(host);
	applyInert();
	return () => {
		const i = stack.lastIndexOf(host);
		if (i >= 0) stack.splice(i, 1);
		applyInert();
	};
}

export function isTopModal(host: HTMLElement): boolean {
	return stack[stack.length - 1] === host;
}

const FOCUSABLE = [
	"a[href]",
	"button:not([disabled])",
	"input:not([disabled]):not([type=hidden])",
	"select:not([disabled])",
	"textarea:not([disabled])",
	"[tabindex]:not([tabindex='-1'])",
	"[contenteditable='true']",
].join(",");

export function focusablesIn(root: HTMLElement): HTMLElement[] {
	return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
		(el) => !el.closest("[hidden],[inert]"),
	);
}

/** Tab 键在 root 内循环；焦点已在 root 外时拉回第一个可聚焦元素。 */
export function trapTab(
	e: KeyboardEvent | ReactKeyboardEvent,
	root: HTMLElement,
) {
	if (e.key !== "Tab") return;
	const items = focusablesIn(root);
	if (items.length === 0) {
		e.preventDefault();
		root.focus();
		return;
	}
	const first = items[0] as HTMLElement;
	const last = items[items.length - 1] as HTMLElement;
	const active = document.activeElement;
	if (!root.contains(active)) {
		e.preventDefault();
		first.focus();
	} else if (e.shiftKey && (active === first || active === root)) {
		e.preventDefault();
		last.focus();
	} else if (!e.shiftKey && active === last) {
		e.preventDefault();
		first.focus();
	}
}
