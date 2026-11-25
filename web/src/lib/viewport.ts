/**
 * 自适应布局：三档断点（compact <600、medium 600–839、expanded ≥840）、折叠屏双段视口与软键盘处理。
 * 软键盘：用 `visualViewport` 计算被键盘遮挡的高度写入 CSS 变量 `--kb-inset`，输入区据此上移；
 * 系统为露出输入框把整页推上去（iOS 平移视觉视口）时拉回顶部，只让输入区自己让位。
 * 双段：Viewport Segments API（`window.viewport.segments`）报告左右两段时，线程页按铰链分成两栏。
 */
import { useSyncExternalStore } from "react";

export type Breakpoint = "compact" | "medium" | "expanded";

declare global {
	interface Window {
		/** Viewport Segments API（Chromium 折叠屏）；不支持的浏览器没有此属性。 */
		viewport?: { segments?: readonly DOMRectReadOnly[] | null };
	}
}

export function breakpointFor(width: number): Breakpoint {
	if (width < 600) return "compact";
	if (width < 840) return "medium";
	return "expanded";
}

function subscribeResize(cb: () => void) {
	window.addEventListener("resize", cb);
	return () => window.removeEventListener("resize", cb);
}

export function useBreakpoint(): Breakpoint {
	return useSyncExternalStore(
		subscribeResize,
		() => breakpointFor(window.innerWidth),
		() => "expanded",
	);
}

// 顶部安全区（状态栏 / 灵动岛）的 px：env() 与 --safe-top 只能在 CSS 里取，用一个隐藏探针读计算值。
// 不缓存数值：没人订阅时（不在线程页）转屏不会有人清缓存，回来会沿用旧值而 env() 已经变了；每次读计算值，number 本身就稳定
let safeTopProbe: HTMLDivElement | null = null;
const safeAreaListeners = new Set<() => void>();

/** 移动壳更新 `--host-safe-*` 后通知安全区订阅者重读计算值（避免未触发 window resize 时页头停留在 0）。 */
export function notifySafeAreaChange(): void {
	for (const cb of [...safeAreaListeners]) cb();
}

function subscribeSafeTop(cb: () => void) {
	const offResize = subscribeResize(cb);
	safeAreaListeners.add(cb);
	return () => {
		offResize();
		safeAreaListeners.delete(cb);
	};
}

function readSafeTop(): number {
	if (!safeTopProbe?.isConnected) {
		safeTopProbe = document.createElement("div");
		safeTopProbe.setAttribute("aria-hidden", "true");
		safeTopProbe.style.cssText =
			"position:fixed;top:0;left:0;width:0;height:0;visibility:hidden;pointer-events:none;padding-top:var(--safe-top, env(safe-area-inset-top))";
		document.body.appendChild(safeTopProbe);
	}
	return Number.parseFloat(getComputedStyle(safeTopProbe).paddingTop) || 0;
}

/** 顶部安全区高度（px）：铺满整屏的移动 WebView（iOS / Android）里是状态栏 / 灵动岛 / 挖孔的高度，其它宿主通常为 0。 */
export function useSafeTop(): number {
	return useSyncExternalStore(subscribeSafeTop, readSafeTop, () => 0);
}

/** 左右双段视口：左段宽、铰链宽（两段之间的缝）、右段宽（px）。 */
export interface DualSegments {
	left: number;
	gap: number;
	right: number;
}

/**
 * 读双段视口：恰好两段、同一行（top 与 height 相同）且第二段在第一段右侧不重叠时返回各段宽度；
 * 没有该 API、单段、上下排列（折叠轴水平）或重叠时返回 null。
 */
export function readSegments(win: Window): DualSegments | null {
	const segs = win.viewport?.segments;
	if (segs?.length !== 2) return null;
	const [a, b] = segs as readonly [DOMRectReadOnly, DOMRectReadOnly];
	if (a.top !== b.top || a.height !== b.height) return null;
	const edge = a.x + a.width;
	if (b.x < edge) return null;
	return { left: a.width, gap: b.x - edge, right: b.width };
}

const SEGMENTS_QUERY = "(horizontal-viewport-segments: 2)";

