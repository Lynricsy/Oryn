/**
 * iOS 原生底栏（iOS 26 系统 UITabBar：按住冒出大液珠、按住横滑换项）的网页侧控制器：原生是哑执行器，策略全在这里
 * （网页随服务器更新，App 发版要走 TestFlight）。
 * - 层级：原生底栏浮在 WKWebView 之上，玻璃折射的是它下面真实的网页内容（侧栏列表一直画到屏幕底边）。
 *   底栏属于侧栏层，卡片盖过来的部分要藏起来：原生把底栏右缘裁到卡片左缘（`reveal` 的 `x`）。
 * - 动画归属：拖动时逐帧发卡片左缘（rAF 合帧、在途至多一条、最新值覆盖），向左（收起方向）按速度预判一段桥的延迟；
 *   松手与非手势开合只发终点，原生按同一时长、缓动与绝对起步时刻（`at`）自己插值。误差方向一律是「底栏比卡片多藏一点」：
 *   打开时原生略晚、关闭时略早（`SYNC_BIAS_MS`）。接管后卡片带 `data-native-bar`，非手势开合晚 `NATIVE_LAG_MS` 起步（index.css）。
 * - 遮挡：原生压在一切网页内容之上，侧栏里的菜单、弹层、提示、软键盘盖到底栏位置时（`setCovered`）原生立即隐藏、
 *   网页胶囊顶上；让开后原生淡回，网页胶囊晚 `UNCOVER_MS` 再撤（短暂重叠，不留空档）。
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
/** 桥的送达余量：原生接管时卡片晚这么久起步（`index.css` 的 `.sink-card[data-native-head|bar]`），两边从同一时刻出发。 */
export const NATIVE_LAG_MS = 34;
/** 起步对齐的方向偏置（毫秒，约一帧）：残余误差只落在「原生藏在卡片后面」那一侧。 */
export const SYNC_BIAS_MS = 8;
/** 侧栏打开且原生接管时遮挡探测的轮询间隔。 */
export const COVER_POLL_MS = 250;
/** 遮挡让开后网页胶囊再留这么久（原生淡回的时长），两者短暂重叠、不留空档。 */
export const UNCOVER_MS = 120;

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
	/** 原生底栏已接管（`set` 成功）。 */
	engaged(): boolean;
	/** 原生底栏此刻看得见：已接管且没被网页内容盖住。为真时网页胶囊隐藏（仍可命中，遮挡探测靠它）。 */
	shown(): boolean;
	subscribe(cb: () => void): () => void;
	setSpec(spec: NativeTabBarSpec | null): void;
	/** 拖动中的一帧：卡片进度、侧栏宽（px）、手指速度（px/ms）。逐帧合并后发给原生。 */
	drag(progress: number, width: number, velocity: number): void;
	/**
	 * 卡片吸附的终点（打开 = 左缘停在侧栏宽，关闭 = 0）。`start` 同 `NativeHeader.cardSettle`：卡片过渡实际起步的绝对时刻，
	 * 给了就等它再发；兑现为 null = 确认卡片没有过渡，原生直接就位。
	 */
	settle(
		open: boolean,
		width: number,
		instant: boolean,
		start?: Promise<number | null>,
	): void;
	/** 网页内容压在底栏位置上：原生隐藏、不接触摸，网页胶囊顶上。 */
	setCovered(covered: boolean): void;
}

/** 控制器用到的计时与帧回调（测试替换成假的）。 */
export interface TabBarEnv {
	setTimeout(cb: () => void, ms: number): number;
	clearTimeout(id: number): void;
	frame(cb: () => void): number;
	cancelFrame(id: number): void;
	/** 与原生同一个钟（Unix 毫秒）。 */
	now(): number;
}

