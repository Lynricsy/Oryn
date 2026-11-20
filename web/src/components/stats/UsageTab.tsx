/**
 * 用量标签（M10 计划 5.3）：指标行、预算卡、主图（系列 = `by` 选的维度，纵轴 = `metric`）、明细表
 * （按模型 / 项目 / 空间 / 线程 / 用途 / 工具），页脚一行成本口径。点项目 / 空间 / 模型 / 线程的行把它设为筛选。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	type BudgetStatus,
	type UsageQuery,
	type UsageStat,
	useToolStats,
	useUsageStats,
} from "../../api/stats-queries";
import { formatCost, formatDuration, formatTokens } from "../../lib/format";
import { useBreakpoint } from "../../lib/viewport";
import { Legend } from "../charts/Legend";
import { StackedBars } from "../charts/StackedBars";
import { Card } from "../ui/data";
import { ErrorText, Spinner, tabClass } from "../ui/primitives";
import { BudgetCard, hasBudget } from "./BudgetCard";
import {
	bucketLabel,
	formatMetric,
	formatRatio,
	type GroupDim,
	groupName,
	metricOf,
	modelOf,
	OTHER_KEY,
	tokensOf,
	vizColor,
} from "./format";
import { type BreakdownRow, BreakdownTable, KpiGrid } from "./parts";
import {
	BYS,
	type ByKey,
	filterQuery,
	METRICS,
	type MetricKey,
	previousWindow,
	type StatsParams,
	type StatsWindow,
} from "./statsParams";

/** 主图的系列数：前 8 + 「其他」。 */
const SERIES_LIMIT = 8;
/** 明细表的行数。 */
const TABLE_LIMIT = 20;

function Segmented<T extends string>({
	label,
	value,
	options,
	attr,
	text,
	onChange,
}: {
	label: string;
	value: T;
	options: readonly T[];
	attr: "data-by" | "data-metric";
	text: (v: T) => string;
	onChange: (v: T) => void;
}): ReactNode {
	return (
		// biome-ignore lint/a11y/useSemanticElements: 分段按钮（aria-pressed），不是表单字段集
		<div
			role="group"
			aria-label={label}
			className="flex min-w-0 items-center gap-0.5 overflow-x-auto rounded-md bg-inset p-0.5"
		>
			{options.map((o) => (
				<button
					key={o}
					type="button"
					{...{ [attr]: o }}
					aria-pressed={value === o}
					onClick={() => onChange(o)}
					className={`${tabClass(value === o)} h-6 ${value === o ? "bg-panel shadow-xs" : ""}`}
				>
					{text(o)}
				</button>
			))}
		</div>
	);
}

/** 明细行的其余列：请求、tokens、TTFT、tokens/s。 */
function usageCells(g: UsageStat): string[] {
	return [
		formatTokens(g.requests),
		formatTokens(tokensOf(g)),
		g.avgTtftMs !== undefined ? formatDuration(g.avgTtftMs) : "—",
		g.tokensPerSecond !== undefined
			? String(Math.round(g.tokensPerSecond))
			: "—",
	];
}

