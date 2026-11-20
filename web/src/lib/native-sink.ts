/**
 * iOS 下沉侧栏的根滚动模式（bridge 8，iOS 26+）：卡片开合 = WKWebView 自带滚动视图的横向滚动。
 * - 文档在原生模式下可横向滚动（`<html data-sink-native>`，见 `index.css`）：侧栏 `position: fixed` 垫底，卡片在右侧 in-flow，
 *   滚到最右（scrollX = max）是关闭、滚到 0 是打开；原生给松手落点，拖动和减速由系统处理。
 * - 原生（`SinkFollow`）观察 contentOffset，原生页头与底栏在同一进程同一帧里跟着卡片走；网页从自己的 scrollX 得知开合
 *   （`sinkRest`），不需要原生回传状态。
 * - 程序化开合由 `host_sink_show` 交给原生（逐帧动画或一步落位）；初始落位、拉回、窗口尺寸变化由网页
 *   `scrollTo({behavior: "instant"})` 做。
 * - `createSinkScroll`：把根滚动的静止位置同步到 `uiStore.sidebarOpen`，并把非触摸、非本页请求引起的位移
 *   （`scrollIntoView`、不带 `preventScroll` 的 `focus()`）立即拉回已提交的静止位。
 * - 回退：宿主没有 `mobile.sink`（浏览器、Android、桌面、iOS bridge < 8）、`set` 被拒（iOS < 26）或 `NATIVE_SINK` 为 false 时
 *   不接管，卡片继续用网页手势（SinkLayout），原生底栏与页头也不接管。
 */
import { useSyncExternalStore } from "react";
import { host, type NativeSinkHost } from "./host";

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

/** 总开关：false = 所有 App 都用网页手势、网页页头与网页底栏。 */
export const NATIVE_SINK = true;
/** 根滚动静止多久（毫秒）算停稳并提交到 `sidebarOpen`。 */
export const SINK_REST_MS = 100;
/** 等原生程序化开合到位的最长时间；超时则请求原生一步落位。 */
export const SINK_REQUEST_MS = 1000;

export type SinkRest = "open" | "closed";

/** 根滚动的静止位置：0 = 打开，最右 = 关闭；中途或文档不可横滚时为 null。 */
export function sinkRest(win: Window): SinkRest | null {
	const max = win.document.documentElement.scrollWidth - win.innerWidth;
	if (max < 1) return null;
	if (win.scrollX <= 0.5) return "open";
	if (win.scrollX >= max - 0.5) return "closed";
	return null;
}

export interface NativeSink {
	/** 原生已在跟随根滚动：页面应进入原生滚动模式，原生底栏 / 页头可以接管。 */
	engaged(): boolean;
	subscribe(cb: () => void): () => void;
	/** SinkLayout 挂载时 true、卸载时 false。 */
	setOn(on: boolean): void;
	/** 原生把根滚到打开位 / 关闭位（`animated` = 逐帧动画，否则一步落位）。 */
	show(open: boolean, animated: boolean): void;
}

/** 底栏 / 页头控制器只需要知道接管与否。 */
export type SinkGate = Pick<NativeSink, "engaged" | "subscribe">;

export function createNativeSink(api: NativeSinkHost | null): NativeSink {
	let disabled = api === null; // set 被拒后本页不再尝试
	let wanted = false;
	let engaged = false;
	const subs = new Set<() => void>();
	const notify = () => {
		for (const cb of [...subs]) cb();
	};
	const sendOn = latest<boolean>(async (on) => {
		// 已回退：排在被拒那条之后的 true 不再发（false 照发）
		if (disabled && on) return;
		try {
			await (api as NativeSinkHost).set(on);
		} catch (e) {
			if (!on) return;
			// 原生不可用（iOS < 26、插件缺失）：本页回到网页手势，不再尝试
			disabled = true;
			wanted = false;
			throw e;
		}
		if (!on || !wanted || engaged) return;
		engaged = true;
		notify();
	}, "native sink");
	const sendShow = latest<{ open: boolean; animated: boolean }>(
		({ open, animated }) => (api as NativeSinkHost).show(open, animated),
		"native sink",
	);
	return {
		engaged: () => engaged,
		subscribe(cb) {
			subs.add(cb);
			return () => {
				subs.delete(cb);
			};
		},
		setOn(on) {
			if (disabled || on === wanted) return;
			wanted = on;
			if (!on && engaged) {
				engaged = false;
				notify();
			}
			sendOn(on);
		},
		show(open, animated) {
			if (engaged) sendShow({ open, animated });
		},
	};
}

/** 本页的根滚动跟随；没有原生能力时一切空操作。 */
export const nativeSink: NativeSink = createNativeSink(
	NATIVE_SINK ? (host.mobile?.sink ?? null) : null,
);

/** 原生已在跟随根滚动（页面处于原生滚动模式）。 */
export function useNativeSinkEngaged(): boolean {
	return useSyncExternalStore(
		nativeSink.subscribe,
		nativeSink.engaged,
		() => false,
	);
}

