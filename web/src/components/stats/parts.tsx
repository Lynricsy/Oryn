/** 统计页的公共块：指标行（`KpiGrid`）与明细表（`BreakdownTable`）。三档布局按 `useBreakpoint()` 切换。 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { useBreakpoint } from "../../lib/viewport";
import { Card, Meter } from "../ui/data";
import { IconButton, Spinner } from "../ui/primitives";

export interface Kpi {
	/** `data-testid="kpi-<name>"` */
	name: string;
	label: string;
	value: string;
	/** 数字下方的小字（构成、较上一周期） */
	sub?: ReactNode;
}

/** 指标行：expanded 4 列、medium 3 列、compact 2 列；每格是该 grid 的直接子元素。 */
export function KpiGrid({ items }: { items: Kpi[] }): ReactNode {
	const bp = useBreakpoint();
	const cols =
		bp === "compact"
			? "grid-cols-2"
			: bp === "medium"
				? "grid-cols-3"
				: "grid-cols-4";
	return (
		<div className={`grid gap-2 ${cols}`}>
			{items.map((k) => (
				<div
					key={k.name}
					data-testid={`kpi-${k.name}`}
					className="flex min-w-0 flex-col gap-0.5 rounded-lg border border-border bg-panel px-3 py-2.5"
				>
					<span className="truncate text-faint text-xs">{k.label}</span>
					<span className="truncate font-semibold text-fg text-lg tabular-nums">
						{k.value}
					</span>
					{k.sub && (
						<span className="truncate text-faint text-xs tabular-nums">
							{k.sub}
						</span>
					)}
				</div>
			))}
		</div>
	);
}

export interface BreakdownRow {
	key: string;
	name: string;
	/** 悬停提示（全名） */
	title?: string;
	/** 主指标（成本或该标签的主指标）的文本 */
	primary: string;
	/** 主指标占合计的比例（0..1），画占比条 */
	share: number;
	/** 其余列（与 `columns` 一一对应）；表格窄时收进点开的详情 */
	cells: string[];
	/** 点行设为筛选；省略 = 不可点（已删除的线程、「其他」） */
	onSelect?: () => void;
}

/** 其余列只在表格自身够宽（≥ 38rem，容器查询）时铺开；更窄（手机、宽屏两列并排）时收进行尾展开的详情。 */
const WIDE = "hidden @[38rem]:block";
const NARROW = "@[38rem]:hidden";

/**
 * 明细表：名称 + 其余列 + 主指标（带占比条）。点行即把它设为筛选。
 * 按表格自身宽度切换：窄时只留名称、主指标与占比条，其余列收进行尾展开按钮的详情里。
 */
export function BreakdownTable({
	title,
	columns,
	primaryLabel,
	rows,
	loading,
	testId,
}: {
	title: string;
	columns: string[];
	primaryLabel: string;
	rows: BreakdownRow[];
	loading?: boolean;
	testId?: string;
}): ReactNode {
	const { t } = useTranslation();
	const [open, setOpen] = useState<string | null>(null);
	return (
		<Card title={title} className="min-w-0">
			<div
				data-testid={testId}
				className="@container flex min-w-0 flex-col text-sm"
			>
				<div className="flex items-center gap-3 border-border border-b px-2 pb-1.5 text-faint text-xs">
					<span className="min-w-0 flex-1">{t("stats.table.name")}</span>
					{columns.map((c) => (
						<span key={c} className={`w-16 shrink-0 text-right ${WIDE}`}>
							{c}
						</span>
					))}
					<span className="w-28 shrink-0 text-right">{primaryLabel}</span>
					{columns.length > 0 && <span className={`w-6 shrink-0 ${NARROW}`} />}
				</div>
				{loading && rows.length === 0 && (
					<div className="flex justify-center py-4 text-muted">
						<Spinner />
					</div>
				)}
				{!loading && rows.length === 0 && (
					<p className="px-2 py-3 text-faint text-xs">{t("stats.empty")}</p>
				)}
				{rows.map((r) => {
					const body = (
						<>
							<span className="min-w-0 flex-1 truncate" title={r.title}>
								{r.name}
							</span>
							{columns.map((c, i) => (
								<span
									key={c}
									className={`w-16 shrink-0 text-right text-muted tabular-nums ${WIDE}`}
								>
									{r.cells[i]}
								</span>
							))}
							<span className="flex w-28 shrink-0 flex-col items-end gap-1">
								<span className="tabular-nums">{r.primary}</span>
								<Meter
									value={r.share * 100}
									label={`${r.name} ${Math.round(r.share * 100)}%`}
									className="w-full"
								/>
							</span>
						</>
					);
					const cls =
						"flex min-h-10 min-w-0 flex-1 items-center gap-3 rounded-md px-2 py-1.5 text-left";
					return (
						<div key={r.key} className="flex min-w-0 flex-col">
							<div className="flex min-w-0 items-center">
								{r.onSelect ? (
									<button
										type="button"
										data-row-key={r.key}
										onClick={r.onSelect}
										className={`${cls} transition-colors duration-150 hover:bg-hover`}
									>
										{body}
									</button>
								) : (
									<div data-row-key={r.key} className={cls}>
										{body}
									</div>
								)}
								{columns.length > 0 && (
									<span className={NARROW}>
										<IconButton
											icon={open === r.key ? "chevronDown" : "chevronRight"}
											box="xs"
											size={13}
											label={t("stats.table.details")}
											aria-expanded={open === r.key}
											onClick={() =>
												setOpen((k) => (k === r.key ? null : r.key))
											}
										/>
									</span>
								)}
							</div>
							{open === r.key && (
								<dl
									className={`mb-1 grid grid-cols-2 gap-x-3 gap-y-0.5 px-2 text-xs ${NARROW}`}
								>
									{columns.map((c, i) => (
										<div key={c} className="contents">
											<dt className="text-faint">{c}</dt>
											<dd className="m-0 text-right text-muted tabular-nums">
												{r.cells[i]}
											</dd>
										</div>
									))}
								</dl>
							)}
						</div>
					);
				})}
			</div>
		</Card>
	);
}
