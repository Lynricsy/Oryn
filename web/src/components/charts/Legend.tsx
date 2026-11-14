/** 图例：色点 +（模型系列）品牌 logo + 名称，横向自动换行。 */
import type { ReactNode } from "react";
import { ModelLogo } from "../ui/ModelLogo";

export interface LegendItem {
	key: string;
	label: string;
	/** CSS 变量名，如 `--viz-1` */
	color: string;
	/** 系列是模型时的 `<provider>/<model>`：名字前放品牌 logo（全站模型名同一写法） */
	model?: string;
	/** 悬停提示（模型全名） */
	title?: string;
}

export function Legend({ items }: { items: LegendItem[] }): ReactNode {
	if (items.length === 0) return null;
	return (
		<ul className="flex flex-wrap gap-x-3 gap-y-1 text-faint text-xs">
			{items.map((it) => (
				<li
					key={it.key}
					title={it.title}
					className="flex min-w-0 items-center gap-1.5"
				>
					<span
						aria-hidden
						className="h-2 w-2 shrink-0 rounded-full"
						style={{ background: `var(${it.color})` }}
					/>
					{it.model && <ModelLogo model={it.model} size={12} />}
					<span className="max-w-48 truncate">{it.label}</span>
				</li>
			))}
		</ul>
	);
}
