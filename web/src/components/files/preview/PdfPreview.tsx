/** PDF：用 artifact 查看器同一套 PdfView（pdf.js 按需渲染、文本层、页码与缩放工具栏）。 */
import type { ReactNode } from "react";
import { PdfView } from "../../../lib/artifacts/office/PdfView";
import { OfficePreview } from "./OfficePreview";
import type { BlobPreviewProps } from "./types";

export default function PdfPreview({ blob }: BlobPreviewProps): ReactNode {
	return <OfficePreview blob={blob} View={PdfView} />;
}
