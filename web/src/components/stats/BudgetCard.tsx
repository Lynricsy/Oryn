/**
 * 预算卡（M10 计划 5.3 + 修订 R4.3）：设了上限时显示当日、当月两条量表（`warn` 警告色、`over` 错误色），
 * 每条带 `data-budget-period` 与 `data-level`；「设置预算」对话框：日上限、月上限、预警线、时区、启用。
 * 对话框底部说明告警怎么送达，「设置 → 通知」链到通知设置。没设上限时卡片不出现，入口是页头导出旁的「预算…」。
 */
import { type ReactNode, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Schemas } from "../../api/client";
import { type BudgetStatus, useSetBudget } from "../../api/stats-queries";
import { formatCost } from "../../lib/format";
import { Badge, Card, Meter, type Tone } from "../ui/data";
import {
	Button,
	Dialog,
	ErrorText,
	Field,
	TextInput,
	Toggle,
} from "../ui/primitives";
import { browserTimeZone } from "./statsParams";

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

export function BudgetCard({
	status,
	onEdit,
}: {
	status: BudgetStatus;
	onEdit: () => void;
}): ReactNode {
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
					<Button size="sm" variant="ghost" onClick={onEdit}>
						{t("stats.budget.edit")}
					</Button>
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

/** 金额输入：空 = 不设；否则须 ≥ 0.01。 */
function parseUsd(s: string): number | undefined | null {
	const v = s.trim();
	if (!v) return undefined;
	const n = Number(v);
	return Number.isFinite(n) && n >= 0.01 ? n : null;
}

export function BudgetDialog({
	status,
	onClose,
}: {
	status: BudgetStatus | undefined;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const b = status?.budget;
	const [daily, setDaily] = useState(b?.dailyUsd ? String(b.dailyUsd) : "");
	const [monthly, setMonthly] = useState(
		b?.monthlyUsd ? String(b.monthlyUsd) : "",
	);
	const [warn, setWarn] = useState(String(b?.warnPct ?? 80));
	const [tz, setTz] = useState(
		b && b.timezone !== "UTC" ? b.timezone : (b?.timezone ?? browserTimeZone()),
	);
	const [enabled, setEnabled] = useState(b?.enabled ?? true);
	const save = useSetBudget();
	const d = parseUsd(daily);
	const m = parseUsd(monthly);
	const w = Number(warn);
	const valid =
		d !== null &&
		m !== null &&
		Number.isInteger(w) &&
		w >= 0 &&
		w <= 99 &&
		tz.trim() !== "";
	const submit = () => {
		if (!valid) return;
		save.mutate(
			{
				...(d !== undefined ? { dailyUsd: d } : {}),
				...(m !== undefined ? { monthlyUsd: m } : {}),
				warnPct: w,
				timezone: tz.trim(),
				enabled,
			},
			{ onSuccess: onClose },
		);
	};
	return (
		<Dialog
			open
			title={t("stats.budget.dialog.title")}
			onClose={onClose}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						variant="primary"
						disabled={!valid || save.isPending}
						onClick={submit}
					>
						{t("common.save")}
					</Button>
				</>
			}
		>
			<form
				className="flex flex-col gap-3"
				onSubmit={(e) => {
					e.preventDefault();
					submit();
				}}
			>
				<div className="grid grid-cols-2 gap-3">
					<Field label={t("stats.budget.dialog.daily")}>
						<TextInput
							inputMode="decimal"
							value={daily}
							placeholder="—"
							onChange={(e) => setDaily(e.target.value)}
						/>
					</Field>
					<Field label={t("stats.budget.dialog.monthly")}>
						<TextInput
							inputMode="decimal"
							value={monthly}
							placeholder="—"
							onChange={(e) => setMonthly(e.target.value)}
						/>
					</Field>
					<Field
						label={t("stats.budget.dialog.warnPct")}
						hint={t("stats.budget.dialog.warnHint")}
					>
						<TextInput
							inputMode="numeric"
							value={warn}
							onChange={(e) => setWarn(e.target.value)}
						/>
					</Field>
					<Field label={t("stats.budget.dialog.timezone")}>
						<TextInput value={tz} onChange={(e) => setTz(e.target.value)} />
					</Field>
				</div>
				<Toggle
					checked={enabled}
					onChange={setEnabled}
					label={t("stats.budget.dialog.enabled")}
				/>
				{(d === null || m === null) && (
					<p className="text-err text-xs">{t("stats.budget.dialog.invalid")}</p>
				)}
				<ErrorText error={save.error} />
				<p className="text-faint text-xs">
					<Trans
						i18nKey="stats.budget.notifyHint"
						components={{
							// 标签名不能用 link：它是 HTML 空元素，Trans 的解析器会把它当自闭合、丢掉里面的文字
							settings: (
								<Link
									to="/settings/notifications"
									className="text-accent-ink underline-offset-2 hover:underline"
								/>
							),
						}}
					/>
				</p>
			</form>
		</Dialog>
	);
}