/** 段数变化：折叠姿态改变时浏览器发 resize，也会让段数媒体查询变化，两个都订阅。 */
function subscribeSegments(cb: () => void) {
	window.addEventListener("resize", cb);
	const mq = window.matchMedia?.(SEGMENTS_QUERY);
	mq?.addEventListener?.("change", cb);
	return () => {
		window.removeEventListener("resize", cb);
		mq?.removeEventListener?.("change", cb);
	};
}

// useSyncExternalStore 要求快照引用稳定：值不变时返回同一个对象
let lastSegments: DualSegments | null = null;

function segmentsSnapshot(): DualSegments | null {
	const next = readSegments(window);
	const prev = lastSegments;
	if (
		next &&
		prev &&
		next.left === prev.left &&
		next.gap === prev.gap &&
		next.right === prev.right
	)
		return prev;
	lastSegments = next;
	return next;
}

/** 当前的左右双段视口（折叠屏半开 / 双屏设备横跨），否则 null。 */
export function useViewportSegments(): DualSegments | null {
	return useSyncExternalStore(subscribeSegments, segmentsSnapshot, () => null);
}
/** 视觉视口缩放偏离 1 超过这个量就是双指放大：视觉视口变矮、偏移都不是软键盘造成的。 */
const SCALE_EPS = 0.01;

/**
 * 软键盘遮住的应用壳底部高度（px）= 壳高 − 视觉视口高。按视觉视口贴着顶算，不扣它被推走的偏移：
 * 推走会由 `settleViewport` 拉回，拉回后输入区正好让出这么多。双指放大时量不准，记 0。
 * 壳高取 `innerHeight` 与 `shell`（量出的 100dvh，应用壳 `h-dvh` 的实际高度）的大者：实测 iOS 27.2 模拟器（带
 * `interactive-widget=resizes-content`）弹键盘时 `innerHeight` 跟着视觉视口缩，但 dvh 不缩、壳仍是整屏高，
 * 只拿 `innerHeight` 会把键盘算成 0、输入坞被压在键盘底下；Chromium 里两者一起缩，壳自己就让开了，结果是 0。
 */
export function keyboardInset(
	win: Pick<Window, "innerHeight" | "visualViewport">,
	shell = 0,
): number {
	const vv = win.visualViewport;
	if (!vv || Math.abs(vv.scale - 1) > SCALE_EPS) return 0;
	return Math.max(0, Math.max(win.innerHeight, shell) - vv.height);
}

/**
 * 页面被推走时拉回顶部。iOS 弹出软键盘会平移视觉视口去露出聚焦的输入框，整页跟着上移一大截；
 * 应用壳不滚动、输入区自己按 `--kb-inset` 让出键盘，这段平移是多余的。键盘收起后页面也常停在平移状态。
 * 不拉回：双指放大中；聚焦的元素拉回后会落到键盘底下（对话框、设置页靠下的输入框没有随键盘让位，仍靠系统平移露出来）。
 * 只动纵轴：下沉侧栏的根横滚模式里横向滚动位置就是侧栏开合。
 */
export function settleViewport(
	win: Pick<
		Window,
		"visualViewport" | "scrollX" | "scrollY" | "scrollTo" | "document"
	>,
): void {
	const vv = win.visualViewport;
	if (!vv || Math.abs(vv.scale - 1) > SCALE_EPS) return;
	if (vv.offsetTop < 1 && win.scrollY < 1) return;
	const focused = win.document.activeElement;
	// 布局视口在顶上（文档不能纵向滚动），客户区坐标就是拉回后的位置；视觉视口高以下是键盘
	if (
		focused &&
		focused !== win.document.body &&
		focused.getBoundingClientRect().bottom > vv.height + 1
	)
		return;
	win.scrollTo(win.scrollX, 0);
}

/**
 * 安装一次：跟踪软键盘高度（`--kb-inset`）与可视高度（`--vvh`），页面被推走时拉回（`settleViewport`），
 * 并在根元素上写 `data-bp`（当前断点）与 `data-segments`（"2" 左右双段，否则 "1"）。
 */
