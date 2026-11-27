// Excel 工作簿（xlsx / xlsm）：普通视图是 `@silurus/ooxml` 自带的网格与工作表标签（XlsxViewer）；缩略图只画第一个
// 工作表的左上区域（XlsxSheetViewer，无滚动条）；表格没有打印视图。
// 评论模式下用库自己的选区：在网格上松开鼠标时取当前工作表、选区与活动单元格的显示文字作锚点。图钉画在区域左上角
// 的单元格上；滚出视口、整张工作表的与别的工作表上的，在右上角按工作表列出，点一下跳过去。

import type { XlsxSheetViewer, XlsxViewer } from "@silurus/ooxml/xlsx";
import {
	type ReactNode,
	useCallback,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { RANGE_PATTERN } from "../../../../sandbox/runtime/messages";
import { clipText, NO_PINS } from "./anchor";
import { MAX_PREVIEW_BYTES } from "./limits";
import { loadXlsx } from "./ooxml";
import { PinDot } from "./pins";
import { OfficeState, ReadyMark, reasonOf } from "./shared";
import type { AnchorPin, OfficeViewProps, ViewerAnchor } from "./types";
import "./office.css";

export function XlsxView(props: OfficeViewProps): ReactNode {
	if (props.bytes.byteLength > MAX_PREVIEW_BYTES)
		return (
			<OfficeState state={{ kind: "too-large" }} strings={props.strings} />
		);
	if (props.view === "print")
		return <div className="oryn-o-state">{props.strings.sheetNoPrint}</div>;
	return <XlsxSheets {...props} />;
}

type SheetState =
	| { kind: "loading" }
	| { kind: "error"; reason: string }
	| { kind: "ready"; sheets: number };

/** 库在网格左侧与上方画的行号列宽、列标行高（CSS px，乘缩放倍率）。 */
const ROW_HEADER_WIDTH = 50;
const COLUMN_HEADER_HEIGHT = 22;
/** Excel 的最大行号与最后一列（整行、整列选区换成单元格区域时用）。 */
const MAX_ROW = 1_048_576;
const LAST_COLUMN = "XFD";
/** 取活动单元格显示文字时，选区上下文最多带的单元格数。 */
const CONTEXT_CELLS = 10_000;

/** 列号（从 1 起）→ 列字母。 */
function columnName(col: number): string {
	let s = "";
	for (let n = col; n > 0; n = Math.floor((n - 1) / 26))
		s = String.fromCharCode(65 + ((n - 1) % 26)) + s;
	return s;
}

/** 选区的活动区域 → 锚点的 `range`；整张工作表或超出范围时为空。 */
function rangeOf(v: XlsxViewer): string | null {
	const sel = v.selectionState;
	const area = sel?.areas[sel.activeAreaIndex];
	if (!area) return null;
	let range: string | null = null;
	switch (area.kind) {
		case "cells": {
			const a = `${columnName(area.left)}${area.top}`;
			const b = `${columnName(area.right)}${area.bottom}`;
			range = a === b ? a : `${a}:${b}`;
			break;
		}
		case "rows":
			range = `A${area.firstRow}:${LAST_COLUMN}${area.lastRow}`;
			break;
		case "columns":
			range = `${columnName(area.firstColumn)}1:${columnName(area.lastColumn)}${MAX_ROW}`;
			break;
	}
	return range !== null && RANGE_PATTERN.test(range) ? range : null;
}

/** 活动单元格的显示文字（库按单元格格式排好的文本）。 */
function activeText(v: XlsxViewer): string {
	const cell = v.selectionState?.activeCell;
	if (!cell) return "";
	const ctx = v.getSelectionContext({ maxCells: CONTEXT_CELLS });
	if (ctx?.kind !== "range") return "";
	const hit = ctx.cells.find(
		(c) => c.address.row === cell.row && c.address.col === cell.col,
	);
	return clipText(hit?.displayText ?? "");
}

function sheetAnchor(v: XlsxViewer): ViewerAnchor {
	const i = v.sheetIndex;
	return {
		page: { number: i + 1, name: clipText(v.sheetNames[i] ?? "") },
		x: null,
		y: null,
		range: rangeOf(v),
		block: null,
		text: activeText(v),
	};
}

/** 切到锚点的工作表并选中区域、滚到区域左上角。 */
async function locateIn(v: XlsxViewer, a: ViewerAnchor): Promise<void> {
	const i = (a.page?.number ?? 0) - 1;
	if (i < 0 || i >= v.sheetCount) return;
	if (v.sheetIndex !== i) await v.goToSheet(i);
	if (!a.range) return;
	v.setSelection(a.range);
	await v.scrollToCell(a.range.split(":")[0] ?? a.range, { align: "center" });
}

interface PlacedPins {
	/** 画在网格上的：相对视图根元素的像素位置。 */
	dots: { pin: AnchorPin; left: number; top: number }[];
	/** 网格上画不出的，按工作表（从 1 起）分组。 */
	listed: { sheet: number; name: string; pins: AnchorPin[] }[];
}

const NOTHING_PLACED: PlacedPins = { dots: [], listed: [] };

function placePins(
	v: XlsxViewer,
	root: HTMLElement,
	pins: readonly AnchorPin[],
): PlacedPins {
	const current = v.sheetIndex + 1;
	const scale = v.getScale();
	const box = root.getBoundingClientRect();
	const grid = v.canvasElement.getBoundingClientRect();
	const dots: PlacedPins["dots"] = [];
	const listed = new Map<number, PlacedPins["listed"][number]>();
	for (const pin of pins) {
		const page = pin.anchor.page;
		if (!page) continue;
		if (page.number === current && pin.anchor.range) {
			const r = v.getCellViewportRect(
				pin.anchor.range.split(":")[0] ?? pin.anchor.range,
			);
			// 左上角被行号列、列标行盖住或滚出网格的不画在格子上
			if (
				r &&
				r.x >= ROW_HEADER_WIDTH * scale - 1 &&
				r.y >= COLUMN_HEADER_HEIGHT * scale - 1 &&
				r.x < grid.width &&
				r.y < grid.height
			) {
				dots.push({
					pin,
					left: grid.left - box.left + r.x,
					top: grid.top - box.top + r.y,
				});
				continue;
			}
		}
		let group = listed.get(page.number);
		if (!group) {
			group = {
				sheet: page.number,
				name: v.sheetNames[page.number - 1] ?? page.name,
				pins: [],
			};
			listed.set(page.number, group);
		}
		group.pins.push(pin);
	}
	return {
		dots,
		listed: [...listed.values()].sort((a, b) => a.sheet - b.sheet),
	};
}

function XlsxSheets({
	bytes,
	view,
	assets,
	strings,
	onReady,
	pickable,
	onPick,
	pins = NO_PINS,
	locate,
}: OfficeViewProps): ReactNode {
	const root = useRef<HTMLDivElement>(null);
	const host = useRef<HTMLDivElement>(null);
	const viewerRef = useRef<XlsxViewer | null>(null);
	const base = assets.ooxml;
	const [state, setState] = useState<SheetState>({ kind: "loading" });
	// 视口滚动、换工作表、缩放、尺寸变化时递增，图钉据此重新摆放
	const [layout, setLayout] = useState(0);
	const hasPins = useRef(false);
	hasPins.current = pins.length > 0;
	const relayout = useCallback(() => {
		if (hasPins.current) setLayout((n) => n + 1);
	}, []);

	useEffect(() => {
		const el = host.current;
		if (!el) return;
		let alive = true;
		let viewer: XlsxViewer | XlsxSheetViewer | null = null;
		setState({ kind: "loading" });
		(async () => {
			const m = await loadXlsx(base);
			if (!alive) return 0;
			if (view === "thumbnail") {
				const canvas = document.createElement("canvas");
				canvas.className = "oryn-o-sheet-canvas";
				el.append(canvas);
				const v = new m.XlsxSheetViewer(canvas, {
					showScrollbars: false,
					enableHyperlinks: false,
				});
				viewer = v;
				await v.load(bytes.slice(0));
				return v.sheetCount;
			}
			// 链接不在沙箱里跳转（外链经库的 window.open 打开会被帧策略拦下）
			const v = new m.XlsxViewer(el, {
				enableHyperlinks: false,
				onSheetChange: relayout,
				onViewportChange: relayout,
				onScaleChange: relayout,
			});
			viewer = v;
			await v.load(bytes.slice(0));
			if (alive) viewerRef.current = v;
			return v.sheetCount;
		})().then(
			(sheets) => {
				if (alive) setState({ kind: "ready", sheets });
			},
			(e: unknown) => {
				if (alive) setState({ kind: "error", reason: reasonOf(e) });
			},
		);
		return () => {
			alive = false;
			viewerRef.current = null;
			viewer?.destroy();
			el.replaceChildren();
		};
	}, [bytes, view, base, relayout]);

	const ready = state.kind === "ready";

	// 点选：在网格（含行号列、列标行）里松开鼠标后，等库把选区更新完再读
	useEffect(() => {
		const el = host.current;
		if (!el || !ready || !pickable || !onPick) return;
		let frame = 0;
		const onUp = (e: PointerEvent) => {
			const v = viewerRef.current;
			if (!v || e.button !== 0) return;
			const g = v.canvasElement.getBoundingClientRect();
			if (
				e.clientX < g.left ||
				e.clientX > g.right ||
				e.clientY < g.top ||
				e.clientY > g.bottom
			)
				return;
			// 滚动条上松开不算
			const t = e.target;
			if (
				t instanceof HTMLElement &&
				(e.offsetX > t.clientWidth || e.offsetY > t.clientHeight)
			)
				return;
			const at = { x: e.clientX, y: e.clientY };
			cancelAnimationFrame(frame);
			frame = requestAnimationFrame(() => {
				if (viewerRef.current === v) onPick(sheetAnchor(v), at);
			});
		};
		el.addEventListener("pointerup", onUp);
		return () => {
			el.removeEventListener("pointerup", onUp);
			cancelAnimationFrame(frame);
		};
	}, [ready, pickable, onPick]);

	useEffect(() => {
		const v = viewerRef.current;
		if (!v || !ready || !locate) return;
		locateIn(v, locate).catch(() => {});
	}, [ready, locate]);

	useEffect(() => {
		const el = root.current;
		if (!el || !ready || typeof ResizeObserver === "undefined") return;
		const ro = new ResizeObserver(relayout);
		ro.observe(el);
		return () => ro.disconnect();
	}, [ready, relayout]);

	const [placed, setPlaced] = useState<PlacedPins>(NOTHING_PLACED);
	// biome-ignore lint/correctness/useExhaustiveDependencies: layout 是重新摆放的信号
	useLayoutEffect(() => {
		const v = viewerRef.current;
		const el = root.current;
		setPlaced(
			v && el && ready && pins.length > 0
				? placePins(v, el, pins)
				: NOTHING_PLACED,
		);
	}, [ready, pins, layout]);

	return (
		<div
			ref={root}
			className={`oryn-o oryn-o-xlsx${view === "thumbnail" ? " oryn-o-xlsx-thumbnail" : ""}`}
		>
			<div ref={host} className="oryn-o-sheet-host" />
			{view === "full" && placed.dots.length > 0 ? (
				<div className="oryn-pins" aria-hidden="true">
					{placed.dots.map((d) => (
						<PinDot
							key={d.pin.n}
							pin={d.pin}
							style={{ left: d.left, top: d.top }}
						/>
					))}
				</div>
			) : null}
			{view === "full" && placed.listed.length > 0 ? (
				<div className="oryn-o-sheet-pins">
					{placed.listed.map((g) => (
						<div key={g.sheet} className="oryn-o-sheet-pin-group">
							<span className="oryn-o-sheet-pin-name">{g.name}</span>
							{g.pins.map((p) => (
								<button
									key={p.n}
									type="button"
									className={`oryn-o-sheet-pin${p.answered ? " oryn-pin-answered" : ""}`}
									aria-label={`${g.name} #${p.n}`}
									onClick={() => {
										const v = viewerRef.current;
										if (v) locateIn(v, p.anchor).catch(() => {});
									}}
								>
									{p.n}
								</button>
							))}
						</div>
					))}
				</div>
			) : null}
			{state.kind === "ready" ? (
				<ReadyMark pages={state.sheets} strings={strings} onReady={onReady} />
			) : (
				<div className="oryn-o-overlay">
					<OfficeState state={state} strings={strings} />
				</div>
			)}
		</div>
	);
}
