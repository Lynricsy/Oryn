/**
 * 产出标签（M10 计划 5.3）：指标行（新建、已交付、已归档、交付率、提交数、每交付成本）、主图（已交付 / 已归档两系列，
 * 交付用成功色、归档用弱色，与 Space 进度图一致）、按项目与按空间两张表（点行设为筛选）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { type OutputStat, useOutputStats } from "../../api/stats-queries";
import { formatCost, formatTokens } from "../../lib/format";
import { useBreakpoint } from "../../lib/viewport";
import { Legend } from "../charts/Legend";
import { StackedBars } from "../charts/StackedBars";
import { Card } from "../ui/data";
import { ErrorText, Spinner } from "../ui/primitives";
import { bucketLabel, formatRatio, groupName, OTHER_KEY } from "./format";
import { type BreakdownRow, BreakdownTable, KpiGrid } from "./parts";
import { coarseGrain, type StatsParams, type StatsWindow } from "./statsParams";

function perShipped(o: OutputStat): string {
	return o.threadsShipped > 0
		? formatCost(o.shippedCostUsd / o.threadsShipped)
		: "—";
}

export function OutputTab({
	params,
	window: w,
	onChange,
}: {
	params: StatsParams;
	window: StatsWindow;
	onChange: (patch: Partial<StatsParams>) => void;
}): ReactNode {
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const scope = {
		from: w.from,
		to: w.to,
		tz: w.tz,
		project: params.project,
		space: params.space,
	};
	const grain = coarseGrain(w.grain);
	const main = useOutputStats({ ...scope, groupBy: grain });
	const byProject = useOutputStats({ ...scope, groupBy: "project" });
	const bySpace = useOutputStats({ ...scope, groupBy: "space" });
	const total = main.data?.total;
	const kpis = total
		? [
				{
					name: "created",
					label: t("stats.kpi.created"),
					value: formatTokens(total.threadsCreated),
				},
				{
					name: "shipped",
					label: t("stats.kpi.shipped"),
					value: formatTokens(total.threadsShipped),
				},
				{
					name: "archived",
					label: t("stats.kpi.archived"),
					value: formatTokens(total.threadsArchived),
				},
				{
					name: "shipRate",
					label: t("stats.kpi.shipRate"),
					value: formatRatio(
						total.threadsShipped,
						total.threadsShipped + total.threadsArchived,
					),
				},
				{
					name: "commits",
					label: t("stats.kpi.commits"),
					value: formatTokens(total.commits),
				},
				{
					name: "costPerShipped",
					label: t("stats.kpi.costPerShipped"),
					value: perShipped(total),
				},
			]
		: [];
	const series = [
		{ key: "shipped", label: t("stats.kpi.shipped"), color: "--good" },
		{ key: "archived", label: t("stats.kpi.archived"), color: "--line-strong" },
	];
	const buckets = main.data?.groups ?? [];
	const rowsOf = (
		dim: "project" | "space",
		data: { groups: OutputStat[]; total: OutputStat } | undefined,
	): BreakdownRow[] => {
		const sum = data?.total.threadsShipped ?? 0;
		return (data?.groups ?? []).map((g) => {
			const { name, title } = groupName(dim, g, t);
			return {
				key: g.key,
				name,
				title,
				primary: formatTokens(g.threadsShipped),
				share: sum > 0 ? g.threadsShipped / sum : 0,
				cells: [
					formatTokens(g.threadsCreated),
					formatTokens(g.threadsArchived),
					formatTokens(g.commits),
					perShipped(g),
				],
				onSelect:
					g.key === OTHER_KEY
						? undefined
						: () => onChange({ [dim]: g.key === "" ? "none" : g.key }),
			};
		});
	};
	const columns = [
		t("stats.kpi.created"),
		t("stats.kpi.archived"),
		t("stats.kpi.commits"),
		t("stats.kpi.costPerShipped"),
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
			<Card title={t("stats.chart.output")}>
				<div className="flex flex-col gap-2">
					<StackedBars
						ariaLabel={t("stats.chart.output")}
						buckets={buckets.map((g) => ({
							key: g.key,
							label: bucketLabel(g.key, g.start, grain),
						}))}
						series={series}
						values={buckets.map((g) => [g.threadsShipped, g.threadsArchived])}
						format={(v) => formatTokens(v)}
					/>
					<Legend items={series} />
				</div>
			</Card>
			<div
				className={`grid items-start gap-3 ${bp === "expanded" ? "grid-cols-2" : "grid-cols-1"}`}
			>
				<BreakdownTable
					testId="table-project"
					title={t("stats.table.byProject")}
					columns={columns}
					primaryLabel={t("stats.kpi.shipped")}
					rows={rowsOf("project", byProject.data)}
					loading={byProject.isPending}
				/>
				<BreakdownTable
					testId="table-space"
					title={t("stats.table.bySpace")}
					columns={columns}
					primaryLabel={t("stats.kpi.shipped")}
					rows={rowsOf("space", bySpace.data)}
					loading={bySpace.isPending}
				/>
			</div>
		</div>
	);
}