export function installViewportTracking(): () => void {
	const vv = window.visualViewport;
	const root = document.documentElement;
	// 量 100dvh 的隐形尺子（与应用壳 h-dvh 同一单位）：keyboardInset 的壳高
	const ruler = document.createElement("div");
	ruler.setAttribute("aria-hidden", "true");
	ruler.style.cssText =
		"position:fixed;top:0;left:0;width:0;height:100dvh;visibility:hidden;pointer-events:none";
	(document.body ?? root).append(ruler);
	const update = () => {
		const height = vv?.height ?? window.innerHeight;
		root.style.setProperty(
			"--kb-inset",
			`${Math.round(keyboardInset(window, ruler.getBoundingClientRect().height))}px`,
		);
		root.style.setProperty("--vvh", `${Math.round(height)}px`);
		root.dataset.bp = breakpointFor(window.innerWidth);
		root.dataset.segments = readSegments(window) ? "2" : "1";
		// 先写 --kb-inset 再判定：输入区按新的让位排版后，才知道拉回会不会把聚焦的输入框压到键盘底下
		settleViewport(window);
	};
	const settle = () => settleViewport(window);
	update();
	const mq = window.matchMedia?.(SEGMENTS_QUERY);
	vv?.addEventListener("resize", update);
	vv?.addEventListener("scroll", update);
	window.addEventListener("resize", update);
	// 根横滚（下沉侧栏）时 scroll 每帧都来：这里只判定拉回，不重写变量
	window.addEventListener("scroll", settle, { passive: true });
	mq?.addEventListener?.("change", update);
	return () => {
		vv?.removeEventListener("resize", update);
		vv?.removeEventListener("scroll", update);
		window.removeEventListener("resize", update);
		window.removeEventListener("scroll", settle);
		mq?.removeEventListener?.("change", update);
		ruler.remove();
	};
}

/** iOS / iPadOS 的 WebKit（Safari 与 WKWebView；iPadOS 桌面版 UA 报 MacIntel，按触点数认）。 */
function isIosWebKit(
	nav: Pick<Navigator, "userAgent" | "platform" | "maxTouchPoints">,
): boolean {
	return (
		/iP(hone|ad|od)/.test(nav.userAgent) ||
		(nav.platform === "MacIntel" && nav.maxTouchPoints > 1)
	);
}

/** viewport 内容追加 `maximum-scale=1`（已有 maximum-scale 时原样返回）。 */
function withoutFocusZoom(content: string): string {
	return /maximum-scale/i.test(content)
		? content
		: `${content}, maximum-scale=1`;
}

/**
 * 安装一次：iOS 上聚焦字号小于 16px 的输入框会自动放大页面，收起键盘后常常不缩回，
 * 整页停在放大 + 平移的状态（对话框被推出屏、原生页头因视口缩放判定被遮挡而一直让位）。
 * viewport 加 `maximum-scale=1` 关掉这种自动放大；iOS 10 起 Safari 仍允许用户双指缩放。其它平台不动（Android 上它会禁掉缩放）。
 */
export function installNoFocusZoom(
	doc: Document = document,
	nav: Pick<Navigator, "userAgent" | "platform" | "maxTouchPoints"> = navigator,
): void {
	if (!isIosWebKit(nav)) return;
	const meta = doc.querySelector<HTMLMetaElement>('meta[name="viewport"]');
	if (meta) meta.content = withoutFocusZoom(meta.content);
}

const MIN_SCALE = /,?\s*minimum-scale\s*=\s*1\b/i;

/**
 * 下沉侧栏的根滚动模式（`lib/native-sink.ts`）里文档比视口宽（侧栏 + 卡片）：viewport 加 `minimum-scale=1`，
 * 免得 WebKit 为了装下整个文档把页面缩小。退出时去掉。重复调用幂等。
 */
export function setMinScale(on: boolean, doc: Document = document): void {
	const meta = doc.querySelector<HTMLMetaElement>('meta[name="viewport"]');
	if (!meta) return;
	const base = meta.content.replace(MIN_SCALE, "");
	meta.content = on ? `${base}, minimum-scale=1` : base;
}

const COARSE = "(pointer: coarse)";

function subscribeCoarse(cb: () => void) {
	const mq = window.matchMedia?.(COARSE);
	mq?.addEventListener("change", cb);
	return () => mq?.removeEventListener("change", cb);
}

/** 主指针是否为粗指针（触屏）：触屏按键条、相机附件、批准页「返回 App」按它显示。 */
export function useCoarsePointer(): boolean {
	return useSyncExternalStore(
		subscribeCoarse,
		() => window.matchMedia?.(COARSE).matches ?? false,
		() => false,
	);
}
