/** CSV / TSV 的表格视图：解析前 `MAX_TABLE_ROWS` 行交给 DataGrid。 */
import { type ReactNode, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { parseDelimited } from "../../../lib/csv";
import { extOf } from "../../../lib/preview-kind";
import { DataGrid } from "./DataGrid";
import { PreviewNote } from "./PreviewNote";

/** 表格视图最多显示的行数（CSV 与电子表格共用）。 */
export const MAX_TABLE_ROWS = 10_000;

export default function CsvPreview({
	text,
	name,
}: {
	text: string;
	name: string;
}): ReactNode {
	const { t } = useTranslation();
	const table = useMemo(
		() =>
			parseDelimited(text, extOf(name) === "tsv" ? "\t" : ",", MAX_TABLE_ROWS),
		[text, name],
	);
	return (
		<div className="flex h-full min-h-0 flex-col">
			{table.truncated && (
				<PreviewNote>
					{t("files.preview.table.rowsCapped", { n: MAX_TABLE_ROWS })}
				</PreviewNote>
			)}
			<div className="min-h-0 flex-1">
				<DataGrid rows={table.rows} />
			</div>
		</div>
	);
}
