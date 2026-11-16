/**
 * 只读表格：CSV / TSV 与电子表格共用。行号 + 列字母（A、B…AA），表头与行号列粘性固定；
 * 行按视口虚拟化，单元格是纯文本（React 转义），不解释任何公式或 HTML。
 */
import { useVirtualizer } from "@tanstack/react-virtual";
import { type ReactNode, useMemo, useRef } from "react";

const ROW_H = 26;
const COL_W = 144;
const NUM_W = 52;
/** 显示的最多列数（更宽的表在右侧截断）。 */
export const MAX_GRID_COLS = 200;

/** 0 → A，25 → Z，26 → AA。 */
export function columnName(i: number): string {
	let s = "";
	for (let n = i + 1; n > 0; n = Math.floor((n - 1) / 26))
		s = String.fromCharCode(65 + ((n - 1) % 26)) + s;
	return s;
}

export function DataGrid({ rows }: { rows: string[][] }): ReactNode {
	const scrollRef = useRef<HTMLDivElement>(null);
	const cols = useMemo(
		() =>
			Math.min(
				MAX_GRID_COLS,
				rows.reduce((m, r) => Math.max(m, r.length), 0),
			),
		[rows],
	);
	const rowV = useVirtualizer({
		count: rows.length,
		getScrollElement: () => scrollRef.current,
		estimateSize: () => ROW_H,
		overscan: 12,
	});
	const width = NUM_W + cols * COL_W;
	const cell =
		"shrink-0 truncate border-border border-r border-b px-2 leading-[26px]";
	return (
		<div
			ref={scrollRef}
			className="h-full overflow-auto font-mono text-xs tabular-nums"
		>
			<div style={{ width, height: ROW_H + rowV.getTotalSize() }}>
				<div
					className="sticky top-0 z-10 flex bg-panel text-faint"
					style={{ width, height: ROW_H }}
				>
					<div
						className={`${cell} sticky left-0 bg-panel`}
						style={{ width: NUM_W }}
					/>
					{Array.from({ length: cols }, (_, c) => (
						<div
							key={columnName(c)}
							className={`${cell} text-center`}
							style={{ width: COL_W }}
						>
							{columnName(c)}
						</div>
					))}
				</div>
				<div className="relative" style={{ height: rowV.getTotalSize() }}>
					{rowV.getVirtualItems().map((v) => {
						const row = rows[v.index] ?? [];
						return (
							<div
								key={v.key}
								className="absolute left-0 flex"
								style={{
									top: 0,
									width,
									height: ROW_H,
									transform: `translateY(${v.start}px)`,
								}}
							>
								<div
									className={`${cell} sticky left-0 bg-panel text-right text-faint`}
									style={{ width: NUM_W }}
								>
									{v.index + 1}
								</div>
								{Array.from({ length: cols }, (_, c) => (
									<div
										key={columnName(c)}
										className={cell}
										style={{ width: COL_W }}
										title={row[c] && row[c].length > 16 ? row[c] : undefined}
									>
										{row[c] ?? ""}
									</div>
								))}
							</div>
						);
					})}
				</div>
			</div>
		</div>
	);
}
