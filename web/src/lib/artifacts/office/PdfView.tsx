// PDF：pdf.js（legacy 构建，自带垫片，兼容 WebKit）在 worker 里解析，主线程逐页画到 canvas。内容不可信：不渲染注释层与
// 表单层（链接、表单控件、内嵌脚本都不生效，注释外观仍画在 canvas 上），不启用 XFA；只叠一层透明文本层供选中复制。
// worker、CMap、标准字体、图像解码 wasm、ICC 配置文件都取自 OfficeAssets.pdfjs，不访问外部网络。
// 普通视图按需渲染：只有进入视口附近（上下各一屏）的页才取页对象、画 canvas、建文本层，离开后释放画布。

import type {
	PDFDocumentLoadingTask,
	PDFDocumentProxy,
	PDFPageProxy,
	RenderTask,
} from "pdfjs-dist";
import {
	GlobalWorkerOptions,
	getDocument,
	PixelsPerInch,
	TextLayer,
	VerbosityLevel,
} from "pdfjs-dist/legacy/build/pdf.mjs";
import {
	type CSSProperties,
	type KeyboardEvent,
	memo,
	type ReactNode,
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
	pointAnchor,
	scrollToPage,
	textAtPoint,
} from "./anchor";
import { PagedView, type PageSource, releaseCanvases } from "./paged";
import { PinLayer } from "./pins";
import { OfficeState, ReadyMark, useLoaded } from "./shared";
import type {
	AnchorPin,
	OfficeStrings,
	OfficeViewProps,
	ViewerAnchor,
} from "./types";
import "./office.css";

/** 缩放档位（相对 PDF 实际尺寸，1 = 100%）；放大 / 缩小在相邻档位间跳。 */
const ZOOM_STEPS = [
	0.25, 0.33, 0.5, 0.67, 0.75, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2, 2.5, 3, 4, 5,
];
const MIN_ZOOM = 0.1;
const MAX_ZOOM = 8;
/** 页列四周留白（px）；适应宽度时扣除。 */
const PAGE_PAD = 16;
/** 单页画布像素上限：超出时降低输出倍率，避免高缩放 × HiDPI 撑爆显存。 */
const MAX_CANVAS_PIXELS = 2 ** 24;
/** 缩放停止多久后才按新倍率重绘画布；期间旧画布按 CSS 拉伸占位。 */
const RERENDER_DELAY_MS = 150;

/** 页面在缩放 1（视口单位，含 userUnit 与页面自带旋转）下的宽高。 */
interface PageSize {
	w: number;
	h: number;
}

interface OpenedPdf {
	task: PDFDocumentLoadingTask;
	doc: PDFDocumentProxy;
	/** 第一页的尺寸：未取到的页先按它占位。 */
	base: PageSize;
}

export function PdfView(props: OfficeViewProps): ReactNode {
	const base = props.assets.pdfjs;
	const state = useLoaded<OpenedPdf>(
		props.bytes,
		async (bytes) => {
			if (bytes.byteLength === 0) throw new Error("empty");
			// worker 按自身脚本地址解析相对路径，这里给的都是绝对地址；沙箱帧里由 worker 垫片转成 blob 引导
			GlobalWorkerOptions.workerSrc = `${base}pdf.worker.min.mjs`;
			const task = getDocument({
				// pdf.js 会把数据转移给 worker：传副本
				data: new Uint8Array(bytes.slice(0)),
				cMapUrl: `${base}cmaps/`,
				cMapPacked: true,
				standardFontDataUrl: `${base}standard_fonts/`,
				wasmUrl: `${base}wasm/`,
				iccUrl: `${base}iccs/`,
				enableXfa: false,
				verbosity: VerbosityLevel.ERRORS,
			});
			try {
				const doc = await task.promise;
				if (doc.numPages === 0) throw new Error("empty");
				const first = (await doc.getPage(1)).getViewport({ scale: 1 });
				return { task, doc, base: { w: first.width, h: first.height } };
			} catch (e) {
				void task.destroy();
				throw e;
			}
		},
		// 销毁加载任务会一并销毁文档与 worker，未完成的渲染随之失效
		(pdf) => void pdf.task.destroy(),
	);
	if (state.kind !== "ready")
		return <OfficeState state={state} strings={props.strings} />;
	const { doc, base: size } = state.value;
	if (props.view === "full")
		return (
			<PdfScroll
				doc={doc}
				base={size}
				strings={props.strings}
				onReady={props.onReady}
				onPick={props.pickable ? props.onPick : undefined}
				pins={props.pins ?? NO_PINS}
				locate={props.locate ?? null}
			/>
		);
	return (
		<PdfPaged
			doc={doc}
			base={size}
			view={props.view}
			strings={props.strings}
			onReady={props.onReady}
		/>
	);
}

