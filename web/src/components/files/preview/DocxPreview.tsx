/** Word 文档（docx / docm / dotx / dotm）：用 artifact 查看器同一套 DocxView 逐页画到 canvas。 */
import type { ReactNode } from "react";
import { DocxView } from "../../../lib/artifacts/office/DocxView";
import { OfficePreview } from "./OfficePreview";
import type { BlobPreviewProps } from "./types";

export default function DocxPreview({ blob }: BlobPreviewProps): ReactNode {
	return <OfficePreview blob={blob} View={DocxView} />;
}
