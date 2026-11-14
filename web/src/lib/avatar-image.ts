/**
 * 头像图片：解码成可裁剪的源图（`loadAvatarSource`），按取景框画到 128×128 并编码成 data URL（`renderAvatar`）。
 * 服务端只收 png/webp/jpeg 且整串 ≤ 128KiB：先用 webp 逐级降质量；浏览器不支持 webp 编码时（会退回 png）改用 jpeg。
 */
import type { Crop } from "./avatar-crop";

export const AVATAR_SIZE = 128;
export const AVATAR_MAX_BYTES = 128 << 10;
const QUALITIES = [0.9, 0.8, 0.65, 0.5, 0.35];

/** 压到上限内仍超出时抛出；调用方据此提示换一张图。 */
export class AvatarTooLargeError extends Error {
	constructor() {
		super("avatar image too large");
		this.name = "AvatarTooLargeError";
	}
}

/** 解码后的源图；`url` 供 `<img>` 预览，用完调 `release()`（可重复调用）。 */
export interface AvatarSource {
	image: CanvasImageSource;
	width: number;
	height: number;
	url: string;
	release(): void;
}

export async function loadAvatarSource(file: Blob): Promise<AvatarSource> {
	const url = URL.createObjectURL(file);
	try {
		let image: CanvasImageSource;
		let width: number;
		let height: number;
		if (typeof createImageBitmap === "function") {
			const bmp = await createImageBitmap(file);
			image = bmp;
			width = bmp.width;
			height = bmp.height;
		} else {
			const img = new Image();
			img.src = url;
			await img.decode();
			image = img;
			width = img.naturalWidth;
			height = img.naturalHeight;
		}
		if (!width || !height) throw new Error("empty image");
		let released = false;
		return {
			image,
			width,
			height,
			url,
			release() {
				if (released) return;
				released = true;
				if ("close" in image && typeof image.close === "function")
					image.close();
				URL.revokeObjectURL(url);
			},
		};
	} catch (e) {
		URL.revokeObjectURL(url);
		throw e;
	}
}

/** 把取景框（源图像素坐标）画到 128×128 画布并编码；质量阶梯都超限时抛 `AvatarTooLargeError`。 */
export function renderAvatar(src: AvatarSource, crop: Crop): string {
	const canvas = document.createElement("canvas");
	canvas.width = AVATAR_SIZE;
	canvas.height = AVATAR_SIZE;
	const ctx = canvas.getContext("2d");
	if (!ctx) throw new Error("canvas 2d unavailable");
	ctx.imageSmoothingQuality = "high";
	ctx.drawImage(
		src.image,
		crop.x,
		crop.y,
		crop.size,
		crop.size,
		0,
		0,
		AVATAR_SIZE,
		AVATAR_SIZE,
	);
	for (const q of QUALITIES) {
		let url = canvas.toDataURL("image/webp", q);
		if (!url.startsWith("data:image/webp"))
			url = canvas.toDataURL("image/jpeg", q);
		if (url.length <= AVATAR_MAX_BYTES) return url;
	}
	throw new AvatarTooLargeError();
}
