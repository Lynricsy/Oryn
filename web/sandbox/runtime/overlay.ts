// 选择 / 评论模式的覆盖层：自定义元素 `<oryn-overlay>`，内容在 closed shadow root 里，页面脚本拿不到也改不了。
// 铺满视口、不接收指针；每次 `render` 按视口坐标摆放悬停框、选中框、标签名与编号图钉。
// 所有 DOM 操作都走 `dom.ts` 里缓存的原生函数（见那里的说明）。

import type { Dom } from "./dom";
import type { Box } from "./messages";

export const OVERLAY_TAG = "oryn-overlay";

/** 画在视口坐标 `(x, y)` 处的评论图钉（元素的左上角）。 */
export interface OverlayPin {
	n: number;
	x: number;
	y: number;
	answered: boolean;
}

/** 一帧要画的东西；`render(null)` 把覆盖层整个藏起来。 */
export interface OverlayScene {
	/** 强调色（任意 CSS 颜色；不合法时沿用上一次的或缺省的蓝色）。 */
	accent: string;
	hover: Box | null;
	selected: Box | null;
	/** 标签名，贴在某个框的左上方。 */
	label: { text: string; box: Box } | null;
	pins: readonly OverlayPin[];
}

export interface Overlay {
	/** 覆盖层的宿主元素：选取时要跳过它。 */
	readonly host: HTMLElement;
	render(scene: OverlayScene | null): void;
}

const DEFAULT_ACCENT = "#2563eb";
const LABEL_HEIGHT = 18;
const PIN_SIZE = 20;

// `:host` 里的 `!important` 胜过页面样式表对这个元素的任何声明
const CSS = `:host{all:initial!important;display:block!important;position:fixed!important;inset:0!important;z-index:2147483647!important;pointer-events:none!important;contain:strict!important;overflow:hidden!important}
.root{--a:${DEFAULT_ACCENT};position:absolute;inset:0}
.box,.label,.pin{position:absolute;box-sizing:border-box;display:none}
.hover{border:1px dashed var(--a);background:color-mix(in srgb,var(--a) 6%,transparent)}
.sel{border:2px solid var(--a)}
.label{height:${LABEL_HEIGHT}px;padding:0 5px;border-radius:3px;background:var(--a);color:#fff;font:500 11px/${LABEL_HEIGHT}px ui-monospace,SFMono-Regular,Menlo,monospace;white-space:nowrap}
.pin{width:${PIN_SIZE}px;height:${PIN_SIZE}px;margin:-${PIN_SIZE / 2}px 0 0 -${PIN_SIZE / 2}px;border-radius:50% 50% 50% 2px;background:var(--a);color:#fff;font:600 11px/${PIN_SIZE}px system-ui,sans-serif;text-align:center;box-shadow:0 1px 3px rgba(0,0,0,.35)}
.pin.answered{background:#a1a1aa;opacity:.75}`;

/**
 * 建好覆盖层（宿主与 closed shadow root 立即建好，先不放进文档），首次 `render` 有内容时才挂到 `<html>` 下。
 * 同时把 `oryn-overlay` 这个名字登记成自己的自定义元素：页面不能再定义同名元素去升级它，
 * 也不能对它 `attachInternals()`（`ElementInternals.shadowRoot` 能拿到 closed shadow root）。
 */
export function createOverlay(dom: Dom): Overlay {
	const { doc } = dom;
	class OverlayElement extends dom.HTMLElement {
		static disabledFeatures = ["internals"];
	}
	try {
		dom.defineElement(OVERLAY_TAG, OverlayElement);
	} catch {
		// 同一个窗口里已经登记过（运行时只会装一次，这里只是兜底）
	}
	const make = (parent: Node, tag: string, cls: string) => {
		const el = dom.createElement(doc, tag);
		if (cls) dom.setAttribute(el, "class", cls);
		dom.appendChild(parent, el);
		return el;
	};

	const host = dom.createElement(doc, OVERLAY_TAG);
	const shadow = dom.attachShadow(host, { mode: "closed" });
	dom.setText(make(shadow, "style", ""), CSS);
	const root = make(shadow, "div", "root");
	const hoverBox = make(root, "div", "box hover");
	const selBox = make(root, "div", "box sel");
	const pinLayer = make(root, "div", "");
	const label = make(root, "div", "label");
	const pinEls: HTMLElement[] = [];
	let shown = 0;
	let accent = "";

	const css = (el: HTMLElement, prop: string, value: string) =>
		dom.setProperty(dom.style(el), prop, value);
	const hide = (el: HTMLElement) => css(el, "display", "none");
	const place = (el: HTMLElement, x: number, y: number) => {
		css(el, "display", "block");
		css(el, "left", `${x}px`);
		css(el, "top", `${y}px`);
	};
	const frame = (el: HTMLElement, box: Box | null) => {
		if (!box) {
			hide(el);
			return;
		}
		place(el, box.x, box.y);
		css(el, "width", `${box.width}px`);
		css(el, "height", `${box.height}px`);
	};

	const attach = () => {
		if (dom.isConnected(host)) return;
		const html = dom.documentElement(doc);
		if (html) dom.appendChild(html, host);
	};

	const render = (scene: OverlayScene | null) => {
		if (!scene) {
			if (dom.isConnected(host)) css(root, "display", "none");
			return;
		}
		attach();
		css(root, "display", "block");
		if (scene.accent !== accent) {
			accent = scene.accent;
			css(root, "--a", accent === "" ? DEFAULT_ACCENT : accent);
		}
		frame(hoverBox, scene.hover);
		frame(selBox, scene.selected);
		if (scene.label) {
			const { box } = scene.label;
			dom.setText(label, scene.label.text);
			// 框贴着视口顶边时标签放进框里
			place(
				label,
				box.x,
				box.y >= LABEL_HEIGHT ? box.y - LABEL_HEIGHT : box.y + 2,
			);
		} else {
			hide(label);
		}
		const pins = scene.pins;
		for (let i = 0; i < pins.length; i++) {
			const pin = pins[i] as OverlayPin;
			let el = pinEls[i];
			if (!el) {
				el = make(pinLayer, "div", "pin");
				pinEls[i] = el;
			}
			dom.setAttribute(el, "class", pin.answered ? "pin answered" : "pin");
			dom.setText(el, `${pin.n}`);
			place(
				el,
				Math.max(pin.x, PIN_SIZE / 2 + 2),
				Math.max(pin.y, PIN_SIZE / 2 + 2),
			);
		}
		for (let i = pins.length; i < shown; i++) {
			const el = pinEls[i];
			if (el) hide(el);
		}
		shown = pins.length;
	};

	return { host, render };
}
