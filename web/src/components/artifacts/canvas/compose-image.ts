/**
 * 「交给 Agent」的附图（计划 M14 6.6）：控制面渲染的画框 PNG 上叠画压在它上面的标注（笔迹、便签底色与文字、形状与箭头）。
 *
 * 换算与取舍是纯函数（`shotSize`、`pageTransform`、`notesOver`、`wrapText`），绘制本身（`composeFrameImage`）
 * 只在浏览器里跑：离屏 `<canvas>` 以画布的世界坐标作画，变换把画框左上角移到原点、按截图宽度缩放，
 * 画框外的部分自然被裁掉。标注的颜色取 canvas.css 里浅色的那一套（截图是页面本身，通常是浅底）。
 */
import {
	type Box,
	type CanvasNote,
	type CanvasPage,
	joinedEnds,
	type NoteColor,
	noteBox,
} from "../../../lib/artifacts/canvas-doc";
import { strokePath } from "../../../lib/artifacts/stroke";
import { arrowHead } from "./notes";

/** 附图的最大宽度：更宽的画框按比例请求这个宽度。 */
export const SHOT_MAX_WIDTH = 1600;
/** 渲染接口的尺寸范围。 */
const SHOT_MIN = 64;
const SHOT_MAX = 8000;

/** 标注颜色：与 canvas.css 浅色主题下的 `--note-*` 相同。 */
export const NOTE_RGB: Record<NoteColor, string> = {
	ink: "#18181b",
	red: "#dc2626",
	orange: "#ea580c",
	yellow: "#ca8a04",
	green: "#16a34a",
	blue: "#2563eb",
	violet: "#7c3aed",
};

/** 便签文字的排版（与 canvas.css 的 `.sticky-text` 相同）。 */
const STICKY_PAD = 12;
const STICKY_FONT = 16;
const STICKY_LINE = 1.4;
const FONT_STACK = "system-ui, -apple-system, 'Segoe UI', sans-serif";

function clamp(v: number): number {
	return Math.min(SHOT_MAX, Math.max(SHOT_MIN, Math.round(v)));
}

/** 向渲染接口请求的截图尺寸：宽超过 1600 时按比例缩到 1600 宽。 */
export function shotSize(page: Pick<CanvasPage, "width" | "height">): {
	width: number;
	height: number;
} {
	if (page.width <= SHOT_MAX_WIDTH)
		return { width: clamp(page.width), height: clamp(page.height) };
	const k = SHOT_MAX_WIDTH / page.width;
	return { width: SHOT_MAX_WIDTH, height: clamp(page.height * k) };
}

/** 世界坐标 → 截图像素的变换：`x' = x·scale + dx`、`y' = y·scale + dy`。 */
export function pageTransform(
	page: Pick<CanvasPage, "x" | "y" | "width">,
	imageWidth: number,
): { scale: number; dx: number; dy: number } {
	const scale = imageWidth / page.width;
	return { scale, dx: -page.x * scale, dy: -page.y * scale };
}

function overlaps(a: Box, b: Box): boolean {
	return (
		a.x <= b.x + b.width &&
		a.x + a.width >= b.x &&
		a.y <= b.y + b.height &&
		a.y + a.height >= b.y
	);
}

/** 画在这个画框截图上的标注：同一画布页、包围盒与画框相交的（保持绘制顺序）。 */
export function notesOver(
	page: CanvasPage,
	notes: readonly CanvasNote[],
	pages: readonly CanvasPage[],
): CanvasNote[] {
	return notes.filter((n) => {
		if (n.canvas !== page.canvas) return false;
		let box = noteBox(n);
		// 吸附的线与箭头按实际端点算
		if (n.kind === "line" || n.kind === "arrow") {
			const e = joinedEnds(n, pages);
			box = {
				x: Math.min(e.x1, e.x2),
				y: Math.min(e.y1, e.y2),
				width: Math.abs(e.x2 - e.x1),
				height: Math.abs(e.y2 - e.y1),
			};
		}
		return overlaps(box, page);
	});
}

/**
 * 按宽度折行：先按 `\n` 分段，段内按字符累加，超宽就换行（英文单词优先在空格处断，与 `overflow-wrap: anywhere` 的效果接近）。
 * `measure` 返回一段文字的宽度。
 */