export interface SinkScrollEnv {
	/** 当前静止位置（`sinkRest(window)`）。 */
	rest(): SinkRest | null;
	/** 程序化开合或截止兜底：`smooth` = 原生逐帧动画，否则原生一步落位。 */
	scrollTo(to: SinkRest, smooth: boolean): void;
	/** 将非本页引起的位移立即拉回已提交的静止位。 */
	pull(to: SinkRest): void;
	setTimeout(cb: () => void, ms: number): number;
	clearTimeout(id: number): void;
}

export interface SinkScroll {
	/** 根滚动的 scroll 事件。 */
	scrolled(): void;
	/** 触点按下 / 全部抬起。 */
	touch(down: boolean): void;
	/** 本页要求开合（按钮、Esc、返回键、选线程等引起的 `sidebarOpen` 变化）。 */
	request(to: SinkRest, smooth: boolean): void;
	dispose(): void;
}

/**
 * 根滚动 → `sidebarOpen` 的同步与守卫。`committed` = 最近一次提交的静止位；`free` 自触点按下或本页请求起为真，
 * 到下一次提交为假。不 free 时任何离开 `committed` 的位移都立即拉回（instant）；free 时停在一端静止 SINK_REST_MS 后提交。
 * 停在中途不由网页吸附：原生给 UIKit 松手落点。WebKit 可能在原生接管拖动时提前发 touchcancel；网页以为手指抬起了，
 * 此时自行吸附会在按住期间把卡片拖走（PR #3 drag1）。触点按着时不提交（等抬起再计时）。
 * 本页请求的目的地（`target`）在提交或手指接管前一直有效：根不在目的地（还没起步、停在中途或仍在另一端）都继续等，
 * 主线程忙时原生动画可能晚起步、scroll 事件可能隔一百多毫秒才来一条，这时按静止提交会把开合反过来。
 * 等满 SINK_REQUEST_MS 仍未到位就再发一次一步落位（经原生，也是异步的），目的地保留、再给一个 SINK_REQUEST_MS 的宽限；
 * 宽限到了仍未到位就放弃目的地，按实际静止位提交（不再重发）。
 */
export function createSinkScroll(
	env: SinkScrollEnv,
	initial: SinkRest,
	commit: (rest: SinkRest) => void,
): SinkScroll {
	let committed = initial;
	let free = false;
	let touching = false;
	let target: SinkRest | null = null;
	/** 截止兜底的一步落位已经发过（本次请求不再重发，宽限到了就放弃）。 */
	let fallen = false;
	let timer: number | null = null;
	/** 本页请求的截止定时器：独立于静止定时器（后者每条 scroll 事件都重排，不能当期限用）。 */
	let deadline: number | null = null;
	const arm = (ms: number) => {
		if (timer !== null) env.clearTimeout(timer);
		timer = env.setTimeout(settle, ms);
	};
	/** 目的地作废：到位提交、手指接管、被新请求替换、卸载、宽限到期。 */
	const drop = () => {
		target = null;
		fallen = false;
		if (deadline !== null) env.clearTimeout(deadline);
		deadline = null;
	};
	const expire = () => {
		deadline = null;
		if (target === null || touching || env.rest() === target) return;
		if (fallen) {
			// 兜底落位也没到：放弃目的地，若仍在中途则等原生落到一端。
			drop();
			arm(SINK_REST_MS);
			return;
		}
		// 原生命令是异步的，落位可能晚到：目的地保留，宽限期内照旧不按静止提交
		fallen = true;
		env.scrollTo(target, false);
		deadline = env.setTimeout(expire, SINK_REQUEST_MS);
	};
	const settle = () => {
		timer = null;
		if (touching) return;
		const rest = env.rest();
		// 请求还没到位：等动画或截止定时器；没有请求时停在中途等原生落到一端。
		if (target !== null && rest !== target) return;
		if (rest === null) return;
		committed = rest;
		free = false;
		drop();
		commit(rest);
	};
	return {
		scrolled() {
			if (!free && env.rest() !== committed) {
				env.pull(committed);
				return;
			}
			arm(SINK_REST_MS);
		},
		touch(down) {
			touching = down;
			if (down) {
				free = true;
				drop();
			} else arm(SINK_REST_MS);
		},
		request(to, smooth) {
			// 已在目标位且已提交时不动；但有在途目标（另一端的动画还没起步或正在走）时照发，让原生停掉旧动画
			if (target === null && env.rest() === to && committed === to) return;
			drop();
			free = true;
			target = to;
			deadline = env.setTimeout(expire, SINK_REQUEST_MS);
			env.scrollTo(to, smooth);
			arm(SINK_REST_MS);
		},
		dispose() {
			if (timer !== null) env.clearTimeout(timer);
			timer = null;
			drop();
		},
	};
}
