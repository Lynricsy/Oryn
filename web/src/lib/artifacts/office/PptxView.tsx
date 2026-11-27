// PowerPoint 演示文稿（pptx / pptm）：`@silurus/ooxml` 的 PptxPresentation 逐张画到 canvas（见 paged.tsx）。
// 普通视图带「演示」：单张铺满、方向键 / 空格 / 点击翻页、Esc 退出；进出演示时回调 onPresent（沙箱里由它请求宿主铺满）。
// 评论模式下点幻灯片给出张号、该张标题与归一化坐标，文字取点中的形状里的文字（库的元素命中测试）。

import type { PptxPresentation } from "@silurus/ooxml/pptx";
import {
	type ReactNode,
	useCallback,
	useEffect,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { clipText, NO_PINS, type Point, pointAnchor } from "./anchor";
import { loadPptx } from "./ooxml";
import {
	fillBox,
	type PageComments,
	PagedView,
	type PageSource,
} from "./paged";
import { OfficeState, useLoaded } from "./shared";
import type { OfficeViewProps } from "./types";
import "./office.css";

/** 幻灯片在 100% 下的 CSS 宽度（与打印 PDF 的 1280×720 窗口一致）。 */
const SLIDE_WIDTH = 1280;

/** 每份演示文稿的各张标题（张号从 1 起），取自库的 Markdown 投影（每张一行 `# <标题> (slide N)`）。 */
const titleCache = new WeakMap<
	PptxPresentation,
	Promise<ReadonlyMap<number, string>>
>();

const TITLE_LINE = /^#{1,6} (.*?) ?\(slide (\d+)\)\s*$/;

function slideTitles(
	pres: PptxPresentation,
): Promise<ReadonlyMap<number, string>> {
	let titles = titleCache.get(pres);
	if (!titles) {
		titles = pres.toMarkdown().then((md) => {
			const out = new Map<number, string>();
			for (const line of md.split("\n")) {
				const m = TITLE_LINE.exec(line);
				if (!m) continue;
				// 去掉 Markdown 转义的反斜杠
				const title = (m[1] ?? "").replace(
					/\\([\\`*_{}[\]()#+\-.!|<>~])/g,
					"$1",
				);
				out.set(Number(m[2]), clipText(title));
			}
			return out;
		});
		titleCache.set(pres, titles);
	}
	return titles;
}

/** 第 i 张一点的锚点：标题取不到为空，点中的形状没有文字时 `text` 为空；库出错只让这两项为空。 */
async function anchorAt(pres: PptxPresentation, i: number, p: Point) {
	const [name, text] = await Promise.all([
		slideTitles(pres).then(
			(t) => t.get(i + 1) ?? "",
			() => "",
		),
		pres
			.getElementContextAt(
				i,
				{ x: p.x * pres.slideWidth, y: p.y * pres.slideHeight },
				{ maxTextCharacters: 200 },
			)
			.then(
				(c) => c?.text ?? "",
				() => "",
			),
	]);
	return pointAnchor({ number: i + 1, name }, p, text);
}
export interface PptxViewProps extends OfficeViewProps {
	/** 进入（true）与退出（false）演示时回调。 */
	onPresent?(on: boolean): void;
}

export function PptxView(props: PptxViewProps): ReactNode {
	const base = props.assets.ooxml;
	const state = useLoaded<PptxPresentation>(
		props.bytes,
		async (bytes) => {
			const m = await loadPptx(base);
			return m.PptxPresentation.load(bytes.slice(0));
		},
		(pres) => pres.destroy(),
	);
	if (state.kind !== "ready")
		return <OfficeState state={state} strings={props.strings} />;
	return <PptxSlides pres={state.value} {...props} />;
}

function PptxSlides({
	pres,
	view,
	strings,
	onReady,
	onPresent,
	pickable,
	onPick,
	pins,
	locate,
}: PptxViewProps & { pres: PptxPresentation }): ReactNode {
	const [presenting, setPresenting] = useState<number | null>(null);
	const source = useMemo<PageSource>(() => {
		const h =
			pres.slideWidth > 0
				? (SLIDE_WIDTH * pres.slideHeight) / pres.slideWidth
				: (SLIDE_WIDTH * 9) / 16;
		return {
			count: pres.slideCount,
			size: () => ({ w: SLIDE_WIDTH, h }),
			render: (canvas, i, width, dpr) =>
				pres.renderSlide(canvas, i, { width, dpr }),
		};
	}, [pres]);

	const present = useRef(onPresent);
	present.current = onPresent;
	const start = () => {
		setPresenting(0);
		present.current?.(true);
	};
	const stop = useCallback(() => {
		setPresenting(null);
		present.current?.(false);
	}, []);

	const comments = useMemo<PageComments>(
		() => ({
			onPick:
				pickable && onPick
					? (i, point, at) => {
							void anchorAt(pres, i, point).then((a) => onPick(a, at));
						}
					: null,
			pickLabel: (i) =>
				strings.commentSlide?.(i + 1) ?? strings.slideLabel(i + 1),
			pins: pins ?? NO_PINS,
			locate: locate ?? null,
		}),
		[pres, pickable, onPick, pins, locate, strings],
	);

	const header =
		view === "full" && source.count > 0 ? (
			<div className="oryn-o-toolbar">
				<span className="oryn-o-grow" />
				<button type="button" className="oryn-o-btn" onClick={start}>
					{strings.present}
				</button>
			</div>
		) : null;

	return (
		<div className="oryn-o oryn-o-pptx">
			<PagedView
				source={source}
				view={view}
				strings={strings}
				label={(i) => strings.slideLabel(i + 1)}
				onReady={onReady}
				header={header}
				comments={comments}
			/>
			{presenting !== null ? (
				<Presenter
					source={source}
					index={presenting}
					strings={strings}
					onGo={setPresenting}
					onExit={stop}
				/>
			) : null}
		</div>
	);
}

function Presenter({
	source,
	index,
	strings,
	onGo,
	onExit,
}: {
	source: PageSource;
	index: number;
	strings: PptxViewProps["strings"];
	onGo(i: number): void;
	onExit(): void;
}): ReactNode {
	const stage = useRef<HTMLDivElement>(null);
	const host = useRef<HTMLDivElement>(null);
	const [box, setBox] = useState({ w: 0, h: 0 });
	const { w, h } = source.size(index);
	const last = source.count - 1;
	const go = useCallback(
		(d: number) => onGo(Math.min(last, Math.max(0, index + d))),
		[onGo, index, last],
	);

	useLayoutEffect(() => {
		const el = stage.current;
		if (!el) return;
		const refit = () => {
			const s = Math.min(el.clientWidth / w, el.clientHeight / h);
			setBox({ w: w * s, h: h * s });
		};
		refit();
		if (typeof ResizeObserver === "undefined") return;
		const ro = new ResizeObserver(refit);
		ro.observe(el);
		return () => ro.disconnect();
	}, [w, h]);

	useEffect(() => {
		const onKey = (e: KeyboardEvent) => {
			if (e.key === "Escape") onExit();
			else if (["ArrowRight", "ArrowDown", "PageDown", " "].includes(e.key))
				go(1);
			else if (["ArrowLeft", "ArrowUp", "PageUp"].includes(e.key)) go(-1);
			else if (e.key === "Home") onGo(0);
			else if (e.key === "End") onGo(last);
			else return;
			e.preventDefault();
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, [go, onExit, onGo, last]);

	useEffect(() => {
		const el = host.current;
		if (!el || box.w <= 0) return;
		let cancelled = false;
		const canvas = document.createElement("canvas");
		source.render(canvas, index, box.w, window.devicePixelRatio || 1).then(
			() => {
				if (cancelled) return;
				el.replaceChildren(fillBox(canvas));
			},
			() => {},
		);
		return () => {
			cancelled = true;
		};
	}, [source, index, box.w]);

	return (
		<div
			ref={stage}
			className="oryn-o-present"
			role="dialog"
			aria-modal="true"
			aria-label={strings.present}
		>
			<button
				type="button"
				className="oryn-o-present-slide"
				aria-label={strings.next}
				style={{ width: box.w, height: box.h }}
				onClick={() => go(1)}
			>
				<div ref={host} className="oryn-o-canvas" />
			</button>
			<div className="oryn-o-present-bar">
				<button
					type="button"
					className="oryn-o-btn"
					aria-label={strings.prev}
					disabled={index === 0}
					onClick={() => go(-1)}
				>
					‹
				</button>
				<span>{strings.counter(index + 1, source.count)}</span>
				<button
					type="button"
					className="oryn-o-btn"
					aria-label={strings.next}
					disabled={index === last}
					onClick={() => go(1)}
				>
					›
				</button>
				<span className="oryn-o-muted">{strings.exitHint}</span>
			</div>
		</div>
	);
}