/** 缩略图与打印视图：按 PageSource 交给 paged.tsx；打印先取齐全部页的尺寸。 */
function PdfPaged({
	doc,
	base,
	view,
	strings,
	onReady,
}: {
	doc: PDFDocumentProxy;
	base: PageSize;
	view: "thumbnail" | "print";
	strings: OfficeStrings;
	onReady?: (pages: number) => void;
}): ReactNode {
	const [pages, setPages] = useState<PDFPageProxy[] | null>(null);
	const [error, setError] = useState<string | null>(null);
	const count = view === "thumbnail" ? 1 : doc.numPages;
	useEffect(() => {
		let alive = true;
		Promise.all(
			Array.from({ length: count }, (_, i) => doc.getPage(i + 1)),
		).then(
			(ps) => alive && setPages(ps),
			(e: unknown) => alive && setError(String(e)),
		);
		return () => {
			alive = false;
		};
	}, [doc, count]);
	const source = useMemo<PageSource | null>(() => {
		if (!pages) return null;
		const unit = PixelsPerInch.PDF_TO_CSS_UNITS;
		return {
			count: view === "thumbnail" ? doc.numPages : pages.length,
			size: (i) => {
				const p = pages[i];
				if (!p) return { w: base.w * unit, h: base.h * unit };
				const vp = p.getViewport({ scale: 1 });
				return { w: vp.width * unit, h: vp.height * unit };
			},
			render: async (canvas, i, width, dpr) => {
				const p = pages[i];
				if (!p) throw new Error(`page ${i + 1}`);
				const viewport = p.getViewport({
					scale: width / p.getViewport({ scale: 1 }).width,
				});
				canvas.width = Math.max(1, Math.floor(viewport.width * dpr));
				canvas.height = Math.max(1, Math.floor(viewport.height * dpr));
				await p.render({
					canvas,
					viewport,
					transform: dpr === 1 ? undefined : [dpr, 0, 0, dpr, 0, 0],
				}).promise;
			},
		};
	}, [pages, doc, base, view]);
	if (error)
		return (
			<OfficeState state={{ kind: "error", reason: error }} strings={strings} />
		);
	if (!source)
		return <OfficeState state={{ kind: "loading" }} strings={strings} />;
	return (
		<div className="oryn-o oryn-o-pdf">
			<PagedView
				source={source}
				view={view}
				strings={strings}
				label={(i) => strings.pageLabel(i + 1)}
				onReady={onReady}
			/>
		</div>
	);
}

