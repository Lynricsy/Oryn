/**
 * iPhone Duo 原生竖栏（iOS 27.1+，SwiftUI Liquid Glass：页头按钮一根胶囊 + 竖排分区一根胶囊）的网页侧控制器。
 * 原生是哑执行器：矩形由壳的 RailProbe 自己算（以相机为中线，与 `rail` 几何事件同一个矩形），这里只发内容。
 * - 内容：`Rail` 每次渲染合成一份 spec（侧栏开关、`useRailActions` 登记的页头操作、分区、深浅与调色板、遮挡），
 *   按 JSON 去重，在途至多一条。原生点按经 `host.onRailTap` 回来，`Rail` 按 key 找回调执行。
 * - 交接：`set` 应答（原生已挂上窗口）后才算接管，`Rail` 换成透明的占位（只留遮挡探测）；没接管之前照画网页竖栏。
 * - 遮挡：原生浮在 WebView 之上，网页的对话框、遮罩盖到竖栏那一列时 spec 带 `covered`，原生隐藏、不接触摸。
 * - 回退：宿主没有原生竖栏（浏览器、Android、bridge < 13）、`set` 被拒（iOS < 27.1）、或 `NATIVE_RAIL` 为 false 时，
 *   本页一直画网页竖栏，行为与以前完全相同。改 `NATIVE_RAIL` 并升级服务器即可全量回退，不需要发版。
 */
import { useSyncExternalStore } from "react";
import {
	host,
	type NativeMenu,
	type NativeRailHost,
	type NativeRailSpec,
	type NativeTabBarItem,
} from "./host";
import { clip, HEADER_LIMIT, nativeItems, SF_ICON } from "./native-header";
import { latest } from "./native-sink";
import type { RailAction } from "./rail";

/** 总开关：false = 所有 App 都画网页竖栏。 */
export const NATIVE_RAIL = true;

/** 与壳 `mobile_logic::valid_rail` 的上限一致：超出的在这里截断，不让整份 spec 被拒。 */
export const RAIL_LIMIT = { key: 200, label: 64, actions: 4 } as const;

export interface RailSpecInput {
	key: string;
	dark: boolean;
	palette: NativeRailSpec["palette"];
	covered: boolean;
	lead: { label: string; active: boolean };
	actions: readonly RailAction[];
	tabs: { label: string; items: NativeTabBarItem[]; selected: string };
}

function railMenu(label: string, items: RailAction["menu"]): NativeMenu {
	return {
		label: clip(label, HEADER_LIMIT.menu),
		dim: false,
		quick: [],
		status: [],
		groups: [nativeItems(items ?? []).slice(0, HEADER_LIMIT.group)],
	};
}

/**
 * 合成整份 spec：图标按 `SF_ICON` 换成 SF Symbol（没列的用 circle）；有菜单的操作带一组菜单项（`nativeItems`，空组时不带菜单）；
 * 操作至多 4 个，各字符串按上限截断；布尔与提醒点只在有时输出。
 */
export function buildRailSpec(i: RailSpecInput): NativeRailSpec {
	const actions = i.actions.slice(0, RAIL_LIMIT.actions).map((a) => {
		const out: NativeRailSpec["actions"][number] = {
			id: clip(a.id, RAIL_LIMIT.label),
			symbol: SF_ICON[a.icon] ?? "circle",
			label: clip(a.label, RAIL_LIMIT.label),
		};
		if (a.active) out.active = true;
		if (a.dot) out.dot = a.dot;
		if (a.menu) {
			const menu = railMenu(a.label, a.menu);
			if (menu.groups[0]?.length) out.menu = menu;
		}
		return out;
	});
	return {
		key: clip(i.key, RAIL_LIMIT.key),
		dark: i.dark,
		palette: { ...i.palette },
		covered: i.covered,
		lead: {
			label: clip(i.lead.label, RAIL_LIMIT.label),
			active: i.lead.active,
		},
		actions,
		tabs: {
			label: clip(i.tabs.label, RAIL_LIMIT.label),
			items: i.tabs.items.map((it) => ({ ...it })),
			selected: i.tabs.selected,
		},
	};
}

export interface NativeRail {
	/** 宿主有原生竖栏且没被拒过（之后仍可能接管）。 */
	available(): boolean;
	/** 原生竖栏已接管（`set` 应答过一份非空 spec）。 */
	engaged(): boolean;
	subscribe(cb: () => void): () => void;
	/** 想要的内容；null = 撤掉原生竖栏。内容没变不重发。 */
	setSpec(spec: NativeRailSpec | null): void;
}

export function createNativeRail(api: NativeRailHost | null): NativeRail {
	let disabled = api === null; // set 被拒后本页不再尝试
	let wanted = ""; // 想要的 spec 的 JSON；"" = 不要原生竖栏
	let engaged = false;
	const subs = new Set<() => void>();
	const notify = () => {
		for (const cb of [...subs]) cb();
	};
	const setEngaged = (next: boolean) => {
		if (next === engaged) return;
		engaged = next;
		notify();
	};
	const send = latest<NativeRailSpec | null>(async (spec) => {
		// 已回退：排在被拒那条之后的 spec 不再发（撤掉照发）
		if (disabled && spec !== null) return;
		try {
			await (api as NativeRailHost).set(spec);
		} catch (e) {
			if (spec === null) return;
			// 原生不可用（iOS < 27.1）：本页回到网页竖栏，不再尝试
			disabled = true;
			wanted = "";
			engaged = false;
			notify();
			throw e;
		}
		// 应答到达时想要的仍是非空：接管（期间又撤掉的不算）
		setEngaged(spec !== null && wanted !== "");
	}, "native rail");

	return {
		available: () => !disabled,
		engaged: () => engaged,
		subscribe(cb) {
			subs.add(cb);
			return () => {
				subs.delete(cb);
			};
		},
		setSpec(spec) {
			if (disabled) return;
			const next = spec ? JSON.stringify(spec) : "";
			if (next === wanted) return;
			wanted = next;
			// 撤掉：网页竖栏立刻回来，不等应答
			if (!spec) setEngaged(false);
			send(spec);
		},
	};
}

/** 本页的原生竖栏控制器；没有原生能力时一切空操作。 */
export const nativeRail: NativeRail = createNativeRail(
	NATIVE_RAIL ? (host.mobile?.rail ?? null) : null,
);

/** 原生竖栏可用（可能还没接管）。 */
export function useNativeRailAvailable(): boolean {
	return useSyncExternalStore(
		nativeRail.subscribe,
		nativeRail.available,
		() => false,
	);
}

/** 原生竖栏已接管：网页竖栏换成透明占位。 */
export function useNativeRailEngaged(): boolean {
	return useSyncExternalStore(
		nativeRail.subscribe,
		nativeRail.engaged,
		() => false,
	);
}
