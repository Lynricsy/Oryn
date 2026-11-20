/**
 * iOS 原生底栏（iOS 26 系统 UITabBar）的网页侧控制器：原生是哑执行器，策略全在这里（网页随服务器更新，App 发版要走 TestFlight）。
 * - 层级：原生底栏垫在整张 WKWebView 之下，接管期间页面透明（`<html data-native-under>`，见 `index.css`），底色交给原生垫底
 *   （spec 的 `bg`，同一个 --bg）。卡片、菜单、弹层都是网页画的，天然压在原生底栏上面：哪里透出底栏由网页合成器逐像素决定。
 * - 触摸：WebView 在上面，原生在 WebView 之上只盖底栏矩形放一块转交层。卡片左缘由原生从根滚动的 contentOffset 自己算
 *   （`native-sink.ts`），这里只告诉它底栏是否被网页内容压住（`setCovered`：侧栏里的菜单、弹层、错误提示、软键盘），
 *   两者都允许时转交层才把触摸交给 UITabBar。
 * - 前提：根滚动跟随已接管（`SinkGate.engaged()`）；没接管时想要的 spec 一律当作 null，接管后补发（壳在 `host_sink_set`
 *   生效前也会拒绝非空 spec）。
 * - 回退：宿主没有原生底栏（浏览器、Android、桌面、bridge < 8）、`set` 被拒（iOS < 26、插件缺失）、或 `NATIVE_TAB_BAR`
 *   为 false 时，本页一直用网页底栏，行为与以前完全相同。改 `NATIVE_TAB_BAR` 并升级服务器即可全量回退，不需要发版。
 */
import { useSyncExternalStore } from "react";
import { host, type NativeTabBarHost, type NativeTabBarSpec } from "./host";
import { latest, nativeSink, type SinkGate } from "./native-sink";
import { SECTION_ORDER, type SectionId } from "./sections";

/** 总开关：false = 所有 App 都用网页底栏。 */
export const NATIVE_TAB_BAR = true;
/** 侧栏打开且原生接管时遮挡探测的轮询间隔。 */
export const COVER_POLL_MS = 250;

export const SF_SYMBOLS: Record<SectionId, string> = {
	threads: "bubble.left.and.bubble.right",
	assistant: "sparkles",
	projects: "folder",
	more: "ellipsis",
};

export interface TabBarSpecInput {
	labels: Record<SectionId, string>;
	selected: SectionId;
	frame: NativeTabBarSpec["frame"];
	dark: boolean;
	accent: string | null;
	/** 页面底色 `#rrggbb`。 */
	bg: string;
	/** 助手头像：栅格化好的 PNG base64 优先，其次单个字形，都没有用 sparkles。 */
	assistant: { image: string | null; glyph: string | null; unread: boolean };
	/** 「更多」上的连接状态点；连接正常为 null。 */
	conn: "warn" | "err" | null;
}

/** 按 SECTION_ORDER 四项生成 spec；没有角标的项不带 `badge` 键。 */
export function buildTabBarSpec(i: TabBarSpecInput): NativeTabBarSpec {
	const items = SECTION_ORDER.map((id): NativeTabBarSpec["items"][number] => {
		let icon: NativeTabBarSpec["items"][number]["icon"] = {
			symbol: SF_SYMBOLS[id],
		};
		let badge: "accent" | "warn" | "err" | null = null;
		if (id === "assistant") {
			if (i.assistant.image) icon = { image: i.assistant.image };
			else if (i.assistant.glyph) icon = { glyph: i.assistant.glyph };
			if (i.assistant.unread) badge = "accent";
		} else if (id === "more") badge = i.conn;
		const item = { id, label: i.labels[id], icon };
		return badge ? { ...item, badge } : item;
	});
	return {
		items,
		selected: i.selected,
		frame: { ...i.frame },
		dark: i.dark,
		accent: i.accent,
		bg: i.bg,
	};
}

/** 调试构建（开发服务器与 mock 构建）：遮挡判定记下命中的元素，便于排查误判；生产构建不记。 */
const DEBUG_COVER = import.meta.env.DEV || import.meta.env.VITE_MOCK === "1";

/** 胶囊位置上最上面的元素不是胶囊自己（菜单、弹层、错误提示盖住了它），或软键盘弹出。 */
export function slotCovered(slot: Element, win: Window): boolean {
	const vv = win.visualViewport;
	// 与 lib/viewport.ts 算 `--kb-inset` 同式
	if (vv && win.innerHeight - vv.height - vv.offsetTop > 1) return true;
	const r = slot.getBoundingClientRect();
	if (r.width <= 0 || r.height <= 0) return true;
	const y = r.top + r.height / 2;
	for (const f of [0.15, 0.5, 0.85]) {
		const x = r.left + r.width * f;
		const hit = win.document.elementFromPoint(x, y);
		if (!hit || !slot.contains(hit)) {
			if (DEBUG_COVER)
				console.debug("[host] native tab bar covered", { x, y, hit });
			return true;
		}
	}
	return false;
}

