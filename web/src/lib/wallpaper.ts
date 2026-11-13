/**
 * 自定义壁纸：把上传的图片等比缩到长边 640px，做一次强高斯模糊，编码成 webp data URL 存进 localStorage。
 * 存 data URL 是为了让 `public/theme-init.js` 在首帧前同步读到，避免闪色；体积压在 100KB 内，超了就降尺寸重试。
 * 模糊走 Canvas filter（先四边镜像外扩、模糊后裁回原尺寸，边缘采到镜像内容而不是透明，不发灰）；
 * Safari 默认不支持 Canvas filter，检测不通过时改在像素数组上跑 CPU 后备（3 次盒式模糊逼近高斯，同样镜像边缘）。
 * 另外量一下模糊结果的亮度分布（感知亮度的 10% / 90% 分位），index.css 据此加厚壁纸上的主题色纱。
 */

export const WALLPAPER_IMAGE_KEY = "oryn.wallpaper.image";
export const WALLPAPER_MAX_BYTES = 100 * 1024;
/** 长边逐级降低：首选 640，体积超限时依次重试 */
export const WALLPAPER_EDGES = [640, 512, 400, 320, 240] as const;
/** 模糊半径占缩放后长边的比例（640 → 38px） */
const BLUR_RATIO = 0.06;
const QUALITY = 0.8;

/** 亮度分位：[暗部 p10, 亮部 p90]，取值 0–1（OKLab 式感知亮度） */
export type WallpaperTone = [number, number];

export interface ProcessedWallpaper {
	url: string;
	tone: WallpaperTone;
	width: number;
	height: number;
}

/** 降到最小尺寸仍超过上限 */
export class WallpaperTooLargeError extends Error {
	constructor() {
		super("wallpaper image too large");
		this.name = "WallpaperTooLargeError";
	}
}

/** localStorage 写入失败（配额满、隐私模式） */
export class WallpaperStorageError extends Error {
	constructor(cause?: unknown) {
		super("wallpaper image could not be stored", { cause });
		this.name = "WallpaperStorageError";
	}
}

/** 等比缩放到长边 = edge（小图同样放大：反正要强模糊，统一尺寸让模糊半径一致）。 */
export function fitSize(
	width: number,
	height: number,
	edge: number,
): { width: number; height: number } {
	const k = edge / Math.max(width, height, 1);
	return {
		width: Math.max(1, Math.round(width * k)),
		height: Math.max(1, Math.round(height * k)),
	};
}

type Source = CanvasImageSource & { width: number; height: number };

function canvas2d(
	width: number,
	height: number,
): [HTMLCanvasElement, CanvasRenderingContext2D] {
	const c = document.createElement("canvas");
	c.width = width;
	c.height = height;
	const ctx = c.getContext("2d", { willReadFrequently: true });
	if (!ctx) throw new Error("canvas 2d unavailable");
	return [c, ctx];
}

/**
 * Canvas 的 `ctx.filter` 能否真的模糊。Safari（17.5 起到 18）默认没有这个特性：
 * 赋值不报错（只在对象上多一个普通属性，读回来也一样），图片却原样画出，所以要画一个点验像素。
 */
export function canvasBlurWorks(): boolean {
	const [dot, dctx] = canvas2d(9, 9);
	dctx.fillStyle = "#fff";
	dctx.fillRect(4, 4, 1, 1);
	const [, ctx] = canvas2d(9, 9);
	ctx.filter = "blur(2px)";
	if (ctx.filter !== "blur(2px)") return false;
	ctx.drawImage(dot, 0, 0);
	// 距中心 2px 处：模糊生效时有几分透明度，没生效时完全透明
	return (ctx.getImageData(2, 4, 1, 1).data[3] ?? 0) > 0;
}

/** 下标越界时按镜像折回（…2 1 0 | 0 1 2 … n-1 | n-1 n-2 …），可多次折返 */
function mirror(i: number, len: number): number {
	const period = 2 * len;
	const k = ((i % period) + period) % period;
	return k < len ? k : period - 1 - k;
}

/** 三次盒式模糊逼近标准差为 sigma 的高斯：返回三个盒宽（奇数）。 */
function gaussBoxes(sigma: number): number[] {
	const n = 3;
	let lo = Math.floor(Math.sqrt((12 * sigma * sigma) / n + 1));
	if (lo % 2 === 0) lo--;
	const m = Math.round(
		(12 * sigma * sigma - n * lo * lo - 4 * n * lo - 3 * n) / (-4 * lo - 4),
	);
	return Array.from({ length: n }, (_, i) => (i < m ? lo : lo + 2));
}

/** 一条线（行或列）上的盒式模糊，滑动窗口求和 O(len)；越界取镜像。 */
function boxLine(
	src: Float32Array,
	dst: Float32Array,
	start: number,
	stride: number,
	len: number,
	r: number,
): void {
	const n = 2 * r + 1;
	for (let c = 0; c < 4; c++) {
		let sum = 0;
		for (let k = -r; k <= r; k++)
			sum += src[start + mirror(k, len) * stride + c] ?? 0;
		for (let i = 0; i < len; i++) {
			dst[start + i * stride + c] = sum / n;
			sum +=
				(src[start + mirror(i + r + 1, len) * stride + c] ?? 0) -
				(src[start + mirror(i - r, len) * stride + c] ?? 0);
		}
	}
}

/**
 * CPU 后备的高斯模糊（RGBA 原地修改）：水平、垂直各 3 次盒式模糊，边缘镜像填充，四角不发灰。
 * 给 Canvas filter 不可用的浏览器用；640px 的图约几十毫秒。
 */
