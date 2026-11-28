/**
 * iPhone Duo 竖栏（iOS 壳的 `rail` 事件，iOS 27.1+）：合上时的外屏、展开后横放的内屏上，系统把状态栏、相机与工具栏、标签栏
 * 竖排在一侧（HIG「Designing for iPhone Duo → Vertical controls」）。壳算好 Oryn 竖栏的矩形与竖栏之外内容区的安全区，这里：
 * - 写到 `<html>`：`data-rail="left|right"`，`--rail-x/y/w/h`（竖栏矩形），`--rail-safe-*`（index.css 的 `:root[data-rail]`
 *   用它们取代 env() 的安全区：竖排的状态栏只占竖栏那一列，不再算成整条顶边）；
 * - 供组件订阅（`useRail`）：AppShell 渲染竖栏（`Rail`：开关侧栏 + 页头操作槽 + 竖排的分区底栏），页头把操作按钮经
 *   `InRail` 送进操作槽（`useRailSlot`）；页面里的侧栏开关带 `data-sidebar-toggle`，竖栏在时由 index.css 隐藏（竖栏顶上有同一个开关）。
 * null = 当前不竖排（内屏竖放、普通 iPhone、浏览器、旧壳）：属性与变量全部撤掉，页面照旧。
 */
import { useSyncExternalStore } from "react";
import type { Host, RailGeometry } from "./host";
import { notifySafeAreaChange } from "./viewport";

const VARS = [
	"--rail-x",
	"--rail-y",
	"--rail-w",
	"--rail-h",
	"--rail-safe-top",
	"--rail-safe-right",
	"--rail-safe-bottom",
	"--rail-safe-left",
] as const;

let current: RailGeometry | null = null;
const subs = new Set<() => void>();

function same(a: RailGeometry | null, b: RailGeometry | null): boolean {
	return JSON.stringify(a) === JSON.stringify(b);
}

/** 写入（或撤掉）竖栏的属性与变量，通知订阅者与安全区读者；几何没变不动。 */
export function applyRail(root: HTMLElement, g: RailGeometry | null): void {
	if (same(current, g)) return;
	current = g;
	const s = root.style;
	if (g) {
		root.dataset.rail = g.side;
		const px = (v: number) => `${v}px`;
		s.setProperty("--rail-x", px(g.frame.x));
		s.setProperty("--rail-y", px(g.frame.y));
		s.setProperty("--rail-w", px(g.frame.width));
		s.setProperty("--rail-h", px(g.frame.height));
		s.setProperty("--rail-safe-top", px(g.safeArea.top));
		s.setProperty("--rail-safe-right", px(g.safeArea.right));
		s.setProperty("--rail-safe-bottom", px(g.safeArea.bottom));
		s.setProperty("--rail-safe-left", px(g.safeArea.left));
	} else {
		delete root.dataset.rail;
		for (const v of VARS) s.removeProperty(v);
	}
	for (const cb of [...subs]) cb();
	notifySafeAreaChange();
}

/** 订阅壳的 `rail` 事件（启动时调用一次）。 */
export function installRail(h: Host, root: HTMLElement): () => void {
	return h.onRail((g) => applyRail(root, g));
}

function subscribe(cb: () => void): () => void {
	subs.add(cb);
	return () => {
		subs.delete(cb);
	};
}

/** 当前竖栏几何；不竖排时为 null。 */
export function useRail(): RailGeometry | null {
	return useSyncExternalStore(
		subscribe,
		() => current,
		() => null,
	);
}

let slot: HTMLElement | null = null;
const slotSubs = new Set<() => void>();

/** 竖栏的页头操作槽（`Rail` 的 ref 回调）；卸载时传 null。 */
export function setRailSlot(el: HTMLElement | null): void {
	if (slot === el) return;
	slot = el;
	for (const cb of [...slotSubs]) cb();
}

function subscribeSlot(cb: () => void): () => void {
	slotSubs.add(cb);
	return () => {
		slotSubs.delete(cb);
	};
}

/** 竖栏的页头操作槽；不竖排或竖栏还没挂上时为 null。 */
export function useRailSlot(): HTMLElement | null {
	return useSyncExternalStore(
		subscribeSlot,
		() => slot,
		() => null,
	);
}
