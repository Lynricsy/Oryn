/**
 * iOS 原生底栏（iOS 26 Liquid Glass 胶囊）的网页侧控制器：原生是哑执行器，策略全在这里（网页随服务器更新，App 发版要走 TestFlight）。
 * - 层级：原生胶囊叠在整张 WKWebView 之上，靠裁切模拟「在卡片之下」：可见部分 = 揭示边（卡片左缘）左侧。
 * - 动画归属：拖拽中网页逐帧推揭示边（rAF 合帧，每帧至多一条、在途至多一条、最新值覆盖）；松手 / 点击后的
 *   260ms 吸附由原生自己插值，网页只发一条端点消息（关侧栏时主线程常被路由渲染占住，逐帧推必卡）。
 * - 34ms 方向性偏移（`NATIVE_LAG_MS`）：误差只许落在「底栏比卡片多藏一点」那一侧。打开时原生晚起步（消息里的 `delay`）；
 *   关闭时卡片晚起步（`index.css` 里 `.sink-card[data-native-bar]` 的 `transition-delay`）；拖拽向左时揭示边按速度提前。
 * - 遮挡：原生栏压在一切网页内容之上，侧栏里的菜单、弹层、错误提示盖到胶囊位置时（`slotCovered`）原生栏淡出、网页胶囊顶上。
 * - 回退：宿主没有原生底栏（浏览器、Android、桌面、bridge < 4）、`set` 被拒（iOS < 26、插件缺失）、或 `NATIVE_TAB_BAR`
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
/** 方向性安全偏移：打开时原生晚起步，关闭时卡片晚起步。 */
export const NATIVE_LAG_MS = 34;
/** 遮挡解除后原生栏淡回的时长。 */
export const UNCOVER_MS = 120;
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
	};
}

/** 揭示边 = 卡片左缘；向左收（velocity < 0，px/ms）时按速度提前 NATIVE_LAG_MS，不小于 0。 */
export function revealEdge(
	progress: number,
	sinkWidth: number,
	velocity: number,
): number {
	return Math.max(
		0,
		progress * sinkWidth + Math.min(0, velocity) * NATIVE_LAG_MS,
	);
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
	/** 原生底栏正在显示：网页胶囊应隐藏。 */
	active(): boolean;
	/** 原生底栏已接管（可能暂时被盖住）：卡片的关闭过渡要晚 NATIVE_LAG_MS。 */
	engaged(): boolean;
	subscribe(cb: () => void): () => void;
	setSpec(spec: NativeTabBarSpec | null): void;
	drag(progress: number, sinkWidth: number, velocity: number): void;
	/** 吸附终点。`lag`：打开时原生再多晚起步的毫秒数（原生页头接管时卡片的非手势打开晚 NATIVE_LAG_MS 起步，底栏要跟着晚）。 */
	settle(
		open: boolean,
		sinkWidth: number,
		instant: boolean,
		lag?: number,
	): void;
	setCovered(covered: boolean): void;
}

/** 帧调度（测试可注入）。 */
export interface FrameScheduler {
	request(cb: () => void): number;
	cancel(id: number): void;
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
	frames: FrameScheduler = {
		request: (cb) => requestAnimationFrame(cb),
		cancel: (id) => cancelAnimationFrame(id),
	},
): NativeTabBar {
	let disabled = tabBar === null; // set 被拒后本页不再尝试
	let wanted = ""; // 想要的 spec 的 JSON；"" = 不要原生栏
	let engaged = false;
	let covered = false;
	let target = false; // 最近一次 settle 的目标
	let width = 0;
	let dragging = false;
	let lastX = 0; // 最近一次入队的揭示边（入队时记，不是发出时）
	let raf = 0;
	let dragX: number | null = null;
	const subs = new Set<() => void>();
	const notify = () => {
		for (const cb of [...subs]) cb();
	};
	const alpha = () => (covered ? 0 : 1);
	const sendReveal = latest<NativeTabBarReveal>(
		(r) => (tabBar as NativeTabBarHost).reveal(r),
		"native tab bar",
	);
	const reveal = (r: NativeTabBarReveal) => {
		lastX = r.x;
		sendReveal(r);
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
		notify();
		// 接管那一刻把当前开合状态补给原生（此前的 settle 都没发）
		reveal({ x: target ? width : 0, alpha: alpha(), ms: 0, delay: 0 });
	}, "native tab bar");
	return {
		active: () => engaged && !covered,
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
				notify();
			}
			sendSpec(spec);
		},
		drag(progress, sinkWidth, velocity) {
			dragging = true;
			width = sinkWidth;
			if (!engaged) return;
			dragX = revealEdge(progress, sinkWidth, velocity);
			if (raf) return;
			raf = frames.request(() => {
				raf = 0;
				if (dragX === null) return;
				reveal({ x: dragX, alpha: alpha(), ms: 0, delay: 0 });
				dragX = null;
			});
		},
		settle(open, sinkWidth, instant, lag) {
			if (!dragging && target === open && width === sinkWidth) return;
			dragging = false;
			target = open;
			width = sinkWidth;
			if (raf) frames.cancel(raf);
			raf = 0;
			dragX = null;
			if (!engaged) return;
			reveal({
				x: open ? sinkWidth : 0,
				alpha: alpha(),
				ms: instant ? 0 : SINK_MS,
				delay: open && !instant ? NATIVE_LAG_MS + (lag ?? 0) : 0,
				ease: [...SINK_EASE],
			});
		},
		setCovered(next) {
			if (next === covered) return;
			covered = next;
			notify();
			if (!engaged) return;
			reveal({
				x: lastX,
				alpha: alpha(),
				ms: next ? 0 : UNCOVER_MS,
				delay: 0,
				ease: [...SINK_EASE],
			});
		},
	};
}

/** 本页的原生底栏控制器；没有原生能力时一切空操作。 */
export const nativeTabBar: NativeTabBar = createNativeTabBar(
	NATIVE_TAB_BAR ? (host.mobile?.tabBar ?? null) : null,
);

/** 原生底栏正在显示（网页胶囊应隐藏）。 */
export function useNativeTabBarActive(): boolean {
	return useSyncExternalStore(
		nativeTabBar.subscribe,
		nativeTabBar.active,
		() => false,
	);
}

/** 原生底栏已接管（卡片关闭过渡要晚 NATIVE_LAG_MS）。 */
export function useNativeTabBarEngaged(): boolean {
	return useSyncExternalStore(
		nativeTabBar.subscribe,
		nativeTabBar.engaged,
		() => false,
	);
}
