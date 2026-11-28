/**
 * 手机（compact）下沉式侧栏，像 ChatGPT / Claude App：侧栏常驻底层（全屏高、页面底色、无外框无圆角，
 * 顶部沉浸由侧栏自己的 safe-area 内边距处理），内容（页头、转录、输入区）是一张卡片，打开时整张右移露出侧栏、
 * 左缘留一条；卡片四角与屏幕圆角同心（index.css 的 `.sink-card`）。
 * 为什么不用悬浮抽屉：悬浮抽屉四周留边，无法与顶部沉浸、屏幕圆角和刘海 / 挖孔区域协调。
 * - 底栏（`SectionTabBar`）是侧栏自己的最后一个子元素：属于侧栏层，平时被卡片盖住，只在打开侧栏时露出；
 *   不放进卡片层，也不常驻屏幕底部（会挤占输入坞、破坏沉浸）。
 * - 状态 = `uiStore.sidebarOpen`（页头开关、Ctrl+B、侧栏里选线程后关闭都走它）。
 * - 手势（触摸）：关闭时在卡片任意位置横向右滑打开（不做系统手势排除：Android 每条边最多排除 200dp，且会与返回手势抢）；
 *   左缘 24px 内起手按横向为主即接管，其余位置要先过 SLOP、横向位移明显大于纵向（`DOMINANT` 倍）且向右才接管，
 *   起手点落在可横滚 / 可编辑的元素里（`blocksSwipe`）或有文本选择时不接管。打开时在露出的卡片条上左滑关闭，轻点关闭。
 *   跟手（按位移插值进度），松手按速度 / 位置吸附（`settleSink`）。
 * - Esc 与返回键关闭：打开时压入一条带标记的历史记录，返回键弹出它即关闭；其它方式关闭时自己 `history.back()` 弹掉；
 *   在侧栏里点链接导航后标记留在栈里，日后返回落到它上面时自动再退一步。
 * - 打开时卡片 `inert`（不可点、读屏忽略），侧栏获得焦点；关闭后焦点还原到打开前的元素。触屏上不还原到输入框
 *   （关闭那一下是用户手势，聚焦输入框会弹出软键盘；原生侧栏钮不夺网页焦点，打开前的焦点常是首页的输入框）。
 * - `prefers-reduced-motion`：不做过渡，直接切换。
 * - iOS 根滚动模式（bridge 8，`lib/native-sink.ts`）：挂载即请求原生跟随根滚动；接管后 `<html data-sink-native>`，
 *   文档可横向滚动；拖动与减速由系统处理，原生给松手落点，不装上面的 JS 手势。根滚动停稳后提交到 `sidebarOpen`；
 *   `sidebarOpen` 由别处改变时请求原生滚到对应一端；非本页引起的横向位移立即拉回。
 *   不接管（浏览器、Android、旧壳、iOS < 26）时一切照旧走 JS 手势。
 * - iOS 原生底栏（`lib/native-tab-bar.ts`）浮在 WebView 之上，右缘由原生按根滚动裁到卡片左缘（侧栏列表一直画到屏幕底边，
 *   玻璃折射它）。这里只在侧栏打开且静止时定时探测底栏位置是否被菜单、弹层、软键盘盖住（`setCovered`，盖住时原生隐藏、
 *   网页胶囊顶上）。
 * - `sink = false`（medium / expanded / 双段）：同一棵元素树退化成普通外框——根换成 `frame` 类名，侧栏层换成 `aside`，
 *   卡片与内容两层包装改为 `display: contents`，手势、历史标记、焦点、原生跟随全部停用。`children` 在 React 树里的位置
 *   不随断点变化，折叠屏折叠 / 展开跨 600px 时应用壳的 `<main>` 不重挂载（焦点、滚动位置、页内状态都留着）。
 */
import {
	Fragment,
	type ReactNode,
	useEffect,
	useLayoutEffect,
	useRef,
	useSyncExternalStore,
} from "react";
import { useTranslation } from "react-i18next";
import {
	createSinkScroll,
	nativeSink,
	type SinkRest,
	type SinkScroll,
	sinkRest,
	useNativeSinkEngaged,
} from "../../lib/native-sink";
import {
	COVER_POLL_MS,
	nativeTabBar,
	slotCovered,
	useNativeTabBarEngaged,
} from "../../lib/native-tab-bar";
import { setMinScale } from "../../lib/viewport";
import { uiStore, useUi } from "../../state/ui";

