// Office 预览的上限，与服务端 internal/artifacts/limits.go 的同名常量一致。

/** 超过它的文件不做预览、不出缩略图（MaxPreviewBytes）。 */
export const MAX_PREVIEW_BYTES = 64 * 1024 * 1024;

/** 打印视图（PDF 导出）最多排开的页数（MaxPrintPages）。 */
export const MAX_PRINT_PAGES = 200;
