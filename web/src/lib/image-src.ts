/** 图片块的 src：转录入库时内联图片被替换为 `blob:sha256:<hex>` 引用，经 `GET /api/v1/blobs/<hex>` 读取。 */
const BLOB_REF = /^blob:sha256:([0-9a-f]{64})$/;

export function imageSrc(data: string, mimeType: string): string {
	const m = BLOB_REF.exec(data);
	return m ? `/api/v1/blobs/${m[1]}` : `data:${mimeType};base64,${data}`;
}
