/**
 * Orb 标签（M10 计划 5.3）：指标行（运行时长、冻结占比、orb 数、启动 / 唤醒 / 失败、setup 中位耗时与慢 setup、
 * 由快照启动的占比）、主图（每桶运行 / 冻结小时）、按类型表（规格、运行、冻结、核时）、orb 列表（运行 / 冻结 / 停机
 * 三段占比条，`data-orb-row`），点一行打开 `OrbDrawer`。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type OrbStat,
	useOrbStats,
	useStatsMeta,
} from "../../api/stats-queries";
import { formatDuration, formatMemoryMB } from "../../lib/format";
import { useBreakpoint } from "../../lib/viewport";
import { Legend } from "../charts/Legend";
import { StackedBars } from "../charts/StackedBars";
import { TIMELINE_COLOR } from "../charts/TimelineStrip";
import { Card } from "../ui/data";
import { ErrorText, Spinner } from "../ui/primitives";
import { bucketLabel, formatHours, formatRatio } from "./format";
import { OrbDrawer } from "./OrbDrawer";
import { type BreakdownRow, BreakdownTable, KpiGrid } from "./parts";
import { coarseGrain, type StatsParams, type StatsWindow } from "./statsParams";

/** 运行 / 冻结 / 停机三段占比条（中性底色上按语义色分段）。 */
function StateBar({ o, label }: { o: OrbStat; label: string }): ReactNode {
	const sum = o.runningMs + o.frozenMs + o.stoppedMs;
	const parts = [
		{ key: "running", ms: o.runningMs, color: TIMELINE_COLOR.running },
		{ key: "frozen", ms: o.frozenMs, color: TIMELINE_COLOR.frozen },
		{ key: "stopped", ms: o.stoppedMs, color: TIMELINE_COLOR.stopped },
	];
	return (
		<span
			role="img"
			aria-label={label}
			className="flex h-1.5 w-full overflow-hidden rounded-full bg-inset"
		>
			{sum > 0 &&
				parts.map((p) =>
					p.ms > 0 ? (
						<span
							key={p.key}
							className="h-full"
							style={{
								width: `${(p.ms / sum) * 100}%`,
								background: `var(${p.color})`,
							}}
						/>
					) : null,
				)}
		</span>
	);
}

