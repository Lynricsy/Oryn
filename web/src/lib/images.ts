/**
 * 消息图片压缩与预算（计划 H5.3）：每张图压到长边 ≤ 2000px 的 WebP（不支持时 JPEG）；
 * 整条消息内联图片（base64 后）总量 ≤ 900KB，超出时拒绝并提示用户改小。
 */

export const MAX_LONG_SIDE = 2000;
export const MESSAGE_IMAGE_BUDGET = 900 * 1024;
const QUALITY_STEPS = [0.85, 0.7, 0.55] as const;

export interface PreparedImage {
	type: "image";
	/** base64，不含 data: 前缀 */
	data: string;
	mimeType: string;
	width: number;
	height: number;
	/** base64 文本长度（即内联占用的字节数） */
	inlineBytes: number;
	name: string;
}

/** 等比缩放到长边不超过 max。 */
export function fitWithin(
	width: number,
	height: number,
	max = MAX_LONG_SIDE,
): { width: number; height: number } {
	const long = Math.max(width, height);
	if (long <= max || long === 0) return { width, height };
	const scale = max / long;
	return {
		width: Math.max(1, Math.round(width * scale)),
		height: Math.max(1, Math.round(height * scale)),
	};
}

export interface BudgetCheck {
	ok: boolean;
	total: number;
	remaining: number;
}

/** 已有图片占用 + 新图片占用是否仍在整条消息预算内。 */
export function checkBudget(
	existing: readonly number[],
	add: number,
	budget = MESSAGE_IMAGE_BUDGET,
): BudgetCheck {
	const used = existing.reduce((a, b) => a + b, 0);
	const total = used + add;
	return { ok: total <= budget, total, remaining: Math.max(0, budget - used) };
}

function blobToBase64(blob: Blob): Promise<string> {
	return blob.arrayBuffer().then((buf) => {
		const bytes = new Uint8Array(buf);
		let bin = "";
		for (let i = 0; i < bytes.length; i += 0x8000)
			bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
		return btoa(bin);
	});
}

function canvasToBlob(
	canvas: HTMLCanvasElement,
	type: string,
	quality: number,
): Promise<Blob | null> {
	return new Promise((resolve) => canvas.toBlob(resolve, type, quality));
}

export class ImageBudgetError extends Error {
	readonly needed: number;
	readonly remaining: number;
	constructor(needed: number, remaining: number) {
		super("image over budget");
		this.name = "ImageBudgetError";
		this.needed = needed;
		this.remaining = remaining;
	}
}

/**
 * 压缩一张图片以装进剩余预算：先 WebP（浏览器不支持时得到的不是 webp，则改 JPEG），质量逐级降低。
 * 装不下抛 ImageBudgetError。
 */
export async function prepareImage(
	file: Blob,
	name: string,
	remaining: number,
): Promise<PreparedImage> {
	const bitmap = await createImageBitmap(file);
	const { width, height } = fitWithin(bitmap.width, bitmap.height);
	const canvas = document.createElement("canvas");
	canvas.width = width;
	canvas.height = height;
	const ctx = canvas.getContext("2d");
	if (!ctx) throw new Error("canvas 2d unavailable");
	ctx.drawImage(bitmap, 0, 0, width, height);
	bitmap.close();
	let mimeType = "image/webp";
	let smallest = Number.POSITIVE_INFINITY;
	for (const q of QUALITY_STEPS) {
		let blob = await canvasToBlob(canvas, mimeType, q);
		if (!blob || blob.type !== mimeType) {
			mimeType = "image/jpeg";
			blob = await canvasToBlob(canvas, mimeType, q);
		}
		if (!blob) continue;
		const data = await blobToBase64(blob);
		smallest = Math.min(smallest, data.length);
		if (data.length <= remaining) {
			return {
				type: "image",
				data,
				mimeType,
				width,
				height,
				inlineBytes: data.length,
				name,
			};
		}
	}
	throw new ImageBudgetError(smallest, remaining);
}
