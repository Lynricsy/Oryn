/**
 * `goal` — goal-mode lifecycle: set/check/complete/pause/resume/drop an objective with an optional token budget.
 * 头部读数是目标状态与预算用量（细条 + 已用 / 预算）；输出段是目标面板：状态、目标原文、token 预算量表、用时、完成报告。
 * 形状见 oryn-core `tui/src/tools/goal.ts`（`GoalToolDetails`：op / goal / remainingTokens / completionBudgetReport）。
 */
import type { CSSProperties, ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { formatDuration, formatTokens } from "../../lib/format";
import { EmptyNote, ErrorBlock, InvalidArg, ResultText } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	isRecord,
	normalizeWs,
	num,
	resultTextOf,
	str,
	truncate,
} from "../util";

interface GoalView {
	objective: string;
	status: string;
	tokenBudget: number | null;
	tokensUsed: number | null;
	timeUsedSeconds: number | null;
}

/** Narrow `details.goal` (untrusted JSON) to the fields we render. */
function goalOf(details: Record<string, unknown> | null): GoalView | null {
	const g = details?.goal;
	if (!isRecord(g)) return null;
	const objective = str(g.objective);
	const status = str(g.status);
	if (objective === null || status === null) return null;
	return {
		objective,
		status,
		tokenBudget: num(g.tokenBudget),
		tokensUsed: num(g.tokensUsed),
		timeUsedSeconds: num(g.timeUsedSeconds),
	};
}

/** 查找表的键（类型守卫）：让拼出的 i18n 键保持字面量类型。 */
function isKeyOf<T extends string>(
	table: Readonly<Record<T, unknown>>,
	key: string,
): key is T {
	return Object.hasOwn(table, key);
}

const OPS = {
	create: true,
	get: true,
	complete: true,
	pause: true,
	resume: true,
	drop: true,
} as const;

const STATUSES = {
	active: true,
	paused: true,
	"budget-limited": true,
	complete: true,
	dropped: true,
} as const;

/** 状态的语气：完成绿、预算耗尽黄、进行中强调色（还在推进），暂停与放弃中性。 */
const STATUS_TONE: Record<string, "ok" | "warn" | "accent"> = {
	complete: "ok",
	"budget-limited": "warn",
	active: "accent",
};

/** 预算用量的比例（0–1）；没有预算时为 null。 */
function usage(goal: GoalView): number | null {
	if (goal.tokenBudget === null || goal.tokenBudget <= 0) return null;
	return Math.min(1, Math.max(0, (goal.tokensUsed ?? 0) / goal.tokenBudget));
}

function useStatusLabel(): (status: string) => string {
	const { t } = useTranslation();
	return (status) =>
		isKeyOf(STATUSES, status) ? t(`toolRender.goal.status.${status}`) : status;
}

