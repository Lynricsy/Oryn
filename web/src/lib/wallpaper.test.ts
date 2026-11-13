import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	blurPixels,
	canvasBlurWorks,
	fitSize,
	processWallpaper,
	storeWallpaperImage,
	WALLPAPER_IMAGE_KEY,
	WALLPAPER_MAX_BYTES,
	WallpaperStorageError,
	WallpaperTooLargeError,
} from "./wallpaper";

/** jsdom 没有 canvas：用假画布记下尺寸、滤镜与导出，体积按「每像素字节数」模拟 */
let bytesPerPixel = 0.1;
/** false 模拟 Safari：filter 赋值无效 */
let filterWorks = true;
let encoded: { width: number; height: number; filter: string }[] = [];
let puts = 0;

function fakeCanvas() {
	let filter = "none";
	const ctx = {
		imageSmoothingQuality: "low",
		fillStyle: "",
		get filter() {
			return filter;
		},
		set filter(v: string) {
			if (filterWorks) filter = v;
		},
		fillRect: vi.fn(),
		setTransform: vi.fn(),
		drawImage: vi.fn(),
		putImageData: () => {
			puts++;
		},
		getImageData: (_x: number, _y: number, w: number, h: number) => ({
			data: new Uint8ClampedArray(w * h * 4).fill(200),
		}),
	};
	const c = {
		width: 0,
		height: 0,
		getContext: () => ctx,
		toDataURL: (type: string) => {
			encoded.push({ width: c.width, height: c.height, filter: ctx.filter });
			const prefix = `data:${type};base64,`;
			return (
				prefix + "A".repeat(Math.round(c.width * c.height * bytesPerPixel))
			);
		},
	};
	return c;
}
beforeEach(() => {
	encoded = [];
	puts = 0;
	bytesPerPixel = 0.1;
	filterWorks = true;
	const create = document.createElement.bind(document);
	vi.spyOn(document, "createElement").mockImplementation(((tag: string) =>
		tag === "canvas"
			? (fakeCanvas() as unknown as HTMLCanvasElement)
			: create(tag)) as typeof document.createElement);
	vi.stubGlobal(
		"createImageBitmap",
		vi.fn(async () => ({ width: 4000, height: 3000, close: vi.fn() })),
	);
});

afterEach(() => {
	vi.restoreAllMocks();
	vi.unstubAllGlobals();
	localStorage.clear();
});

describe("fitSize", () => {
	it("等比缩放到长边", () => {
		expect(fitSize(4000, 3000, 640)).toEqual({ width: 640, height: 480 });
		expect(fitSize(1000, 2000, 640)).toEqual({ width: 320, height: 640 });
		expect(fitSize(200, 100, 640)).toEqual({ width: 640, height: 320 });
	});
});

describe("processWallpaper", () => {
	it("长边 640、模糊半径约 6%，导出 webp 并量出亮度分位", async () => {
		const out = await processWallpaper(new Blob(["x"]));
		expect(out).toMatchObject({ width: 640, height: 480 });
		expect(out.url.startsWith("data:image/webp")).toBe(true);
		expect(out.url.length).toBeLessThanOrEqual(WALLPAPER_MAX_BYTES);
		expect(encoded).toEqual([
			{ width: 640, height: 480, filter: "blur(38px)" },
		]);
		// 假画布每个像素都是 rgb(200,200,200)
		expect(out.tone[0]).toBeCloseTo(0.833, 3);
		expect(out.tone[1]).toBe(out.tone[0]);
	});

	it("超过上限时逐级降尺寸重试", async () => {
		// 640×480 → 122880 字节超限；512×384 → 78643 字节通过
		bytesPerPixel = 0.4;
		const out = await processWallpaper(new Blob(["x"]));
		expect(encoded.map((e) => e.width)).toEqual([640, 512]);
		expect(out).toMatchObject({ width: 512, height: 384 });
		expect(encoded[1]?.filter).toBe("blur(31px)");
	});

	it("降到最小仍超限时抛 WallpaperTooLargeError", async () => {
		bytesPerPixel = 10;
		await expect(processWallpaper(new Blob(["x"]))).rejects.toBeInstanceOf(
			WallpaperTooLargeError,
		);
	});
	it("Canvas filter 无效（Safari）时自动走 CPU 后备", async () => {
		filterWorks = false;
		expect(canvasBlurWorks()).toBe(false);
		const out = await processWallpaper(new Blob(["x"]));
		expect(out).toMatchObject({ width: 640, height: 480 });
		// 后备路径：像素经 putImageData 写回缩放画布，导出时没有 filter
		expect(puts).toBe(1);
		expect(encoded).toEqual([{ width: 640, height: 480, filter: "none" }]);
	});
});

