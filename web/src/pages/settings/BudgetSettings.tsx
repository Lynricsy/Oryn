/**
 * 设置 → 通知与预算 → 预算（M10 计划 5.3 + 修订 R4.3）：预算只在这里改，统计页的预算卡只看、并链到这里。
 * 一行一件事：告警开关（立即保存）、日上限 / 月上限（本期已用与级别）、预警线、时区；「编辑」对话框改后四项。
 * 每次保存都整体 PUT `/stats/budget`（`useSetBudget`，成功后写回查询缓存）。告警走通知中心、推送与勾选了
 * 「预算告警」事件的渠道。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Schemas } from "../../api/client";
import {
	type Budget,
	type BudgetStatus,
	useBudget,
	useSetBudget,
} from "../../api/stats-queries";
import { browserTimeZone } from "../../components/stats/statsParams";
import {
	Button,
	Dialog,
	ErrorText,
	Spinner,
	TextInput,
	Toggle,
} from "../../components/ui/primitives";
import {
	FormField,
	Group,
	GroupList,
	GroupRow,
} from "../../components/ui/workbench";
import { formatCost } from "../../lib/format";

/** 金额输入：空 = 不设；否则须 ≥ 0.01（null = 无效）。 */
function parseUsd(s: string): number | undefined | null {
	const v = s.trim();
	if (!v) return undefined;
	const n = Number(v);
	return Number.isFinite(n) && n >= 0.01 ? n : null;
}

/** 本期已用与级别（日 / 月上限行的说明）。 */
function PeriodMeta({
	period,
	kind,
}: {
	period: Schemas["BudgetPeriod"];
	kind: "day" | "month";
}): ReactNode {
	const { t } = useTranslation();
	return (
		<>
			<span className="tabular-nums">
				{t(`notifySettings.budget.spent.${kind}`, {
					spent: formatCost(period.spentUsd),
				})}
			</span>
			{period.level !== "none" && (
				<>
					<span aria-hidden className="text-faint">
						·
					</span>
					<span
						data-budget-level={period.level}
						className={period.level === "over" ? "text-err" : "text-warn-ink"}
					>
						{t(`stats.budget.level.${period.level}`)}
					</span>
				</>
			)}
		</>
	);
}

function Value({ children }: { children: ReactNode }): ReactNode {
	return <span className="text-fg text-sm tabular-nums">{children}</span>;
}

export function BudgetGroup(): ReactNode {
	const { t } = useTranslation();
	const budget = useBudget();
	const save = useSetBudget();
	const [editing, setEditing] = useState(false);
	const s = budget.data;
	return (
		<Group
			title={t("notifySettings.budget.title")}
			action={
				<>
					<Link
						to="/stats/usage"
						className="text-[12.5px] text-accent-ink underline-offset-2 hover:underline"
					>
						{t("notifySettings.budget.viewUsage")}
					</Link>
					<Button size="sm" disabled={!s} onClick={() => setEditing(true)}>
						{t("common.edit")}
					</Button>
				</>
			}
		>
			{budget.isPending && <Spinner />}
			<ErrorText error={budget.error} />
			{s && (
				<GroupList>
					<GroupRow
						icon="bell"
						title={t("notifySettings.budget.enabled")}
						meta={t("notifySettings.budget.enabledHint")}
						trailing={
							<Toggle
								hideLabel
								label={t("notifySettings.budget.enabled")}
								checked={s.budget.enabled}
								disabled={save.isPending}
								onChange={(enabled) => save.mutate({ ...s.budget, enabled })}
							/>
						}
					>
						{save.error ? <ErrorText error={save.error} /> : null}
					</GroupRow>
					{(["day", "month"] as const).map((kind) => {
						const limit =
							kind === "day" ? s.budget.dailyUsd : s.budget.monthlyUsd;
						return (
							<GroupRow
								key={kind}
								icon="coin"
								title={t(`notifySettings.budget.limit.${kind}`)}
								meta={<PeriodMeta period={s[kind]} kind={kind} />}
								trailing={
									<Value>
										{limit ? formatCost(limit) : t("stats.budget.noLimit")}
									</Value>
								}
							/>
						);
					})}
					<GroupRow
						icon="gauge"
						title={t("notifySettings.budget.warnPct")}
						meta={t("notifySettings.budget.warnHint")}
						trailing={<Value>{s.budget.warnPct}%</Value>}
					/>
					<GroupRow
						icon="clock"
						title={t("notifySettings.budget.timezone")}
						meta={t("notifySettings.budget.timezoneHint")}
						trailing={<Value>{s.budget.timezone}</Value>}
					/>
				</GroupList>
			)}
			{editing && (
				<BudgetDialog budget={s?.budget} onClose={() => setEditing(false)} />
			)}
		</Group>
	);
}

