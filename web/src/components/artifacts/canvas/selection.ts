/**
 * 画布编辑器的选择与变换（计划 M14 6.3）：命中测试、框选、包围盒，以及移动 / 缩放 / 复制 / 删除生成的操作。
 * 纯函数，输入是当前文档，输出是要提交的 `CanvasOp`。
 */
import {
	type Box,
	type CanvasDoc,
	type CanvasNote,
	type CanvasOp,
	type CanvasPage,
	canvasId,
	isShape,
	joinedEnds,
	noteBox,
	PAGE_MAX,
	PAGE_MIN,
	STICKY_WIDTH,
	TEXT_SIZE,
} from "../../../lib/artifacts/canvas-doc";
import type { Point } from "./camera";

/** 复制画框时的偏移；标注的偏移。 */
export const PAGE_COPY_OFFSET = 48;
export const NOTE_COPY_OFFSET = 24;

export type CanvasItem =
	| { type: "page"; value: CanvasPage }
	| { type: "note"; value: CanvasNote };

/** 当前画布页上的画框与标注（按绘制顺序；标注画在画框之上）。 */
export function itemsOn(doc: CanvasDoc, canvas: string): CanvasItem[] {
	return [
		...doc.pages
			.filter((p) => p.canvas === canvas)
			.map((value): CanvasItem => ({ type: "page", value })),
		...doc.notes
			.filter((n) => n.canvas === canvas)
			.map((value): CanvasItem => ({ type: "note", value })),
	];
}

export function findItem(doc: CanvasDoc, id: string): CanvasItem | null {
	const page = doc.pages.find((p) => p.id === id);
	if (page) return { type: "page", value: page };
	const note = doc.notes.find((n) => n.id === id);
	return note ? { type: "note", value: note } : null;
}

/** 对象的包围盒；吸附了画框的线按实际端点算。 */
export function itemBox(doc: CanvasDoc, item: CanvasItem): Box {
	if (item.type === "page") {
		const { x, y, width, height } = item.value;
		return { x, y, width, height };
	}
	const n = item.value;
	if (isShape(n) && (n.kind === "line" || n.kind === "arrow")) {
		const e = joinedEnds(n, doc.pages);
		return {
			x: Math.min(e.x1, e.x2),
			y: Math.min(e.y1, e.y2),
			width: Math.abs(e.x2 - e.x1),
			height: Math.abs(e.y2 - e.y1),
		};
	}
	return noteBox(n);
}

function inside(p: Point, b: Box, pad: number): boolean {
	return (
		p.x >= b.x - pad &&
		p.x <= b.x + b.width + pad &&
		p.y >= b.y - pad &&
		p.y <= b.y + b.height + pad
	);
}

/** 点到线段的距离。 */
function segmentDistance(
	p: Point,
	x1: number,
	y1: number,
	x2: number,
	y2: number,
): number {
	const dx = x2 - x1;
	const dy = y2 - y1;
	const len2 = dx * dx + dy * dy;
	const t =
		len2 === 0
			? 0
			: Math.max(0, Math.min(1, ((p.x - x1) * dx + (p.y - y1) * dy) / len2));
	return Math.hypot(p.x - (x1 + t * dx), p.y - (y1 + t * dy));
}

