/**
 * iOS 原生底栏（iOS 26 系统 UITabBar）的网页侧控制器：原生是哑执行器，策略全在这里（网页随服务器更新，App 发版要走 TestFlight）。
 * - 层级：原生底栏垫在整张 WKWebView 之下，接管期间页面透明（`<html data-native-under>`，见 `index.css`），底色交给原生垫底
 *   （spec 的 `bg`，同一个 --bg）。卡片、菜单、弹层都是网页画的，天然压在原生底栏上面：哪里透出底栏由网页合成器逐像素决定，
 *   开合、拖动都不需要原生做任何动画，也就不存在两个进程各自插值、边缘对不齐的问题。
 * - 触摸：WebView 在上面，原生在 WebView 之上只盖底栏矩形放一块转交层。这里告诉它卡片左缘（`reveal`：吸附终点与拖动开始各一条）
 *   与底栏是否被网页内容压住（`setCovered`：侧栏里的菜单、弹层、错误提示、软键盘），两者都允许时转交层才把触摸交给 UITabBar。
 * - 回退：宿主没有原生底栏（浏览器、Android、桌面、bridge < 7）、`set` 被拒（iOS < 26、插件缺失）、或 `NATIVE_TAB_BAR`
 *   为 false 时，本页一直用网页底栏，行为与以前完全相同。改 `NATIVE_TAB_BAR` 并升级服务器即可全量回退，不需要发版。
 */
import { useSyncExternalStore } from "react";
import {
	host,
	type NativeTabBarHost,
	type NativeTabBarReveal,
	type NativeTabBarSpec,
} from "./host";
import { SECTION_ORDER, type SectionId } from "./sections";

/** 总开关：false = 所有 App 都用网页底栏。 */
export const NATIVE_TAB_BAR = true;
/** 下沉卡片的开合时长（= `index.css` 里 `.sink-card` 的 `translate 260ms`）。 */
export const SINK_MS = 260;
/** 开合缓动（= `index.css` 的 `--ease`）。 */
export const SINK_EASE: readonly [number, number, number, number] = [
	0.2, 0.8, 0.2, 1,
];
/** 桥的送达余量：原生页头接管时卡片晚这么久起步（`index.css` 的 `.sink-card[data-native-head]`），两边从同一时刻出发。 */
export const NATIVE_LAG_MS = 34;
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
	/** 卡片左缘（视口 x）：吸附终点（打开 = 侧栏宽，关闭 = 0）与拖动开始（0）各报一次。 */
	reveal(x: number): void;
	/** 网页内容压在底栏位置上：原生底栏不接触摸（看得见的部分本来就由网页合成器决定）。 */
	setCovered(covered: boolean): void;
}

/** 最新值覆盖、同时至多一个在途：桥慢的时候不排队。拒绝只记日志（`tag` 标明是哪个原生部件）。 */
export function latest<T>(
	send: (v: T) => Promise<unknown>,
	tag: string,
): (v: T) => void {
	let busy = false;
	let next: { v: T } | null = null;
	const pump = () => {
		if (busy || !next) return;
		const { v } = next;
		next = null;
		busy = true;
		void send(v)
			.then(
				() => undefined,
				(e) => console.warn(`[host] ${tag}`, e),
			)
			.finally(() => {
				busy = false;
				pump();
			});
	};
	return (v) => {
		next = { v };
		pump();
	};
}

export function createNativeTabBar(
	tabBar: NativeTabBarHost | null,
): NativeTabBar {
	let disabled = tabBar === null; // set 被拒后本页不再尝试
	let wanted = ""; // 想要的 spec 的 JSON；"" = 不要原生栏
	let engaged = false;
	let x = 0;
	let covered = false;
	let sent = ""; // 最近一次发出的触摸状态；接管 / 撤栏时清空，下一次照发
	const subs = new Set<() => void>();
	const notify = () => {
		for (const cb of [...subs]) cb();
	};
	const sendReveal = latest<NativeTabBarReveal>(
		(r) => (tabBar as NativeTabBarHost).reveal(r),
		"native tab bar",
	);
	const push = () => {
		if (!engaged) return;
		const key = `${x}|${covered}`;
		if (key === sent) return;
		sent = key;
		sendReveal({ x, covered });
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
		sent = "";
		notify();
		// 接管那一刻把当前开合与遮挡补给原生（此前的都没发）
		push();
	}, "native tab bar");
	return {
		engaged: () => engaged,
		subscribe(cb) {
			subs.add(cb);
			return () => {
				subs.delete(cb);
			};
		},
		setSpec(spec) {
			if (disabled) return;
			const key = spec ? JSON.stringify(spec) : "";
			if (key === wanted) return;
			wanted = key;
			if (!spec && engaged) {
				engaged = false;
				sent = "";
				notify();
			}
			sendSpec(spec);
		},
		reveal(next) {
			x = next;
			push();
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
);

/** 原生底栏已接管（网页胶囊隐藏、页面透明）。 */
export function useNativeTabBarEngaged(): boolean {
	return useSyncExternalStore(
		nativeTabBar.subscribe,
		nativeTabBar.engaged,
		() => false,
	);
}