/** 头像 data URL → px×px（居中裁成方形）PNG 的 base64；加载或画布失败为 null。 */
export function rasterizeAvatar(src: string, px = 60): Promise<string | null> {
	return new Promise((resolve) => {
		const img = new Image();
		img.onload = () => {
			try {
				const w = img.naturalWidth;
				const h = img.naturalHeight;
				const side = Math.min(w, h);
				const canvas = document.createElement("canvas");
				canvas.width = px;
				canvas.height = px;
				const ctx = side > 0 ? canvas.getContext("2d") : null;
				if (!ctx) return resolve(null);
				ctx.drawImage(
					img,
					(w - side) / 2,
					(h - side) / 2,
					side,
					side,
					0,
					0,
					px,
					px,
				);
				const url = canvas.toDataURL("image/png");
				const prefix = "data:image/png;base64,";
				resolve(url.startsWith(prefix) ? url.slice(prefix.length) : null);
			} catch {
				resolve(null);
			}
		};
		img.onerror = () => resolve(null);
		img.src = src;
	});
}

export interface NativeTabBar {
	/** 原生底栏已接管：网页胶囊隐藏，页面透明（原生垫底）。 */
	engaged(): boolean;
	subscribe(cb: () => void): () => void;
	setSpec(spec: NativeTabBarSpec | null): void;
	/** 网页内容压在底栏位置上：原生底栏不接触摸（看得见的部分本来就由网页合成器决定）。 */
	setCovered(covered: boolean): void;
}

export function createNativeTabBar(
	tabBar: NativeTabBarHost | null,
	sink: SinkGate,
): NativeTabBar {
	let disabled = tabBar === null; // set 被拒后本页不再尝试
	let want: NativeTabBarSpec | null = null; // 页面想要的 spec（根滚动没接管时不发）
	let wanted = ""; // 最近一次要求原生的 spec 的 JSON；"" = 不要原生栏
	let engaged = false;
	let covered = false;
	let sent: boolean | null = null; // 最近一次发出的遮挡；接管 / 撤栏时清空，下一次照发
	const subs = new Set<() => void>();
	const notify = () => {
		for (const cb of [...subs]) cb();
	};
	const sendCover = latest<boolean>(
		(c) => (tabBar as NativeTabBarHost).cover(c),
		"native tab bar",
	);
	const push = () => {
		if (!engaged || sent === covered) return;
		sent = covered;
		sendCover(covered);
	};
	const sendSpec = latest<NativeTabBarSpec | null>(async (spec) => {
		// 已回退：排在被拒那条之后的 spec 不再发（撤栏照发）
		if (disabled && spec !== null) return;
		try {
			await (tabBar as NativeTabBarHost).set(spec);
		} catch (e) {
			if (spec === null) return;
			// 原生不可用（iOS < 26、插件缺失）：本页回到网页底栏，不再尝试
			disabled = true;
			wanted = "";
			engaged = false;
			notify();
			throw e;
		}
		if (spec === null || wanted === "" || engaged) return;
		engaged = true;
		sent = null;
		notify();
		// 接管那一刻把当前遮挡补给原生（此前的都没发）
		push();
	}, "native tab bar");
	const sync = () => {
		if (disabled) return;
		const spec = sink.engaged() ? want : null;
		const key = spec ? JSON.stringify(spec) : "";
		if (key === wanted) return;
		wanted = key;
		if (!spec && engaged) {
			engaged = false;
			sent = null;
			notify();
		}
		sendSpec(spec);
	};
	sink.subscribe(sync);
	return {
		engaged: () => engaged,
		subscribe(cb) {
			subs.add(cb);
			return () => {
				subs.delete(cb);
			};
		},
		setSpec(spec) {
			want = spec;
			sync();
		},
		setCovered(next) {
			covered = next;
			push();
		},
	};
}

/** 本页的原生底栏控制器；没有原生能力时一切空操作。 */
export const nativeTabBar: NativeTabBar = createNativeTabBar(
	NATIVE_TAB_BAR ? (host.mobile?.tabBar ?? null) : null,
	nativeSink,
);

/** 原生底栏已接管（网页胶囊隐藏、页面透明）。 */
export function useNativeTabBarEngaged(): boolean {
	return useSyncExternalStore(
		nativeTabBar.subscribe,
		nativeTabBar.engaged,
		() => false,
	);
}
