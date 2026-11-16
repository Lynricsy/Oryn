/** 二进制预览组件的统一入参：内容已整份读入（受 `MAX_PREVIEW_BLOB_BYTES` 限制），`name` 为文件名（含扩展名）。 */
export interface BlobPreviewProps {
	blob: Blob;
	name: string;
}
