/**
 * PDF：pdf.js（legacy 构建，自带 core-js 垫片，兼容桌面壳的 WebKit）在 worker 里解析，主线程逐页画到 canvas。
 * 内容来自沙箱、不可信：不渲染注释层与表单层（链接、表单控件、内嵌脚本都不生效，注释外观仍画在 canvas 上），
 * 不启用 XFA；只叠一层透明文本层供选中复制。CMap、标准字体、图像解码 wasm、ICC 配置文件走同源
 * `/pdfjs/<dir>/`（见 vite.config.ts 的 pdfjsAssets），不访问外部网络。
 * 页面按需渲染：只有进入视口附近（上下各一屏）的页才取页对象、画 canvas、建文本层，离开后释放画布。
 */
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
import workerUrl from "pdfjs-dist/legacy/build/pdf.worker.min.mjs?url";
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
import { useTranslation } from "react-i18next";
import { ErrorText, IconButton, Spinner } from "../../ui/primitives";
import type { BlobPreviewProps } from "./types";

GlobalWorkerOptions.workerSrc = workerUrl;

// worker 按自身脚本地址解析相对路径，这里给出绝对地址；末尾必须带 `/`
const ASSET_BASE = new URL(
	`${import.meta.env.BASE_URL}pdfjs/`,
	window.location.origin,
).href;

/** 缩放档位（相对 PDF 实际尺寸，1 = 100%）；放大/缩小按钮在相邻档位间跳 */
const ZOOM_STEPS = [
	0.25, 0.33, 0.5, 0.67, 0.75, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2, 2.5, 3, 4, 5,
];
const MIN_ZOOM = 0.1;
const MAX_ZOOM = 8;
/** 页列四周留白（px），与页列的 `p-4` 一致；适应宽度时扣除 */
const PAGE_PAD = 16;
/** 单页画布像素上限：超出时降低输出倍率，避免高缩放 × HiDPI 撑爆显存 */
const MAX_CANVAS_PIXELS = 2 ** 24;
/** 缩放停止多久后才按新倍率重绘画布；期间旧画布按 CSS 拉伸占位 */
const RERENDER_DELAY_MS = 150;

/**
 * pdf.js 文本层所需的最小样式（摘自 pdfjs-dist/web/pdf_viewer.css，只保留选中复制用得到的部分）。
 * 文本层尺寸由 pdf.js 以 `--total-scale-factor` 计算，页容器上设置该变量。
 */
const TEXT_LAYER_CSS = `
.oryn-pdf .textLayer{position:absolute;inset:0;overflow:clip;opacity:1;line-height:1;text-align:initial;letter-spacing:normal;word-spacing:normal;-webkit-text-size-adjust:none;text-size-adjust:none;forced-color-adjust:none;transform-origin:0 0;caret-color:CanvasText;z-index:0;--min-font-size:1;--text-scale-factor:calc(var(--total-scale-factor) * var(--min-font-size));--min-font-size-inv:calc(1 / var(--min-font-size))}
.oryn-pdf .textLayer :is(span,br){color:transparent;position:absolute;white-space:pre;cursor:text;transform-origin:0% 0%;-webkit-user-select:text;user-select:text}
.oryn-pdf .textLayer > :not(.markedContent),.oryn-pdf .textLayer .markedContent span:not(.markedContent){z-index:1;--font-height:0;font-size:calc(var(--text-scale-factor) * var(--font-height));--scale-x:1;--rotate:0deg;transform:rotate(var(--rotate)) scaleX(var(--scale-x)) scale(var(--min-font-size-inv))}
.oryn-pdf .textLayer .markedContent{display:contents}
.oryn-pdf .textLayer span[role="img"]{-webkit-user-select:none;user-select:none;cursor:default}
.oryn-pdf .textLayer ::selection{background:rgba(0 0 255 / 0.25);background:color-mix(in srgb, AccentColor, transparent 50%);color:transparent}
.oryn-pdf .textLayer br::selection{background:transparent}
.oryn-pdf .textLayer[data-main-rotation="90"]{transform:rotate(90deg) translateY(-100%)}
.oryn-pdf .textLayer[data-main-rotation="180"]{transform:rotate(180deg) translate(-100%,-100%)}
.oryn-pdf .textLayer[data-main-rotation="270"]{transform:rotate(270deg) translateX(-100%)}
`;

