/**
 * 预算卡（M10 计划 5.3 + 修订 R4.3）：设了上限时显示当日、当月两条量表（`warn` 警告色、`over` 错误色），
 * 每条带 `data-budget-period` 与 `data-level`。统计页只看预算：改预算在「设置 → 通知与预算」
 * （pages/settings/BudgetSettings.tsx），卡片与页头的「预算…」都是链过去的 `BudgetSettingsLink`。
 * 没设上限时卡片不出现，入口只有页头导出旁的「预算…」。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Schemas } from "../../api/client";
import type { BudgetStatus } from "../../api/stats-queries";
import { formatCost } from "../../lib/format";
import { Badge, Card, Meter, type Tone } from "../ui/data";
import { Icon } from "../ui/icons";

type Period = Schemas["BudgetPeriod"];

const LEVEL_TONE: Record<Period["level"], Tone> = {
	none: "neutral",
	warn: "warn",
	over: "err",
};

/** 设了日或月上限。 */
export function hasBudget(s: BudgetStatus | undefined): boolean {
	return Boolean(s && (s.budget.dailyUsd || s.budget.monthlyUsd));
}

function PeriodRow({
	period,
	kind,
}: {
	period: Period;
	kind: "day" | "month";
}): ReactNode {
	const { t } = useTranslation();
	const limit = period.limitUsd;
	return (
		<div
			data-budget-period={kind}
			data-level={period.level}
			className="flex flex-col gap-1"
		>
			<div className="flex items-baseline gap-2 text-xs">
				<span className="font-medium text-muted">
					{t(`stats.budget.${kind}`)}
				</span>
				<span className="text-faint">{period.key}</span>
				<span className="min-w-0 flex-1" />
				{period.level !== "none" && (
					// 文字用 ink 令牌（≥ 4.5:1）；量表仍用原警告 / 错误色。`--bad` 本身在明暗两套卡片底上已 ≥ 4.5:1
					<span
						data-testid="budget-level"
						className={period.level === "over" ? "text-err" : "text-warn-ink"}
					>
						{t(`stats.budget.level.${period.level}`)}
					</span>
				)}
				<span className="text-fg tabular-nums">
					{limit
						? t("stats.budget.spent", {
								spent: formatCost(period.spentUsd),
								limit: formatCost(limit),
							})
						: formatCost(period.spentUsd)}
				</span>
			</div>
			{limit ? (
				<Meter
					value={period.spentUsd}
					max={limit}
					tone={LEVEL_TONE[period.level]}
					label={t(`stats.budget.${kind}`)}
				/>
			) : (
				<span className="text-faint text-xs">{t("stats.budget.noLimit")}</span>
			)}
		</div>
	);
}

/** 链到预算设置（设置 → 通知与预算），外观同小号按钮。 */
export function BudgetSettingsLink({
	children,
	variant = "secondary",
}: {
	children: ReactNode;
	variant?: "secondary" | "ghost";
}): ReactNode {
	return (
		<Link
			to="/settings/notifications"
			className={`inline-flex h-7 select-none items-center justify-center gap-1 whitespace-nowrap rounded-[10px] px-2.5 font-medium text-xs transition-colors duration-150 ${variant === "secondary" ? "border border-border bg-panel text-fg shadow-xs hover:bg-hover" : "text-muted hover:bg-hover hover:text-fg"}`}
		>
			{variant === "secondary" && <Icon name="coin" size={13} />}
			{children}
		</Link>
	);
}

export function BudgetCard({ status }: { status: BudgetStatus }): ReactNode {
	const { t } = useTranslation();
	return (
		<Card
			title={t("stats.budget.title")}
			icon="coin"
			action={
				<span className="flex items-center gap-1.5">
					{!status.budget.enabled && (
						<Badge>{t("stats.budget.disabled")}</Badge>
					)}
					<BudgetSettingsLink variant="ghost">
						{t("stats.budget.manage")}
					</BudgetSettingsLink>
				</span>
			}
		>
			<div data-testid="budget-card" className="flex flex-col gap-3">
				<PeriodRow period={status.day} kind="day" />
				<PeriodRow period={status.month} kind="month" />
			</div>
		</Card>
	);
}
