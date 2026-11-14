/**
 * 桌面壳的自绘标题栏（无系统标题栏，bridge ≥ 3）：
 * - 布局取壳注入的 `__ORYN_HOST__.chrome = {platform, controls, buttons, inset}`（首帧前就在，页面直接按它留位置）。
 *   `controls`：`native-left` = macOS 原生红绿灯在左上（`inset.left` 是页头要让出的宽度）；`custom-left` / `custom-right` =
 *   页面在左 / 右自绘 `buttons`（按给出的顺序）；`none` 或没有 `chrome` = 什么都不做。
 * - 窗口状态（最大化 / 全屏 / 聚焦）：挂载时 `host_window{action:"state"}` 取一次，之后壳经 `oryn:host` 的 `window` 事件推送。
 * - 动作：`host_window{action}`（minimize / toggleMaximize / close / startDrag）；关闭等同原生关闭（壳按「关闭时隐藏到托盘」处理）。
 * - 拖动区：带 `data-window-drag` 的元素整棵子树（`"false"` 关闭）。按住左键移动超过 3px 才开始拖动、双击切换最大化；
 *   路径上先遇到可交互元素（按钮、链接、输入框、tabindex、交互 role）就不拖，交互元素不会被拖动区吃掉。
 * 浏览器、移动壳、旧壳（bridge < 3）里 `createWindowChrome` 为 null：不显示按钮、不改留白、不装监听。
 */
import { useSyncExternalStore } from "react";
import { findInvoke, HOST_EVENT, type Invoke } from "./host";

export type ChromeControls =
	| "native-left"
	| "custom-left"
	| "custom-right"
	| "none";
export type WindowButton = "minimize" | "maximize" | "close";
export type DesktopPlatform = "linux" | "macos" | "windows";

export interface ChromeInset {
	/** 标题行高度（CSS px）。 */
	top: number;
	/** 原生控件占掉的左侧宽度（从窗口左缘量）。 */
	left: number;
	right: number;
}

export interface HostChrome {
	platform: DesktopPlatform;
	controls: ChromeControls;
	/** 自绘按钮从左到右的顺序；原生控件时为空。 */
	buttons: WindowButton[];
	inset: ChromeInset;
}

export interface WindowFlags {
	maximized: boolean;
	fullscreen: boolean;
	focused: boolean;
}

export type WindowAction =
	| "minimize"
	| "toggleMaximize"
	| "close"
	| "startDrag";

export interface WindowChrome {
	readonly chrome: HostChrome;
	getFlags(): WindowFlags;
	subscribe(cb: () => void): () => void;
	act(action: WindowAction): void;
}

/** 拖动区属性：整棵子树都可拖，值为 `"false"` 时关闭。 */
export const DRAG_ATTR = "data-window-drag";

const CONTROLS: readonly ChromeControls[] = [
	"native-left",
	"custom-left",
	"custom-right",
	"none",
];
const BUTTONS: readonly WindowButton[] = ["minimize", "maximize", "close"];
const DESKTOP: readonly DesktopPlatform[] = ["linux", "macos", "windows"];

function px(v: unknown): number {
	return typeof v === "number" && Number.isFinite(v) && v > 0 ? v : 0;
}

/** 解析 `__ORYN_HOST__.chrome`；不认识的平台 / 控件取值返回 null，未知按钮名丢掉。 */
export function parseChrome(v: unknown): HostChrome | null {
	if (!v || typeof v !== "object") return null;
	const o = v as Record<string, unknown>;
	const platform = DESKTOP.find((p) => p === o.platform);
	const controls = CONTROLS.find((c) => c === o.controls);
	if (!platform || !controls) return null;
	const buttons: WindowButton[] = [];
	if (Array.isArray(o.buttons))
		for (const b of o.buttons) {
			const name = BUTTONS.find((x) => x === b);
			if (name && !buttons.includes(name)) buttons.push(name);
		}
	const inset = (o.inset ?? {}) as Record<string, unknown>;
	return {
		platform,
		controls,
		buttons,
		inset: { top: px(inset.top), left: px(inset.left), right: px(inset.right) },
	};
}

function parseFlags(v: unknown, prev: WindowFlags): WindowFlags {
	if (!v || typeof v !== "object") return prev;
	const o = v as Record<string, unknown>;
	const bool = (x: unknown, d: boolean) => (typeof x === "boolean" ? x : d);
	return {
		maximized: bool(o.maximized, prev.maximized),
		fullscreen: bool(o.fullscreen, prev.fullscreen),
		focused: bool(o.focused, prev.focused),
	};
}

const CLICKABLE = new Set([
	"A",
	"BUTTON",
	"INPUT",
	"SELECT",
	"TEXTAREA",
	"LABEL",
	"SUMMARY",
]);
const INTERACTIVE_ROLES = new Set([
	"button",
	"link",
	"menuitem",
	"tab",
	"checkbox",
	"radio",
	"switch",
	"option",
	"slider",
	"textbox",
]);

function interactive(el: Element): boolean {
	const editable = el.getAttribute("contenteditable");
	const tabindex = el.getAttribute("tabindex");
	return (
		CLICKABLE.has(el.tagName) ||
		(editable !== null && editable !== "false") ||
		(tabindex !== null && tabindex !== "-1") ||
		INTERACTIVE_ROLES.has(el.getAttribute("role") ?? "")
	);
}

