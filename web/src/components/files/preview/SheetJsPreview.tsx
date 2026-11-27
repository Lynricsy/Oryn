/**
 * 非 OOXML 的电子表格（xlsb / xls / xltx / ods / fods / numbers）：SheetJS 解析出每个工作表的显示文本（按单元格格式化，
 * 不求值公式、不渲染 HTML），交给 DataGrid；底部按工作表切换。每表只解析前 `MAX_TABLE_ROWS` 行。
 * 旧版 xls 可能是非 UTF 代码页（如 GBK），所以带上 SheetJS 的代码页表。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { read, set_cptable, utils } from "xlsx";
import * as cptable from "xlsx/dist/cpexcel.full.mjs";
import { ErrorText, Spinner, tabClass } from "../../ui/primitives";
import { MAX_TABLE_ROWS } from "./CsvPreview";
import { DataGrid } from "./DataGrid";
import { PreviewNote } from "./PreviewNote";
import type { BlobPreviewProps } from "./types";

set_cptable(cptable);

interface Sheet {
	name: string;
	rows: string[][];
	truncated: boolean;
}

async function loadSheets(blob: Blob): Promise<Sheet[]> {
	const wb = read(new Uint8Array(await blob.arrayBuffer()), {
		type: "array",
		dense: true,
		sheetRows: MAX_TABLE_ROWS,
		cellFormula: false,
		cellHTML: false,
	});
	return wb.SheetNames.map((name) => {
		const ws = wb.Sheets[name];
		if (!ws) return { name, rows: [], truncated: false };
		const rows = utils.sheet_to_json<string[]>(ws, {
			header: 1,
			raw: false,
			defval: "",
			blankrows: true,
		});
		// sheetRows 截断时 SheetJS 把原始范围留在 `!fullref`
		const full = ws["!fullref"] as string | undefined;
		return {
			name,
			rows,
			truncated:
				full !== undefined && utils.decode_range(full).e.r + 1 > rows.length,
		};
	});
}

export default function SheetJsPreview({ blob }: BlobPreviewProps): ReactNode {
	const { t } = useTranslation();
	const [sheets, setSheets] = useState<Sheet[] | null>(null);
	const [error, setError] = useState<unknown>(null);
	const [active, setActive] = useState(0);

	useEffect(() => {
		let alive = true;
		setSheets(null);
		setError(null);
		setActive(0);
		loadSheets(blob).then(
			(s) => alive && setSheets(s),
			(e) => alive && setError(e),
		);
		return () => {
			alive = false;
		};
	}, [blob]);

	if (error)
		return (
			<div className="p-3">
				<ErrorText error={error} />
			</div>
		);
	if (!sheets)
		return (
			<div className="p-3">
				<Spinner />
			</div>
		);
	const sheet = sheets[active];
	return (
		<div className="flex h-full min-h-0 flex-col">
			{sheet?.truncated && (
				<PreviewNote>
					{t("files.preview.table.rowsCapped", { n: MAX_TABLE_ROWS })}
				</PreviewNote>
			)}
			<div className="min-h-0 flex-1">
				{sheet && sheet.rows.length > 0 ? (
					<DataGrid rows={sheet.rows} />
				) : (
					<p className="p-3 text-faint text-sm">
						{t("files.preview.table.empty")}
					</p>
				)}
			</div>
			{sheets.length > 1 && (
				<div
					role="tablist"
					aria-label={t("files.preview.table.sheets")}
					className="flex shrink-0 gap-0.5 overflow-x-auto border-border border-t px-2 py-1"
				>
					{sheets.map((s, i) => (
						<button
							key={s.name}
							type="button"
							role="tab"
							aria-selected={i === active}
							className={`${tabClass(i === active)} ${i === active ? "bg-hover" : ""} shrink-0`}
							onClick={() => setActive(i)}
						>
							{s.name}
						</button>
					))}
				</div>
			)}
		</div>
	);
}
