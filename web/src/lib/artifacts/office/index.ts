// Office 预览组件的入口（契约 C4）。四个视图各自带样式；只用其中一种时直接 import 对应文件，免得把 pdf.js 一并打包。

export { DocxView } from "./DocxView";
export { MAX_PREVIEW_BYTES, MAX_PRINT_PAGES } from "./limits";
export { PdfView } from "./PdfView";
export { PptxView, type PptxViewProps } from "./PptxView";
export type {
	AnchorPin,
	OfficeAssets,
	OfficeStrings,
	OfficeView,
	OfficeViewProps,
	ViewerAnchor,
} from "./types";
export { XlsxView } from "./XlsxView";
