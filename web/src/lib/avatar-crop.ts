/**
 * 头像裁剪的取景框数学（纯函数）：取景框是源图上的一个正方形，用源图像素坐标表示。
 * 图不动框动：拖动改位置，缩放改边长；任何操作之后都夹回图内，边长在 [minCropSize, 短边] 之间。
 */

export interface Crop {
	x: number;
	y: number;
	size: number;
}

export interface Dim {
	w: number;
	h: number;
}

/** 最大放大倍数：取景框边长最小 = 短边 / MAX_ZOOM。 */
export const MAX_ZOOM = 8;

/** 取景框四角：拖动某角时对角不动。 */
export type Corner = "nw" | "ne" | "sw" | "se";

const clamp = (v: number, lo: number, hi: number) =>
	Math.min(Math.max(v, lo), hi);

const shortSide = (d: Dim) => Math.min(d.w, d.h);

/** 取景框的最小边长：短边 / MAX_ZOOM，但不小于 32px（短边本身不足 32px 时就是短边）。 */
export function minCropSize(d: Dim): number {
	const s = shortSide(d);
	return Math.max(s / MAX_ZOOM, Math.min(32, s));
}

/** 居中的最大正方形。 */
export function initialCrop(d: Dim): Crop {
	const s = shortSide(d);
	return { x: (d.w - s) / 2, y: (d.h - s) / 2, size: s };
}

/** 边长夹到 [minCropSize, 短边]，位置夹到图内。 */
export function clampCrop(c: Crop, d: Dim): Crop {
	const size = clamp(c.size, minCropSize(d), shortSide(d));
	return {
		x: clamp(c.x, 0, d.w - size),
		y: clamp(c.y, 0, d.h - size),
		size,
	};
}

export function moveCrop(c: Crop, dx: number, dy: number, d: Dim): Crop {
	return clampCrop({ x: c.x + dx, y: c.y + dy, size: c.size }, d);
}

/** 以 (ax, ay)（源图坐标）为不动点把边长乘以 factor。 */
export function zoomCrop(
	c: Crop,
	factor: number,
	ax: number,
	ay: number,
	d: Dim,
): Crop {
	const size = clamp(c.size * factor, minCropSize(d), shortSide(d));
	const f = size / c.size;
	return clampCrop({ x: ax - (ax - c.x) * f, y: ay - (ay - c.y) * f, size }, d);
}

/** 当前放大倍数：短边 / 边长，∈ [1, MAX_ZOOM]。 */
export function zoomOf(c: Crop, d: Dim): number {
	return shortSide(d) / c.size;
}

/** 以取景框中心为不动点缩放到指定倍数。 */
export function cropAtZoom(c: Crop, zoom: number, d: Dim): Crop {
	const size = shortSide(d) / clamp(zoom, 1, MAX_ZOOM);
	return zoomCrop(c, size / c.size, c.x + c.size / 2, c.y + c.size / 2, d);
}

/**
 * 拖动某个角到 (px, py)（源图坐标）：对角不动，边长取指针到对角在两个方向上距离的较大者，
 * 再夹到 [minCropSize, 对角往该方向还剩的空间]。
 */
export function resizeFromCorner(
	c: Crop,
	corner: Corner,
	px: number,
	py: number,
	d: Dim,
): Crop {
	const east = corner === "ne" || corner === "se";
	const south = corner === "sw" || corner === "se";
	const ax = east ? c.x : c.x + c.size;
	const ay = south ? c.y : c.y + c.size;
	const roomX = east ? d.w - ax : ax;
	const roomY = south ? d.h - ay : ay;
	const want = Math.max(east ? px - ax : ax - px, south ? py - ay : ay - py);
	const size = clamp(want, minCropSize(d), Math.min(roomX, roomY));
	return clampCrop(
		{ x: east ? ax : ax - size, y: south ? ay : ay - size, size },
		d,
	);
}