/** 标注是否被点 p 命中（tolerance 是世界单位）：线与笔迹按到线的距离，其余按包围盒。 */
function hitsNote(
	doc: CanvasDoc,
	n: CanvasNote,
	p: Point,
	tolerance: number,
): boolean {
	switch (n.kind) {
		case "pen": {
			const reach = n.width / 2 + tolerance;
			if (!inside(p, noteBox(n), tolerance)) return false;
			for (let i = 0; i + 1 < n.points.length; i += 3) {
				const x1 = n.points[i] ?? 0;
				const y1 = n.points[i + 1] ?? 0;
				const x2 = n.points[i + 3] ?? x1;
				const y2 = n.points[i + 4] ?? y1;
				if (segmentDistance(p, x1, y1, x2, y2) <= reach) return true;
			}
			return false;
		}
		case "line":
		case "arrow": {
			const e = joinedEnds(n, doc.pages);
			return (
				segmentDistance(p, e.x1, e.y1, e.x2, e.y2) <= n.width / 2 + tolerance
			);
		}
		case "rect":
		case "ellipse": {
			// 空心形状只在描边附近命中，里面的画框还能点到
			const b = noteBox(n);
			if (!inside(p, b, tolerance + n.width / 2)) return false;
			const reach = tolerance + n.width / 2;
			if (n.kind === "rect")
				return (
					Math.abs(p.x - b.x) <= reach ||
					Math.abs(p.x - (b.x + b.width)) <= reach ||
					Math.abs(p.y - b.y) <= reach ||
					Math.abs(p.y - (b.y + b.height)) <= reach
				);
			const rx = b.width / 2;
			const ry = b.height / 2;
			if (rx < reach || ry < reach) return true;
			const cx = b.x + rx;
			const cy = b.y + ry;
			const d = Math.hypot((p.x - cx) / rx, (p.y - cy) / ry);
			return Math.abs(d - 1) * Math.min(rx, ry) <= reach;
		}
		default:
			return inside(p, noteBox(n), tolerance);
	}
}

/** 点 p 处最上层的对象（先标注、后画框，各自从后往前）；没有时 null。 */
export function hitTest(
	doc: CanvasDoc,
	canvas: string,
	p: Point,
	tolerance: number,
): CanvasItem | null {
	for (let i = doc.notes.length - 1; i >= 0; i--) {
		const n = doc.notes[i];
		if (n && n.canvas === canvas && hitsNote(doc, n, p, tolerance))
			return { type: "note", value: n };
	}
	for (let i = doc.pages.length - 1; i >= 0; i--) {
		const pg = doc.pages[i];
		if (pg && pg.canvas === canvas && inside(p, pg, 0))
			return { type: "page", value: pg };
	}
	return null;
}

/** 点 p 处最上层的画框（评论工具、画框内部交互用）。 */
export function pageAt(
	doc: CanvasDoc,
	canvas: string,
	p: Point,
): CanvasPage | null {
	for (let i = doc.pages.length - 1; i >= 0; i--) {
		const pg = doc.pages[i];
		if (pg && pg.canvas === canvas && inside(p, pg, 0)) return pg;
	}
	return null;
}

/** 与框选矩形相交的对象 id（按绘制顺序）。 */
export function idsInRect(doc: CanvasDoc, canvas: string, r: Box): string[] {
	const hit = (b: Box) =>
		b.x <= r.x + r.width &&
		b.x + b.width >= r.x &&
		b.y <= r.y + r.height &&
		b.y + b.height >= r.y;
	return itemsOn(doc, canvas)
		.filter((it) => hit(itemBox(doc, it)))
		.map((it) => it.value.id);
}

/** 两个点围成的矩形。 */
export function rectOf(a: Point, b: Point): Box {
	return {
		x: Math.min(a.x, b.x),
		y: Math.min(a.y, b.y),
		width: Math.abs(b.x - a.x),
		height: Math.abs(b.y - a.y),
	};
}

/**
 * 画布页里画框的阅读顺序：从上到下、同一行从左到右（顶边相差不到较矮画框一半高的算同一行）。
 * 评论锚点的 `page.number` 是画框在这里的序号（从 1 起）。
 */
export function readingOrder(doc: CanvasDoc, canvas: string): CanvasPage[] {
	const pages = doc.pages
		.filter((p) => p.canvas === canvas)
		.toSorted((a, b) => a.y - b.y || a.x - b.x);
	const rows: CanvasPage[][] = [];
	for (const p of pages) {
		const row = rows[rows.length - 1];
		const first = row?.[0];
		if (row && first && p.y - first.y < Math.min(first.height, p.height) / 2)
			row.push(p);
		else rows.push([p]);
	}
	return rows.flatMap((r) => r.toSorted((a, b) => a.x - b.x));
}