function PdfScroll({
	doc,
	base,
	strings,
	onReady,
	onPick,
	pins,
	locate,
}: {
	doc: PDFDocumentProxy;
	base: PageSize;
	strings: OfficeStrings;
	onReady?: (pages: number) => void;
	/** 评论模式下的点选；为空时不响应。 */
	onPick?: (anchor: ViewerAnchor, at: Point) => void;
	pins: readonly AnchorPin[];
	locate: ViewerAnchor | null;
}): ReactNode {
	const scrollRef = useRef<HTMLDivElement>(null);
	const pageEls = useRef<(HTMLDivElement | null)[]>([]);
	const observerRef = useRef<IntersectionObserver | null>(null);
	/** 滚动位置占内容总高的比例；缩放后据此恢复，阅读位置不跳。 */
	const scrollFrac = useRef(0);
	const rafRef = useRef(0);
	const [width, setWidth] = useState(0);
	const [zoom, setZoom] = useState<number | "fit">("fit");
	const [near, setNear] = useState<ReadonlySet<number> | "all">(() =>
		typeof IntersectionObserver === "undefined" ? "all" : new Set(),
	);
	const [current, setCurrent] = useState(1);
	const [pageInput, setPageInput] = useState<string | null>(null);
	const [ready, setReady] = useState(false);
	const numPages = doc.numPages;

	// 适应宽度按第一页宽度计算；容器宽度未知（0）时先不排版
	const fitZoom =
		width > 0
			? (width - 2 * PAGE_PAD - 1) / (base.w * PixelsPerInch.PDF_TO_CSS_UNITS)
			: 0;
	const cssZoom = Math.min(
		MAX_ZOOM,
		Math.max(MIN_ZOOM, zoom === "fit" ? fitZoom : zoom),
	);
	// 传给 getViewport 的倍率：100% 对应 PDF 的实际物理尺寸（72pt → 96px）
	const scale = width > 0 ? cssZoom * PixelsPerInch.PDF_TO_CSS_UNITS : 0;

	// 画布重绘倍率：首次立即生效，之后等缩放 / 尺寸变化停下来再重绘
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

	useLayoutEffect(() => {
		const el = scrollRef.current;
		if (!el) return;
		setWidth(el.clientWidth);
		if (typeof ResizeObserver === "undefined") return;
		const ro = new ResizeObserver(() => setWidth(el.clientWidth));
		ro.observe(el);
		return () => ro.disconnect();
	}, []);

	// 缩放改变页高后按比例恢复滚动位置
	useLayoutEffect(() => {
		const el = scrollRef.current;
		if (el && scale > 0) el.scrollTop = scrollFrac.current * el.scrollHeight;
	}, [scale]);

	// 一个观察器盯住所有页：进入视口上下各一屏范围内的页才渲染
	useEffect(() => {
		const root = scrollRef.current;
		if (!root || typeof IntersectionObserver === "undefined") return;
		const io = new IntersectionObserver(
			(entries) => {
				setNear((prev) => {
					const next = new Set(prev === "all" ? [] : prev);
					for (const e of entries) {
						const n = Number((e.target as HTMLElement).dataset.page);
						if (e.isIntersecting) next.add(n);
						else next.delete(n);
					}
					return next;
				});
			},
			{ root, rootMargin: "100% 0px" },
		);
		for (const el of pageEls.current) if (el) io.observe(el);
		observerRef.current = io;
		return () => {
			io.disconnect();
			observerRef.current = null;
		};
	}, []);

	const registerPage = useCallback((n: number, el: HTMLDivElement | null) => {
		pageEls.current[n - 1] = el;
		if (el) observerRef.current?.observe(el);
	}, []);

	const onSettled = useCallback((n: number) => {
		if (n === 1) setReady(true);
	}, []);

	useEffect(() => () => cancelAnimationFrame(rafRef.current), []);

	const onScroll = () => {
		cancelAnimationFrame(rafRef.current);
		rafRef.current = requestAnimationFrame(() => {
			const el = scrollRef.current;
			if (!el) return;
			scrollFrac.current =
				el.scrollHeight > 0 ? el.scrollTop / el.scrollHeight : 0;
			// 当前页：顶边位于视口 40% 高度之上的最后一页（二分查找，页按顺序排列）
			const mark = el.scrollTop + el.clientHeight * 0.4;
			const els = pageEls.current;
			let lo = 0;
			let hi = numPages - 1;
			while (lo < hi) {
				const mid = (lo + hi + 1) >> 1;
				const top = els[mid]?.offsetTop ?? Number.POSITIVE_INFINITY;
				if (top <= mark) lo = mid;
				else hi = mid - 1;
			}
			setCurrent(lo + 1);
		});
	};

	const goToPage = (n: number) => {
		const el = scrollRef.current;
		const page = pageEls.current[n - 1];
		if (el && page) scrollToPage(el, page, null, PAGE_PAD);
	};

	useEffect(() => {
		const n = locate?.page?.number;
		const el = scrollRef.current;
		const page = n === undefined ? null : pageEls.current[n - 1];
		if (el && page) scrollToPage(el, page, locate?.y ?? null, PAGE_PAD);
	}, [locate]);

	const byPage = useMemo(() => pinsByPage(pins), [pins]);
	// 页组件是 memo 的：点选回调保持同一个引用，最新的 onPick 经 ref 取
	const pickRef = useRef(onPick);
	pickRef.current = onPick;
	const pickPage = useCallback((n: number, point: Point, at: Point) => {
		// 文字取点中的文本层片段（pdf.js 按原文位置摆放的透明文字）
		const text = textAtPoint(
			at,
			(el) =>
				!el.classList.contains("textLayer") &&
				el.closest(`.oryn-o-page[data-page="${n}"] .textLayer`) !== null,
		);
		pickRef.current?.(pointAnchor({ number: n, name: "" }, point, text), at);
	}, []);

	const commitPageInput = () => {
		const n = Number.parseInt(pageInput ?? "", 10);
		if (Number.isFinite(n)) goToPage(Math.min(numPages, Math.max(1, n)));
		setPageInput(null);
	};

	const onPageKey = (e: KeyboardEvent<HTMLInputElement>) => {
		if (e.key === "Enter") commitPageInput();
		else if (e.key === "Escape") setPageInput(null);
	};

	const pages: ReactNode[] = [];
	if (scale > 0 && renderScale > 0) {
		for (let n = 1; n <= numPages; n++) {
			pages.push(
				<PdfPage
					key={n}
					doc={doc}
					pageNumber={n}
					base={base}
					scale={scale}
					renderScale={renderScale}
					near={near === "all" || near.has(n)}
					register={registerPage}
					onSettled={onSettled}
					strings={strings}
					pins={byPage.get(n) ?? NO_PINS}
					onPick={onPick ? pickPage : undefined}
				/>,
			);
		}
	}

	return (
		<div className="oryn-o oryn-o-pdf oryn-o-paged">
			<div className="oryn-o-toolbar">
				<input
					aria-label={strings.pageNumber}
					inputMode="numeric"
					className="oryn-o-page-input"
					value={pageInput ?? String(current)}
					onFocus={(e) => {
						setPageInput(String(current));
						e.currentTarget.select();
					}}
					onChange={(e) => setPageInput(e.target.value.replace(/\D/g, ""))}
					onKeyDown={onPageKey}
					onBlur={commitPageInput}
				/>
				<span className="oryn-o-muted">/ {numPages}</span>
				<span className="oryn-o-grow" />
				<button
					type="button"
					className="oryn-o-icon-btn"
					aria-label={strings.zoomOut}
					title={strings.zoomOut}
					disabled={cssZoom <= MIN_ZOOM}
					onClick={() =>
						setZoom(ZOOM_STEPS.findLast((s) => s < cssZoom * 0.999) ?? MIN_ZOOM)
					}
				>
					−
				</button>
				<span className="oryn-o-zoom">{Math.round(cssZoom * 100)}%</span>
				<button
					type="button"
					className="oryn-o-icon-btn"
					aria-label={strings.zoomIn}
					title={strings.zoomIn}
					disabled={cssZoom >= MAX_ZOOM}
					onClick={() =>
						setZoom(ZOOM_STEPS.find((s) => s > cssZoom * 1.001) ?? MAX_ZOOM)
					}
				>
					+
				</button>
				<button
					type="button"
					className="oryn-o-icon-btn"
					aria-label={strings.fitWidth}
					title={strings.fitWidth}
					aria-pressed={zoom === "fit"}
					onClick={() => setZoom("fit")}
				>
					↔
				</button>
			</div>
			<div ref={scrollRef} onScroll={onScroll} className="oryn-o-scroll">
				<div className="oryn-o-pages">{pages}</div>
			</div>
			{ready ? (
				<ReadyMark pages={numPages} strings={strings} onReady={onReady} />
			) : null}
		</div>
	);
}

