/**
 * 图片块的 src：转录入库时内联图片被替换为 `blob:sha256:<hex>` 引用，经 `GET /api/v1/blobs/<hex>` 读取。
 * 图片来自 agent / 工具输出（低信任），只接受栅格图类型；SVG、HTML 等可成为活动文档的类型一律拒绝。
 */
const BLOB_REF = /^blob:sha256:([0-9a-f]{64})$/;

export const RASTER_IMAGE_TYPES: Readonly<Record<string, true>> = {
	"image/png": true,
	"image/jpeg": true,
	"image/gif": true,
	"image/webp": true,
	"image/avif": true,
};

/** 规范化后的栅格图 MIME；不在白名单内返回 null。 */
export function rasterMime(mimeType: string): string | null {
	const mime = mimeType.split(";")[0]?.trim().toLowerCase() ?? "";
	return RASTER_IMAGE_TYPES[mime] === true ? mime : null;
}

/** 可安全放进 `<img src>` 的地址；类型不受支持或数据不像 base64 时返回 null。 */
export function imageSrc(data: string, mimeType: string): string | null {
	const mime = rasterMime(mimeType);
	if (!mime) return null;
	const m = BLOB_REF.exec(data);
	if (m) return `/api/v1/blobs/${m[1]}`;
	if (!/^[A-Za-z0-9+/=\s]*$/.test(data)) return null;
	return `data:${mime};base64,${data}`;
}
