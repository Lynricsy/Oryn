/**
 * 画布编辑器的排列（计划 M14 6.4）：对齐、等距、层级、锁定、尺寸预设，以及画布页的新建 / 改名 / 复制 / 删除。
 * 纯函数，输入是当前文档，输出是要提交的 `CanvasOp`（经 `CanvasSync.commit` 提交，逆操作由它相对于提交前的文档生成，可撤销）。
 */
import {
	type CanvasDoc,
	type CanvasOp,
	type CanvasPage,
	canvasId,
	isShape,
	PAGE_MAX,
	PAGE_MIN,
} from "../../../lib/artifacts/canvas-doc";
import { findItem } from "./selection";

/** 对齐的六种方式：左、水平居中、右、顶、垂直居中、底。 */
export type AlignEdge =
	| "left"
	| "centerH"
	| "right"
	| "top"
	| "centerV"
	| "bottom";

export const ALIGN_EDGES: readonly AlignEdge[] = [
	"left",
	"centerH",
	"right",
	"top",
	"centerV",
	"bottom",
];

/** 对齐至少要选中的画框数；等距至少要选中的画框数。 */
export const ALIGN_MIN = 2;
export const DISTRIBUTE_MIN = 3;

function round2(v: number): number {
	return Math.round(v * 100) / 100;
}

/** 选中的对象里的画框（按文档顺序）。 */
export function selectedPages(
	doc: CanvasDoc,
	ids: readonly string[],
): CanvasPage[] {
	const set = new Set(ids);
	return doc.pages.filter((p) => set.has(p.id));
}

function setXY(page: CanvasPage, x: number, y: number): CanvasOp[] {
	const ops: CanvasOp[] = [];
	if (round2(x) !== page.x)
		ops.push({ op: "set", id: page.id, key: "x", value: round2(x) });
	if (round2(y) !== page.y)
		ops.push({ op: "set", id: page.id, key: "y", value: round2(y) });
	return ops;
}

/**
 * 把选中的画框按 edge 对齐：参照是全部选中画框的外包围盒；锁定的画框参与参照但不动。
 * 选中的画框少于 `ALIGN_MIN` 个时没有操作。
 */
export function alignOps(
	doc: CanvasDoc,
	ids: readonly string[],
	edge: AlignEdge,
): CanvasOp[] {
	const pages = selectedPages(doc, ids);
	if (pages.length < ALIGN_MIN) return [];
	const left = Math.min(...pages.map((p) => p.x));
	const top = Math.min(...pages.map((p) => p.y));
	const right = Math.max(...pages.map((p) => p.x + p.width));
	const bottom = Math.max(...pages.map((p) => p.y + p.height));
	return pages
		.filter((p) => !p.locked)
		.flatMap((p) => {
			switch (edge) {
				case "left":
					return setXY(p, left, p.y);
				case "centerH":
					return setXY(p, (left + right) / 2 - p.width / 2, p.y);
				case "right":
					return setXY(p, right - p.width, p.y);
				case "top":
					return setXY(p, p.x, top);
				case "centerV":
					return setXY(p, p.x, (top + bottom) / 2 - p.height / 2);
				default:
					return setXY(p, p.x, bottom - p.height);
			}
		});
}

/**
 * 等距：按位置排序，首尾两个不动，中间的画框依次排开，使相邻画框之间的间距相等（画框重叠时间距为负）。
 * 锁定的画框占着自己的位置不动。选中的画框少于 `DISTRIBUTE_MIN` 个时没有操作。
 */
export function distributeOps(
	doc: CanvasDoc,
	ids: readonly string[],
	axis: "x" | "y",
): CanvasOp[] {
	const pages = selectedPages(doc, ids);
	if (pages.length < DISTRIBUTE_MIN) return [];
	const size = (p: CanvasPage) => (axis === "x" ? p.width : p.height);
	const pos = (p: CanvasPage) => (axis === "x" ? p.x : p.y);
	const sorted = pages.toSorted((a, b) => pos(a) - pos(b));
	const first = sorted[0];
	const last = sorted[sorted.length - 1];
	if (!first || !last) return [];
	const span = pos(last) + size(last) - pos(first);
	const total = sorted.reduce((s, p) => s + size(p), 0);
	const gap = (span - total) / (sorted.length - 1);
	const ops: CanvasOp[] = [];
	let at = pos(first);
	for (const p of sorted) {
		if (p !== first && p !== last && !p.locked)
			ops.push(...(axis === "x" ? setXY(p, at, p.y) : setXY(p, p.x, at)));
		at += size(p) + gap;
	}
	return ops;
}

/**
 * 移到最前 / 最后：画框在画框里、标注在标注里调整绘制顺序（标注总画在画框之上）。
 * 多个对象保持彼此原来的先后。
 */
export function orderOps(
	doc: CanvasDoc,
	ids: readonly string[],
	to: "front" | "back",
): CanvasOp[] {
	const set = new Set(ids);
	const ops: CanvasOp[] = [];
	for (const list of [doc.pages, doc.notes] as const) {
		const chosen = list.filter((x) => set.has(x.id));
		if (to === "front")
			for (const x of chosen)
				ops.push({ op: "order", id: x.id, index: list.length - 1 });
		else
			for (const x of chosen.toReversed())
				ops.push({ op: "order", id: x.id, index: 0 });
	}
	return ops;
}