interface PdfPageProps {
	doc: PDFDocumentProxy;
	pageNumber: number;
	/** 页对象未取到前用第一页尺寸占位。 */
	base: PageSize;
	/** 版面倍率：决定页框尺寸与文本层。 */
	scale: number;
	/** 画布绘制倍率：缩放停下后才追上 `scale`。 */
	renderScale: number;
	near: boolean;
	register: (n: number, el: HTMLDivElement | null) => void;
	/** 这一页第一次画完（或失败）时回调。 */
	onSettled: (n: number) => void;
	strings: OfficeStrings;
	pins: readonly AnchorPin[];
	/** 评论模式下点了这一页：页号（从 1 起）、页内归一化坐标、视口坐标。 */
	onPick?: (n: number, point: Point, at: Point) => void;
}

const PdfPage = memo(function PdfPage({
	doc,
	pageNumber,
	base,
	scale,
	renderScale,
	near,
	register,
	onSettled,
	strings,
	pins,
	onPick,
}: PdfPageProps): ReactNode {
	const boxRef = useRef<HTMLDivElement>(null);
	const canvasHostRef = useRef<HTMLDivElement>(null);
	const textLayerRef = useRef<TextLayer | null>(null);
	const scaleRef = useRef(scale);
	scaleRef.current = scale;
	const [page, setPage] = useState<PDFPageProxy | null>(null);
	const [failed, setFailed] = useState(false);

	const setBox = useCallback(
		(el: HTMLDivElement | null) => {
			boxRef.current = el;
			register(pageNumber, el);
		},
		[register, pageNumber],
	);

	// 第一次接近视口时取页对象；之后一直保留（文档销毁时统一释放）
	useEffect(() => {
		if (!near || page) return;
		let alive = true;
		doc.getPage(pageNumber).then(
			(p) => alive && setPage(p),
			() => {
				if (!alive) return;
				setFailed(true);
				onSettled(pageNumber);
			},
		);
		return () => {
			alive = false;
		};
	}, [doc, pageNumber, near, page, onSettled]);

	const size = useMemo<PageSize>(() => {
		if (!page) return base;
		const vp = page.getViewport({ scale: 1 });
		return { w: vp.width, h: vp.height };
	}, [page, base]);

	// 画布：每次绘制用新 canvas，画完再替换旧的，缩放时不闪白；离开视口范围即释放
	useEffect(() => {
		const host = canvasHostRef.current;
		if (!host) return;
		if (!page || !near) {
			releaseCanvases(host);
			return;
		}
		const viewport = page.getViewport({ scale: renderScale });
		const ratio = Math.min(
			window.devicePixelRatio || 1,
			Math.sqrt(MAX_CANVAS_PIXELS / (viewport.width * viewport.height)),
		);
		const canvas = document.createElement("canvas");
		canvas.width = Math.max(1, Math.floor(viewport.width * ratio));
		canvas.height = Math.max(1, Math.floor(viewport.height * ratio));
		const sx = canvas.width / viewport.width;
		const sy = canvas.height / viewport.height;
		let task: RenderTask | null = page.render({
			canvas,
			viewport,
			transform: sx === 1 && sy === 1 ? undefined : [sx, 0, 0, sy, 0, 0],
		});
		task.promise.then(
			() => {
				task = null;
				releaseCanvases(host);
				host.append(canvas);
				setFailed(false);
				onSettled(pageNumber);
			},
			(e: unknown) => {
				task = null;
				canvas.width = 0;
				canvas.height = 0;
				if (!(e instanceof Error && e.name === "RenderingCancelledException")) {
					setFailed(true);
					onSettled(pageNumber);
				}
			},
		);
		return () => {
			task?.cancel();
		};
	}, [page, near, renderScale, onSettled, pageNumber]);

	// 文本层：随页一起建立；缩放只更新 CSS 变量与 TextLayer 内部倍率，不重建
	useEffect(() => {
		const box = boxRef.current;
		if (!box || !page || !near) return;
		const div = document.createElement("div");
		div.className = "textLayer";
		box.append(div);
		const layer = new TextLayer({
			textContentSource: page.streamTextContent({
				includeMarkedContent: true,
				disableNormalization: true,
			}),
			container: div,
			viewport: page.getViewport({ scale: scaleRef.current }),
		});
		textLayerRef.current = layer;
		// 取消或文本提取失败只影响选中复制，不打断画布显示
		layer.render().catch(() => {});
		return () => {
			layer.cancel();
			div.remove();
			textLayerRef.current = null;
		};
	}, [page, near]);

	useEffect(() => {
		if (page)
			textLayerRef.current?.update({ viewport: page.getViewport({ scale }) });
	}, [page, scale]);

	const style = {
		width: size.w * scale,
		height: size.h * scale,
		"--total-scale-factor": scale * (page?.userUnit ?? 1),
		"--scale-round-x": "1px",
		"--scale-round-y": "1px",
	} as CSSProperties;

	return (
		<div
			ref={setBox}
			data-page={pageNumber}
			className="oryn-o-page"
			style={style}
		>
			{/* 只有画布层是 img；文本层、点选按钮与图钉都是它的兄弟元素，读屏器能读到 */}
			<div
				ref={canvasHostRef}
				className="oryn-o-canvas"
				role="img"
				aria-label={strings.pageLabel(pageNumber)}
			/>
			{failed ? (
				<div className="oryn-o-page-error">
					{strings.pageFailed(pageNumber)}
				</div>
			) : null}
			{onPick ? (
				<button
					type="button"
					className="oryn-pick"
					aria-label={
						strings.commentPage?.(pageNumber) ?? strings.pageLabel(pageNumber)
					}
					onClick={(e) => {
						const { point, at } = pickPoint(e);
						onPick(pageNumber, point, at);
					}}
				/>
			) : null}
			<PinLayer pins={pins} />
		</div>
	);
});
