/**
 * 上下文菜单的全局分发：鼠标右键、键盘菜单键 / Shift+F10、触屏长按三种触发，统一交给登记表（`registry.ts`）
 * 收集菜单组，再交给宿主（`components/ui/ContextMenu.tsx`）渲染。
 * 接管策略：有菜单项才接管；普通输入框与 Shift+右键一律原生；没有菜单项时桌面壳屏蔽原生菜单、浏览器放行。
 * 注意不拿 `scroll` 当取消信号：转录贴底跟随每个流式增量都会改 scrollTop。
 */
import { host } from "../host";
import { resolveContext } from "./registry";
import { hitsSelection, readDomSelection } from "./selection";
import { openContextMenu } from "./store";
import type { ContextRequest } from "./types";

const PLAIN_EDITABLE =
	'input, textarea, select, [contenteditable=""], [contenteditable="true"]';
const CUSTOM_SURFACE = ".cm-editor, .xterm";
const MENU_SURFACE =
	'[data-testid="context-menu"], [data-testid="context-sheet"]';
const TOUCH_TARGET = "[data-ctx-touch]";
const LONG_PRESS_MS = 500;
const LONG_PRESS_SLOP = 10;
/** 菜单键按下后浏览器还会自己派生一次 contextmenu：这段时间内的那次吞掉 */
const KEY_ECHO_MS = 500;
/** 长按开出菜单、手指抬起后这么久内的那次 click 吞掉（不让长按顺带点开对象） */
const SWALLOW_CLICK_MS = 700;

function targetOf(e: Event): Element | null {
	const t = e.target;
	return t instanceof Element ? t : t instanceof Node ? t.parentElement : null;
}

/** 收集菜单并决定是否拦下原生菜单；开出了菜单时返回 true。 */
function finish(
	e: Event,
	base: Omit<ContextRequest, "askThreadId">,
	restoreFocus: HTMLElement | null,
): boolean {
	const { groups, suppress } = resolveContext(base);
	if (groups.length > 0) {
		e.preventDefault();
		openContextMenu({
			groups,
			x: base.x,
			y: base.y,
			trigger: base.trigger,
			restoreFocus,
		});
		return true;
	}
	if (suppress || (host.desktop !== null && !base.target.closest(".cm-editor")))
		e.preventDefault();
	return false;
}

export function installContextMenu(): () => void {
	let lastKey = Number.NEGATIVE_INFINITY;
	let press: {
		id: number;
		x: number;
		y: number;
		target: Element;
		timer: ReturnType<typeof setTimeout>;
	} | null = null;
	let swallowClick = false;
	let swallowTimer: ReturnType<typeof setTimeout> | undefined;

	const onContextMenu = (e: MouseEvent) => {
		if (e.defaultPrevented) return;
		const target = targetOf(e);
		if (!target) return;
		if (target.closest(MENU_SURFACE)) {
			e.preventDefault();
			return;
		}
		if (performance.now() - lastKey < KEY_ECHO_MS) {
			e.preventDefault();
			return;
		}
		// Android 长按：菜单由长按计时器负责开
		if ((e as Partial<PointerEvent>).pointerType === "touch") {
			if (target.closest(TOUCH_TARGET)) e.preventDefault();
			return;
		}
		if (e.shiftKey) return;
		if (!target.closest(CUSTOM_SURFACE) && target.closest(PLAIN_EDITABLE))
			return;
		const dom = readDomSelection();
		const selection =
			dom && hitsSelection(dom, e.clientX, e.clientY, target) ? dom : null;
		finish(
			e,
			{ trigger: "mouse", x: e.clientX, y: e.clientY, target, selection },
			null,
		);
	};

	const onKeyDown = (e: KeyboardEvent) => {
		if (!(e.key === "ContextMenu" || (e.key === "F10" && e.shiftKey))) return;
		const active = document.activeElement ?? document.body;
		// 终端里 Shift+F10 是给 TUI 程序的
		if (
			active.closest(".xterm") ||
			(!active.closest(".cm-editor") && active.closest(PLAIN_EDITABLE))
		)
			return;
		const dom = readDomSelection();
		// 选区只在与焦点相关时才算：焦点已移到别的对象上时，要的是那个对象自己的菜单
		const selection =
			dom &&
			(active === document.body ||
				active.contains(dom.root) ||
				dom.root.contains(active))
				? dom
				: null;
		let target: Element;
		let x: number;
		let y: number;
		if (selection) {
			target = selection.endEl;
			const r =
				selection.rects[selection.rects.length - 1] ??
				selection.endEl.getBoundingClientRect();
			x = r.right;
			y = r.bottom;
		} else {
			target = active;
			const r = active.getBoundingClientRect();
			x = Math.min(r.left + 24, r.right);
			y = r.bottom;
		}
		const opened = finish(
			e,
			{ trigger: "keyboard", x, y, target, selection },
			active instanceof HTMLElement ? active : null,
		);
		if (opened) lastKey = performance.now();
	};

	const cancelPress = () => {
		if (!press) return;
		clearTimeout(press.timer);
		press = null;
	};

	const onPointerDown = (e: PointerEvent) => {
		swallowClick = false;
		clearTimeout(swallowTimer);
		cancelPress();
		if (e.pointerType !== "touch") return;
		const target = targetOf(e);
		if (!target?.closest(TOUCH_TARGET)) return;
		const { clientX: x, clientY: y } = e;
		press = {
			id: e.pointerId,
			x,
			y,
			target,
			timer: setTimeout(() => {
				press = null;
				const { groups } = resolveContext({
					trigger: "touch",
					x,
					y,
					target,
					selection: null,
				});
				if (groups.length === 0) return;
				document.getSelection()?.removeAllRanges();
				navigator.vibrate?.(8);
				openContextMenu({ groups, x, y, trigger: "touch", restoreFocus: null });
				swallowClick = true;
			}, LONG_PRESS_MS),
		};
	};

	const onPointerMove = (e: PointerEvent) => {
		if (!press || e.pointerId !== press.id) return;
		if (Math.hypot(e.clientX - press.x, e.clientY - press.y) > LONG_PRESS_SLOP)
			cancelPress();
	};

	const onPointerEnd = () => {
		cancelPress();
		if (swallowClick) {
			clearTimeout(swallowTimer);
			swallowTimer = setTimeout(() => {
				swallowClick = false;
			}, SWALLOW_CLICK_MS);
		}
	};

	const onClick = (e: MouseEvent) => {
		if (!swallowClick) return;
		swallowClick = false;
		clearTimeout(swallowTimer);
		e.preventDefault();
		e.stopPropagation();
	};

	document.addEventListener("contextmenu", onContextMenu);
	document.addEventListener("keydown", onKeyDown, true);
	document.addEventListener("pointerdown", onPointerDown, {
		capture: true,
		passive: true,
	});
	document.addEventListener("pointermove", onPointerMove, {
		capture: true,
		passive: true,
	});
	document.addEventListener("pointerup", onPointerEnd, true);
	document.addEventListener("pointercancel", onPointerEnd, true);
	document.addEventListener("click", onClick, true);
	return () => {
		cancelPress();
		clearTimeout(swallowTimer);
		document.removeEventListener("contextmenu", onContextMenu);
		document.removeEventListener("keydown", onKeyDown, true);
		document.removeEventListener("pointerdown", onPointerDown, true);
		document.removeEventListener("pointermove", onPointerMove, true);
		document.removeEventListener("pointerup", onPointerEnd, true);
		document.removeEventListener("pointercancel", onPointerEnd, true);
		document.removeEventListener("click", onClick, true);
	};
}
