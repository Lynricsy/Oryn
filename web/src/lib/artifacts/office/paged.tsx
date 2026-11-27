// 分页 canvas 列表：Word 的页与 PowerPoint 的幻灯片共用。普通视图纵向滚动、按宽度适配，只画视口上下各一屏内的页、
// 离开后释放画布；缩略图只画第一页并铺满宽度；打印视图按原始尺寸逐页画完全部页，每页一个分页块。

import {
	type ReactNode,
	type RefObject,
	useCallback,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import {
	NO_PINS,
	type Point,
	pickPoint,
	pinsByPage,
	scrollToPage,
} from "./anchor";
import { MAX_PRINT_PAGES } from "./limits";
import { PinLayer } from "./pins";
import { PrintPageStyle, ReadyMark } from "./shared";
import type {
	AnchorPin,
	OfficeStrings,
	OfficeView,
	ViewerAnchor,
} from "./types";

/** 一份可以逐页画到 canvas 上的文档。 */
export interface PageSource {
	count: number;
	/** 第 i 页在 100% 下的 CSS 尺寸（px）。 */
	size(i: number): { w: number; h: number };
	/** 把第 i 页按 CSS 宽度 width、像素比 dpr 画到 canvas 上。 */
	render(
		canvas: HTMLCanvasElement,
		i: number,
		width: number,
		dpr: number,
	): Promise<void>;
}

/** 页列四周与页间的留白（px）。 */
const PAD = 16;
/** 适配宽度时最多放大到 2 倍。 */
const MAX_FIT = 2;
/** 宽度变化停下多久后才按新倍率重画；期间旧画布按 CSS 拉伸占位。 */
const RERENDER_DELAY_MS = 150;
/** 单页画布像素上限：超出时降低像素比。 */
const MAX_CANVAS_PIXELS = 2 ** 24;

/** 移除并清空宿主里的画布；尺寸置 0 让浏览器立即回收位图内存。 */
export function releaseCanvases(host: HTMLElement): void {
	for (const c of host.querySelectorAll("canvas")) {
		c.width = 0;
		c.height = 0;
	}
	host.replaceChildren();
}

/** 库按 width × dpr 设画布像素尺寸并可能写内联的 CSS 尺寸：清掉，让样式表把画布铺满页框。 */
export function fillBox(canvas: HTMLCanvasElement): HTMLCanvasElement {
	canvas.style.removeProperty("width");
	canvas.style.removeProperty("height");
	return canvas;
}

/** 用新 canvas 画完再替换旧的（缩放时不闪白）；cancelled 为真时丢弃结果。 */
async function drawPage(
	source: PageSource,
	i: number,
	width: number,
	height: number,
	host: HTMLElement,
	cancelled: () => boolean,
): Promise<boolean> {
	const dpr = Math.min(
		window.devicePixelRatio || 1,
		Math.sqrt(MAX_CANVAS_PIXELS / Math.max(1, width * height)),
	);
	const canvas = document.createElement("canvas");
	await source.render(canvas, i, width, dpr);
	if (cancelled()) {
		canvas.width = 0;
		canvas.height = 0;
		return false;
	}
	releaseCanvases(host);
	host.append(fillBox(canvas));
	return true;
}

interface PageBoxProps {
	source: PageSource;
	index: number;
	/** 页框的版面倍率。 */
	scale: number;
	/** 画布绘制倍率（0 = 先不画）。 */
	renderScale: number;
	near: boolean;
	label: string;
	strings: OfficeStrings;
	className?: string;
	register?: (i: number, el: HTMLDivElement | null) => void;
	/** 这一页画完（或失败）时回调。 */
	onSettled?: (i: number) => void;
	/** 这一页上的评论图钉。 */
	pins?: readonly AnchorPin[];
	/** 评论模式下点了这一页：页序号（从 0 起）、页内归一化坐标、视口坐标。 */
	onPick?: (i: number, point: Point, at: Point) => void;
}

function PageBox({
	source,
	index,
	scale,
	renderScale,
	near,
	label,
	strings,
	className = "oryn-o-page",
	register,
	onSettled,
	pins = NO_PINS,
	onPick,
}: PageBoxProps): ReactNode {
	const host = useRef<HTMLDivElement>(null);
	const [failed, setFailed] = useState(false);
	const settled = useRef(onSettled);
	settled.current = onSettled;
	const { w, h } = source.size(index);

	const setBox = useCallback(
		(el: HTMLDivElement | null) => register?.(index, el),
		[register, index],
	);

	useEffect(() => {
		const el = host.current;
		if (!el) return;
		if (!near || renderScale <= 0) {
			releaseCanvases(el);
			return;
		}
		let cancelled = false;
		drawPage(
			source,
			index,
			w * renderScale,
			h * renderScale,
			el,
			() => cancelled,
		).then(
			(ok) => {
				if (!ok) return;
				setFailed(false);
				settled.current?.(index);
			},
			() => {
				if (cancelled) return;
				setFailed(true);
				settled.current?.(index);
			},
		);
		return () => {
			cancelled = true;
		};
	}, [source, index, near, renderScale, w, h]);

	return (
		<div
			ref={setBox}
			data-page={index + 1}
			className={className}
			role="img"
			aria-label={label}
			style={{ width: w * scale, height: h * scale }}
		>
			<div ref={host} className="oryn-o-canvas" />
			{failed ? (
				<div className="oryn-o-page-error">{strings.pageFailed(index + 1)}</div>
			) : null}
			{onPick ? (
				<button
					type="button"
					className="oryn-pick"
					aria-label={label}
					onClick={(e) => {
						const { point, at } = pickPoint(e);
						onPick(index, point, at);
					}}
				/>
			) : null}
			<PinLayer pins={pins} />
		</div>
	);
}

/** 普通视图里的评论：点选、图钉与定位（缩略图与打印视图不用）。 */
export interface PageComments {
	/** 评论模式下点了第 i 页（从 0 起）；为空时不响应点选。 */
	onPick: ((i: number, point: Point, at: Point) => void) | null;
	pins: readonly AnchorPin[];
	/** 要滚动定位到的锚点（按 `page.number` 与 `y`）。 */
	locate: ViewerAnchor | null;
}

interface PagedViewProps {
	source: PageSource;
	view: OfficeView;
	strings: OfficeStrings;
	/** 第 i 页（从 0 起）的无障碍名称。 */
	label(i: number): string;
	onReady?(pages: number): void;
	/** 普通视图里页列上方的工具条。 */
	header?: ReactNode;
	comments?: PageComments;
}

export function PagedView(props: PagedViewProps): ReactNode {
	if (props.view === "thumbnail") return <ThumbnailPage {...props} />;
	if (props.view === "print") return <PrintPages {...props} />;
	return <ScrollPages {...props} />;
}

/** 元素内容区宽度，随尺寸变化更新（没有 ResizeObserver 的环境只量一次）。 */
function useWidth(ref: RefObject<HTMLElement | null>): number {
	const [width, setWidth] = useState(0);
	useLayoutEffect(() => {
		const el = ref.current;
		if (!el) return;
		setWidth(el.clientWidth);
		if (typeof ResizeObserver === "undefined") return;
		const ro = new ResizeObserver(() => setWidth(el.clientWidth));
		ro.observe(el);
		return () => ro.disconnect();
	}, [ref]);
	return width;
}

function ScrollPages({
	source,
	strings,
	label,
	onReady,
	header,
	comments,
}: PagedViewProps): ReactNode {
	const scrollRef = useRef<HTMLDivElement>(null);
	const pageEls = useRef<(HTMLDivElement | null)[]>([]);
	const observer = useRef<IntersectionObserver | null>(null);
	const width = useWidth(scrollRef);
	const [near, setNear] = useState<ReadonlySet<number> | "all">(() =>
		typeof IntersectionObserver === "undefined" ? "all" : new Set(),
	);
	const [ready, setReady] = useState(false);

	let maxW = 1;
	for (let i = 0; i < source.count; i++)
		maxW = Math.max(maxW, source.size(i).w);
	const scale =
		width > 0 ? Math.min(MAX_FIT, Math.max(0.1, (width - 2 * PAD) / maxW)) : 0;

	// 画布倍率：首次立即生效，之后等宽度变化停下来再重画
	const [renderScale, setRenderScale] = useState(0);
	useEffect(() => {
		if (scale === renderScale) return;
		if (renderScale === 0) {
			setRenderScale(scale);
			return;
		}
		const id = window.setTimeout(
			() => setRenderScale(scale),
			RERENDER_DELAY_MS,
		);
		return () => window.clearTimeout(id);
	}, [scale, renderScale]);

	useEffect(() => {
		const root = scrollRef.current;
		if (!root || typeof IntersectionObserver === "undefined") return;
		const io = new IntersectionObserver(
			(entries) => {
				setNear((prev) => {
					const next = new Set(prev === "all" ? [] : prev);
					for (const e of entries) {
						const n = Number((e.target as HTMLElement).dataset.page) - 1;
						if (e.isIntersecting) next.add(n);
						else next.delete(n);
					}
					return next;
				});
			},
			{ root, rootMargin: "100% 0px" },
		);
		for (const el of pageEls.current) if (el) io.observe(el);
		observer.current = io;
		return () => {
			io.disconnect();
			observer.current = null;
		};
	}, []);

	const register = useCallback((i: number, el: HTMLDivElement | null) => {
		const old = pageEls.current[i];
		if (old && old !== el) observer.current?.unobserve(old);
		pageEls.current[i] = el;
		if (el) observer.current?.observe(el);
	}, []);

	const onSettled = useCallback((i: number) => {
		if (i === 0) setReady(true);
	}, []);

	const pins = comments?.pins ?? NO_PINS;
	const byPage = useMemo(() => pinsByPage(pins), [pins]);
	const onPick = comments?.onPick ?? undefined;

	const locate = comments?.locate ?? null;
	useEffect(() => {
		const n = locate?.page?.number;
		const el = scrollRef.current;
		const page = n === undefined ? null : pageEls.current[n - 1];
		if (el && page) scrollToPage(el, page, locate?.y ?? null, PAD);
	}, [locate]);

	const pages: ReactNode[] = [];
	if (scale > 0)
		for (let i = 0; i < source.count; i++)
			pages.push(
				<PageBox
					key={i}
					source={source}
					index={i}
					scale={scale}
					renderScale={renderScale}
					near={near === "all" || near.has(i)}
					label={label(i)}
					strings={strings}
					register={register}
					onSettled={onSettled}
					pins={byPage.get(i + 1)}
					onPick={onPick}
				/>,
			);

	return (
		<div className="oryn-o-paged">
			{header}
			<div ref={scrollRef} className="oryn-o-scroll">
				<div className="oryn-o-pages">{pages}</div>
			</div>
			{ready ? (
				<ReadyMark pages={source.count} strings={strings} onReady={onReady} />
			) : null}
		</div>
	);
}

function ThumbnailPage({
	source,
	strings,
	label,
	onReady,
}: PagedViewProps): ReactNode {
	const ref = useRef<HTMLDivElement>(null);
	const width = useWidth(ref);
	const [ready, setReady] = useState(false);
	const first = source.count > 0 ? source.size(0) : null;
	const scale = first && width > 0 ? width / first.w : 0;
	const onSettled = useCallback(() => setReady(true), []);
	return (
		<div ref={ref} className="oryn-o-thumbnail">
			{scale > 0 ? (
				<PageBox
					source={source}
					index={0}
					scale={scale}
					renderScale={scale}
					near
					label={label(0)}
					strings={strings}
					className="oryn-o-page oryn-o-page-flat"
					onSettled={onSettled}
				/>
			) : null}
			{ready ? (
				<ReadyMark pages={source.count} strings={strings} onReady={onReady} />
			) : null}
		</div>
	);
}

function PrintPages({
	source: raw,
	strings,
	label,
	onReady,
}: PagedViewProps): ReactNode {
	// 逐页画：画完第 i 页才开始第 i+1 页，避免同时解码全部页
	const [done, setDone] = useState(0);
	const onSettled = useCallback(
		(i: number) => setDone((d) => Math.max(d, i + 1)),
		[],
	);
	// 页框取整到整像素、与 @page 一致：差零点几像素也会溢出到下一张纸，多出空白页
	const source = useMemo<PageSource>(
		() => ({
			...raw,
			size: (i) => {
				const s = raw.size(i);
				return { w: Math.floor(s.w), h: Math.floor(s.h) };
			},
		}),
		[raw],
	);
	if (source.count > MAX_PRINT_PAGES)
		return <div className="oryn-o-state">{strings.tooManyPages}</div>;
	const first = source.count > 0 ? source.size(0) : { w: 1, h: 1 };
	const pages: ReactNode[] = [];
	for (let i = 0; i < source.count; i++)
		pages.push(
			<PageBox
				key={i}
				source={source}
				index={i}
				scale={1}
				renderScale={1}
				near={i <= done}
				label={label(i)}
				strings={strings}
				className="oryn-o-page oryn-o-page-flat oryn-o-print-page"
				onSettled={onSettled}
			/>,
		);
	return (
		<div className="oryn-o-print">
			<PrintPageStyle w={first.w} h={first.h} />
			{done >= source.count ? (
				<ReadyMark pages={source.count} strings={strings} onReady={onReady} />
			) : null}
			{pages}
		</div>
	);
}
