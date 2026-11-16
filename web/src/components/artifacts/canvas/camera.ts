/**
 * 画布编辑器的镜头（计划 M14 6.3）：屏幕坐标 = 世界坐标 × zoom + (x, y)，坐标都相对于画布区左上角。
 * 纯函数，组件只持有一个 `Camera` 值。
 */
import type { Box } from "../../../lib/artifacts/canvas-doc";

export interface Camera {
	x: number;
	y: number;
	zoom: number;
}

export interface Point {
	x: number;
	y: number;
}

/** 缩放档（`+` / `-` 与缩放按钮在这些档之间跳）。 */
export const ZOOM_STEPS = [0.05, 0.1, 0.25, 0.5, 1, 2, 4] as const;
export const MIN_ZOOM = 0.05;
export const MAX_ZOOM = 4;
/** 适合全部 / 选中时四周留的屏幕像素。 */
export const FIT_PAD = 64;

export const IDENTITY: Camera = { x: 0, y: 0, zoom: 1 };

export function clampZoom(zoom: number): number {
	if (!Number.isFinite(zoom)) return 1;
	return Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, zoom));
}

export function toWorld(cam: Camera, sx: number, sy: number): Point {
	return { x: (sx - cam.x) / cam.zoom, y: (sy - cam.y) / cam.zoom };
}

export function toScreen(cam: Camera, wx: number, wy: number): Point {
	return { x: wx * cam.zoom + cam.x, y: wy * cam.zoom + cam.y };
}

/** 以屏幕点 (px, py) 为中心缩放到 zoom：该点下的世界坐标不变。 */
export function zoomAt(
	cam: Camera,
	zoom: number,
	px: number,
	py: number,
): Camera {
	const z = clampZoom(zoom);
	const w = toWorld(cam, px, py);
	return { x: px - w.x * z, y: py - w.y * z, zoom: z };
}

/** 平移屏幕像素。 */
export function panBy(cam: Camera, dx: number, dy: number): Camera {
	return { x: cam.x + dx, y: cam.y + dy, zoom: cam.zoom };
}

/** 下一档（dir = 1 放大，-1 缩小），以画布区中心为中心。已经在两档之间时跳到相邻的那一档。 */
export function stepZoom(
	cam: Camera,
	dir: 1 | -1,
	width: number,
	height: number,
): Camera {
	const eps = 1e-6;
	const next =
		dir > 0
			? ZOOM_STEPS.find((z) => z > cam.zoom + eps)
			: [...ZOOM_STEPS].reverse().find((z) => z < cam.zoom - eps);
	return zoomAt(cam, next ?? cam.zoom, width / 2, height / 2);
}

/** 把 box 整个放进 width × height 的画布区并居中；不放大到超过 maxZoom。box 为空时回到原点 100%。 */
export function fitBox(
	box: Box | null,
	width: number,
	height: number,
	maxZoom = 1,
	pad = FIT_PAD,
): Camera {
	if (!box || width <= 0 || height <= 0)
		return { x: width / 2, y: height / 2, zoom: 1 };
	const zoom = clampZoom(
		Math.min(
			maxZoom,
			Math.max(1, width - 2 * pad) / Math.max(1, box.width),
			Math.max(1, height - 2 * pad) / Math.max(1, box.height),
		),
	);
	return {
		x: (width - box.width * zoom) / 2 - box.x * zoom,
		y: (height - box.height * zoom) / 2 - box.y * zoom,
		zoom,
	};
}

/**
 * 让 box 出现在视口里：已经完整可见时不动；放得下时只平移最少的距离（四周留 pad）；放不下时适合它。
 */
export function reveal(
	cam: Camera,
	box: Box,
	width: number,
	height: number,
	pad = FIT_PAD,
): Camera {
	const w = box.width * cam.zoom;
	const h = box.height * cam.zoom;
	if (w > width - 2 * pad || h > height - 2 * pad)
		return fitBox(box, width, height, cam.zoom, pad);
	const s = toScreen(cam, box.x, box.y);
	let dx = 0;
	let dy = 0;
	if (s.x < pad) dx = pad - s.x;
	else if (s.x + w > width - pad) dx = width - pad - (s.x + w);
	if (s.y < pad) dy = pad - s.y;
	else if (s.y + h > height - pad) dy = height - pad - (s.y + h);
	return dx === 0 && dy === 0 ? cam : panBy(cam, dx, dy);
}

/** 滚轮事件里与镜头有关的字段。 */
export interface WheelInput {
	deltaX: number;
	deltaY: number;
	/** 0 像素、1 行、2 页（与 `WheelEvent.deltaMode` 相同）。 */
	deltaMode: number;
	ctrlKey: boolean;
	metaKey: boolean;
}

const LINE_PX = 16;

/**
 * 滚轮：平移；按住 `Ctrl` / `⌘`（含触控板双指捏合，浏览器把它报成带 ctrlKey 的滚轮）以指针为中心缩放。
 * (px, py) 是指针在画布区里的位置；pageSize 是 deltaMode = 2 时一页的像素。
 */
export function wheelCamera(
	cam: Camera,
	e: WheelInput,
	px: number,
	py: number,
	pageSize = 800,
): Camera {
	const unit = e.deltaMode === 1 ? LINE_PX : e.deltaMode === 2 ? pageSize : 1;
	const dx = e.deltaX * unit;
	const dy = e.deltaY * unit;
	if (e.ctrlKey || e.metaKey)
		return zoomAt(cam, cam.zoom * Math.exp(-dy * 0.01), px, py);
	return panBy(cam, -dx, -dy);
}

/** 世界坐标里可见的矩形。 */
export function viewBox(cam: Camera, width: number, height: number): Box {
	const a = toWorld(cam, 0, 0);
	return {
		x: a.x,
		y: a.y,
		width: width / cam.zoom,
		height: height / cam.zoom,
	};
}

/** 一组矩形的外包围盒；空时 null。 */
export function unionBox(boxes: readonly Box[]): Box | null {
	if (boxes.length === 0) return null;
	let left = Number.POSITIVE_INFINITY;
	let top = Number.POSITIVE_INFINITY;
	let right = Number.NEGATIVE_INFINITY;
	let bottom = Number.NEGATIVE_INFINITY;
	for (const b of boxes) {
		left = Math.min(left, b.x);
		top = Math.min(top, b.y);
		right = Math.max(right, b.x + b.width);
		bottom = Math.max(bottom, b.y + b.height);
	}
	return { x: left, y: top, width: right - left, height: bottom - top };
}
