/**
 * Word / PowerPoint 导出（计划 M14 5.4）共用的图片处理：两种格式都只稳妥支持 PNG、JPEG、GIF。
 * 这三种直接从文件头读出尺寸；其它格式（幻灯片的 `images/` 不限格式：WebP、SVG、AVIF…）在浏览器里用 `<img>` 解码、
 * 画到 canvas 上转成 PNG，转不了的由调用方画占位框。
 */

export type RasterType = "png" | "jpg" | "gif";

export interface RasterImage {
	data: Uint8Array;
	type: RasterType;
	/** 像素尺寸。 */
	width: number;
	height: number;
}

/** 从文件头认出 PNG / JPEG / GIF 并读出像素尺寸；不是这三种或文件头不完整时为 `null`。 */
export function rasterInfo(
	b: Uint8Array,
): { type: RasterType; width: number; height: number } | null {
	const v = new DataView(b.buffer, b.byteOffset, b.byteLength);
	const n = b.length;
	if (n >= 24 && v.getUint32(0) === 0x89504e47) {
		const width = v.getUint32(16);
		const height = v.getUint32(20);
		return width && height ? { type: "png", width, height } : null;
	}
	if (n >= 10 && v.getUint16(0) === 0x4749 && v.getUint8(2) === 0x46) {
		const width = v.getUint16(6, true);
		const height = v.getUint16(8, true);
		return width && height ? { type: "gif", width, height } : null;
	}
	if (n >= 4 && v.getUint16(0) === 0xffd8) {
		// 逐段跳过，直到帧头（SOF0–SOF15，除去 DHT C4、JPG C8、DAC CC）
		let i = 2;
		while (i + 3 < n) {
			if (v.getUint8(i) !== 0xff) return null;
			const marker = v.getUint8(i + 1);
			if (marker === 0xff) {
				i++;
				continue;
			}
			if (marker === 0x01 || (marker >= 0xd0 && marker <= 0xd8)) {
				i += 2;
				continue;
			}
			if (
				marker >= 0xc0 &&
				marker <= 0xcf &&
				marker !== 0xc4 &&
				marker !== 0xc8 &&
				marker !== 0xcc
			) {
				if (i + 8 >= n) return null;
				const height = v.getUint16(i + 5);
				const width = v.getUint16(i + 7);
				return width && height ? { type: "jpg", width, height } : null;
			}
			i += 2 + v.getUint16(i + 2);
		}
	}
	return null;
}

/** 浏览器解码需要 MIME 的格式（SVG 必须带 `image/svg+xml` 才能当图片加载）。 */
const MIME_BY_EXT: Record<string, string> = {
	svg: "image/svg+xml",
	webp: "image/webp",
	avif: "image/avif",
	bmp: "image/bmp",
	ico: "image/x-icon",
	png: "image/png",
	jpg: "image/jpeg",
	jpeg: "image/jpeg",
	gif: "image/gif",
};

/** 没有固有尺寸的图（只写了 viewBox 的 SVG）按这个尺寸栅格化。 */
const FALLBACK_SIZE = { width: 1600, height: 1200 };
/** 栅格化的长边上限（像素）：再大只是让文件变胖。 */
const MAX_RASTER_SIDE = 3200;

/**
 * 把一张图变成 Office 能嵌入的格式：PNG / JPEG / GIF 原样返回；其它格式在浏览器里转成 PNG。
 * 解码失败（坏文件、浏览器不认识的格式、没有 canvas）回 `null`。
 */
export async function toRaster(
	bytes: Uint8Array,
	path: string,
): Promise<RasterImage | null> {
	const info = rasterInfo(bytes);
	if (info) return { data: bytes, ...info };
	if (typeof document === "undefined" || typeof Image === "undefined")
		return null;
	const ext = path.slice(path.lastIndexOf(".") + 1).toLowerCase();
	const url = URL.createObjectURL(
		new Blob([bytes as BlobPart], { type: MIME_BY_EXT[ext] ?? "" }),
	);
	try {
		const img = new Image();
		img.src = url;
		await img.decode();
		let width = img.naturalWidth || FALLBACK_SIZE.width;
		let height = img.naturalHeight || FALLBACK_SIZE.height;
		const k = Math.min(1, MAX_RASTER_SIDE / Math.max(width, height));
		width = Math.max(1, Math.round(width * k));
		height = Math.max(1, Math.round(height * k));
		const canvas = document.createElement("canvas");
		canvas.width = width;
		canvas.height = height;
		const ctx = canvas.getContext("2d");
		if (!ctx) return null;
		ctx.drawImage(img, 0, 0, width, height);
		const png = await new Promise<Blob | null>((resolve) =>
			canvas.toBlob(resolve, "image/png"),
		);
		if (!png) return null;
		return {
			data: new Uint8Array(await png.arrayBuffer()),
			type: "png",
			width,
			height,
		};
	} catch {
		return null;
	} finally {
		URL.revokeObjectURL(url);
	}
}

/** Uint8Array → base64（分块调用 `btoa`，避免大图撑爆参数个数）。 */
export function toBase64(bytes: Uint8Array): string {
	let bin = "";
	for (let i = 0; i < bytes.length; i += 0x8000)
		bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
	return btoa(bin);
}

/** 版本里的相对路径：去掉开头的 `./`，解开百分号编码（解不开就用原样）。 */
export function assetPath(src: string): string {
	const p = src.replace(/^(\.\/)+/, "");
	try {
		return decodeURI(p);
	} catch {
		return p;
	}
}