export function wrapText(
	text: string,
	maxWidth: number,
	measure: (s: string) => number,
): string[] {
	const out: string[] = [];
	for (const para of text.split("\n")) {
		let line = "";
		for (const ch of Array.from(para)) {
			const next = line + ch;
			if (line === "" || measure(next) <= maxWidth) {
				line = next;
				continue;
			}
			const space = line.lastIndexOf(" ");
			if (ch !== " " && space > 0) {
				out.push(line.slice(0, space));
				line = line.slice(space + 1) + ch;
			} else {
				out.push(line);
				line = ch === " " ? "" : ch;
			}
		}
		out.push(line);
	}
	return out;
}

function drawNote(
	ctx: CanvasRenderingContext2D,
	note: CanvasNote,
	pages: readonly CanvasPage[],
): void {
	const color = NOTE_RGB[note.color];
	ctx.save();
	ctx.strokeStyle = color;
	ctx.fillStyle = color;
	ctx.lineCap = "round";
	ctx.lineJoin = "round";
	switch (note.kind) {
		case "text": {
			ctx.font = `${note.size}px ${FONT_STACK}`;
			ctx.textBaseline = "alphabetic";
			note.text.split("\n").forEach((line, i) => {
				ctx.fillText(line, note.x, note.y + note.size + i * note.size * 1.3);
			});
			break;
		}
		case "sticky": {
			const b = noteBox(note);
			if (note.done) ctx.globalAlpha = 0.5;
			const base = ctx.globalAlpha;
			ctx.beginPath();
			ctx.roundRect(b.x, b.y, b.width, b.height, 6);
			ctx.globalAlpha = base * 0.25;
			ctx.fill();
			ctx.globalAlpha = base * 0.5;
			ctx.stroke();
			ctx.globalAlpha = base;
			ctx.beginPath();
			ctx.rect(b.x, b.y, b.width, b.height);
			ctx.clip();
			ctx.fillStyle = NOTE_RGB.ink;
			ctx.font = `${STICKY_FONT}px ${FONT_STACK}`;
			ctx.textBaseline = "top";
			const lineHeight = STICKY_FONT * STICKY_LINE;
			const lines = wrapText(
				note.text,
				b.width - 2 * STICKY_PAD,
				(s) => ctx.measureText(s).width,
			);
			lines.forEach((line, i) => {
				ctx.fillText(
					line,
					b.x + STICKY_PAD,
					b.y + STICKY_PAD + i * lineHeight + (lineHeight - STICKY_FONT) / 2,
				);
			});
			break;
		}
		case "pen":
			ctx.lineWidth = note.width;
			if (note.marker) ctx.globalAlpha = 0.35;
			ctx.stroke(new Path2D(strokePath(note.points)));
			break;
		case "rect":
		case "ellipse": {
			const b = noteBox(note);
			ctx.lineWidth = note.width;
			ctx.beginPath();
			if (note.kind === "rect") ctx.rect(b.x, b.y, b.width, b.height);
			else
				ctx.ellipse(
					b.x + b.width / 2,
					b.y + b.height / 2,
					b.width / 2,
					b.height / 2,
					0,
					0,
					2 * Math.PI,
				);
			ctx.stroke();
			break;
		}
		default: {
			const { x1, y1, x2, y2 } = joinedEnds(note, pages);
			let d = `M${x1} ${y1}L${x2} ${y2}`;
			if (note.kind === "arrow") d += arrowHead(x1, y1, x2, y2, note.width);
			ctx.lineWidth = note.width;
			ctx.stroke(new Path2D(d));
			break;
		}
	}
	ctx.restore();
}

/** 把 `notes` 叠画到画框截图 `png` 上，导出 PNG。`pages` 是整份文档的画框（吸附端点用）。 */
export async function composeFrameImage(
	png: Blob,
	page: CanvasPage,
	notes: readonly CanvasNote[],
	pages: readonly CanvasPage[],
): Promise<Blob> {
	const bitmap = await createImageBitmap(png);
	try {
		const canvas = document.createElement("canvas");
		canvas.width = bitmap.width;
		canvas.height = bitmap.height;
		const ctx = canvas.getContext("2d");
		if (!ctx) throw new Error("canvas 2d unavailable");
		ctx.drawImage(bitmap, 0, 0);
		const { scale, dx, dy } = pageTransform(page, bitmap.width);
		ctx.setTransform(scale, 0, 0, scale, dx, dy);
		for (const n of notesOver(page, notes, pages)) drawNote(ctx, n, pages);
		return await new Promise<Blob>((resolve, reject) =>
			canvas.toBlob(
				(b) => (b ? resolve(b) : reject(new Error("toBlob failed"))),
				"image/png",
			),
		);
	} finally {
		bitmap.close();
	}
}