/** 编辑上限、预警线与时区；告警开关留在行尾，这里原样带上。 */
function BudgetDialog({
	budget: b,
	onClose,
}: {
	budget: BudgetStatus["budget"] | undefined;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const [daily, setDaily] = useState(b?.dailyUsd ? String(b.dailyUsd) : "");
	const [monthly, setMonthly] = useState(
		b?.monthlyUsd ? String(b.monthlyUsd) : "",
	);
	const [warn, setWarn] = useState(String(b?.warnPct ?? 80));
	// 从没设过预算时（服务端缺省 UTC、也没有上限）默认本机时区
	const [tz, setTz] = useState(
		b && (b.timezone !== "UTC" || b.dailyUsd || b.monthlyUsd)
			? b.timezone
			: browserTimeZone(),
	);
	const save = useSetBudget();
	const d = parseUsd(daily);
	const m = parseUsd(monthly);
	const w = Number(warn);
	const valid =
		d !== null &&
		m !== null &&
		warn.trim() !== "" &&
		Number.isInteger(w) &&
		w >= 0 &&
		w <= 99 &&
		tz.trim() !== "";
	const submit = () => {
		if (!valid || save.isPending) return;
		const body: Budget = {
			...(d !== undefined ? { dailyUsd: d } : {}),
			...(m !== undefined ? { monthlyUsd: m } : {}),
			warnPct: w,
			timezone: tz.trim(),
			enabled: b?.enabled ?? true,
		};
		save.mutate(body, { onSuccess: onClose });
	};
	return (
		<Dialog
			open
			title={t("notifySettings.budget.dialogTitle")}
			onClose={onClose}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						type="submit"
						form="budget-form"
						variant="primary"
						disabled={!valid || save.isPending}
					>
						{t("common.save")}
					</Button>
				</>
			}
		>
			<form
				id="budget-form"
				className="flex flex-col gap-4"
				onSubmit={(e) => {
					e.preventDefault();
					submit();
				}}
			>
				<div className="grid gap-4 sm:grid-cols-2">
					<FormField
						label={t("notifySettings.budget.limit.dayUsd")}
						optional={t("notifySettings.budget.optional")}
					>
						{(p) => (
							<TextInput
								{...p}
								inputMode="decimal"
								value={daily}
								placeholder="—"
								onChange={(e) => setDaily(e.target.value)}
							/>
						)}
					</FormField>
					<FormField
						label={t("notifySettings.budget.limit.monthUsd")}
						optional={t("notifySettings.budget.optional")}
					>
						{(p) => (
							<TextInput
								{...p}
								inputMode="decimal"
								value={monthly}
								placeholder="—"
								onChange={(e) => setMonthly(e.target.value)}
							/>
						)}
					</FormField>
					<FormField
						label={t("notifySettings.budget.warnPctField")}
						aside={t("notifySettings.budget.warnZero")}
					>
						{(p) => (
							<TextInput
								{...p}
								inputMode="numeric"
								value={warn}
								onChange={(e) => setWarn(e.target.value)}
							/>
						)}
					</FormField>
					<FormField
						label={t("notifySettings.budget.timezone")}
						aside={t("notifySettings.budget.timezoneField")}
					>
						{(p) => (
							<TextInput
								{...p}
								value={tz}
								onChange={(e) => setTz(e.target.value)}
							/>
						)}
					</FormField>
				</div>
				{(d === null || m === null) && (
					<p className="text-err text-xs">
						{t("notifySettings.budget.invalid")}
					</p>
				)}
				<ErrorText error={save.error} />
			</form>
		</Dialog>
	);
}