/** 被锁定的对象不能移动或缩放。 */
export function movable(doc: CanvasDoc, ids: readonly string[]): string[] {
	return ids.filter((id) => {
		const it = findItem(doc, id);
		return it !== null && !it.value.locked;
	});
}

function round2(v: number): number {
	return Math.round(v * 100) / 100;
}

/** 把对象平移 (dx, dy) 的操作。吸附了画框的线端点跟着画框走，只移动没吸附的那端。 */
function moveItem(item: CanvasItem, dx: number, dy: number): CanvasOp[] {
	const set = (key: string, value: unknown): CanvasOp => ({
		op: "set",
		id: item.value.id,
		key,
		value,
	});
	if (item.type === "page")
		return [
			set("x", round2(item.value.x + dx)),
			set("y", round2(item.value.y + dy)),
		];
	const n = item.value;
	switch (n.kind) {
		case "text":
		case "sticky":
			return [set("x", round2(n.x + dx)), set("y", round2(n.y + dy))];
		case "pen":
			return [
				set(
					"points",
					n.points.map((v, i) =>
						i % 3 === 0 ? round2(v + dx) : i % 3 === 1 ? round2(v + dy) : v,
					),
				),
			];
		default: {
			const ops: CanvasOp[] = [];
			if (n.from === null)
				ops.push(set("x1", round2(n.x1 + dx)), set("y1", round2(n.y1 + dy)));
			if (n.to === null)
				ops.push(set("x2", round2(n.x2 + dx)), set("y2", round2(n.y2 + dy)));
			return ops;
		}
	}
}

/** 平移一组对象（跳过锁定的）。 */
export function moveOps(
	doc: CanvasDoc,
	ids: readonly string[],
	dx: number,
	dy: number,
): CanvasOp[] {
	return movable(doc, ids).flatMap((id) => {
		const it = findItem(doc, id);
		return it ? moveItem(it, dx, dy) : [];
	});
}

/** 缩放手柄：四个角。 */
export type Handle = "nw" | "ne" | "sw" | "se";

/** 拖动手柄到 p 之后的矩形（对角固定；宽高不小于 min）。 */
export function resizeBox(start: Box, handle: Handle, p: Point, min = 1): Box {
	const left = handle === "nw" || handle === "sw";
	const top = handle === "nw" || handle === "ne";
	const fx = left ? start.x + start.width : start.x;
	const fy = top ? start.y + start.height : start.y;
	const w = Math.max(min, left ? fx - p.x : p.x - fx);
	const h = Math.max(min, top ? fy - p.y : p.y - fy);
	return { x: left ? fx - w : fx, y: top ? fy - h : fy, width: w, height: h };
}

/** 把对象从 from 缩放到 to 的操作。 */
export function resizeOps(
	doc: CanvasDoc,
	item: CanvasItem,
	from: Box,
	to: Box,
): CanvasOp[] {
	const id = item.value.id;
	const set = (key: string, value: unknown): CanvasOp => ({
		op: "set",
		id,
		key,
		value,
	});
	const sx = from.width === 0 ? 1 : to.width / from.width;
	const sy = from.height === 0 ? 1 : to.height / from.height;
	const mapX = (x: number) => round2(to.x + (x - from.x) * sx);
	const mapY = (y: number) => round2(to.y + (y - from.y) * sy);
	if (item.type === "page") {
		const clamp = (v: number) =>
			Math.round(Math.min(PAGE_MAX, Math.max(PAGE_MIN, v)));
		return [
			set("x", round2(to.x)),
			set("y", round2(to.y)),
			set("width", clamp(to.width)),
			set("height", clamp(to.height)),
		];
	}
	const n = item.value;
	switch (n.kind) {
		case "text": {
			const size = Math.min(
				TEXT_SIZE.max,
				Math.max(TEXT_SIZE.min, round2(n.size * sy)),
			);
			return [
				set("x", round2(to.x)),
				set("y", round2(to.y)),
				set("size", size),
			];
		}
		case "sticky":
			return [
				set("x", round2(to.x)),
				set("y", round2(to.y)),
				set(
					"width",
					Math.min(
						STICKY_WIDTH.max,
						Math.max(STICKY_WIDTH.min, round2(to.width)),
					),
				),
			];
		case "pen":
			return [
				set(
					"points",
					n.points.map((v, i) =>
						i % 3 === 0 ? mapX(v) : i % 3 === 1 ? mapY(v) : v,
					),
				),
			];
		default: {
			// 缩放线时以实际端点为准，吸附随之解除
			const e =
				n.kind === "line" || n.kind === "arrow" ? joinedEnds(n, doc.pages) : n;
			const ops: CanvasOp[] = [
				set("x1", mapX(e.x1)),
				set("y1", mapY(e.y1)),
				set("x2", mapX(e.x2)),
				set("y2", mapY(e.y2)),
			];
			if (n.from !== null) ops.push(set("from", null));
			if (n.to !== null) ops.push(set("to", null));
			return ops;
		}
	}
}

