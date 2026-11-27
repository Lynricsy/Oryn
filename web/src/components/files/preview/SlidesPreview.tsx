/** 演示文稿（pptx / pptm / ppsx / potx）：用 artifact 查看器同一套 PptxView 逐张画到 canvas，带「演示」。 */
import type { ReactNode } from "react";
import { PptxView } from "../../../lib/artifacts/office/PptxView";
import { OfficePreview } from "./OfficePreview";
import type { BlobPreviewProps } from "./types";

export default function SlidesPreview({ blob }: BlobPreviewProps): ReactNode {
	return <OfficePreview blob={blob} View={PptxView} />;
}
