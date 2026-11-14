/** 图表浮层（玻璃材质的 HTML 元素，`data-testid="chart-tooltip"`）：标题 + 各系列的值 + 合计。 */
import type { ReactNode } from "react";
import { ModelLogo } from "../ui/ModelLogo";

export interface TooltipRow {
	key: string;
	label: string;
	/** CSS 变量名，如 `--viz-1` */
	color: string;
	value: string;
	/** 系列是模型时的 `<provider>/<model>`：名字前放品牌 logo */
	model?: string;
}

/**
 * 浮层按 `x`（容器内像素）水平定位并夹在容器内；`align` 决定以 x 为左缘还是右缘（靠右半边时向左展开，不出容器）。
 */
export function ChartTooltip({
	title,
	rows,
	total,
	x,
	width,
}: {
	title: ReactNode;
	rows: TooltipRow[];
	total?: { label: string; value: string };
	x: number;
	/** 容器宽度 */
	width: number;
}): ReactNode {
	const right = x > width / 2;
	return (
		<div
			data-testid="chart-tooltip"
			role="status"
			className="glass pointer-events-none absolute top-1 z-10 min-w-36 max-w-[min(16rem,calc(100%-0.5rem))] rounded-md px-2.5 py-2 text-xs"
			style={
				right
					? { right: Math.max(4, width - x + 8) }
					: { left: Math.max(4, x + 8) }
			}
		>
			<div className="mb-1 font-medium text-fg">{title}</div>
			<ul className="flex flex-col gap-0.5">
				{rows.map((r) => (
					<li key={r.key} className="flex items-center gap-2">
						<span
							aria-hidden
							className="h-2 w-2 shrink-0 rounded-full"
							style={{ background: `var(${r.color})` }}
						/>
						{r.model && <ModelLogo model={r.model} size={12} />}
						<span className="min-w-0 flex-1 truncate text-muted">
							{r.label}
						</span>
						<span className="shrink-0 text-fg tabular-nums">{r.value}</span>
					</li>
				))}
			</ul>
			{total && (
				<div className="mt-1 flex items-center gap-2 border-border border-t pt-1">
					<span className="min-w-0 flex-1 text-muted">{total.label}</span>
					<span className="shrink-0 font-medium text-fg tabular-nums">
						{total.value}
					</span>
				</div>
			)}
		</div>
	);
}
