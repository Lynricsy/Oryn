// 页面运行时的选择与评论模式：接管页面的指针事件选取元素，回报选中元素的源码偏移、标签、文本与可编辑样式，
// 画覆盖层（悬停框、选中框、标签名、评论图钉），并按父页面的要求临时预览样式与文字。
// 只在跟随最新版本的帧上装（`config.runtime`）；运行在不可信页面里，DOM 操作一律走 `dom.ts` 缓存的原生函数。

import {
	EDITABLE_PROPS,
	type EditableStyle,
} from "../../src/lib/artifacts/editable";
import { type Dom, now } from "./dom";
import {
	type Box,
	isLoc,
	MAX_STYLE_VALUE_CHARS,
	type PageMessageBody,
	type PageSelection,
	type ParentMessage,
	SELECTION_TEXT_CHARS,
	type SelectMode,
	type SelectPin,
	TAG_PATTERN,
} from "./messages";
import { createOverlay, type OverlayPin, type OverlayScene } from "./overlay";

/** 每秒最多处理的选取（点击与 `select-pick`）次数；超出的选取被吞掉、不回报。 */
export const MAX_PICKS_PER_SECOND = 20;

/** 源码标记属性：`data-oryn-src="<mark>:<字节偏移>"`，由服务端在本次响应里给每个起始标签加上。 */
export const SOURCE_ATTR = "data-oryn-src";

/** 设计画布的源码位置属性：`data-oryn-loc="src/frames/x.tsx:行:列"`，由设计引擎在 dev 与构建里给每个 JSX 元素加上。 */
export const LOC_ATTR = "data-oryn-loc";

// 选择模式下吞掉的指针事件（页面自己的监听器收不到，也不触发默认行为）；`click` 与 `pointermove` 另行处理
const SWALLOWED = [
	"pointerdown",
	"mousedown",
	"pointerup",
	"mouseup",
	"dblclick",
	"auxclick",
];

// 没有内容的元素：不算「全是文本」，不能改文字。查表一律比较 `=== true`，原型上的同名属性不会误中
const VOID_TAGS: Record<string, true> = {
	area: true,
	base: true,
	br: true,
	col: true,
	embed: true,
	hr: true,
	img: true,
	input: true,
	link: true,
	meta: true,
	source: true,
	track: true,
	wbr: true,
};

// 取文本摘录时跳过内容的元素（与服务端 PlainText 去掉 script / style 内容一致）
const TEXTLESS_TAGS: Record<string, true> = {
	script: true,
	style: true,
	template: true,
	noscript: true,
};

// 摘录前最多收集的原始文本长度：折叠空白后取前 120 个字符，留足余量
const RAW_TEXT_BUDGET = 4096;

const ELEMENT_NODE = 1;
const TEXT_NODE = 3;
const CDATA_NODE = 4;

const OFFSET_DIGITS = /^[0-9]{1,15}$/;
const SPACES = /\s+/g;

/**
 * 滑动窗口限速：最近 `windowMs` 毫秒里最多放行 `max` 次。返回判定函数，入参是当前时刻（毫秒）。
 * 用定长环形缓冲记最近放行的时刻，不依赖页面能改写的数组方法。
 */
export function createPickLimiter(
	max: number,
	windowMs: number,
): (at: number) => boolean {
	const ring: number[] = [];
	let next = 0;
	return (at) => {
		const oldest = ring[next];
		if (oldest !== undefined && at - oldest < windowMs) return false;
		ring[next] = at;
		next = (next + 1) % max;
		return true;
	};
}

/** 前 `n` 个码点。 */
function firstRunes(s: string, n: number): string {
	let i = 0;
	for (let k = 0; k < n && i < s.length; k++) {
		const c = s.charCodeAt(i);
		i += c >= 0xd800 && c <= 0xdbff && i + 1 < s.length ? 2 : 1;
	}
	return s.slice(0, i);
}

export interface SelectionConfig {
	/** 本次响应的源码标记；为空串时（查看器页）一切元素的 `src` 都是 `null`。 */
	mark: string;
	path: string;
}

export interface SelectionHandle {
	/** 处理父页面发来的选择类消息（`select-*`、`preview-*`）；别的消息忽略。 */
	handle(msg: ParentMessage): void;
}