/** 事件路径（从目标往上）是否落在拖动区里、且之间没有可交互元素。 */
export function inDragRegion(path: readonly EventTarget[]): boolean {
	for (const t of path) {
		if (!(t instanceof Element)) continue;
		const attr = t.getAttribute(DRAG_ATTR);
		if (attr === "false") return false;
		if (interactive(t)) return false;
		if (attr !== null) return true;
	}
	return false;
}

/** 按下后移动超过这么多 CSS px 才交给窗口管理器拖动（单纯点击不抓指针，双击才认得出来）。 */
const DRAG_SLOP = 3;

/**
 * 在 `win` 上装拖动区监听：按下左键并开始移动时交给窗口管理器拖动，双击切换最大化。
 * 不在按下的瞬间就拖：Linux 上按下即 `begin_move_drag` 会让窗口管理器抓走指针，WebView 收不到这次松开，
 * 第二次按下也就不算双击；macOS 的双击本来就要在松开且没动时才算（`dblclick` 正是如此）。
 */
function installDrag(win: Window, act: (a: WindowAction) => void) {
	const doc = win.document as Document | undefined;
	if (!doc) return;
	let pressed: { x: number; y: number } | null = null;
	doc.addEventListener("mousedown", (e) => {
		pressed = null;
		if (e.button !== 0 || !inDragRegion(e.composedPath())) return;
		// 不选中文字、不让拖动区抢走输入框焦点
		e.preventDefault();
		pressed = { x: e.clientX, y: e.clientY };
	});
	doc.addEventListener("mousemove", (e) => {
		if (!pressed) return;
		if ((e.buttons & 1) === 0) {
			pressed = null;
			return;
		}
		if (
			Math.abs(e.clientX - pressed.x) < DRAG_SLOP &&
			Math.abs(e.clientY - pressed.y) < DRAG_SLOP
		)
			return;
		pressed = null;
		act("startDrag");
	});
	doc.addEventListener("mouseup", () => {
		pressed = null;
	});
	doc.addEventListener("dblclick", (e) => {
		if (e.button === 0 && inDragRegion(e.composedPath())) act("toggleMaximize");
	});
}

/** 按注入的 window 判定（测试可传假 window）；不在支持自绘标题栏的桌面壳里返回 null。 */
export function createWindowChrome(win: Window): WindowChrome | null {
	const meta = win.__ORYN_HOST__;
	if (meta?.kind !== "tauri" || (meta.bridge ?? 0) < 3) return null;
	const invoke: Invoke | null = findInvoke(win);
	const chrome = parseChrome(meta.chrome);
	if (!invoke || !chrome || chrome.controls === "none") return null;

	let flags: WindowFlags = {
		maximized: false,
		fullscreen: false,
		focused: true,
	};
	const subs = new Set<() => void>();
	const set = (next: WindowFlags) => {
		if (
			next.maximized === flags.maximized &&
			next.fullscreen === flags.fullscreen &&
			next.focused === flags.focused
		)
			return;
		flags = next;
		for (const cb of [...subs]) cb();
	};
	const call = (action: WindowAction | "state") => {
		invoke<WindowFlags>("host_window", { action }).then(
			(r) => set(parseFlags(r, flags)),
			(e) => console.warn(`[window] ${action} failed`, e),
		);
	};

	win.addEventListener(HOST_EVENT, (ev) => {
		const d = (ev as CustomEvent<unknown>).detail as { type?: unknown } | null;
		if (d && typeof d === "object" && d.type === "window")
			set(parseFlags(d, flags));
	});
	installDrag(win, call);
	call("state");

	return {
		chrome,
		getFlags: () => flags,
		subscribe: (cb) => {
			subs.add(cb);
			return () => subs.delete(cb);
		},
		act: call,
	};
}

let current: WindowChrome | null = createWindowChrome(
	typeof window === "undefined" ? ({} as Window) : window,
);
const swaps = new Set<() => void>();

/** 当前页面的标题栏适配（浏览器与移动壳里为 null）。 */
export function windowChrome(): WindowChrome | null {
	return current;
}

/** 测试与截图夹具用：换掉当前页面的标题栏适配。 */
export function setWindowChrome(wc: WindowChrome | null): void {
	current = wc;
	for (const cb of [...swaps]) cb();
}

export interface WindowChromeView {
	chrome: HostChrome;
	flags: WindowFlags;
	act: (action: WindowAction) => void;
}

let cachedFor: WindowChrome | null = null;
let cachedFlags: WindowFlags | null = null;
let cachedView: WindowChromeView | null = null;

function snapshot(): WindowChromeView | null {
	const wc = current;
	if (!wc) return null;
	const flags = wc.getFlags();
	if (wc !== cachedFor || flags !== cachedFlags) {
		cachedFor = wc;
		cachedFlags = flags;
		cachedView = { chrome: wc.chrome, flags, act: wc.act };
	}
	return cachedView;
}

function subscribe(cb: () => void): () => void {
	swaps.add(cb);
	let off = current?.subscribe(cb) ?? (() => {});
	const onSwap = () => {
		off();
		off = current?.subscribe(cb) ?? (() => {});
	};
	swaps.add(onSwap);
	return () => {
		off();
		swaps.delete(cb);
		swaps.delete(onSwap);
	};
}

/** 标题栏布局与窗口状态；不在自绘标题栏的桌面壳里为 null。 */
export function useWindowChrome(): WindowChromeView | null {
	return useSyncExternalStore(subscribe, snapshot, () => null);
}