export function OrbsTab({
	params,
	window: w,
}: {
	params: StatsParams;
	window: StatsWindow;
}): ReactNode {
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const scope = { from: w.from, to: w.to, tz: w.tz, project: params.project };
	const grain = coarseGrain(w.grain);
	const main = useOrbStats({ ...scope, groupBy: grain });
	const byType = useOrbStats({ ...scope, groupBy: "type" });
	const list = useOrbStats({ ...scope, groupBy: "orb" });
	const meta = useStatsMeta();
	const [open, setOpen] = useState<{ id: string; title: string } | null>(null);
	const total = main.data?.total;
	const setup = main.data?.setup;
	const kpis =
		total && setup
			? [
					{
						name: "running",
						label: t("stats.kpi.running"),
						value: formatHours(total.runningMs),
					},
					{
						name: "frozen",
						label: t("stats.kpi.frozenShare"),
						value: formatRatio(
							total.frozenMs,
							total.runningMs + total.frozenMs,
						),
						sub: formatHours(total.frozenMs),
					},
					{
						name: "orbs",
						label: t("stats.kpi.orbs"),
						value: String(total.orbs),
					},
					{
						name: "spawns",
						label: t("stats.kpi.spawns"),
						value: `${total.spawns} / ${total.wakes} / ${total.failures}`,
					},
					{
						name: "setup",
						label: t("stats.kpi.setup"),
						value:
							setup.p50Ms !== undefined ? formatDuration(setup.p50Ms) : "—",
						sub: t("stats.kpi.slowSetups", { n: setup.slowRuns }),
					},
					{
						name: "snapshot",
						label: t("stats.kpi.snapshot"),
						value: formatRatio(total.snapshotSpawns, total.spawns),
					},
				]
			: [];
	const series = [
		{
			key: "running",
			label: t("stats.orbs.state.running"),
			color: TIMELINE_COLOR.running,
		},
		{
			key: "frozen",
			label: t("stats.orbs.state.frozen"),
			color: TIMELINE_COLOR.frozen,
		},
	];
	const buckets = main.data?.groups ?? [];
	const typeSum = byType.data?.total.runningMs ?? 0;
	const typeRows: BreakdownRow[] = (byType.data?.groups ?? []).map((g) => ({
		key: g.key,
		name: g.type ?? g.key,
		primary: formatHours(g.runningMs),
		share: typeSum > 0 ? g.runningMs / typeSum : 0,
		cells: [
			g.cpus !== undefined && g.memoryMB !== undefined
				? `${g.cpus}C/${formatMemoryMB(g.memoryMB)}`
				: "—",
			formatHours(g.frozenMs),
			g.cpus !== undefined ? formatHours(g.runningMs * g.cpus) : "—",
		],
	}));
	const orbs = list.data?.groups ?? [];
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
			<Card title={t("stats.chart.orbs")}>
				<div className="flex flex-col gap-2">
					<StackedBars
						ariaLabel={t("stats.chart.orbs")}
						buckets={buckets.map((g) => ({
							key: g.key,
							label: bucketLabel(g.key, g.start, grain),
						}))}
						series={series}
						values={buckets.map((g) => [
							g.runningMs / 3_600_000,
							g.frozenMs / 3_600_000,
						])}
						format={(h) => formatHours(h * 3_600_000)}
					/>
					<Legend items={series} />
				</div>
			</Card>
			<div
				className={`grid gap-3 ${bp === "expanded" ? "grid-cols-2" : "grid-cols-1"}`}
			>
				<BreakdownTable
					testId="table-type"
					title={t("stats.orbs.byType")}
					columns={[
						t("stats.orbs.spec"),
						t("stats.orbs.frozen"),
						t("stats.orbs.coreHours"),
					]}
					primaryLabel={t("stats.orbs.running")}
					rows={typeRows}
					loading={byType.isPending}
				/>
				<Card title={t("stats.orbs.list")} className="min-w-0">
					<ErrorText error={list.error} />
					{list.isPending && <Spinner />}
					{!list.isPending && orbs.length === 0 && (
						<p className="px-2 py-3 text-faint text-xs">{t("stats.empty")}</p>
					)}
					<ul className="flex min-w-0 flex-col">
						{orbs.map((o) => {
							const title = o.label || t("stats.orbs.untitled");
							return (
								<li key={o.key}>
									<button
										type="button"
										data-orb-row={o.key}
										onClick={() => setOpen({ id: o.key, title })}
										className="flex w-full min-w-0 flex-col gap-1 rounded-md px-2 py-1.5 text-left transition-colors duration-150 hover:bg-hover"
									>
										<span className="flex w-full min-w-0 items-baseline gap-2 text-sm">
											<span className="min-w-0 flex-1 truncate" title={o.key}>
												{title}
											</span>
											<span className="shrink-0 text-faint text-xs">
												{o.type ?? ""}
											</span>
											<span className="shrink-0 tabular-nums">
												{formatHours(o.runningMs)}
											</span>
										</span>
										<StateBar
											o={o}
											label={t("stats.orbs.barLabel", {
												running: formatHours(o.runningMs),
												frozen: formatHours(o.frozenMs),
												stopped: formatHours(o.stoppedMs),
											})}
										/>
									</button>
								</li>
							);
						})}
					</ul>
				</Card>
			</div>
			{open && (
				<OrbDrawer
					orbId={open.id}
					title={open.title}
					from={w.from}
					to={w.to}
					samplesRetentionMs={meta.data?.retention.samplesMs ?? 0}
					onClose={() => setOpen(null)}
				/>
			)}
		</div>
	);
}