/** 左缘起手区宽度（px）。 */
export const SINK_EDGE = 24;
/** 判定为横向拖动前的最小位移（px）。 */
const SLOP = 8;
/** 非左缘起手时，横向位移至少是纵向的这么多倍才算横滑。 */
const DOMINANT = 1.5;
/** 起手点在这些元素里时不接管：它们自己要横滑、拖选或编辑（代码块、表格、终端、同级标签条、输入框）。 */
const OWN_GESTURE =
	'pre, table, input, textarea, select, [contenteditable]:not([contenteditable="false"]), .cm-editor, .xterm, [data-testid="terminal-view"], [data-testid="thread-tabs"], [role="tablist"]';
/** 会弹出软键盘的元素：触屏上关闭侧栏时不把焦点还原到它们。 */
const TEXT_ENTRY =
	'input, textarea, [contenteditable]:not([contenteditable="false"])';

/**
 * 卡片中部起手的右滑是否该让给元素自己：命中 `OWN_GESTURE`，或从起手点到卡片之间有可横滚且没滚到最左的容器
 * （右滑会先把它往左滚）。
 */
export function blocksSwipe(target: Element | null, stop: Element): boolean {
	if (!target) return false;
	const own = target.closest(OWN_GESTURE);
	if (own && stop.contains(own)) return true;
	for (
		let el: Element | null = target;
		el && el !== stop;
		el = el.parentElement
	) {
		if (el.scrollLeft <= 0 || el.scrollWidth <= el.clientWidth) continue;
		const ox = getComputedStyle(el).overflowX;
		if (ox === "auto" || ox === "scroll") return true;
	}
	return false;
}

/** 速度阈值（px/ms）：快速一甩即按方向吸附。 */
const FLING = 0.3;
/** 历史记录里的标记字段。 */
const MARK = "orynSidebar";

/** 松手吸附：速度够快按方向，否则按过半与否。返回是否停在打开。 */
export function settleSink(progress: number, velocity: number): boolean {
	if (velocity > FLING) return true;
	if (velocity < -FLING) return false;
	return progress >= 0.5;
}

/** 拖动进度：起始进度加上位移占侧栏宽度的比例，夹到 [0, 1]。 */
export function sinkProgress(start: number, dx: number, width: number): number {
	if (width <= 0) return start;
	return Math.min(1, Math.max(0, start + dx / width));
}

const REDUCE = "(prefers-reduced-motion: reduce)";

function subscribeReduce(cb: () => void) {
	const mq = window.matchMedia?.(REDUCE);
	mq?.addEventListener("change", cb);
	return () => mq?.removeEventListener("change", cb);
}

function useReducedMotion(): boolean {
	return useSyncExternalStore(
		subscribeReduce,
		() => window.matchMedia?.(REDUCE).matches ?? false,
		() => false,
	);
}

function marked(): boolean {
	const st = window.history.state as Record<string, unknown> | null;
	return !!st && st[MARK] === true;
}

/** 侧栏宽度（= 打开时卡片左缘的 x）；还没布局时退回视口宽。 */
function sinkWidth(side: HTMLElement | null): number {
	return side?.getBoundingClientRect().width || window.innerWidth;
}

/** 原生模式：卡片离开关闭位而 store 还是关着（拖动、程序化开合途中）时带 `data-dragging`，切到打开时的圆角、投影与裁剪。 */
function markMoving(card: HTMLElement, open: boolean) {
	card.toggleAttribute("data-dragging", !open && sinkRest(window) !== "closed");
}

