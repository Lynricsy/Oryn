/**
 * 统计页 `/stats/:tab`（M10 计划 5.3）：用量 / Orb / 产出三个标签，范围与筛选都在 URL 查询串里（刷新、分享后视图不变）。
 * 独立的全宽数据页（不进设置），整页懒加载。回填未完成时页头下方一条提示。
 * 用量标签挂载期间把通知 store 里未读的预算通知逐条按 id 标为已读（修订 R4.3：看到即已读）。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	Navigate,
	useNavigate,
	useParams,
	useSearchParams,
} from "react-router";
import { statsExportUrl, useBudget, useStatsMeta } from "../api/stats-queries";
import { BudgetDialog, hasBudget } from "../components/stats/BudgetCard";
import { OrbsTab } from "../components/stats/OrbsTab";
import { OutputTab } from "../components/stats/OutputTab";
import {
	type ExportLinks,
	StatsTitleBar,
	StatsToolbar,
} from "../components/stats/StatsHeader";
import {
	coarseGrain,
	type FilterKey,
	filterQuery,
	parseStatsParams,
	STATS_TABS,
	type StatsParams,
	type StatsTab,
	type StatsWindow,
	statsSearch,
	statsWindow,
} from "../components/stats/statsParams";
import { UsageTab } from "../components/stats/UsageTab";
import { Callout, Sheet } from "../components/ui/data";
import { Button } from "../components/ui/primitives";
import { useBreakpoint } from "../lib/viewport";
import { notificationsStore, useNotifications } from "../state/notifications";

/** 各标签认的筛选：orb 只按项目，产出按项目与空间。 */
const TAB_FILTERS: Record<StatsTab, readonly FilterKey[]> = {
	usage: ["project", "space", "model", "thread"],
	orbs: ["project"],
	output: ["project", "space"],
};

function exportLinks(
	tab: StatsTab,
	params: StatsParams,
	w: StatsWindow,
): ExportLinks {
	const filters = filterQuery(params);
	const win = { from: w.from, to: w.to, tz: w.tz };
	const view =
		tab === "usage"
			? {
					kind: "usage" as const,
					q: { ...win, ...filters, groupBy: params.by, series: w.grain },
				}
			: tab === "orbs"
				? {
						kind: "orbs" as const,
						q: {
							...win,
							project: filters.project,
							groupBy: coarseGrain(w.grain),
						},
					}
				: {
						kind: "output" as const,
						q: {
							...win,
							project: filters.project,
							space: filters.space,
							groupBy: coarseGrain(w.grain),
						},
					};
	return {
		viewCsv: statsExportUrl(view.kind, view.q, "csv"),
		viewJson: statsExportUrl(view.kind, view.q, "json"),
		recordsCsv: statsExportUrl(
			"records",
			{ from: w.from, to: w.to, ...filters },
			"csv",
		),
	};
}

/** 进入用量标签时，把未读的预算通知逐条按 id 标已读。 */
function useReadBudgetNotifications(active: boolean): void {
	const unread = useNotifications((s) =>
		s.items
			.filter((n) => n.kind === "budget" && !n.readAt)
			.map((n) => n.id)
			.join(","),
	);
	useEffect(() => {
		if (!active || !unread) return;
		for (const id of unread.split(","))
			void notificationsStore
				.getState()
				.markRead({ id })
				.catch(() => {});
	}, [active, unread]);
}

export function StatsPage(): ReactNode {
	const { tab: raw } = useParams();
	const tab = STATS_TABS.find((x) => x === raw);
	if (!tab) return <Navigate to="/stats/usage" replace />;
	return <StatsScreen tab={tab} />;
}

function StatsScreen({ tab }: { tab: StatsTab }): ReactNode {
	const { t } = useTranslation();
	const bp = useBreakpoint();
	const navigate = useNavigate();
	const [sp] = useSearchParams();
	const params = parseStatsParams(sp);
	const meta = useStatsMeta();
	const budget = useBudget();
	const [budgetOpen, setBudgetOpen] = useState(false);
	useReadBudgetNotifications(tab === "usage");
	// 窗口按「今天」算；`all` 的起点等 meta（没有则退回 30 天）
	const w = statsWindow(params, Date.now(), meta.data?.earliestTs);
	const onChange = (patch: Partial<StatsParams>) => {
		const search = statsSearch({ ...params, ...patch });
		navigate(`/stats/${tab}${search ? `?${search}` : ""}`);
	};
	const backfill = meta.data?.backfill;
	return (
		<Sheet inset={bp === "expanded"} className="flex h-full min-h-0 flex-col">
			<StatsTitleBar tab={tab} params={params} />
			<div
				data-testid="stats-scroll"
				className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden p-3 pb-safe sm:p-5"
			>
				<div className="mx-auto flex max-w-7xl min-w-0 flex-col gap-3">
					<StatsToolbar
						params={params}
						filterKeys={TAB_FILTERS[tab]}
						onChange={onChange}
						links={exportLinks(tab, params, w)}
						extra={
							tab === "usage" && budget.data && !hasBudget(budget.data) ? (
								<Button
									size="sm"
									icon="coin"
									onClick={() => setBudgetOpen(true)}
								>
									{t("stats.budget.open")}
								</Button>
							) : undefined
						}
					/>
					{backfill && (
						<Callout tone="neutral" role="status">
							{t("stats.backfill", {
								done: backfill.done,
								total: backfill.total,
							})}
						</Callout>
					)}
					{tab === "usage" && (
						<UsageTab
							params={params}
							window={w}
							onChange={onChange}
							budget={budget.data}
							onEditBudget={() => setBudgetOpen(true)}
						/>
					)}
					{tab === "orbs" && <OrbsTab params={params} window={w} />}
					{tab === "output" && (
						<OutputTab params={params} window={w} onChange={onChange} />
					)}
				</div>
			</div>
			{budgetOpen && (
				<BudgetDialog
					status={budget.data}
					onClose={() => setBudgetOpen(false)}
				/>
			)}
		</Sheet>
	);
}
