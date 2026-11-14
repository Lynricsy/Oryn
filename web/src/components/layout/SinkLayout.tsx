/**
 * 手机（compact）下沉式侧栏，像 ChatGPT / Claude App：侧栏常驻底层（全屏高、页面底色、无外框无圆角，
 * 顶部沉浸由侧栏自己的 safe-area 内边距处理），内容（页头、转录、输入区）是一张卡片，打开时整张右移露出侧栏、
 * 左缘留一条；卡片四角与屏幕圆角同心（index.css 的 `.sink-card`）。
 * 为什么不用悬浮抽屉：悬浮抽屉四周留边，无法与顶部沉浸、屏幕圆角和刘海 / 挖孔区域协调。
 * - 状态 = `uiStore.sidebarOpen`（页头开关、Ctrl+B、侧栏里选线程后关闭都走它）。
 * - 手势（触摸）：关闭时从左缘 24px 内右滑打开；打开时在露出的卡片条上左滑关闭，轻点关闭。跟手（按位移插值进度），
 *   松手按速度 / 位置吸附（`settleSink`）。
 * - Esc 与返回键关闭：打开时压入一条带标记的历史记录，返回键弹出它即关闭；其它方式关闭时自己 `history.back()` 弹掉；
 *   在侧栏里点链接导航后标记留在栈里，日后返回落到它上面时自动再退一步。
 * - 打开时卡片 `inert`（不可点、读屏忽略），侧栏获得焦点；关闭后焦点还原到打开前的元素。
 * - `prefers-reduced-motion`：不做过渡，直接切换。
 */
import {
	type ReactNode,
	useEffect,
	useLayoutEffect,
	useRef,
	useSyncExternalStore,
} from "react";
import { useTranslation } from "react-i18next";
import { uiStore, useUi } from "../../state/ui";

/** 左缘起手区宽度（px）。 */
export const SINK_EDGE = 24;
/** 判定为横向拖动前的最小位移（px）。 */
const SLOP = 8;
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

export function SinkLayout({
	sidebar,
	children,
}: {
	sidebar: ReactNode;
	children: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const open = useUi((s) => s.sidebarOpen);
	const reduce = useReducedMotion();
	const cardRef = useRef<HTMLDivElement>(null);
	const sideRef = useRef<HTMLDivElement>(null);
	const restore = useRef<HTMLElement | null>(null);
	const openRef = useRef(open);
	openRef.current = open;

	// 历史标记：打开时压入；关闭时若标记仍在栈顶（没有因导航被盖住）就弹掉
	useEffect(() => {
		if (open) {
			if (!marked())
				window.history.pushState(
					{ ...(window.history.state ?? {}), [MARK]: true },
					"",
				);
			return;
		}
		if (marked()) window.history.back();
	}, [open]);

	useEffect(() => {
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
	}, []);

	// 焦点：打开时移入侧栏，关闭后还原
	useLayoutEffect(() => {
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
		if (back?.isConnected) back.focus({ preventScroll: true });
	}, [open]);

	useEffect(() => {
		if (!open) return;
		const onKey = (e: KeyboardEvent) => {
			if (e.key !== "Escape" || e.defaultPrevented) return;
			e.preventDefault();
			uiStore.getState().setSidebar(false);
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, [open]);

	// 触摸手势：跟手写 --sink-p，松手吸附
	useEffect(() => {
		const card = cardRef.current;
		if (!card) return;
		let start: { x: number; y: number; p: number; t: number } | null = null;
		let dragging = false;
		let last = { x: 0, t: 0, v: 0 };
		let progress = 0;
		const width = () =>
			sideRef.current?.getBoundingClientRect().width || window.innerWidth;
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
			// 关闭时只认左缘起手；打开时整张露出的卡片都可以拖
			if (!isOpen && touch.clientX > SINK_EDGE) return;
			const now = e.timeStamp || performance.now();
			start = { x: touch.clientX, y: touch.clientY, p: isOpen ? 1 : 0, t: now };
			last = { x: touch.clientX, t: now, v: 0 };
			progress = start.p;
		};
		const onMove = (e: TouchEvent) => {
			const touch = e.touches[0];
			if (!start || !touch) return;
			const dx = touch.clientX - start.x;
			const dy = touch.clientY - start.y;
			if (!dragging) {
				if (Math.abs(dx) < SLOP && Math.abs(dy) < SLOP) return;
				// 竖向为主：交给滚动
				if (Math.abs(dy) > Math.abs(dx)) {
					start = null;
					return;
				}
				dragging = true;
				card.setAttribute("data-dragging", "");
			}
			e.preventDefault();
			const now = e.timeStamp || performance.now();
			const dt = now - last.t;
			if (dt > 0)
				last = { x: touch.clientX, t: now, v: (touch.clientX - last.x) / dt };
			progress = sinkProgress(start.p, dx, width());
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
		};
	}, []);

	return (
		<div className="sink-root relative h-dvh w-full overflow-hidden text-fg">
			<div
				ref={sideRef}
				tabIndex={-1}
				inert={!open}
				data-testid="sink-sidebar"
				// isolate：栏内带 z-index 的元素（fork 树引导线）不得越过上层卡片
				className="sink-sidebar absolute inset-y-0 left-0 isolate w-[var(--sink-w)] outline-none"
			>
				{sidebar}
			</div>
			<div
				ref={cardRef}
				data-testid="sink-card"
				data-open={open ? "" : undefined}
				data-instant={reduce ? "" : undefined}
				className="sink-card absolute inset-0 flex"
			>
				{/* 壁纸晕染是半透明渐变：垫在卡片的不透明底色上，底层侧栏不会透上来 */}
				<div
					aria-hidden
					className="shell-wash pointer-events-none absolute inset-0"
				/>
				<div
					inert={open}
					data-testid="sink-content"
					className="relative flex h-full min-w-0 flex-1"
				>
					{children}
				</div>
				{open && (
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