/**
 * 在页面上装选择与评论模式。`dom` 必须在页面脚本运行之前取得；`send` 把消息发给父页面。
 * 捕获阶段的指针监听在这里注册，所以要在运行时的链接拦截之前调用：选择模式下点链接只算选取，不算跳转。
 */
export function installSelection(
	dom: Dom,
	config: SelectionConfig,
	send: (body: PageMessageBody) => void,
): SelectionHandle {
	const { win, doc } = dom;
	const overlay = createOverlay(dom);
	const allowPick = createPickLimiter(MAX_PICKS_PER_SECOND, 1000);
	const prefix = config.mark === "" ? "" : `${config.mark}:`;

	let mode: SelectMode = null;
	let accent = "";
	let hover: Element | null = null;
	let selected: Element | null = null;
	let pins: SelectPin[] = [];
	// 第一次预览前选中元素的 `style` 属性与文字（文字只在预览过文字时记下），`preview-reset` 据此还原
	let snapshot: {
		el: Element;
		style: string | null;
		text: string | null;
	} | null = null;
	// 源码偏移 → 元素；DOM 变化后作废，下次查找时重建
	let index: Record<string, Element> | null = null;
	let scheduled = false;

	const observer = new dom.MutationObserver(() => {
		index = null;
	});
	dom.observe(observer, doc, {
		childList: true,
		subtree: true,
		attributes: true,
		attributeFilter: [SOURCE_ATTR],
	});

	const tagOf = (el: Element) => dom.localName(el).toLowerCase();

	// 元素自己带的、属于本次响应的源码偏移。祖先的偏移不能借用：锚点与直接编辑都要求偏移处正是这个标签
	const srcOf = (el: Element): number | null => {
		if (prefix === "") return null;
		const v = dom.getAttribute(el, SOURCE_ATTR);
		if (v === null || v.slice(0, prefix.length) !== prefix) return null;
		const digits = v.slice(prefix.length);
		return OFFSET_DIGITS.test(digits) ? Number(digits) : null;
	};

	const buildIndex = (): Record<string, Element> => {
		const out = Object.create(null) as Record<string, Element>;
		const list = dom.querySelectorAll(doc, `[${SOURCE_ATTR}]`);
		const n = dom.listLength(list);
		for (let i = 0; i < n; i++) {
			const el = dom.listItem(list, i) as Element;
			const src = srcOf(el);
			if (src !== null && out[src] === undefined) out[src] = el;
		}
		return out;
	};

	const find = (src: number): Element | null => {
		if (prefix === "") return null;
		let el = index?.[src];
		// 变动的通知是异步的：命中的元素已不在文档里或偏移变了就当场重建
		if (!index || (el && (!dom.isConnected(el) || srcOf(el) !== src))) {
			index = buildIndex();
			el = index[src];
		}
		return el ?? null;
	};

	// 设计画布的源码位置：元素自己的，没有就取最近祖先的（组件内部的元素也能指回帧源码里的调用处）
	const locOf = (el: Element): string | null => {
		let n: Node | null = el;
		while (n && dom.nodeType(n) === ELEMENT_NODE) {
			const v = dom.getAttribute(n as Element, LOC_ATTR);
			if (isLoc(v)) return v;
			n = dom.parentNode(n);
		}
		return null;
	};

	// 带这个源码位置的第一个元素（同一处 JSX 渲染出多个元素时取文档顺序的第一个）
	const findLoc = (loc: string): Element | null => {
		const list = dom.querySelectorAll(doc, `[${LOC_ATTR}]`);
		const n = dom.listLength(list);
		for (let i = 0; i < n; i++) {
			const el = dom.listItem(list, i) as Element;
			if (dom.getAttribute(el, LOC_ATTR) === loc) return el;
		}
		return null;
	};

	// 事件目标或坐标处的元素 → 可选取的元素；`<html>`、`<body>` 与覆盖层算「没有元素」（整个页面）
	const resolve = (target: EventTarget | Node | null): Element | null => {
		try {
			let n = target as Node | null;
			while (n) {
				if (dom.nodeType(n) === ELEMENT_NODE) {
					if (n === overlay.host) return null;
					const tag = tagOf(n as Element);
					if (tag === "html" || tag === "body") return null;
					if (TAG_PATTERN.test(tag)) return n as Element;
				}
				n = dom.parentNode(n);
			}
		} catch {
			// 目标不是节点（例如 window）
		}
		return null;
	};

	const isLive = (el: Element | null): el is Element =>
		el !== null && dom.isConnected(el);

	const isTextOnly = (el: Element): boolean => {
		if (VOID_TAGS[tagOf(el)] === true) return false;
		for (let n = dom.firstChild(el); n; n = dom.nextSibling(n)) {
			if (dom.nodeType(n) !== TEXT_NODE) return false;
		}
		return true;
	};

	const textOf = (el: Element): string => {
		let raw = "";
		let n = dom.firstChild(el);
		while (n && raw.length < RAW_TEXT_BUDGET) {
			const type = dom.nodeType(n);
			if (type === TEXT_NODE || type === CDATA_NODE)
				raw += dom.nodeValue(n) ?? "";
			const child =
				type === ELEMENT_NODE && TEXTLESS_TAGS[tagOf(n as Element)] !== true
					? dom.firstChild(n)
					: null;
			if (child) {
				n = child;
				continue;
			}
			// 没有子节点可进：找下一个兄弟，没有就往上退
			while (n && n !== el && !dom.nextSibling(n)) n = dom.parentNode(n);
			n = n && n !== el ? dom.nextSibling(n) : null;
		}
		return firstRunes(raw.replace(SPACES, " ").trim(), SELECTION_TEXT_CHARS);
	};

	const styleOf = (el: Element): EditableStyle => {
		const cs = dom.getComputedStyle(el);
		const out = {} as EditableStyle;
		for (let i = 0; i < EDITABLE_PROPS.length; i++) {
			const prop = EDITABLE_PROPS[i] as (typeof EDITABLE_PROPS)[number];
			out[prop] = dom
				.getPropertyValue(cs, prop)
				.slice(0, MAX_STYLE_VALUE_CHARS);
		}
		return out;
	};

	const describe = (el: Element): PageSelection => ({
		path: config.path,
		src: srcOf(el),
		tag: tagOf(el),
		text: textOf(el),
		textOnly: isTextOnly(el),
		style: styleOf(el),
		loc: locOf(el),
	});

	const boxOf = (el: Element): Box => {
		const r = dom.rect(el);
		return { x: r.left, y: r.top, width: r.width, height: r.height };
	};

	const scene = (): OverlayScene | null => {
		const hoverBox = isLive(hover) && hover !== selected ? boxOf(hover) : null;
		const selBox = isLive(selected) ? boxOf(selected) : null;
		const drawn: OverlayPin[] = [];
		let k = 0;
		for (let i = 0; i < pins.length; i++) {
			const pin = pins[i] as SelectPin;
			const el = pin.src === null ? null : find(pin.src);
			if (!el) continue;
			const b = boxOf(el);
			drawn[k++] = { n: pin.n, x: b.x, y: b.y, answered: pin.answered };
		}
		if (!hoverBox && !selBox && k === 0) return null;
		let label: OverlayScene["label"] = null;
		if (hoverBox && hover) label = { text: tagOf(hover), box: hoverBox };
		else if (selBox && selected) label = { text: tagOf(selected), box: selBox };
		return { accent, hover: hoverBox, selected: selBox, label, pins: drawn };
	};

	// 有东西要画时每帧重画一次，跟上滚动、布局变化与动画；没有时藏起覆盖层并停下
	const paint = () => {
		scheduled = false;
		const s = scene();
		overlay.render(s);
		if (s) redraw();
	};
	const redraw = () => {
		if (scheduled) return;
		scheduled = true;
		dom.frame(paint);
	};

	const setHover = (el: Element | null) => {
		if (el === hover) return;
		hover = el;
		redraw();
	};

	const select = (el: Element | null) => {
		if (el !== selected) {
			// 换了元素：之前的预览要么已经保存，要么由父页面先发 preview-reset，这里只丢掉快照
			snapshot = null;
			selected = el;
		}
		redraw();
	};

	const pick = (target: EventTarget | Node | null, x: number, y: number) => {
		if (!allowPick(now())) return;
		const el = resolve(target);
		select(el);
		send({ kind: "select", selection: el ? describe(el) : null, at: { x, y } });
	};

	const takeSnapshot = (el: Element) => {
		if (snapshot?.el !== el) {
			snapshot = { el, style: dom.getAttribute(el, "style"), text: null };
		}
		return snapshot;
	};

	const previewStyle = (style: Partial<EditableStyle>) => {
		if (!isLive(selected)) return;
		let decl: CSSStyleDeclaration;
		try {
			decl = dom.style(selected);
		} catch {
			return; // 不是 HTML 元素（SVG 等）：不能直接编辑
		}
		let first = true;
		for (let i = 0; i < EDITABLE_PROPS.length; i++) {
			const prop = EDITABLE_PROPS[i] as (typeof EDITABLE_PROPS)[number];
			const value = style[prop];
			if (value === undefined) continue;
			if (first) {
				takeSnapshot(selected);
				first = false;
			}
			dom.setProperty(decl, prop, value);
		}
		redraw();
	};

	const previewText = (text: string) => {
		if (!isLive(selected) || !isTextOnly(selected)) return;
		const snap = takeSnapshot(selected);
		if (snap.text === null) snap.text = dom.getText(selected) ?? "";
		dom.setText(selected, text);
		redraw();
	};

	const resetPreview = () => {
		if (!snapshot) return;
		const { el, style, text } = snapshot;
		snapshot = null;
		if (style === null) dom.removeAttribute(el, "style");
		else dom.setAttribute(el, "style", style);
		if (text !== null) dom.setText(el, text);
		redraw();
	};

	const swallow = (event: Event) => {
		if (mode === null) return;
		dom.preventDefault(event);
		dom.stopImmediatePropagation(event);
	};
	for (let i = 0; i < SWALLOWED.length; i++) {
		win.addEventListener(SWALLOWED[i] as string, swallow, true);
	}
	win.addEventListener(
		"pointermove",
		(event: Event) => {
			if (mode === null) return;
			dom.stopImmediatePropagation(event);
			setHover(resolve(dom.target(event)));
		},
		true,
	);
	win.addEventListener(
		"pointerout",
		(event: Event) => {
			// 指针离开了帧
			if (mode !== null && dom.relatedTarget(event as MouseEvent) === null) {
				setHover(null);
			}
		},
		true,
	);
	win.addEventListener(
		"click",
		(event: Event) => {
			if (mode === null) return;
			dom.preventDefault(event);
			dom.stopImmediatePropagation(event);
			const e = event as MouseEvent;
			pick(dom.target(e), dom.clientX(e), dom.clientY(e));
		},
		true,
	);

	return {
		handle(msg) {
			switch (msg.kind) {
				case "select-mode":
					mode = msg.mode;
					accent = msg.accent;
					if (mode === null) {
						hover = null;
						select(null);
					}
					redraw();
					break;
				case "select-point":
					if (mode !== null)
						setHover(resolve(dom.elementFromPoint(msg.x, msg.y)));
					break;
				case "select-leave":
					setHover(null);
					break;
				case "select-pick":
					if (mode !== null)
						pick(dom.elementFromPoint(msg.x, msg.y), msg.x, msg.y);
					break;
				case "select-source": {
					if (msg.src === null) {
						select(null);
						break;
					}
					const el = find(msg.src);
					select(el);
					send({ kind: "select", selection: el ? describe(el) : null });
					break;
				}
				case "select-pins":
					pins = msg.pins;
					redraw();
					break;
				case "select-locate": {
					const el = find(msg.src);
					send({
						kind: "select",
						selection: isLive(selected) ? describe(selected) : null,
						located: { src: msg.src, box: el ? boxOf(el) : null },
					});
					break;
				}
				case "select-loc": {
					const el = findLoc(msg.loc);
					select(el);
					send({ kind: "select", selection: el ? describe(el) : null });
					break;
				}
				case "preview-style":
					previewStyle(msg.style);
					break;
				case "preview-text":
					previewText(msg.text);
					break;
				case "preview-reset":
					resetPreview();
					break;
			}
		},
	};
}
