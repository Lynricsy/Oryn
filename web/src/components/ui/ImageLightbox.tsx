/**
 * 应用内全屏图片灯箱：
 * - 全屏深色沉浸舞台 + 毛玻璃悬浮顶栏 / 底栏，支持单击隐藏/显示 UI 栏；
 * - 鼠标滚轮（光标锚定）、双指捏合、双击、快捷键与按钮缩放（0.25x ~ 8x），适应窗口与 1:1 原始像素一键切换；
 * - 放大后鼠标/单指自由拖拽平移；未放大时移动端支持左右滑切图、下滑关闭；
 * - 逆时针/顺时针 90° 旋转、水平翻转、一键复位；
 * - 三种背景底纹切换（深色 / 透明棋盘格 / 浅色）；
 * - 多图画廊支持左右切换与底部缩略图胶片条；
 * - 复制图片到剪贴板（PNG）、下载原图；
 * - 严格安全边界：仅经 `<img>` 渲染，不导航不新开文档窗口；完整接入 `pushModal` 栈、Tab 焦点圈定与 Esc 关闭。
 */
import {
	type HTMLAttributeReferrerPolicy,
	type MouseEvent,
	type PointerEvent,
	type ReactNode,
	useCallback,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import {
	canCopyImage,
	copyImageToClipboard,
	downloadImage,
} from "../../lib/context-menu/image-actions";
import { isTopModal, pushModal, trapTab } from "../../lib/modal";
import { showToast } from "../../state/toasts";
import { Icon, type IconName } from "./icons";
import { Spinner } from "./primitives";

export interface LightboxImage {
	src: string;
	alt?: string;
	/** 顶部显示的标题/说明；省略时回退到 alt 或文件名 */
	caption?: string;
	/** 下载时建议的文件名 */
	filename?: string;
	referrerPolicy?: HTMLAttributeReferrerPolicy;
}

export interface ImageLightboxProps {
	images: readonly LightboxImage[];
	initialIndex?: number;
	onClose: () => void;
	onIndexChange?: (index: number) => void;
}

type BackdropMode = "dark" | "checker" | "light";

const MIN_ZOOM = 0.25;
const MAX_ZOOM = 8;
const ZOOM_STEPS = [0.25, 0.5, 0.75, 1, 1.5, 2, 3, 4, 6, 8];

function clamp(v: number, min: number, max: number): number {
	return Math.min(max, Math.max(min, v));
}

function nextBackdrop(cur: BackdropMode): BackdropMode {
	return cur === "dark" ? "checker" : cur === "checker" ? "light" : "dark";
}

function DockBtn({
	icon,
	label,
	active,
	disabled,
	onClick,
}: {
	icon: IconName;
	label: string;
	active?: boolean;
	disabled?: boolean;
	onClick: () => void;
}): ReactNode {
	return (
		<button
			type="button"
			disabled={disabled}
			aria-label={label}
			title={label}
			onClick={onClick}
			className={`inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-lg transition-colors duration-150 focus-visible:outline-2 focus-visible:outline-[var(--ring)] focus-visible:outline-offset-1 disabled:pointer-events-none disabled:opacity-35 ${
				active
					? "bg-white/20 text-white"
					: "text-white/80 hover:bg-white/12 hover:text-white active:bg-white/20"
			}`}
		>
			<Icon name={icon} size={16} />
		</button>
	);
}

export function ImageLightbox({
	images,
	initialIndex = 0,
	onClose,
	onIndexChange,
}: ImageLightboxProps): ReactNode {
	const { t } = useTranslation();
	const count = images.length;
	const [index, setIndex] = useState(() =>
		count > 0 ? clamp(initialIndex, 0, count - 1) : 0,
	);
	const safeIndex = count > 0 ? clamp(index, 0, count - 1) : 0;
	const current = images[safeIndex];

	const [zoom, setZoom] = useState(1);
	const [pan, setPan] = useState({ x: 0, y: 0 });
	const [rotation, setRotation] = useState(0);
	const [flipH, setFlipH] = useState(false);
	const [backdrop, setBackdrop] = useState<BackdropMode>("dark");
	const [hudVisible, setHudVisible] = useState(true);
	const [natural, setNatural] = useState<{ w: number; h: number } | null>(null);
	const [fitted, setFitted] = useState<{ w: number; h: number } | null>(null);
	const [loading, setLoading] = useState(true);
	const [failed, setFailed] = useState(false);
	const [retryKey, setRetryKey] = useState(0);
	const [copied, setCopied] = useState(false);
	const [interacting, setInteracting] = useState(false);
	const [swipeOffset, setSwipeOffset] = useState({ x: 0, y: 0 });

	const hostRef = useRef<HTMLDivElement>(null);
	const dialogRef = useRef<HTMLDivElement>(null);
	const stageRef = useRef<HTMLDivElement>(null);
	const imgRef = useRef<HTMLImageElement>(null);
	const thumbRefs = useRef<Map<number, HTMLButtonElement>>(new Map());
	const onCloseRef = useRef(onClose);
	onCloseRef.current = onClose;

	// 记下打开灯箱前的聚焦元素以便关闭后还原
	const openerRef = useRef<HTMLElement | null | undefined>(undefined);
	if (openerRef.current === undefined) {
		openerRef.current =
			typeof document !== "undefined" &&
			document.activeElement instanceof HTMLElement
				? document.activeElement
				: null;
	}

	// 最新状态 ref 供非 React 原生事件（wheel / keydown）读取，避免频繁重绑监听
	const viewRef = useRef({ zoom, pan, safeIndex, count });
	viewRef.current = { zoom, pan, safeIndex, count };

	const resetTransform = useCallback(() => {
		setZoom(1);
		setPan({ x: 0, y: 0 });
		setRotation(0);
		setFlipH(false);
		setSwipeOffset({ x: 0, y: 0 });
	}, []);

	const goTo = useCallback(
		(next: number) => {
			if (count === 0) return;
			const clamped = clamp(next, 0, count - 1);
			if (clamped === viewRef.current.safeIndex) return;
			setIndex(clamped);
			onIndexChange?.(clamped);
			resetTransform();
			setNatural(null);
			setFitted(null);
			setLoading(true);
			setFailed(false);
			setCopied(false);
		},
		[count, onIndexChange, resetTransform],
	);

	// 切换图片时让对应缩略图滚入可视区
	useEffect(() => {
		const btn = thumbRefs.current.get(safeIndex);
		btn?.scrollIntoView?.({
			behavior: "smooth",
			block: "nearest",
			inline: "center",
		});
	}, [safeIndex]);

	// 图片 src 变化时重置加载状态
	const currentSrc = current?.src ?? "";
	// biome-ignore lint/correctness/useExhaustiveDependencies: retryKey 显式触发重载
	useEffect(() => {
		setLoading(true);
		setFailed(false);
		setNatural(null);
		setFitted(null);
		// 如果浏览器已同步完成缓存解码，直接同步尺寸
		const el = imgRef.current;
		if (el?.complete && el.naturalWidth > 0) {
			setNatural({ w: el.naturalWidth, h: el.naturalHeight });
			setFitted({ w: el.clientWidth, h: el.clientHeight });
			setLoading(false);
		}
	}, [currentSrc, retryKey]);

	const zoomTo = useCallback(
		(target: number, anchor?: { cx: number; cy: number }) => {
			const nextZoom = clamp(target, MIN_ZOOM, MAX_ZOOM);
			setZoom((prevZoom) => {
				if (Math.abs(nextZoom - prevZoom) < 1e-4) return prevZoom;
				if (nextZoom <= 1.001) {
					setPan({ x: 0, y: 0 });
				} else if (anchor) {
					const ratio = nextZoom / prevZoom;
					setPan((p) => ({
						x: anchor.cx - (anchor.cx - p.x) * ratio,
						y: anchor.cy - (anchor.cy - p.y) * ratio,
					}));
				}
				return nextZoom;
			});
		},
		[],
	);

	const stepZoom = useCallback(
		(dir: 1 | -1) => {
			const cur = viewRef.current.zoom;
			const next =
				dir > 0
					? ZOOM_STEPS.find((z) => z > cur + 0.02)
					: ZOOM_STEPS.toReversed().find((z) => z < cur - 0.02);
			zoomTo(next ?? (dir > 0 ? MAX_ZOOM : MIN_ZOOM));
		},
		[zoomTo],
	);

	const toggleActualSize = useCallback(() => {
		if (!natural || !fitted || fitted.w <= 0) {
			zoomTo(viewRef.current.zoom === 1 ? 2 : 1);
			return;
		}
		const actualZoom = clamp(natural.w / fitted.w, MIN_ZOOM, MAX_ZOOM);
		const nearActual =
			Math.abs(viewRef.current.zoom - actualZoom) < 0.05 ||
			Math.abs(actualZoom - 1) < 0.03;
		if (nearActual) {
			setZoom(1);
			setPan({ x: 0, y: 0 });
		} else {
			setZoom(actualZoom);
			setPan({ x: 0, y: 0 });
		}
	}, [natural, fitted, zoomTo]);
	// 模态栈、inert 与全局键盘快捷键
	useLayoutEffect(() => {
		const host = hostRef.current;
		const dialog = dialogRef.current;
		if (!host || !dialog) return;
		const opener = openerRef.current;
		const previous = opener && !host.contains(opener) ? opener : null;
		const release = pushModal(host);
		if (!dialog.contains(document.activeElement)) {
			dialog.focus({ preventScroll: true });
		}
		const onKey = (e: KeyboardEvent) => {
			if (!isTopModal(host)) return;
			if (e.key === "Tab") {
				trapTab(e, dialog);
				return;
			}
			if (e.key === "Escape") {
				e.preventDefault();
				onCloseRef.current();
				return;
			}
			if (e.metaKey || e.ctrlKey || e.altKey) return;
			switch (e.key) {
				case "ArrowLeft":
					e.preventDefault();
					goTo(viewRef.current.safeIndex - 1);
					break;
				case "ArrowRight":
					e.preventDefault();
					goTo(viewRef.current.safeIndex + 1);
					break;
				case "+":
				case "=":
					e.preventDefault();
					stepZoom(1);
					break;
				case "-":
				case "_":
					e.preventDefault();
					stepZoom(-1);
					break;
				case "0":
					e.preventDefault();
					resetTransform();
					break;
				case "1":
					e.preventDefault();
					toggleActualSize();
					break;
				case "r":
				case "R":
					e.preventDefault();
					setRotation((deg) => deg + (e.shiftKey ? -90 : 90));
					break;
				case "b":
				case "B":
					e.preventDefault();
					setBackdrop(nextBackdrop);
					break;
			}
		};
		window.addEventListener("keydown", onKey);
		return () => {
			window.removeEventListener("keydown", onKey);
			release();
			if (previous?.isConnected) previous.focus();
		};
	}, [goTo, resetTransform, stepZoom, toggleActualSize]);

	// 舞台滚轮缩放（non-passive 以阻止底层滚动与浏览器页面缩放）
	useEffect(() => {
		const stage = stageRef.current;
		if (!stage) return;
		const onWheel = (e: WheelEvent) => {
			e.preventDefault();
			const rect = stage.getBoundingClientRect();
			const cx = e.clientX - (rect.left + rect.width / 2);
			const cy = e.clientY - (rect.top + rect.height / 2);
			const factor = Math.exp(-e.deltaY * (e.ctrlKey ? 0.01 : 0.0022));
			zoomTo(viewRef.current.zoom * factor, { cx, cy });
		};
		stage.addEventListener("wheel", onWheel, { passive: false });
		return () => stage.removeEventListener("wheel", onWheel);
	}, [zoomTo]);

	// 指针手势状态：拖拽平移 / 双指捏合 / 移动端滑动切图与下滑关闭
	const pointersRef = useRef<Map<number, { x: number; y: number }>>(new Map());
	const gestureRef = useRef<{
		mode: "none" | "pan" | "swipe" | "pinch";
		startX: number;
		startY: number;
		startPanX: number;
		startPanY: number;
		moved: boolean;
		pinchDist: number;
		pinchZoom: number;
		pinchMidX: number;
		pinchMidY: number;
	}>({
		mode: "none",
		startX: 0,
		startY: 0,
		startPanX: 0,
		startPanY: 0,
		moved: false,
		pinchDist: 0,
		pinchZoom: 1,
		pinchMidX: 0,
		pinchMidY: 0,
	});
	const suppressClickRef = useRef(false);

	const onStagePointerDown = (e: PointerEvent<HTMLDivElement>) => {
		if (e.button !== 0 && e.pointerType === "mouse") return;
		if ((e.target as HTMLElement).closest("button, a")) return;
		const stage = stageRef.current;
		if (!stage) return;
		try {
			stage.setPointerCapture(e.pointerId);
		} catch {
			// jsdom 或部分环境不支持 pointer capture
		}
		pointersRef.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
		const pts = Array.from(pointersRef.current.values());
		if (pts.length === 2) {
			const [a, b] = pts as [
				{ x: number; y: number },
				{ x: number; y: number },
			];
			const rect = stage.getBoundingClientRect();
			gestureRef.current = {
				mode: "pinch",
				startX: 0,
				startY: 0,
				startPanX: pan.x,
				startPanY: pan.y,
				moved: true,
				pinchDist: Math.hypot(b.x - a.x, b.y - a.y) || 1,
				pinchZoom: zoom,
				pinchMidX: (a.x + b.x) / 2 - (rect.left + rect.width / 2),
				pinchMidY: (a.y + b.y) / 2 - (rect.top + rect.height / 2),
			};
			setInteracting(true);
		} else if (pts.length === 1) {
			const mode =
				zoom > 1.01 ? "pan" : e.pointerType === "touch" ? "swipe" : "none";
			gestureRef.current = {
				mode,
				startX: e.clientX,
				startY: e.clientY,
				startPanX: pan.x,
				startPanY: pan.y,
				moved: false,
				pinchDist: 0,
				pinchZoom: zoom,
				pinchMidX: 0,
				pinchMidY: 0,
			};
			if (mode !== "none") setInteracting(true);
		}
	};

	const onStagePointerMove = (e: PointerEvent<HTMLDivElement>) => {
		if (!pointersRef.current.has(e.pointerId)) return;
		pointersRef.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
		const g = gestureRef.current;
		const pts = Array.from(pointersRef.current.values());

		if (g.mode === "pinch" && pts.length >= 2) {
			const [a, b] = pts as [
				{ x: number; y: number },
				{ x: number; y: number },
			];
			const dist = Math.hypot(b.x - a.x, b.y - a.y) || 1;
			const nextZoom = clamp(
				g.pinchZoom * (dist / g.pinchDist),
				MIN_ZOOM,
				MAX_ZOOM,
			);
			const ratio = nextZoom / g.pinchZoom;
			setZoom(nextZoom);
			setPan(
				nextZoom <= 1.001
					? { x: 0, y: 0 }
					: {
							x: g.pinchMidX - (g.pinchMidX - g.startPanX) * ratio,
							y: g.pinchMidY - (g.pinchMidY - g.startPanY) * ratio,
						},
			);
			return;
		}

		const dx = e.clientX - g.startX;
		const dy = e.clientY - g.startY;
		if (Math.hypot(dx, dy) > 4) g.moved = true;

		if (g.mode === "pan") {
			setPan({ x: g.startPanX + dx, y: g.startPanY + dy });
		} else if (g.mode === "swipe") {
			setSwipeOffset({ x: dx, y: dy });
		}
	};

	const finishPointer = (e: PointerEvent<HTMLDivElement>) => {
		if (!pointersRef.current.has(e.pointerId)) return;
		pointersRef.current.delete(e.pointerId);
		const g = gestureRef.current;
		if (g.moved) suppressClickRef.current = true;

		if (g.mode === "swipe" && pointersRef.current.size === 0) {
			const dx = e.clientX - g.startX;
			const dy = e.clientY - g.startY;
			if (Math.abs(dy) > 96 && Math.abs(dy) > Math.abs(dx) * 1.4 && dy > 0) {
				onCloseRef.current();
			} else if (
				count > 1 &&
				Math.abs(dx) > 56 &&
				Math.abs(dx) > Math.abs(dy) * 1.3
			) {
				goTo(safeIndex + (dx < 0 ? 1 : -1));
			}
			setSwipeOffset({ x: 0, y: 0 });
		}

		if (pointersRef.current.size === 0) {
			g.mode = "none";
			setInteracting(false);
		}
	};

	const onStageClick = (e: MouseEvent<HTMLDivElement>) => {
		if (suppressClickRef.current) {
			suppressClickRef.current = false;
			return;
		}
		if (e.target === stageRef.current) {
			onClose();
		} else if (e.target === imgRef.current) {
			setHudVisible((v) => !v);
		}
	};

	const onStageDoubleClick = (e: MouseEvent<HTMLDivElement>) => {
		if ((e.target as HTMLElement).closest("button, a")) return;
		const stage = stageRef.current;
		if (!stage) return;
		if (zoom > 1.05 || Math.abs(pan.x) > 4 || Math.abs(pan.y) > 4) {
			setZoom(1);
			setPan({ x: 0, y: 0 });
		} else {
			const rect = stage.getBoundingClientRect();
			const cx = e.clientX - (rect.left + rect.width / 2);
			const cy = e.clientY - (rect.top + rect.height / 2);
			const target =
				natural && fitted && fitted.w > 0
					? clamp(Math.max(2, natural.w / fitted.w), 1.5, 4)
					: 2;
			zoomTo(target, { cx, cy });
		}
	};

	const handleCopy = async () => {
		if (!current) return;
		try {
			await copyImageToClipboard(current.src);
			setCopied(true);
			showToast({ text: t("image.lightbox.copied") });
			window.setTimeout(() => setCopied(false), 1800);
		} catch {
			showToast({ text: t("image.lightbox.copyFailed"), tone: "err" });
		}
	};

	const handleDownload = () => {
		if (!current) return;
		downloadImage(current.src, current.filename || current.caption);
	};

	const effectivePercent = useMemo(() => {
		if (natural && fitted && fitted.w > 0) {
			return Math.round(((fitted.w * zoom) / natural.w) * 100);
		}
		return Math.round(zoom * 100);
	}, [natural, fitted, zoom]);

	const isTransformed =
		Math.abs(zoom - 1) > 0.01 ||
		Math.abs(pan.x) > 1 ||
		Math.abs(pan.y) > 1 ||
		rotation % 360 !== 0 ||
		flipH;

	if (!current || typeof document === "undefined") return null;

	const titleText =
		current.caption ||
		current.filename ||
		current.alt ||
		t("image.lightbox.title");
	const dismissOpacity =
		swipeOffset.y > 0 ? clamp(1 - swipeOffset.y / 280, 0.35, 1) : 1;

	const backdropClass =
		backdrop === "light"
			? "bg-[#f2f4f8]/96"
			: backdrop === "checker"
				? "lightbox-checker"
				: "bg-black/92 backdrop-blur-md";

	const backdropLabel =
		backdrop === "dark"
			? t("image.lightbox.backdropDark")
			: backdrop === "checker"
				? t("image.lightbox.backdropChecker")
				: t("image.lightbox.backdropLight");

	const showCopy = canCopyImage();

	return createPortal(
		<div
			ref={hostRef}
			className="theme-dark lightbox-in fixed inset-0 z-50 select-none"
		>
			<div
				ref={dialogRef}
				role="dialog"
				aria-modal="true"
				aria-label={titleText}
				tabIndex={-1}
				className={`relative flex h-full w-full flex-col overflow-hidden outline-none transition-colors duration-200 ${backdropClass}`}
				style={{ opacity: dismissOpacity }}
			>
				{/* 顶部浮层：计数器、文件名/尺寸、复制/下载/关闭 */}
				<header
					className={`pointer-events-none absolute inset-x-0 top-0 z-20 flex items-center justify-between gap-3 bg-gradient-to-b from-black/70 via-black/35 to-transparent px-3 pt-[max(0.75rem,env(safe-area-inset-top))] pb-6 transition-opacity duration-200 sm:px-5 ${
						hudVisible ? "opacity-100" : "opacity-0"
					}`}
				>
					<div className="pointer-events-auto flex min-w-0 items-center gap-2 rounded-xl border border-white/10 bg-[#12151c]/75 px-3 py-1.5 text-white shadow-lg backdrop-blur-md">
						{count > 1 && (
							<span className="shrink-0 rounded-md bg-white/12 px-1.5 py-0.5 font-mono font-medium text-white/90 text-xs tabular-nums">
								{t("image.lightbox.counter", {
									current: safeIndex + 1,
									total: count,
								})}
							</span>
						)}
						<span
							className="min-w-0 max-w-[42vw] truncate font-medium text-sm text-white/95 sm:max-w-[36rem]"
							title={titleText}
						>
							{titleText}
						</span>
						{natural && (
							<span className="hidden shrink-0 font-mono text-white/55 text-xs tabular-nums sm:inline">
								{natural.w} × {natural.h}
							</span>
						)}
					</div>

					<div className="pointer-events-auto flex shrink-0 items-center gap-1 rounded-xl border border-white/10 bg-[#12151c]/75 p-1 shadow-lg backdrop-blur-md">
						{showCopy && (
							<DockBtn
								icon={copied ? "check" : "copy"}
								label={
									copied ? t("image.lightbox.copied") : t("image.lightbox.copy")
								}
								active={copied}
								onClick={() => void handleCopy()}
							/>
						)}
						<DockBtn
							icon="download"
							label={t("image.lightbox.download")}
							onClick={handleDownload}
						/>
						<span aria-hidden className="mx-0.5 h-4 w-px bg-white/15" />
						<DockBtn icon="close" label={t("common.close")} onClick={onClose} />
					</div>
				</header>

				{/* 中央可交互画布舞台 */}
				{/* biome-ignore lint/a11y/noStaticElementInteractions: 画布手势舞台；键盘操作由模态层统一在 window keydown 处理 */}
				{/* biome-ignore lint/a11y/useKeyWithClickEvents: 键盘关闭 / 缩放 / 切图由模态层统一监听 */}
				<div
					ref={stageRef}
					data-testid="lightbox-stage"
					onPointerDown={onStagePointerDown}
					onPointerMove={onStagePointerMove}
					onPointerUp={finishPointer}
					onPointerCancel={finishPointer}
					onClick={onStageClick}
					onDoubleClick={onStageDoubleClick}
					className={`relative flex min-h-0 flex-1 touch-none items-center justify-center overflow-hidden p-4 sm:p-10 ${
						zoom > 1.01
							? interacting
								? "cursor-grabbing"
								: "cursor-grab"
							: "cursor-zoom-in"
					}`}
				>
					{loading && !failed && (
						<div className="pointer-events-none absolute inset-0 flex items-center justify-center">
							<span className="grid h-10 w-10 place-items-center rounded-full border border-white/12 bg-[#12151c]/80 text-white shadow-lg backdrop-blur-md">
								<Spinner size={18} />
							</span>
						</div>
					)}

					{failed ? (
						<div className="flex flex-col items-center gap-3 rounded-xl border border-white/12 bg-[#141821]/90 px-6 py-5 text-center text-white shadow-2xl backdrop-blur-md">
							<Icon name="alert" size={24} className="text-err" />
							<div className="font-medium text-sm">
								{t("image.lightbox.loadFailed")}
							</div>
							<button
								type="button"
								onClick={() => setRetryKey((k) => k + 1)}
								className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-white/15 bg-white/10 px-3 text-white text-xs transition-colors hover:bg-white/20 focus-visible:outline-2 focus-visible:outline-[var(--ring)]"
							>
								<Icon name="refresh" size={13} />
								{t("image.lightbox.retry")}
							</button>
						</div>
					) : (
						<img
							ref={imgRef}
							key={`${current.src}#${retryKey}`}
							src={current.src}
							alt={current.alt ?? ""}
							referrerPolicy={current.referrerPolicy}
							draggable={false}
							onLoad={(e) => {
								const el = e.currentTarget;
								setNatural({ w: el.naturalWidth, h: el.naturalHeight });
								setFitted({ w: el.clientWidth, h: el.clientHeight });
								setLoading(false);
								setFailed(false);
							}}
							onError={() => {
								setLoading(false);
								setFailed(true);
							}}
							className={`block max-w-[calc(100vw-2rem)] rounded-sm object-contain shadow-[0_20px_60px_rgba(0,0,0,0.55)] will-change-transform sm:max-w-[calc(100vw-6rem)] ${
								count > 1
									? "max-h-[calc(100dvh-13.5rem)]"
									: "max-h-[calc(100dvh-9.5rem)]"
							} ${
								interacting
									? "transition-none"
									: "transition-transform duration-180 ease-out"
							} ${loading ? "opacity-0" : "opacity-100"}`}
							style={{
								transform: `translate3d(${pan.x + swipeOffset.x}px, ${pan.y + swipeOffset.y}px, 0) rotate(${rotation}deg) scale(${flipH ? -zoom : zoom}, ${zoom})`,
							}}
						/>
					)}

					{/* 多图左右切换箭头 */}
					{count > 1 && (
						<>
							<button
								type="button"
								disabled={safeIndex === 0}
								aria-label={t("image.lightbox.prev")}
								title={t("image.lightbox.prev")}
								onClick={(e) => {
									e.stopPropagation();
									goTo(safeIndex - 1);
								}}
								className={`absolute top-1/2 left-3 z-20 grid h-10 w-10 -translate-y-1/2 place-items-center rounded-full border border-white/12 bg-[#12151c]/75 text-white shadow-lg backdrop-blur-md transition-[opacity,background-color] duration-150 hover:bg-white/20 focus-visible:outline-2 focus-visible:outline-[var(--ring)] disabled:pointer-events-none disabled:opacity-20 sm:left-5 ${
									hudVisible ? "opacity-100" : "pointer-events-none opacity-0"
								}`}
							>
								<Icon name="chevronLeft" size={20} />
							</button>
							<button
								type="button"
								disabled={safeIndex >= count - 1}
								aria-label={t("image.lightbox.next")}
								title={t("image.lightbox.next")}
								onClick={(e) => {
									e.stopPropagation();
									goTo(safeIndex + 1);
								}}
								className={`absolute top-1/2 right-3 z-20 grid h-10 w-10 -translate-y-1/2 place-items-center rounded-full border border-white/12 bg-[#12151c]/75 text-white shadow-lg backdrop-blur-md transition-[opacity,background-color] duration-150 hover:bg-white/20 focus-visible:outline-2 focus-visible:outline-[var(--ring)] disabled:pointer-events-none disabled:opacity-20 sm:right-5 ${
									hudVisible ? "opacity-100" : "pointer-events-none opacity-0"
								}`}
							>
								<Icon name="chevronRight" size={20} />
							</button>
						</>
					)}
				</div>

				{/* 底部悬浮工具坞 + 多图缩略图胶片条 */}
				<footer
					className={`pointer-events-none absolute inset-x-0 bottom-0 z-20 flex flex-col items-center gap-2.5 bg-gradient-to-t from-black/75 via-black/35 to-transparent px-3 pt-6 pb-[max(0.875rem,env(safe-area-inset-bottom))] transition-opacity duration-200 ${
						hudVisible ? "opacity-100" : "opacity-0"
					}`}
				>
					<div className="pointer-events-auto flex max-w-full flex-wrap items-center justify-center gap-0.5 rounded-2xl border border-white/12 bg-[#12151c]/82 p-1 shadow-2xl backdrop-blur-md">
						<DockBtn
							icon="zoomOut"
							label={t("image.lightbox.zoomOut")}
							disabled={zoom <= MIN_ZOOM + 0.01}
							onClick={() => stepZoom(-1)}
						/>
						<button
							type="button"
							title={t("image.lightbox.actual")}
							onClick={toggleActualSize}
							className="inline-flex h-8 min-w-14 items-center justify-center rounded-lg px-2 font-mono font-medium text-white/90 text-xs tabular-nums transition-colors duration-150 hover:bg-white/12 hover:text-white focus-visible:outline-2 focus-visible:outline-[var(--ring)]"
						>
							{`${effectivePercent}%`}
						</button>
						<DockBtn
							icon="zoomIn"
							label={t("image.lightbox.zoomIn")}
							disabled={zoom >= MAX_ZOOM - 0.01}
							onClick={() => stepZoom(1)}
						/>
						<span aria-hidden className="mx-1 h-4 w-px bg-white/15" />
						<DockBtn
							icon="rotateCcw"
							label={t("image.lightbox.rotateCcw")}
							onClick={() => setRotation((d) => d - 90)}
						/>
						<DockBtn
							icon="rotateCw"
							label={t("image.lightbox.rotateCw")}
							onClick={() => setRotation((d) => d + 90)}
						/>
						<DockBtn
							icon="flipH"
							label={t("image.lightbox.flipH")}
							active={flipH}
							onClick={() => setFlipH((v) => !v)}
						/>
						<span aria-hidden className="mx-1 h-4 w-px bg-white/15" />
						<DockBtn
							icon="checkerboard"
							label={`${t("image.lightbox.backdrop")} · ${backdropLabel}`}
							active={backdrop !== "dark"}
							onClick={() => setBackdrop(nextBackdrop)}
						/>
						<DockBtn
							icon="frameCorners"
							label={t("image.lightbox.reset")}
							disabled={!isTransformed}
							onClick={resetTransform}
						/>
					</div>

					{count > 1 && (
						<div
							role="listbox"
							aria-label={t("image.lightbox.thumbnails")}
							className="pointer-events-auto flex max-w-full items-center gap-1.5 overflow-x-auto rounded-xl border border-white/10 bg-[#12151c]/75 p-1.5 shadow-lg backdrop-blur-md"
						>
							{images.map((img, i) => {
								const active = i === safeIndex;
								return (
									<button
										// biome-ignore lint/suspicious/noArrayIndexKey: 同一张图可能在画廊重复出现，下标作去重后缀
										key={`${img.src}-${i}`}
										ref={(el) => {
											if (el) thumbRefs.current.set(i, el);
											else thumbRefs.current.delete(i);
										}}
										type="button"
										role="option"
										aria-selected={active}
										aria-label={
											img.caption || img.alt || t("image.alt", { n: i + 1 })
										}
										onClick={() => goTo(i)}
										className={`relative h-11 w-11 shrink-0 overflow-hidden rounded-lg transition-[opacity,transform] duration-150 focus-visible:outline-2 focus-visible:outline-[var(--ring)] ${
											active
												? "scale-105 opacity-100 ring-2 ring-white"
												: "opacity-55 hover:opacity-90"
										}`}
									>
										<img
											src={img.src}
											alt=""
											referrerPolicy={img.referrerPolicy}
											className="h-full w-full object-cover"
											loading="lazy"
										/>
									</button>
								);
							})}
						</div>
					)}
				</footer>
			</div>
		</div>,
		document.body,
	);
}