/** 选中的对象是否全部锁定（锁定按钮据此在「锁定」与「解锁」之间切换）。 */
export function allLocked(doc: CanvasDoc, ids: readonly string[]): boolean {
	return (
		ids.length > 0 &&
		ids.every((id) => findItem(doc, id)?.value.locked === true)
	);
}

/** 把选中的对象设成锁定 / 解锁（已经是该状态的跳过）。 */
export function lockOps(
	doc: CanvasDoc,
	ids: readonly string[],
	locked: boolean,
): CanvasOp[] {
	return ids.flatMap((id): CanvasOp[] => {
		const it = findItem(doc, id);
		return it && it.value.locked !== locked
			? [{ op: "set", id, key: "locked", value: locked }]
			: [];
	});
}

/** 画框的尺寸预设。 */
export const FRAME_PRESETS = [
	{ key: "desktop", width: 1440, height: 900 },
	{ key: "laptop", width: 1280, height: 800 },
	{ key: "tablet", width: 834, height: 1194 },
	{ key: "phone", width: 390, height: 844 },
] as const;

export type FramePreset = (typeof FRAME_PRESETS)[number]["key"] | "custom";

/** 画框当前尺寸对应的预设；都不是时 `custom`。 */
export function presetOf(page: CanvasPage): FramePreset {
	return (
		FRAME_PRESETS.find(
			(p) => p.width === page.width && p.height === page.height,
		)?.key ?? "custom"
	);
}

/** 画框尺寸：取整并夹在 64–8000 里。 */
export function clampPageSize(v: number): number {
	return Math.round(Math.min(PAGE_MAX, Math.max(PAGE_MIN, v)));
}

/** 把画框改成某个尺寸（左上角不动）。 */
export function sizeOps(
	page: CanvasPage,
	width: number,
	height: number,
): CanvasOp[] {
	const ops: CanvasOp[] = [];
	const w = clampPageSize(width);
	const h = clampPageSize(height);
	if (w !== page.width)
		ops.push({ op: "set", id: page.id, key: "width", value: w });
	if (h !== page.height)
		ops.push({ op: "set", id: page.id, key: "height", value: h });
	return ops;
}

/** 套用尺寸预设；`custom` 没有操作。 */
export function presetOps(page: CanvasPage, preset: FramePreset): CanvasOp[] {
	const p = FRAME_PRESETS.find((x) => x.key === preset);
	return p ? sizeOps(page, p.width, p.height) : [];
}

// ------------------------------------------------------------ 画布页

/** 新建一个画布页（追加在最后）。返回操作与新页的 id。 */
export function addSheetOps(name: string): { ops: CanvasOp[]; id: string } {
	const id = canvasId("c");
	return { ops: [{ op: "add", object: { type: "canvas", id, name } }], id };
}

/** 给画布页改名；名字没变或为空时没有操作。 */
export function renameSheetOps(
	doc: CanvasDoc,
	id: string,
	name: string,
): CanvasOp[] {
	const next = name.trim();
	const sheet = doc.canvases.find((c) => c.id === id);
	if (!sheet || next === "" || next === sheet.name) return [];
	return [{ op: "set", id, key: "name", value: next }];
}

/** 能不能删这个画布页：最后一个画布页不能删。 */
export function canDeleteSheet(doc: CanvasDoc, id: string): boolean {
	return doc.canvases.length > 1 && doc.canvases.some((c) => c.id === id);
}

/** 删除画布页（连带它的画框与标注）；最后一个画布页不能删，返回 null。 */
export function deleteSheetOps(doc: CanvasDoc, id: string): CanvasOp[] | null {
	return canDeleteSheet(doc, id) ? [{ op: "remove", id }] : null;
}

/**
 * 复制画布页：新页紧跟在原页之后，画框原位复制（带 `copyOf`，落版本时服务端复制它的 HTML），
 * 标注原样复制，吸附关系指向复制出来的画框。返回操作与新页的 id。
 */
export function copySheetOps(
	doc: CanvasDoc,
	id: string,
	name: string,
): { ops: CanvasOp[]; id: string } | null {
	const index = doc.canvases.findIndex((c) => c.id === id);
	if (index < 0) return null;
	const nid = canvasId("c");
	const ops: CanvasOp[] = [
		{ op: "add", object: { type: "canvas", id: nid, name } },
		{ op: "order", id: nid, index: index + 1 },
	];
	const ids = new Map<string, string>();
	for (const p of doc.pages) {
		if (p.canvas !== id) continue;
		const pid = canvasId("p");
		ids.set(p.id, pid);
		const { file: _file, ...rest } = p;
		ops.push({
			op: "add",
			object: { type: "page", ...rest, id: pid, canvas: nid },
			copyOf: p.id,
		});
	}
	for (const n of doc.notes) {
		if (n.canvas !== id) continue;
		const copy: Record<string, unknown> = {
			...n,
			id: canvasId("n"),
			canvas: nid,
		};
		if (isShape(n)) {
			copy.from = n.from === null ? null : (ids.get(n.from) ?? null);
			copy.to = n.to === null ? null : (ids.get(n.to) ?? null);
		}
		ops.push({ op: "add", object: { type: "note", ...copy } });
	}
	return { ops, id: nid };
}
