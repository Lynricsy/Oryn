/**
 * 首条消息过渡：首页居中的输入托盘 → 线程页底部输入坞，看上去始终是同一块玻璃。
 *
 * 用 View Transitions：两边托盘在各自状态里挂 `.dock-morphing`（`view-transition-name: composer-dock`），
 * 外壳的位置与宽高交给 `::view-transition-group` 插值，模糊也在 group 上、实时模糊底下的页面快照，
 * 底色与描边画在 image-pair 上；新旧内容按原尺寸锚在左下角，早段快速交叉淡化，不拉伸文字（样式见 index.css「首条消息过渡」）。
 * 旧托盘由首页直接写类名（截旧快照前必须已生效），新托盘在线程页挂载时读本模块的阶段决定是否挂类名，
 * 同一时刻页面上只有一个元素带这个名字。不支持 View Transitions 时直接切换。
 */
import { useSyncExternalStore } from "react";

/** idle：没有过渡；dock：托盘变形；fade：减少动效时只做 120ms 淡入 */
export type DockMorph = "idle" | "dock" | "fade";

let phase: DockMorph = "idle";
/** 本次过渡是否从首页发送进来（线程页据此在结束后把焦点交给输入框） */
let arrival = false;
let arrived: (() => void) | null = null;
const listeners = new Set<() => void>();

function setPhase(next: DockMorph): void {
	phase = next;
	for (const l of listeners) l();
}

function subscribe(l: () => void): () => void {
	listeners.add(l);
	return () => listeners.delete(l);
}

export function useDockMorph(): DockMorph {
	return useSyncExternalStore(
		subscribe,
		() => phase,
		() => "idle",
	);
}

/** 线程页输入坞挂载后调用：新状态已提交，浏览器可以截新快照。没有进行中的过渡时什么都不做。 */
export function dockArrived(): void {
	arrived?.();
	arrived = null;
}

/** 线程页挂载时取一次：是否刚从首页发送进来（取后清除）。 */
export function takeArrival(): boolean {
	const v = arrival;
	arrival = false;
	return v;
}

/**
 * 从首页托盘过渡到线程页：`go` 里做导航。
 * `tray` 是首页的玻璃托盘元素；为空或减少动效时只做整页淡入。
 */
export function morphToThread(tray: HTMLElement | null, go: () => void): void {
	arrival = true;
	const doc = document;
	if (typeof doc.startViewTransition !== "function") {
		go();
		return;
	}
	const reduce =
		window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
	const mode: DockMorph = reduce || !tray ? "fade" : "dock";
	if (mode === "dock") tray?.classList.add("dock-morphing");
	const root = doc.documentElement;
	root.dataset.morph = mode;
	// 线程页挂载即放行；万一迟迟不挂载，600ms 后照常截图，不卡住页面（lib 未到 es2024，没有 Promise.withResolvers）
	const ready = new Promise<void>((resolve) => {
		arrived = resolve;
		setTimeout(resolve, 600);
	});
	const vt = doc.startViewTransition(async () => {
		setPhase(mode);
		go();
		await ready;
	});
	vt.finished.finally(() => {
		delete root.dataset.morph;
		arrived = null;
		setPhase("idle");
	});
}
