/**
 * CSV / TSV 解析（RFC 4180）：字段可用双引号包裹，引号内的分隔符、换行与 `""` 转义原样保留；行尾接受 CRLF / LF / CR。
 * 只解析前 `maxRows` 行，`truncated` 表示还有更多行；文件被截断读取时最后一行可能残缺，由调用方提示。
 */
export interface CsvTable {
	rows: string[][];
	truncated: boolean;
}

export function parseDelimited(
	text: string,
	delimiter: "," | "\t",
	maxRows: number,
): CsvTable {
	const rows: string[][] = [];
	let row: string[] = [];
	let field = "";
	let quoted = false;
	let i = text.charCodeAt(0) === 0xfeff ? 1 : 0;
	const endRow = () => {
		row.push(field);
		rows.push(row);
		row = [];
		field = "";
	};
	for (; i < text.length; i++) {
		const c = text[i];
		if (quoted) {
			if (c !== '"') field += c;
			else if (text[i + 1] === '"') {
				field += '"';
				i++;
			} else quoted = false;
			continue;
		}
		if (c === '"' && field === "") quoted = true;
		else if (c === delimiter) {
			row.push(field);
			field = "";
		} else if (c === "\n" || c === "\r") {
			if (c === "\r" && text[i + 1] === "\n") i++;
			endRow();
			if (rows.length === maxRows)
				return { rows, truncated: i + 1 < text.length };
		} else field += c;
	}
	// 末尾没有换行的最后一行
	if (field !== "" || row.length > 0) endRow();
	return { rows, truncated: false };
}