export function SinkLayout({
	sink = true,
	sidebar,
	aside,
	frame,
	children,
}: {
	/** false = 普通外框：只渲染 `aside` 与 `children`，下沉侧栏的行为全部停用。 */
	sink?: boolean;
	/** 下沉侧栏层的内容（只在 `sink` 时渲染）。 */
	sidebar: ReactNode;
	/** 普通外框时排在内容之前的部分（列侧栏、悬浮抽屉）。 */
	aside?: ReactNode;
	/** 普通外框时根元素的类名。 */
	frame?: string;
	children: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const open = useUi((s) => s.sidebarOpen);
	const reduce = useReducedMotion();
	const engaged = useNativeTabBarEngaged();
	const native = useNativeSinkEngaged();
	const scrollRef = useRef<SinkScroll | null>(null);
	const cardRef = useRef<HTMLDivElement>(null);
	const sideRef = useRef<HTMLDivElement>(null);
	const restore = useRef<HTMLElement | null>(null);
	const openRef = useRef(open);
	openRef.current = open;
	// 历史标记：打开时压入；关闭时若标记仍在栈顶（没有因导航被盖住）就弹掉
	useEffect(() => {
		if (!sink) return;
		if (open) {
			if (!marked())
				window.history.pushState(
					{ ...(window.history.state ?? {}), [MARK]: true },
					"",
				);
			return;
		}
		if (marked()) window.history.back();
	}, [open, sink]);

	useEffect(() => {
		if (!sink) return;
		const onPop = () => {
			if (openRef.current) {
				if (!marked()) uiStore.getState().setSidebar(false);
				return;
			}
			// 关闭状态落到标记上（打开时在侧栏里导航留下的）：再退一步跳过它
			if (marked()) window.history.back();
		};
		window.addEventListener("popstate", onPop);
		return () => window.removeEventListener("popstate", onPop);
	}, [sink]);

	// 焦点：打开时移入侧栏，关闭后还原
	useLayoutEffect(() => {
		// 退出下沉模式等同卸载：丢掉待还原的焦点，回来时不会把焦点拉回很久以前的元素
		if (!sink) {
			restore.current = null;
			return;
		}
		if (open) {
			const active = document.activeElement;
			restore.current =
				active instanceof HTMLElement && !sideRef.current?.contains(active)
					? active
					: null;
			sideRef.current?.focus({ preventScroll: true });
			return;
		}
		const back = restore.current;
		restore.current = null;
		if (!back?.isConnected) return;
		if (
			back.matches(TEXT_ENTRY) &&
			window.matchMedia?.("(pointer: coarse)").matches
		)
			return;
		back.focus({ preventScroll: true });
	}, [open, sink]);

	useEffect(() => {
		if (!sink || !open) return;
		const onKey = (e: KeyboardEvent) => {
			if (e.key !== "Escape" || e.defaultPrevented) return;
			e.preventDefault();
			uiStore.getState().setSidebar(false);
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, [open, sink]);

	// 原生底栏的遮挡探测：只在打开且接管时；卡片停在打开位才判定（开合途中卡片还压在槽位上，命中卡片不是被弹层盖住）。
	// 定时探测，抬手 / 松键 / 软键盘变化 / 根滚动之后下一帧补探
	useEffect(() => {
		if (!sink || !open || !engaged) return;
		let frame = 0;
		const check = () => {
			if (sinkRest(window) !== "open") return;
			const slot = sideRef.current?.querySelector("[data-tabbar-slot]");
			if (slot) nativeTabBar.setCovered(slotCovered(slot, window));
		};
		const soon = () => {
			if (frame) return;
			frame = requestAnimationFrame(() => {
				frame = 0;
				check();
			});
		};
		check();
		const poll = window.setInterval(check, COVER_POLL_MS);
		const vv = window.visualViewport;
		document.addEventListener("pointerup", soon, true);
		document.addEventListener("keyup", soon, true);
		window.addEventListener("scroll", soon, { passive: true });
		vv?.addEventListener("resize", soon);
		return () => {
			window.clearInterval(poll);
			cancelAnimationFrame(frame);
			document.removeEventListener("pointerup", soon, true);
			document.removeEventListener("keyup", soon, true);
			window.removeEventListener("scroll", soon);
			vv?.removeEventListener("resize", soon);
			nativeTabBar.setCovered(false);
		};
	}, [open, engaged, sink]);

	// 根滚动跟随：进入下沉模式即请求（没有原生能力时是空操作），退出时撤掉
	useEffect(() => {
		if (!sink) return;
		nativeSink.setOn(true);
		return () => nativeSink.setOn(false);
	}, [sink]);

	// 原生滚动模式：文档可横滚（index.css 的 `:root[data-sink-native]`），根滚动与 sidebarOpen 互相同步
	useLayoutEffect(() => {
		const card = cardRef.current;
		if (!sink || !native || !card) return;
		const root = document.documentElement;
		root.setAttribute("data-sink-native", "");
		setMinScale(true);
		// 根滚动位置就是开合状态。关闭时 history.back() 弹掉同文档的历史标记，不能恢复标记记录的打开位。
		const restoration = window.history.scrollRestoration;
		window.history.scrollRestoration = "manual";
		const max = () => root.scrollWidth - window.innerWidth;
		const jump = (to: SinkRest) =>
			window.scrollTo({
				left: to === "open" ? 0 : max(),
				behavior: "instant",
			});
		const here = (): SinkRest => (openRef.current ? "open" : "closed");
		jump(here());
		const s = createSinkScroll(
			{
				rest: () => sinkRest(window),
				scrollTo: (to, smooth) => nativeSink.show(to === "open", smooth),
				pull: jump,
				setTimeout: (cb, ms) => window.setTimeout(cb, ms),
				clearTimeout: (id) => window.clearTimeout(id),
			},
			here(),
			(rest) => {
				const next = rest === "open";
				if (next !== openRef.current) uiStore.getState().setSidebar(next);
			},
		);
		scrollRef.current = s;
		const onScroll = () => {
			markMoving(card, openRef.current);
			s.scrolled();
		};
		const onTouch = (e: TouchEvent) => s.touch(e.touches.length > 0);
		const onResize = () => jump(here());
		const opts = { passive: true };
		window.addEventListener("scroll", onScroll, opts);
		window.addEventListener("touchstart", onTouch, opts);
		window.addEventListener("touchend", onTouch, opts);
		window.addEventListener("touchcancel", onTouch, opts);
		window.addEventListener("resize", onResize);
		return () => {
			window.removeEventListener("scroll", onScroll);
			window.removeEventListener("touchstart", onTouch);
			window.removeEventListener("touchend", onTouch);
			window.removeEventListener("touchcancel", onTouch);
			window.removeEventListener("resize", onResize);
			s.dispose();
			scrollRef.current = null;
			card.removeAttribute("data-dragging");
			root.removeAttribute("data-sink-native");
			setMinScale(false);
			window.history.scrollRestoration = restoration;
			window.scrollTo(0, 0);
		};
	}, [native, sink]);

	// sidebarOpen 由别处改变（按钮、Esc、返回键、选线程）：请求滚到对应一端；根滚动自己提交的变化在这里是空操作
	useLayoutEffect(() => {
		if (!sink || !native) return;
		scrollRef.current?.request(open ? "open" : "closed", !reduce);
		if (cardRef.current) markMoving(cardRef.current, open);
	}, [open, reduce, native, sink]);

	// 触摸手势（非原生模式）：跟手写 --sink-p，松手吸附
	useEffect(() => {
		const card = cardRef.current;
		if (!sink || !card || native) return;
		let start: {
			x: number;
			y: number;
			p: number;
			t: number;
			edge: boolean;
		} | null = null;
		let dragging = false;
		let last = { x: 0, t: 0, v: 0 };
		let progress = 0;
		const width = () => sinkWidth(sideRef.current);
		const end = () => {
			start = null;
			if (!dragging) return;
			dragging = false;
			card.removeAttribute("data-dragging");
			card.style.removeProperty("--sink-p");
			const next = settleSink(progress, last.v);
			if (next !== openRef.current) uiStore.getState().setSidebar(next);
		};
		const onStart = (e: TouchEvent) => {
			const touch = e.touches[0];
			if (!touch || e.touches.length > 1) {
				start = null;
				return;
			}
			const isOpen = openRef.current;
			// 打开时整张露出的卡片都可以拖；关闭时左缘直接认，其余位置让给自带手势的元素与文本选择
			const edge = !isOpen && touch.clientX <= SINK_EDGE;
			if (!isOpen && !edge) {
				const sel = window.getSelection?.();
				if (sel && !sel.isCollapsed) return;
				if (blocksSwipe(e.target instanceof Element ? e.target : null, card))
					return;
			}
			const now = e.timeStamp || performance.now();
			start = {
				x: touch.clientX,
				y: touch.clientY,
				p: isOpen ? 1 : 0,
				t: now,
				edge,
			};
			last = { x: touch.clientX, t: now, v: 0 };
			progress = start.p;
		};
		const onMove = (e: TouchEvent) => {
			const touch = e.touches[0];
			if (!start || !touch) return;
			const dx = touch.clientX - start.x;
			const dy = touch.clientY - start.y;
			const now = e.timeStamp || performance.now();
			if (!dragging) {
				if (Math.abs(dx) < SLOP && Math.abs(dy) < SLOP) return;
				// 竖向为主：交给滚动；关闭时卡片中部起手还要求横向明显占优且向右
				const mid = start.p === 0 && !start.edge;
				if (
					Math.abs(dy) > Math.abs(dx) ||
					(mid && (dx <= 0 || Math.abs(dx) < Math.abs(dy) * DOMINANT))
				) {
					start = null;
					return;
				}
				dragging = true;
				card.setAttribute("data-dragging", "");
			}
			e.preventDefault();
			const dt = now - last.t;
			if (dt > 0)
				last = { x: touch.clientX, t: now, v: (touch.clientX - last.x) / dt };
			const w = width();
			progress = sinkProgress(start.p, dx, w);
			card.style.setProperty("--sink-p", String(progress));
		};
		card.addEventListener("touchstart", onStart, { passive: true });
		card.addEventListener("touchmove", onMove, { passive: false });
		card.addEventListener("touchend", end);
		card.addEventListener("touchcancel", end);
		return () => {
			card.removeEventListener("touchstart", onStart);
			card.removeEventListener("touchmove", onMove);
			card.removeEventListener("touchend", end);
			card.removeEventListener("touchcancel", end);
			// 手势没等到 touchend / touchcancel 就被打断（折叠屏展开退出下沉模式、转入原生滚动）：卡片元素留着，
			// 清掉拖动标记与进度，否则回来时 .sink-card 读到旧进度卡在半开；不走 end()，打断不提交开合
			if (dragging) {
				card.removeAttribute("data-dragging");
				card.style.removeProperty("--sink-p");
			}
		};
	}, [native, sink]);

	// 两种模式的元素树逐层同构（根 → 卡片 → 内容 → children），只换类名与属性：children 不随断点重挂载
	return (
		<div
			className={
				sink ? "sink-root relative h-dvh w-full overflow-hidden text-fg" : frame
			}
		>
			{sink ? (
				<div
					key="sink"
					ref={sideRef}
					tabIndex={-1}
					inert={!open}
					data-testid="sink-sidebar"
					// isolate：栏内带 z-index 的元素（fork 树引导线）不得越过上层卡片
					className="sink-sidebar absolute inset-y-0 left-0 isolate flex w-[var(--sink-w)] flex-col outline-none"
				>
					<div className="min-h-0 flex-1">{sidebar}</div>
				</div>
			) : (
				<Fragment key="aside">{aside}</Fragment>
			)}
			<div
				ref={cardRef}
				data-testid={sink ? "sink-card" : undefined}
				data-open={sink && open ? "" : undefined}
				data-instant={sink && reduce ? "" : undefined}
				className={sink ? "sink-card absolute inset-0 flex" : "contents"}
			>
				{/* 壁纸晕染是半透明渐变：垫在卡片的不透明底色上，底层侧栏不会透上来 */}
				{sink && (
					<div
						aria-hidden
						className="shell-wash pointer-events-none absolute inset-0"
					/>
				)}
				<div
					inert={sink && open}
					data-testid={sink ? "sink-content" : undefined}
					className={sink ? "relative flex h-full min-w-0 flex-1" : "contents"}
				>
					{children}
				</div>
				{sink && open && (
					// 露出的卡片条：轻点关闭（拖动由卡片上的触摸手势处理）
					<button
						type="button"
						aria-label={t("common.close")}
						data-testid="sink-close"
						className="absolute inset-0 z-[70] cursor-default"
						onClick={() => uiStore.getState().setSidebar(false)}
					/>
				)}
			</div>
		</div>
	);
}