export function blurPixels(
	data: Uint8ClampedArray,
	width: number,
	height: number,
	sigma: number,
): void {
	let a = Float32Array.from(data);
	let b = new Float32Array(data.length);
	for (const size of gaussBoxes(sigma)) {
		const r = (size - 1) / 2;
		for (let y = 0; y < height; y++) boxLine(a, b, y * width * 4, 4, width, r);
		[a, b] = [b, a];
		for (let x = 0; x < width; x++) boxLine(a, b, x * 4, width * 4, height, r);
		[a, b] = [b, a];
	}
	data.set(a);
}

/**
 * 缩放 → 强模糊。Canvas filter 可用时：四边镜像外扩 → filter 模糊 → 裁回原尺寸；
 * 不可用时在像素数组上跑 CPU 后备（自带镜像边缘）。
 */
function blurAt(src: Source, edge: number, gpu: boolean): HTMLCanvasElement {
	const { width: w, height: h } = fitSize(src.width, src.height, edge);
	const radius = Math.round(Math.max(w, h) * BLUR_RATIO);
	const [scaled, sctx] = canvas2d(w, h);
	sctx.imageSmoothingQuality = "high";
	sctx.drawImage(src, 0, 0, w, h);
	if (!gpu) {
		const img = sctx.getImageData(0, 0, w, h);
		blurPixels(img.data, w, h, radius);
		sctx.putImageData(img, 0, 0);
		return scaled;
	}
	// 外扩 3σ：高斯核在这个距离外的权重可以忽略；不超过图片自身尺寸，镜像一次就够
	const pad = Math.min(radius * 3, w, h);
	const [padded, pctx] = canvas2d(w + 2 * pad, h + 2 * pad);
	for (const i of [-1, 0, 1])
		for (const j of [-1, 0, 1]) {
			// i/j = -1 / 1：左右 / 上下的镜像副本；0：原图居中
			pctx.setTransform(
				i ? -1 : 1,
				0,
				0,
				j ? -1 : 1,
				pad + (i === 1 ? 2 * w : 0),
				pad + (j === 1 ? 2 * h : 0),
			);
			pctx.drawImage(scaled, 0, 0, w, h);
		}
	const [out, octx] = canvas2d(w, h);
	octx.filter = `blur(${radius}px)`;
	octx.drawImage(padded, -pad, -pad);
	return out;
}

/** 模糊结果的感知亮度分位（缩到 32px 宽采样，足够代表大块色面）。 */
function measureTone(c: HTMLCanvasElement): WallpaperTone {
	const { width: w, height: h } = fitSize(c.width, c.height, 32);
	const [, ctx] = canvas2d(w, h);
	ctx.drawImage(c, 0, 0, w, h);
	const d = ctx.getImageData(0, 0, w, h).data;
	const lin = (v: number) => {
		const s = v / 255;
		return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
	};
	const ls: number[] = [];
	for (let i = 0; i < d.length; i += 4)
		ls.push(
			Math.cbrt(
				0.2126 * lin(d[i] ?? 0) +
					0.7152 * lin(d[i + 1] ?? 0) +
					0.0722 * lin(d[i + 2] ?? 0),
			),
		);
	ls.sort((a, b) => a - b);
	const at = (p: number) =>
		Math.round((ls[Math.floor((ls.length - 1) * p)] ?? 0.5) * 1000) / 1000;
	return [at(0.1), at(0.9)];
}

function encode(c: HTMLCanvasElement): string {
	const url = c.toDataURL("image/webp", QUALITY);
	// 不支持 webp 编码的浏览器会退回 png，改用 jpeg
	return url.startsWith("data:image/webp")
		? url
		: c.toDataURL("image/jpeg", QUALITY);
}

async function decode(file: Blob): Promise<Source> {
	if (typeof createImageBitmap === "function") return createImageBitmap(file);
	const url = URL.createObjectURL(file);
	try {
		const img = new Image();
		img.src = url;
		await img.decode();
		return img;
	} finally {
		URL.revokeObjectURL(url);
	}
}

export async function processWallpaper(
	file: Blob,
): Promise<ProcessedWallpaper> {
	const src = await decode(file);
	const gpu = canvasBlurWorks();
	try {
		for (const edge of WALLPAPER_EDGES) {
			const c = blurAt(src, edge, gpu);
			const url = encode(c);
			if (url.length <= WALLPAPER_MAX_BYTES)
				return { url, tone: measureTone(c), width: c.width, height: c.height };
		}
	} finally {
		if ("close" in src && typeof src.close === "function") src.close();
	}
	throw new WallpaperTooLargeError();
}

export function readWallpaperImage(): string | null {
	try {
		const v = globalThis.localStorage?.getItem(WALLPAPER_IMAGE_KEY) ?? null;
		return v?.startsWith("data:image/") ? v : null;
	} catch {
		return null;
	}
}

/** 写入失败（配额满等）时抛出 WallpaperStorageError；localStorage 里原有的图片保持不变。 */
export function storeWallpaperImage(url: string): void {
	try {
		localStorage.setItem(WALLPAPER_IMAGE_KEY, url);
	} catch (err) {
		throw new WallpaperStorageError(err);
	}
}

export function clearWallpaperImage(): void {
	try {
		localStorage.removeItem(WALLPAPER_IMAGE_KEY);
	} catch {
		// 隐私模式下静默
	}
}
