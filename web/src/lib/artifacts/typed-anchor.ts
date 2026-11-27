/**
 * 类型化 artifact 的评论锚点（M31 契约 C6）。类型化锚点的 `path` 恒为该类型的入口文件，由入口的扩展名就能
 * 判出锚点的形态：页（Word / PDF）、幻灯片、工作表或 Markdown 块；HTML 页面的锚点返回 `null`。
 */

/** 类型化锚点的形态。 */
export type TypedAnchorKind = "page" | "slide" | "sheet" | "block";

const KIND_BY_EXT: Record<string, TypedAnchorKind> = {
	docx: "page",
	docm: "page",
	pdf: "page",
	pptx: "slide",
	pptm: "slide",
	json: "slide",
	xlsx: "sheet",
	xlsm: "sheet",
	md: "block",
};

/** 按锚点路径（入口文件）的扩展名给出类型化锚点的形态；不是类型化锚点（HTML 页面等）时为 `null`。 */
export function typedAnchorKind(path: string): TypedAnchorKind | null {
	const ext = path.slice(path.lastIndexOf(".") + 1).toLowerCase();
	return Object.hasOwn(KIND_BY_EXT, ext) ? (KIND_BY_EXT[ext] ?? null) : null;
}

const ENTRY_BY_TYPE: Record<string, string> = {
	document: "document.md",
	slides: "slides.json",
	docx: "document.docx",
	docm: "document.docm",
	xlsx: "workbook.xlsx",
	xlsm: "workbook.xlsm",
	pptx: "presentation.pptx",
	pptm: "presentation.pptm",
	pdf: "document.pdf",
};

/** 类型化 artifact 的入口文件（同服务端 C1）；不认识的类型为 `null`。评论锚点的 `path` 恒为它。 */
export function typedEntry(type: string | undefined): string | null {
	return type !== undefined && Object.hasOwn(ENTRY_BY_TYPE, type)
		? (ENTRY_BY_TYPE[type] ?? null)
		: null;
}
