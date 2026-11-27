/**
 * 电子表格：xlsx / xlsm 用 artifact 查看器同一套 XlsxView（`@silurus/ooxml` 的网格，带格式、合并单元格与图表）；
 * 其余格式（xlsb / xls / xltx / ods / fods / numbers）按需加载 SheetJS 的纯文本网格（SheetJsPreview）。
 */
import { lazy, type ReactNode } from "react";
import { officeTypeOfName } from "../../../lib/artifacts/office/formats";
import { XlsxView } from "../../../lib/artifacts/office/XlsxView";
import { OfficePreview } from "./OfficePreview";
import type { BlobPreviewProps } from "./types";

const SheetJsPreview = lazy(() => import("./SheetJsPreview"));

export default function SheetPreview({
	blob,
	name,
}: BlobPreviewProps): ReactNode {
	const type = officeTypeOfName(name);
	if (type === "xlsx" || type === "xlsm")
		return <OfficePreview blob={blob} View={XlsxView} />;
	return <SheetJsPreview blob={blob} name={name} />;
}