/** 页面在缩放 1（视口单位，含 userUnit 与页面自带旋转）下的宽高 */
interface PageSize {
	w: number;
	h: number;
}

type LoadState =
	| { kind: "loading" }
	/** `key`：已知情形（空文档、加密、损坏）的文案键，渲染时再翻译，切换语言不必重新加载文档 */
	| {
			kind: "error";
			error: unknown;
			key?:
				| "files.preview.pdf.password"
				| "files.preview.pdf.invalid"
				| "files.preview.pdf.empty";
	  }
	| { kind: "ready"; doc: PDFDocumentProxy; base: PageSize };

export default function PdfPreview({ blob }: BlobPreviewProps): ReactNode {
	const { t } = useTranslation();
	const [state, setState] = useState<LoadState>({ kind: "loading" });

	useEffect(() => {
		let alive = true;
		let task: PDFDocumentLoadingTask | null = null;
		setState({ kind: "loading" });
		(async () => {
			if (blob.size === 0) throw new Error("empty");
			const data = new Uint8Array(await blob.arrayBuffer());
			if (!alive) return;
			task = getDocument({
				data,
				cMapUrl: `${ASSET_BASE}cmaps/`,
				cMapPacked: true,
				standardFontDataUrl: `${ASSET_BASE}standard_fonts/`,
				wasmUrl: `${ASSET_BASE}wasm/`,
				iccUrl: `${ASSET_BASE}iccs/`,
				enableXfa: false,
				verbosity: VerbosityLevel.ERRORS,
			});
			const doc = await task.promise;
			if (doc.numPages === 0) throw new Error("empty");
			const first = (await doc.getPage(1)).getViewport({ scale: 1 });
			if (alive)
				setState({
					kind: "ready",
					doc,
					base: { w: first.width, h: first.height },
				});
		})().catch((e: unknown) => {
			if (!alive) return;
			// 加密文档不弹密码框：未设置 onPassword 时 pdf.js 以 PasswordException 拒绝
			const name = e instanceof Error ? e.name : "";
			const key =
				name === "PasswordException"
					? "files.preview.pdf.password"
					: name === "InvalidPDFException"
						? "files.preview.pdf.invalid"
						: e instanceof Error && e.message === "empty"
							? "files.preview.pdf.empty"
							: undefined;
			setState({ kind: "error", error: e, key });
		});
		return () => {
			alive = false;
			// 销毁加载任务会一并销毁文档与 worker，未完成的渲染随之失效
			void task?.destroy();
		};
	}, [blob]);

	if (state.kind === "error") {
		return (
			<div className="p-3">
				{state.key ? (
					<p className="text-muted text-sm">{t(state.key)}</p>
				) : (
					<ErrorText error={state.error} />
				)}
			</div>
		);
	}
	if (state.kind === "loading") {
		return (
			<div className="flex h-full items-center justify-center gap-2 bg-inset text-muted text-sm">
				<Spinner />
				{t("files.preview.pdf.loading")}
			</div>
		);
	}
	return <PdfViewer doc={state.doc} base={state.base} />;
}