/** 删除一组对象。 */
export function deleteOps(ids: readonly string[]): CanvasOp[] {
	return ids.map((id) => ({ op: "remove", id }));
}

/**
 * 复制一组对象：画框偏移 48 并带 `copyOf`（落版本时由服务端复制它的 HTML），标注偏移 24。
 * 返回操作与新对象的 id（按原顺序）。
 */
export function duplicateOps(
	doc: CanvasDoc,
	ids: readonly string[],
): { ops: CanvasOp[]; ids: string[] } {
	const ops: CanvasOp[] = [];
	const out: string[] = [];
	for (const id of ids) {
		const it = findItem(doc, id);
		if (!it) continue;
		if (it.type === "page") {
			const nid = canvasId("p");
			const { file: _file, ...rest } = it.value;
			ops.push({
				op: "add",
				object: {
					type: "page",
					...rest,
					id: nid,
					x: round2(it.value.x + PAGE_COPY_OFFSET),
					y: round2(it.value.y + PAGE_COPY_OFFSET),
					locked: false,
				},
				copyOf: it.value.id,
			});
			out.push(nid);
			continue;
		}
		const nid = canvasId("n");
		const moved = moveItem(it, NOTE_COPY_OFFSET, NOTE_COPY_OFFSET);
		const copy: Record<string, unknown> = {
			...it.value,
			id: nid,
			locked: false,
		};
		for (const op of moved) if (op.op === "set") copy[op.key] = op.value;
		// 复制品不再吸附原来的画框：端点按实际位置写死
		if (isShape(it.value) && (it.value.from !== null || it.value.to !== null)) {
			const e = joinedEnds(it.value, doc.pages);
			Object.assign(copy, {
				x1: round2(e.x1 + NOTE_COPY_OFFSET),
				y1: round2(e.y1 + NOTE_COPY_OFFSET),
				x2: round2(e.x2 + NOTE_COPY_OFFSET),
				y2: round2(e.y2 + NOTE_COPY_OFFSET),
				from: null,
				to: null,
			});
		}
		ops.push({ op: "add", object: { type: "note", ...copy, by: null } });
		out.push(nid);
	}
	return { ops, ids: out };
}

/** 点到画框边框的距离（在框外按到矩形的距离，框内按到最近一条边）。 */
export function edgeDistance(p: Point, b: Box): number {
	const dx = Math.max(b.x - p.x, 0, p.x - (b.x + b.width));
	const dy = Math.max(b.y - p.y, 0, p.y - (b.y + b.height));
	if (dx > 0 || dy > 0) return Math.hypot(dx, dy);
	return Math.min(
		p.x - b.x,
		b.x + b.width - p.x,
		p.y - b.y,
		b.y + b.height - p.y,
	);
}

/** 线与箭头的端点吸附：落在某个画框边框 tolerance 之内时返回它（最上层的）。 */
export function attachAt(
	doc: CanvasDoc,
	canvas: string,
	p: Point,
	tolerance: number,
): CanvasPage | null {
	for (let i = doc.pages.length - 1; i >= 0; i--) {
		const pg = doc.pages[i];
		if (pg && pg.canvas === canvas && edgeDistance(p, pg) <= tolerance)
			return pg;
	}
	return null;
}
