/**
 * 移动与缩放时的吸附（计划 M14 6.3）：对其它画框的左 / 中 / 右与上 / 中 / 下线吸附，阈值是屏幕像素
 * （`SNAP_PX`，调用方按镜头换算成世界单位）。命中时给出参考线。纯函数。
 */
import type { Box } from "../../../lib/artifacts/canvas-doc";

/** 吸附阈值（屏幕像素）。 */
export const SNAP_PX = 6;

/** 一条参考线：`axis = "x"` 是竖线（世界坐标 x = pos，从 from 到 to 的 y 范围），`"y"` 是横线。 */
export interface Guide {
	axis: "x" | "y";
	pos: number;
	from: number;
	to: number;
}

export interface Snap {
	/** 吸附后要额外移动的距离（世界单位）。 */
	dx: number;
	dy: number;
	guides: Guide[];
}

/** 矩形的哪几条线参与吸附：start 左 / 上，mid 中线，end 右 / 下。 */
export type Edge = "start" | "mid" | "end";

const ALL: readonly Edge[] = ["start", "mid", "end"];

function lines(b: Box, axis: "x" | "y"): Record<Edge, number> {
	const s = axis === "x" ? b.x : b.y;
	const len = axis === "x" ? b.width : b.height;
	return { start: s, mid: s + len / 2, end: s + len };
}

interface AxisHit {
	delta: number;
	pos: number;
}

/** 一个轴上距离最近、且不超过 threshold 的对齐；没有时 null。 */
function nearest(
	box: Box,
	others: readonly Box[],
	axis: "x" | "y",
	edges: readonly Edge[],
	threshold: number,
): AxisHit | null {
	const mine = lines(box, axis);
	let best: AxisHit | null = null;
	for (const o of others) {
		const theirs = lines(o, axis);
		for (const e of edges) {
			for (const t of ALL) {
				const delta = theirs[t] - mine[e];
				if (Math.abs(delta) > threshold) continue;
				if (!best || Math.abs(delta) < Math.abs(best.delta))
					best = { delta, pos: theirs[t] };
			}
		}
	}
	return best;
}

/** 吸附后落在 pos 上、与之对齐的全部矩形连成的参考线范围。 */
function guideFor(
	axis: "x" | "y",
	pos: number,
	box: Box,
	others: readonly Box[],
): Guide {
	const across = (b: Box): [number, number] =>
		axis === "x" ? [b.y, b.y + b.height] : [b.x, b.x + b.width];
	let [from, to] = across(box);
	for (const o of others) {
		const l = lines(o, axis);
		if (
			Math.abs(l.start - pos) < 1e-6 ||
			Math.abs(l.mid - pos) < 1e-6 ||
			Math.abs(l.end - pos) < 1e-6
		) {
			const [a, b] = across(o);
			from = Math.min(from, a);
			to = Math.max(to, b);
		}
	}
	return { axis, pos, from, to };
}

/**
 * box 按 edges 指定的线去吸附 others：每个轴取最近的一个对齐（距离 ≤ threshold，世界单位）。
 * 移动时两个轴都用三条线；缩放时只用被拖动的那条边。
 */
export function snapBox(
	box: Box,
	others: readonly Box[],
	threshold: number,
	edges: { x: readonly Edge[]; y: readonly Edge[] } = { x: ALL, y: ALL },
): Snap {
	const hx = edges.x.length
		? nearest(box, others, "x", edges.x, threshold)
		: null;
	const hy = edges.y.length
		? nearest(box, others, "y", edges.y, threshold)
		: null;
	const dx = hx?.delta ?? 0;
	const dy = hy?.delta ?? 0;
	const moved = { ...box, x: box.x + dx, y: box.y + dy };
	const guides: Guide[] = [];
	if (hx) guides.push(guideFor("x", hx.pos, moved, others));
	if (hy) guides.push(guideFor("y", hy.pos, moved, others));
	return { dx, dy, guides };
}