export function UsageTab({
	params,
	window: w,
	onChange,
	budget,
	onEditBudget,
}: {
	params: StatsParams;
	window: StatsWindow;
	onChange: (patch: Partial<StatsParams>) => void;
	budget: BudgetStatus | undefined;
	onEditBudget: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const scope = { from: w.from, to: w.to, tz: w.tz, ...filterQuery(params) };
	const main = useUsageStats({
		...scope,
		groupBy: params.by,
		series: w.grain,
		limit: SERIES_LIMIT,
	});
	// 指标行另取 groupBy=total：按模型 / 用途分组时工具调用与回合不归属到组（为 0），主图的 total 不能直接用
	const totals = useUsageStats({ ...scope, groupBy: "total" });
	const prevWin = previousWindow(w);
	const prev = useUsageStats(
		{ ...scope, ...prevWin, groupBy: "total" },
		params.range !== "all",
	);
	const table = (groupBy: UsageQuery["groupBy"]) => ({
		...scope,
		groupBy,
		limit: TABLE_LIMIT,
	});
	const byModel = useUsageStats(table("model"));
	const byProject = useUsageStats(table("project"));
	const bySpace = useUsageStats(table("space"));
	const byThread = useUsageStats(table("thread"));
	const byPurpose = useUsageStats(table("purpose"));
	const { model: _model, ...toolScope } = scope;
	const tools = useToolStats(
		{ ...toolScope, limit: TABLE_LIMIT },
		!params.model,
	);

	const total = totals.data?.total;
	const groups = main.data?.groups ?? [];
	const series = groups.map((g, i) => {
		const { name, title } = groupName(params.by, g, t);
		return {
			key: g.key,
			label: name,
			title,
			color: vizColor(i, g.key),
			model: modelOf(params.by, g.key),
		};
	});
	const points = main.data?.series ?? [];
	const metric = params.metric;
	const values = points.map((p) =>
		series.map((s) => {
			const g = p.groups.find((x) => x.key === s.key);
			return g ? metricOf(metric, g) : 0;
		}),
	);
	const prevCost = prev.data?.total.costUsd;
	const delta =
		total && prevCost !== undefined && prevCost > 0
			? ((total.costUsd - prevCost) / prevCost) * 100
			: undefined;
	const kpis = total
		? [
				{
					name: "cost",
					label: t("stats.kpi.cost"),
					value: formatCost(total.costUsd),
					sub:
						delta !== undefined
							? t("stats.kpi.costDelta", {
									pct: `${delta >= 0 ? "+" : "−"}${Math.abs(Math.round(delta))}%`,
								})
							: undefined,
				},
				{
					name: "tokens",
					label: t("stats.kpi.tokens"),
					value: formatTokens(tokensOf(total)),
					sub: t("stats.kpi.tokensDetail", {
						input: formatTokens(total.inputTokens),
						output: formatTokens(total.outputTokens),
						read: formatTokens(total.cacheReadTokens),
						write: formatTokens(total.cacheWriteTokens),
					}),
				},
				{
					name: "cache",
					label: t("stats.kpi.cacheHit"),
					value: formatRatio(
						total.cacheReadTokens,
						total.inputTokens + total.cacheReadTokens + total.cacheWriteTokens,
					),
				},
				{
					name: "requests",
					label: t("stats.kpi.requests"),
					value: formatTokens(total.requests),
					sub: t("stats.kpi.errorRate", {
						pct: formatRatio(total.requestErrors, total.requests),
					}),
				},
				{
					name: "turns",
					label: t("stats.kpi.turns"),
					value: formatTokens(total.turns),
				},
				{
					name: "tools",
					label: t("stats.kpi.tools"),
					value: formatTokens(total.toolCalls),
					sub: t("stats.kpi.errorRate", {
						pct: formatRatio(total.toolErrors, total.toolCalls),
					}),
				},
				{
					name: "ttft",
					label: t("stats.kpi.ttft"),
					value:
						total.avgTtftMs !== undefined
							? formatDuration(total.avgTtftMs)
							: "—",
				},
				{
					name: "tps",
					label: t("stats.kpi.tps"),
					value:
						total.tokensPerSecond !== undefined
							? `${Math.round(total.tokensPerSecond)}/s`
							: "—",
				},
			]
		: [];

	const filterOf: Partial<Record<GroupDim, keyof StatsParams>> = {
		model: "model",
		project: "project",
		space: "space",
		thread: "thread",
	};
	const rowsOf = (
		dim: GroupDim,
		data: { groups: UsageStat[]; total: UsageStat } | undefined,
	): BreakdownRow[] => {
		const sum = data?.total.costUsd ?? 0;
		return (data?.groups ?? []).map((g) => {
			const { name, title } = groupName(dim, g, t);
			const key = filterOf[dim];
			const selectable =
				key !== undefined &&
				g.key !== OTHER_KEY &&
				!(dim === "thread" && g.key === "") &&
				!(dim === "model" && g.key === "");
			const value = g.key === "" ? "none" : g.key;
			return {
				key: g.key,
				name,
				title,
				model: modelOf(dim, g.key),
				primary: formatCost(g.costUsd),
				share: sum > 0 ? g.costUsd / sum : 0,
				cells: usageCells(g),
				onSelect:
					selectable && key ? () => onChange({ [key]: value }) : undefined,
			};
		});
	};
	const columns = [
		t("stats.table.requests"),
		t("stats.table.tokens"),
		t("stats.table.ttft"),
		t("stats.table.tps"),
	];
	const toolRows: BreakdownRow[] = (tools.data?.tools ?? []).map((x) => ({
		key: x.tool,
		name: x.tool === OTHER_KEY ? t("stats.other") : x.tool,
		primary: formatTokens(x.calls),
		share:
			tools.data && tools.data.total.calls > 0
				? x.calls / tools.data.total.calls
				: 0,
		cells: [formatTokens(x.errors), formatRatio(x.errors, x.calls)],
	}));
	const tables: ReactNode[] = [
		<BreakdownTable
			key="model"
			testId="table-model"
			title={t("stats.table.byModel")}
			columns={columns}
			primaryLabel={t("stats.table.cost")}
			rows={rowsOf("model", byModel.data)}
			loading={byModel.isPending}
		/>,
		<BreakdownTable
			key="project"
			testId="table-project"
			title={t("stats.table.byProject")}
			columns={columns}
			primaryLabel={t("stats.table.cost")}
			rows={rowsOf("project", byProject.data)}
			loading={byProject.isPending}
		/>,
		<BreakdownTable
			key="space"
			testId="table-space"
			title={t("stats.table.bySpace")}
			columns={columns}
			primaryLabel={t("stats.table.cost")}
			rows={rowsOf("space", bySpace.data)}
			loading={bySpace.isPending}
		/>,
		<BreakdownTable
			key="thread"
			testId="table-thread"
			title={t("stats.table.byThread")}
			columns={columns}
			primaryLabel={t("stats.table.cost")}
			rows={rowsOf("thread", byThread.data)}
			loading={byThread.isPending}
		/>,
		<BreakdownTable
			key="purpose"
			testId="table-purpose"
			title={t("stats.table.byPurpose")}
			columns={columns}
			primaryLabel={t("stats.table.cost")}
			rows={rowsOf("purpose", byPurpose.data)}
			loading={byPurpose.isPending}
		/>,
		...(params.model
			? []
			: [
					<BreakdownTable
						key="tools"
						testId="table-tools"
						title={t("stats.table.byTool")}
						columns={[t("stats.table.errors"), t("stats.table.errorRate")]}
						primaryLabel={t("stats.table.calls")}
						rows={toolRows}
						loading={tools.isPending}
					/>,
				]),
	];
	return (
		<div className="flex flex-col gap-3">
			<ErrorText error={main.error} />
			{main.isPending ? (
				<div className="flex justify-center p-6 text-muted">
					<Spinner size={18} />
				</div>
			) : (
				<KpiGrid items={kpis} />
			)}
			{budget && hasBudget(budget) && (
				<BudgetCard status={budget} onEdit={onEditBudget} />
			)}
			<Card
				title={t("stats.chart.usage", {
					metric: t(`stats.metric.${metric}`),
					by: t(`stats.by.${params.by}`),
				})}
			>
				<div className="flex flex-col gap-2">
					<div className="flex flex-wrap items-center gap-2">
						<Segmented<ByKey>
							label={t("stats.by.label")}
							value={params.by}
							options={BYS}
							attr="data-by"
							text={(v) => t(`stats.by.${v}`)}
							onChange={(by) => onChange({ by })}
						/>
						<Segmented<MetricKey>
							label={t("stats.metric.label")}
							value={metric}
							options={METRICS}
							attr="data-metric"
							text={(v) => t(`stats.metric.${v}`)}
							onChange={(m) => onChange({ metric: m })}
						/>
					</div>
					<StackedBars
						ariaLabel={t("stats.chart.usage", {
							metric: t(`stats.metric.${metric}`),
							by: t(`stats.by.${params.by}`),
						})}
						buckets={points.map((p) => ({
							key: p.key,
							label: bucketLabel(p.key, p.start, w.grain),
						}))}
						series={series}
						values={values}
						format={(v) => formatMetric(metric, v)}
					/>
					<Legend items={series} />
					{!main.isPending && total && total.requests === 0 && (
						<p className="text-faint text-xs">{t("stats.empty")}</p>
					)}
				</div>
			</Card>
			{/* 两列卡片按内容高度顶对齐：短表不被长表撑出大片空白 */}
			<div
				className={`grid items-start gap-3 ${bp === "expanded" ? "grid-cols-2" : "grid-cols-1"}`}
			>
				{tables}
			</div>
			<p className="text-faint text-xs">{t("stats.costNote")}</p>
		</div>
	);
}
