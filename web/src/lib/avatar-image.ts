/**
 * agent 头像图片：居中裁成正方形、缩到 128×128，编码成 data URL。
 * 服务端只收 png/webp/jpeg 且整串 ≤ 128KiB：先用 webp 逐级降质量；浏览器不支持 webp 编码时（会退回 png）改用 jpeg。
 */

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

async function decode(
	file: Blob,
): Promise<CanvasImageSource & { width: number; height: number }> {
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

export async function avatarDataUrl(file: Blob): Promise<string> {
	const src = await decode(file);
	const side = Math.min(src.width, src.height);
	const canvas = document.createElement("canvas");
	canvas.width = AVATAR_SIZE;
	canvas.height = AVATAR_SIZE;
	const ctx = canvas.getContext("2d");
	if (!ctx) throw new Error("canvas 2d unavailable");
	ctx.imageSmoothingQuality = "high";
	ctx.drawImage(
		src,
		(src.width - side) / 2,
		(src.height - side) / 2,
		side,
		side,
		0,
		0,
		AVATAR_SIZE,
		AVATAR_SIZE,
	);
	if ("close" in src && typeof src.close === "function") src.close();
	for (const q of QUALITIES) {
		let url = canvas.toDataURL("image/webp", q);
		if (!url.startsWith("data:image/webp"))
			url = canvas.toDataURL("image/jpeg", q);
		if (url.length <= AVATAR_MAX_BYTES) return url;
	}
	throw new AvatarTooLargeError();
}
