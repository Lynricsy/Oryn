/**
 * 自适应布局：三档断点（compact <600、medium 600–839、expanded ≥840）、折叠屏双段视口与软键盘处理。
 * 软键盘：用 `visualViewport` 计算被键盘遮挡的高度写入 CSS 变量 `--kb-inset`，输入区据此上移。
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

/**
 * 安装一次：跟踪软键盘高度（`--kb-inset`）与可视高度（`--vvh`），
 * 并在根元素上写 `data-bp`（当前断点）与 `data-segments`（"2" 左右双段，否则 "1"）。
 */
export function installViewportTracking(): () => void {
	const vv = window.visualViewport;
	const root = document.documentElement;
	const update = () => {
		const height = vv?.height ?? window.innerHeight;
		const offset = vv?.offsetTop ?? 0;
		const inset = Math.max(0, window.innerHeight - height - offset);
		root.style.setProperty("--kb-inset", `${Math.round(inset)}px`);
		root.style.setProperty("--vvh", `${Math.round(height)}px`);
		root.dataset.bp = breakpointFor(window.innerWidth);
		root.dataset.segments = readSegments(window) ? "2" : "1";
	};
	update();
	const mq = window.matchMedia?.(SEGMENTS_QUERY);
	vv?.addEventListener("resize", update);
	vv?.addEventListener("scroll", update);
	window.addEventListener("resize", update);
	mq?.addEventListener?.("change", update);
	return () => {
		vv?.removeEventListener("resize", update);
		vv?.removeEventListener("scroll", update);
		window.removeEventListener("resize", update);
		mq?.removeEventListener?.("change", update);
	};
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