const browserEnv: TabBarEnv = {
	setTimeout: (cb, ms) => window.setTimeout(cb, ms),
	clearTimeout: (id) => window.clearTimeout(id),
	frame: (cb) => requestAnimationFrame(cb),
	cancelFrame: (id) => cancelAnimationFrame(id),
	now: () => Date.now(),
};

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
	env: TabBarEnv = browserEnv,
): NativeTabBar {
	let disabled = tabBar === null; // set 被拒后本页不再尝试
	let wanted = ""; // 想要的 spec 的 JSON；"" = 不要原生栏
	let engaged = false;
	let covered = false;
	let webShown = true; // 网页胶囊看得见（未接管、被盖住，或让开后的重叠期）
	let uncoverTimer = 0;
	let x = 0; // 原生裁切右缘的目标 = 卡片左缘
	let open = false; // 最近一次吸附的目标
	let width = 0;
	let dragging = false;
	let dragX: number | null = null; // 这一帧还没发出的拖动位置
	let raf = 0;
	let seq = 0; // 吸附序号：等起步时刻期间又吸附 / 起拖时，旧的那条作废
	const subs = new Set<() => void>();
	const notify = () => {
		for (const cb of [...subs]) cb();
	};
	const sendReveal = latest<NativeTabBarReveal>(
		(r) => (tabBar as NativeTabBarHost).reveal(r),
		"native tab bar",
	);
	const reveal = (r: Omit<NativeTabBarReveal, "covered">) => {
		if (engaged) sendReveal({ ...r, covered });
	};
	const setWebShown = (next: boolean) => {
		env.clearTimeout(uncoverTimer);
		if (next === webShown) return;
		webShown = next;
		notify();
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
			setWebShown(true);
			notify();
			throw e;
		}
		if (spec === null || wanted === "" || engaged) return;
		engaged = true;
		// 接管那一刻把当前卡片左缘与遮挡补给原生（此前的都没发）
		reveal({ x, ms: 0 });
		setWebShown(covered);
		notify();
	}, "native tab bar");
	return {
		engaged: () => engaged,
		shown: () => engaged && !webShown,
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
				setWebShown(true);
				notify();
			}
			sendSpec(spec);
		},
		drag(progress, w, velocity) {
			seq++;
			dragging = true;
			width = w;
			// 往左（收起方向）按速度预判一段桥的延迟：原生晚到也不会探出卡片左缘；往右不预判，晚到只会多藏一点
			dragX = Math.min(
				w,
				Math.max(0, progress * w + Math.min(0, velocity) * NATIVE_LAG_MS),
			);
			if (raf) return;
			raf = env.frame(() => {
				raf = 0;
				if (dragX === null) return;
				x = dragX;
				dragX = null;
				reveal({ x, ms: 0 });
			});
		},
		settle(next, w, instant, start) {
			if (!dragging && next === open && w === width) return;
			dragging = false;
			open = next;
			width = w;
			if (raf) env.cancelFrame(raf);
			raf = 0;
			dragX = null;
			x = next ? w : 0;
			const mine = ++seq;
			const target = x;
			const predicted = env.now() + NATIVE_LAG_MS;
			const go = (begin: number | null, still: boolean) => {
				if (mine !== seq) return;
				if (instant || still) {
					reveal({ x: target, ms: 0 });
					return;
				}
				reveal({
					x: target,
					ms: SINK_MS,
					ease: [...SINK_EASE],
					// 打开时晚一帧、关闭时早一帧：底栏右缘始终落在卡片左缘之内
					at: (begin ?? predicted) + (next ? SYNC_BIAS_MS : -SYNC_BIAS_MS),
				});
			};
			if (start && !instant)
				start.then(
					(at) => go(at, at === null),
					() => undefined,
				);
			else go(null, false);
		},
		setCovered(next) {
			if (next === covered) return;
			covered = next;
			// 只换可见性，不打断原生正在走的裁切过渡（x 与目标相同、ms 0）
			reveal({ x, ms: 0 });
			if (!engaged) return;
			if (next) {
				setWebShown(true);
				return;
			}
			env.clearTimeout(uncoverTimer);
			uncoverTimer = env.setTimeout(() => {
				webShown = false;
				notify();
			}, UNCOVER_MS);
		},
	};
}

/** 本页的原生底栏控制器；没有原生能力时一切空操作。 */
export const nativeTabBar: NativeTabBar = createNativeTabBar(
	NATIVE_TAB_BAR ? (host.mobile?.tabBar ?? null) : null,
);

/** 原生底栏已接管（卡片按原生的送达余量晚起步）。 */
export function useNativeTabBarEngaged(): boolean {
	return useSyncExternalStore(
		nativeTabBar.subscribe,
		nativeTabBar.engaged,
		() => false,
	);
}

/** 原生底栏此刻看得见：网页胶囊该隐藏。 */
export function useNativeTabBarShown(): boolean {
	return useSyncExternalStore(
		nativeTabBar.subscribe,
		nativeTabBar.shown,
		() => false,
	);
}