describe("blurPixels（CPU 后备）", () => {
	/** 灰度 RGBA 图，alpha 恒为 255 */
	function gray(w: number, h: number, f: (x: number, y: number) => number) {
		const d = new Uint8ClampedArray(w * h * 4);
		for (let y = 0; y < h; y++)
			for (let x = 0; x < w; x++) {
				const i = (y * w + x) * 4;
				d.fill(f(x, y), i, i + 3);
				d[i + 3] = 255;
			}
		return d;
	}
	const at = (d: Uint8ClampedArray, w: number, x: number, y: number) =>
		d[(y * w + x) * 4] ?? -1;
	/** 横向相邻像素差的方差 */
	function diffVariance(d: Uint8ClampedArray, w: number, h: number) {
		const ds: number[] = [];
		for (let y = 0; y < h; y++)
			for (let x = 1; x < w; x++) ds.push(at(d, w, x, y) - at(d, w, x - 1, y));
		const mean = ds.reduce((a, b) => a + b, 0) / ds.length;
		return ds.reduce((a, b) => a + (b - mean) ** 2, 0) / ds.length;
	}

	it("棋盘格被抹平，四角与中心一样亮（边缘不发灰），alpha 保持不透明", () => {
		const w = 48;
		const h = 32;
		const d = gray(w, h, (x, y) => (((x >> 1) + (y >> 1)) % 2 ? 255 : 0));
		const before = diffVariance(d, w, h);
		blurPixels(d, w, h, 4);
		expect(diffVariance(d, w, h)).toBeLessThan(before / 100);
		const center = at(d, w, w / 2, h / 2);
		expect(center).toBeGreaterThan(110);
		expect(center).toBeLessThan(145);
		for (const [x, y] of [
			[0, 0],
			[w - 1, 0],
			[0, h - 1],
			[w - 1, h - 1],
		] as const)
			expect(Math.abs(at(d, w, x, y) - center)).toBeLessThan(12);
		expect(d[3]).toBe(255);
	});

	it("硬边被抹开成渐变，远离硬边处保持原值", () => {
		const w = 64;
		const h = 4;
		const d = gray(w, h, (x) => (x < w / 2 ? 0 : 255));
		blurPixels(d, w, h, 4);
		const row = Array.from({ length: w }, (_, x) => at(d, w, x, 1));
		// 硬边两侧变成中间值，且单调递增
		expect(row[w / 2 - 1]).toBeGreaterThan(60);
		expect(row[w / 2]).toBeLessThan(195);
		for (let x = 1; x < w; x++)
			expect(row[x] ?? 0).toBeGreaterThanOrEqual(row[x - 1] ?? 0);
		// 镜像边缘：两端仍是纯黑 / 纯白，没有被拉向灰
		expect(row[0]).toBe(0);
		expect(row[w - 1]).toBe(255);
	});
});

describe("storeWallpaperImage", () => {
	it("写入 localStorage", () => {
		storeWallpaperImage("data:image/webp;base64,AAAA");
		expect(localStorage.getItem(WALLPAPER_IMAGE_KEY)).toBe(
			"data:image/webp;base64,AAAA",
		);
	});

	it("配额满时抛 WallpaperStorageError，旧图片保持不变", () => {
		localStorage.setItem(WALLPAPER_IMAGE_KEY, "data:image/webp;base64,OLD");
		vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
			throw new DOMException("quota", "QuotaExceededError");
		});
		expect(() => storeWallpaperImage("data:image/webp;base64,NEW")).toThrow(
			WallpaperStorageError,
		);
		expect(localStorage.getItem(WALLPAPER_IMAGE_KEY)).toBe(
			"data:image/webp;base64,OLD",
		);
	});
});