function PdfViewer({
	doc,
	base,
}: {
	doc: PDFDocumentProxy;
	base: PageSize;
}): ReactNode {
	const { t } = useTranslation();
	const scrollRef = useRef<HTMLDivElement>(null);
	const pageEls = useRef<(HTMLDivElement | null)[]>([]);
	const observerRef = useRef<IntersectionObserver | null>(null);
	/** 滚动位置占内容总高的比例；缩放后据此恢复，阅读位置不跳 */
	const scrollFrac = useRef(0);
	const rafRef = useRef(0);
	const [width, setWidth] = useState(0);
	const [zoom, setZoom] = useState<number | "fit">("fit");
	const [near, setNear] = useState<ReadonlySet<number>>(() => new Set());
	const [current, setCurrent] = useState(1);
	const [pageInput, setPageInput] = useState<string | null>(null);
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

	// 画布重绘倍率：首次立即生效，之后等缩放/尺寸变化停下来再重绘
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
		if (!root) return;
		const io = new IntersectionObserver(
			(entries) => {
				setNear((prev) => {
					const next = new Set(prev);
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
		if (el && page) el.scrollTop = page.offsetTop - PAGE_PAD;
	};

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
					near={near.has(n)}
					register={registerPage}
				/>,
			);
		}
	}

	return (
		<div className="oryn-pdf flex h-full min-h-0 flex-col">
			<style>{TEXT_LAYER_CSS}</style>
			<div className="flex h-9 shrink-0 items-center gap-1 border-border border-b px-2 text-muted text-xs">
				<input
					aria-label={t("files.preview.pdf.pageNumber")}
					inputMode="numeric"
					className="h-6 w-10 rounded border border-border bg-inset px-1 text-center text-fg tabular-nums outline-none focus:border-accent"
					value={pageInput ?? String(current)}
					onFocus={(e) => {
						setPageInput(String(current));
						e.currentTarget.select();
					}}
					onChange={(e) => setPageInput(e.target.value.replace(/\D/g, ""))}
					onKeyDown={onPageKey}
					onBlur={commitPageInput}
				/>
				<span className="tabular-nums">/ {numPages}</span>
				<div className="flex-1" />
				<IconButton
					icon="zoomOut"
					box="sm"
					size={15}
					label={t("files.preview.pdf.zoomOut")}
					disabled={cssZoom <= MIN_ZOOM}
					onClick={() =>
						setZoom(ZOOM_STEPS.findLast((s) => s < cssZoom * 0.999) ?? MIN_ZOOM)
					}
				/>
				<span className="w-11 text-center tabular-nums">
					{Math.round(cssZoom * 100)}%
				</span>
				<IconButton
					icon="zoomIn"
					box="sm"
					size={15}
					label={t("files.preview.pdf.zoomIn")}
					disabled={cssZoom >= MAX_ZOOM}
					onClick={() =>
						setZoom(ZOOM_STEPS.find((s) => s > cssZoom * 1.001) ?? MAX_ZOOM)
					}
				/>
				<IconButton
					icon="distributeH"
					box="sm"
					size={15}
					label={t("files.preview.pdf.fitWidth")}
					active={zoom === "fit"}
					onClick={() => setZoom("fit")}
				/>
			</div>
			<div
				ref={scrollRef}
				onScroll={onScroll}
				className="relative min-h-0 flex-1 overflow-auto bg-inset [scrollbar-gutter:stable]"
			>
				<div className="flex w-max min-w-full flex-col items-center gap-3 p-4">
					{pages}
				</div>
			</div>
		</div>
	);
}

interface PdfPageProps {
	doc: PDFDocumentProxy;
	pageNumber: number;
	/** 页对象未取到前用第一页尺寸占位 */
	base: PageSize;
	/** 版面倍率：决定页框尺寸与文本层 */
	scale: number;
	/** 画布绘制倍率：缩放停下后才追上 `scale` */
	renderScale: number;
	near: boolean;
	register: (n: number, el: HTMLDivElement | null) => void;
}

const PdfPage = memo(function PdfPage({
	doc,
	pageNumber,
	base,
	scale,
	renderScale,
	near,
	register,
}: PdfPageProps): ReactNode {
	const { t } = useTranslation();
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
			() => alive && setFailed(true),
		);
		return () => {
			alive = false;
		};
	}, [doc, pageNumber, near, page]);

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
		canvas.className = "absolute inset-0 h-full w-full";
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
			},
			(e: unknown) => {
				task = null;
				canvas.width = 0;
				canvas.height = 0;
				if (!(e instanceof Error && e.name === "RenderingCancelledException"))
					setFailed(true);
			},
		);
		return () => {
			task?.cancel();
		};
	}, [page, near, renderScale]);

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
			className="relative shrink-0 overflow-hidden bg-white shadow-sm ring-1 ring-border"
			style={style}
		>
			<div ref={canvasHostRef} className="absolute inset-0" />
			{failed ? (
				<div className="absolute inset-0 flex items-center justify-center p-3 text-center text-faint text-xs">
					{t("files.preview.pdf.pageError", { page: pageNumber })}
				</div>
			) : null}
		</div>
	);
});

/** 移除并清空宿主里的画布；把尺寸置 0 让浏览器立即回收位图内存 */
function releaseCanvases(host: HTMLElement) {
	for (const c of host.querySelectorAll("canvas")) {
		c.width = 0;
		c.height = 0;
	}
	host.replaceChildren();
}
