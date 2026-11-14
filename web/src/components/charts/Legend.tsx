/** 图例：色点 + 名称，横向自动换行。 */
import type { ReactNode } from "react";

export interface LegendItem {
	key: string;
	label: string;
	/** CSS 变量名，如 `--viz-1` */
	color: string;
}

export function Legend({ items }: { items: LegendItem[] }): ReactNode {
	if (items.length === 0) return null;
	return (
		<ul className="flex flex-wrap gap-x-3 gap-y-1 text-faint text-xs">
			{items.map((it) => (
				<li key={it.key} className="flex min-w-0 items-center gap-1.5">
					<span
						aria-hidden
						className="h-2 w-2 shrink-0 rounded-full"
						style={{ background: `var(${it.color})` }}
					/>
					<span className="max-w-48 truncate">{it.label}</span>
				</li>
			))}
		</ul>
	);
}
