/**
 * iPhone Duo 竖栏（iOS 壳的 `rail` 事件，iOS 27.1+）：合上时的外屏、展开后横放的内屏上，系统把状态栏、相机与工具栏、标签栏
 * 竖排在一侧（HIG「Designing for iPhone Duo → Vertical controls」）。壳算好 Oryn 竖栏的矩形与竖栏之外内容区的安全区，这里：
 * - 写到 `<html>`：`data-rail="left|right"`，`--rail-x/y/w/h`（竖栏矩形），`--rail-safe-*`（index.css 的 `:root[data-rail]`
 *   用它们取代 env() 的安全区：竖排的状态栏只占竖栏那一列，不再算成整条顶边）；
 * - 供组件订阅（`useRail`）：AppShell 渲染竖栏（`Rail`：开关侧栏 + 页头操作 + 竖排的分区；壳 bridge ≥ 13 时由原生画，
 *   见 `native-rail.ts`）；页头把自己的操作登记进来（`useRailActions`，竖栏在时页头里不再画这几个钮）；页面里的侧栏开关
 *   带 `data-sidebar-toggle`，竖栏在时由 index.css 隐藏（竖栏顶上有同一个开关）。
 * null = 当前不竖排（内屏竖放、普通 iPhone、浏览器、旧壳）：属性与变量全部撤掉，页面照旧。
 */
import { useEffect, useRef, useSyncExternalStore } from "react";
import type { IconName } from "../components/ui/icons";
import type { MenuItem } from "../components/ui/primitives";
import type { Host, RailGeometry } from "./host";
import { nativeItems } from "./native-header";
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

/** 页头登记进竖栏的一个操作：有 `menu` 的点开是菜单，否则点了调 `onClick`。 */
export interface RailAction {
	id: string;
	icon: IconName;
	label: string;
	/** 开着（如信息面板开着）：强调色。 */
	active?: boolean;
	dot?: "accent" | "warn";
	onClick?: () => void;
	menu?: readonly MenuItem[];
}

const owners = new Map<symbol, readonly RailAction[]>();
/** 各页头登记的操作合在一起（按登记先后）；只在「形状」变了时换新数组（订阅者据此重渲染）。 */
let actions: readonly RailAction[] = [];
/** 最新一份（回调每次渲染都换）：点按时从这里取。 */
let latestActions: readonly RailAction[] = [];
let shape = "[]";
const actionSubs = new Set<() => void>();

/** 操作的可比较形状：菜单的 label 可能是 JSX，按原生菜单项（纯文本）比。 */
function shapeOf(list: readonly RailAction[]): string {
	return JSON.stringify(
		list.map((a) => [
			a.id,
			a.icon,
			a.label,
			a.active ?? false,
			a.dot ?? null,
			a.menu ? nativeItems(a.menu) : null,
		]),
	);
}

function publish(): void {
	latestActions = [...owners.values()].flat();
	const next = shapeOf(latestActions);
	if (next === shape) return;
	shape = next;
	actions = latestActions;
	for (const cb of [...actionSubs]) cb();
}

/**
 * 页头把自己的操作登记进竖栏（每次渲染后交一次，回调总是最新的；内容没变不通知）；`null` = 不登记（竖栏不在时也可以照登，
 * 只是没人显示）。卸载时撤掉。
 */
export function useRailActions(list: readonly RailAction[] | null): void {
	const id = useRef<symbol | null>(null);
	id.current ??= Symbol("rail");
	const key = id.current;
	useEffect(() => {
		if (list) owners.set(key, list);
		else owners.delete(key);
		publish();
	});
	useEffect(
		() => () => {
			owners.delete(key);
			publish();
		},
		[key],
	);
}

function subscribeActions(cb: () => void): () => void {
	actionSubs.add(cb);
	return () => {
		actionSubs.delete(cb);
	};
}

/** 当前登记的全部操作（形状变了才是新数组）。 */
export function useRailActionList(): readonly RailAction[] {
	return useSyncExternalStore(
		subscribeActions,
		() => actions,
		() => actions,
	);
}

/** 按 id 取最新登记的操作（点按回调用）。 */
export function railAction(id: string): RailAction | undefined {
	return latestActions.find((a) => a.id === id);
}
