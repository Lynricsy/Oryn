/**
 * 演示模式（计划 M14 6.8）：铺满窗口的固定深色对话框，一次一页居中。页面与画布类型的 artifact 用它
 * （文档与幻灯片用各自查看器里的演示，经 `present` 消息让帧铺满窗口，不经过这里）。
 *
 * - 有尺寸的页（画布的画框）按原始尺寸排版，整体缩放到「四周留 48、底栏 72」之后放得下的最大尺寸；
 *   没给尺寸的页（单页 artifact）直接铺满底栏以上的区域。页面保持可交互。
 * - 键盘：`→` / 空格下一页，`←` / `Shift+空格` 上一页，`Esc` 退出；翻页不越界。焦点在页面帧里时按键归页面。
 * - 触屏（9.2）：左右轻扫翻页（向左下一页）。手势从页面帧以外（四周留白、底栏）开始才收得到：帧里的触摸归页面本身。
 * - 底栏：上一页、「{名字} · {n} / {N}」、下一页、「Esc 退出」与退出按钮。
 * 渲染到 `document.body` 的门户里，按键在捕获阶段处理并截住（不落到画布编辑器或查看器的快捷键上）。
 */
import {
	type ReactNode,
	type RefObject,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { IconButton } from "../ui/primitives";

/** 页四周留白与底栏高度（像素）。 */
export const PRESENT_MARGIN = 48;
export const PRESENT_BAR = 72;
/** 轻扫翻页：水平移动至少这么多像素，且明显大于垂直移动。 */
export const SWIPE_PX = 48;

/** 一次触摸从 `from` 到 `to` 是不是左右轻扫：返回翻页方向（向左扫 = 下一页 = 1），不是则 0。 */
export function swipeDelta(
	from: { x: number; y: number },
	to: { x: number; y: number },
): -1 | 0 | 1 {
	const dx = to.x - from.x;
	const dy = to.y - from.y;
	if (Math.abs(dx) < SWIPE_PX || Math.abs(dx) < 1.5 * Math.abs(dy)) return 0;
	return dx < 0 ? 1 : -1;
}

export interface PresenterPage {
	id: string;
	name: string;
	/** 页的原始尺寸；不给 = 铺满。 */
	width?: number;
	height?: number;
}

/** 把 `width × height` 放进 `area` 的最大缩放（不放大到超过区域，也不为负）。 */
export function fitScale(
	width: number,
	height: number,
	area: { width: number; height: number },
): number {
	if (width <= 0 || height <= 0) return 0;
	return Math.max(0, Math.min(area.width / width, area.height / height));
}

/** 元素的内容尺寸（ResizeObserver）；没有 ResizeObserver 的环境取窗口尺寸。 */
export function useBoxSize(ref: RefObject<HTMLElement | null>): {
	width: number;
	height: number;
} {
	const [size, setSize] = useState({ width: 0, height: 0 });
	useLayoutEffect(() => {
		const el = ref.current;
		if (!el) return;
		const measure = () => {
			if (el.clientWidth > 0 && el.clientHeight > 0)
				setSize({ width: el.clientWidth, height: el.clientHeight });
		};
		measure();
		if (typeof ResizeObserver === "undefined") {
			if (el.clientWidth === 0 && el.clientHeight === 0)
				setSize({ width: window.innerWidth, height: window.innerHeight });
			return;
		}
		const ro = new ResizeObserver(([entry]) => {
			if (entry)
				setSize({
					width: entry.contentRect.width,
					height: entry.contentRect.height,
				});
		});
		ro.observe(el);
		return () => ro.disconnect();
	}, [ref]);
	return size;
}

/** 按原始尺寸排版、整体缩放后居中的一页：`children` 铺满 `width × height` 的盒子。 */
export function ScaledPage({
	width,
	height,
	scale,
	children,
}: {
	width: number;
	height: number;
	scale: number;
	children: ReactNode;
}): ReactNode {
	return (
		<div
			className="shrink-0 overflow-hidden"
			style={{ width: width * scale, height: height * scale }}
		>
			<div
				style={{
					width,
					height,
					transform: `scale(${scale})`,
					transformOrigin: "0 0",
				}}
			>
				{children}
			</div>
		</div>
	);
}

export interface PresenterProps<P extends PresenterPage> {
	/** artifact 标题（对话框的读屏名）。 */
	title: string;
	/** 按演示顺序。 */
	pages: readonly P[];
	/** 从第几页开始（从 0 起）。 */
	start?: number;
	renderPage(page: P): ReactNode;
	onClose(): void;
}

export function Presenter<P extends PresenterPage>({
	title,
	pages,
	start = 0,
	renderPage,
	onClose,
}: PresenterProps<P>): ReactNode {
	const { t } = useTranslation();
	const [index, setIndex] = useState(start);
	const rootRef = useRef<HTMLDivElement>(null);
	const stageRef = useRef<HTMLDivElement>(null);
	const area = useBoxSize(stageRef);
	const total = pages.length;
	// 页数变少（别处删了画框）时停在最后一页
	const at = Math.max(0, Math.min(index, total - 1));
	const page = pages[at];

	const go = (delta: number) =>
		setIndex((i) => Math.max(0, Math.min(total - 1, i + delta)));
	// 触屏轻扫：按下的位置（只认触摸与触控笔，鼠标拖动不翻页）
	const swipeRef = useRef<{ id: number; x: number; y: number } | null>(null);
	const keyRef = useRef<(e: KeyboardEvent) => boolean>(() => false);
	keyRef.current = (e) => {
		if (e.ctrlKey || e.metaKey || e.altKey) return false;
		switch (e.key) {
			case "Escape":
				onClose();
				return true;
			case "ArrowRight":
				go(1);
				return true;
			case "ArrowLeft":
				go(-1);
				return true;
			case " ":
				go(e.shiftKey ? -1 : 1);
				return true;
			default:
				return false;
		}
	};
	useEffect(() => {
		const onKey = (e: KeyboardEvent) => {
			if (!keyRef.current(e)) return;
			e.preventDefault();
			e.stopPropagation();
		};
		window.addEventListener("keydown", onKey, true);
		return () => window.removeEventListener("keydown", onKey, true);
	}, []);

	// 打开时把焦点移进来，关闭后还给原来的元素
	useEffect(() => {
		const before = document.activeElement as HTMLElement | null;
		rootRef.current?.focus({ preventScroll: true });
		return () => before?.focus?.({ preventScroll: true });
	}, []);

	const width = page?.width;
	const height = page?.height;

	return createPortal(
		<div
			ref={rootRef}
			role="dialog"
			aria-modal="true"
			aria-label={t("artifacts.present.title", { title })}
			tabIndex={-1}
			data-testid="artifact-presenter"
			className="theme-dark fixed inset-0 z-50 flex touch-pan-y flex-col bg-bg text-fg outline-none"
			onPointerDown={(e) => {
				if (e.pointerType === "mouse" || !e.isPrimary) return;
				swipeRef.current = { id: e.pointerId, x: e.clientX, y: e.clientY };
			}}
			onPointerUp={(e) => {
				const from = swipeRef.current;
				swipeRef.current = null;
				if (!from || from.id !== e.pointerId) return;
				const d = swipeDelta(from, { x: e.clientX, y: e.clientY });
				if (d !== 0) go(d);
			}}
			onPointerCancel={() => {
				swipeRef.current = null;
			}}
		>
			<div
				ref={stageRef}
				className="relative flex min-h-0 flex-1 items-center justify-center"
			>
				{page &&
					(width !== undefined && height !== undefined ? (
						<ScaledPage
							key={page.id}
							width={width}
							height={height}
							scale={fitScale(width, height, {
								width: area.width - 2 * PRESENT_MARGIN,
								height: area.height - 2 * PRESENT_MARGIN,
							})}
						>
							{renderPage(page)}
						</ScaledPage>
					) : (
						<div key={page.id} className="absolute inset-0">
							{renderPage(page)}
						</div>
					))}
			</div>
			<div
				className="relative flex shrink-0 items-center justify-center gap-2 px-4"
				style={{ height: PRESENT_BAR }}
			>
				{total > 1 && (
					<IconButton
						icon="back"
						label={t("artifacts.present.prev")}
						data-testid="presenter-prev"
						disabled={at === 0}
						onClick={() => go(-1)}
					/>
				)}
				<span
					data-testid="presenter-position"
					className="min-w-0 truncate text-sm tabular-nums"
				>
					{page
						? total > 1
							? t("artifacts.present.position", {
									name: page.name,
									n: at + 1,
									total,
								})
							: page.name
						: ""}
				</span>
				{total > 1 && (
					<IconButton
						icon="forward"
						label={t("artifacts.present.next")}
						data-testid="presenter-next"
						disabled={at >= total - 1}
						onClick={() => go(1)}
					/>
				)}
				<span className="absolute right-4 flex items-center gap-2">
					<span className="hidden text-muted text-xs sm:inline">
						{t("artifacts.present.escHint")}
					</span>
					<IconButton
						icon="close"
						label={t("artifacts.present.close")}
						data-testid="presenter-close"
						onClick={onClose}
					/>
				</span>
			</div>
		</div>,
		document.body,
	);
}