function Summary({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const details = detailsRecord(result);
	const goal = goalOf(details);
	const op = str(details?.op) ?? str(args.op);
	const objective = goal?.objective ?? str(args.objective);
	return (
		<>
			{op === null && args.op !== undefined ? (
				<InvalidArg what="op" />
			) : (
				<span className="tv-goal-op">
					{op && isKeyOf(OPS, op) ? t(`toolRender.goal.op.${op}`) : (op ?? "?")}
				</span>
			)}
			{objective !== null && objective.trim() !== "" && (
				<span className="tv-muted">{truncate(normalizeWs(objective), 64)}</span>
			)}
		</>
	);
}

/** 右侧读数：预算细条 + 已用 / 预算（无预算只写已用），再加目标状态（完成绿、耗尽黄）。 */
function Meta({ result }: ToolRenderProps): ReactNode {
	const label = useStatusLabel();
	const goal = goalOf(detailsRecord(result));
	if (!goal) return null;
	const ratio = usage(goal);
	const tone = STATUS_TONE[goal.status];
	return (
		<>
			{ratio !== null && (
				<span
					className={`tv-goal-mini${ratio >= 1 ? " tv-goal-mini--full" : ""}`}
					style={{ "--tv-frac": ratio } as CSSProperties}
					aria-hidden="true"
				/>
			)}
			{goal.tokensUsed !== null && (
				<span>
					{formatTokens(goal.tokensUsed)}
					{goal.tokenBudget !== null && ` / ${formatTokens(goal.tokenBudget)}`}
				</span>
			)}
			<span
				className={
					tone === "ok" || tone === "warn" ? `tv-meta-${tone}` : undefined
				}
			>
				{label(goal.status)}
			</span>
		</>
	);
}

/** 输入：要设定的目标全文与预算（只有 create 带这些参数）。 */
function Input({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const objective = str(args.objective)?.trim();
	const budget = num(args.token_budget);
	if (!objective && budget === null) return null;
	return (
		<div className="tv-goal-in">
			{objective && <div className="tv-goal-objective">{objective}</div>}
			{budget !== null && (
				<div className="tv-goal-budget">
					{t("toolRender.goal.budget", { n: formatTokens(budget) })}
				</div>
			)}
		</div>
	);
}

/** 输出：目标面板（状态、目标原文、预算量表、用时）+ 完成报告；没有目标时给空状态，出错时给错误。 */
function Result({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const label = useStatusLabel();
	if (!result) return null;
	const details = detailsRecord(result);
	const goal = goalOf(details);
	if (result.isError) return <ErrorBlock text={resultTextOf(result)} />;
	if (!goal) {
		return details !== null ? (
			<EmptyNote>{t("toolRender.goal.none")}</EmptyNote>
		) : (
			<ResultText result={result} maxLines={10} />
		);
	}
	const objective = goal.objective.trim();
	// 输入段已有同一句目标时不再重复
	const showObjective =
		objective !== "" && objective !== (str(args.objective)?.trim() ?? "");
	const ratio = usage(goal);
	const used = goal.tokensUsed ?? 0;
	const remaining =
		num(details?.remainingTokens) ??
		(goal.tokenBudget !== null ? Math.max(0, goal.tokenBudget - used) : null);
	const seconds = goal.timeUsedSeconds ?? 0;
	const tone = STATUS_TONE[goal.status] ?? "idle";
	// 完成报告（completionBudgetReport）是写给模型的「向用户报告用量」提示，面板已给出同样的用量，不再重复
	return (
		<div className="tv-goal-panel">
			<div className="tv-goal-head">
				<span className={`tv-goal-status tv-goal-status--${tone}`}>
					<span className="tv-goal-status-dot" aria-hidden="true" />
					{label(goal.status)}
				</span>
				{seconds > 0 && (
					<span className="tv-goal-time">
						{t("toolRender.goal.elapsed", {
							d: formatDuration(seconds * 1000),
						})}
					</span>
				)}
			</div>
			{showObjective && <div className="tv-goal-objective">{objective}</div>}
			{ratio !== null ? (
				<div className="tv-goal-gauge">
					<div
						className={`tv-goal-bar${ratio >= 1 ? " tv-goal-bar--full" : ratio >= 0.8 && goal.status !== "complete" ? " tv-goal-bar--high" : ""}`}
						role="progressbar"
						aria-valuenow={Math.round(ratio * 100)}
						aria-valuemin={0}
						aria-valuemax={100}
						aria-label={t("toolRender.goal.usage")}
						style={{ "--tv-frac": ratio } as CSSProperties}
					/>
					<div className="tv-goal-gauge-legend">
						<span>
							<b>{formatTokens(used)}</b> /{" "}
							{formatTokens(goal.tokenBudget ?? 0)}{" "}
							{t("toolRender.goal.tokens")}
						</span>
						<span>{Math.round(ratio * 100)}%</span>
						{remaining !== null && (
							<span className="tv-goal-left">
								{t("toolRender.goal.left", { n: formatTokens(remaining) })}
							</span>
						)}
					</div>
				</div>
			) : (
				goal.tokensUsed !== null && (
					<div className="tv-goal-gauge-legend">
						<span>
							<b>{formatTokens(used)}</b> {t("toolRender.goal.tokens")}
						</span>
						<span className="tv-goal-left">
							{t("toolRender.goal.noBudget")}
						</span>
					</div>
				)
			)}
		</div>
	);
}

export const goalRenderer: ToolRenderer = { Summary, Meta, Input, Result };
