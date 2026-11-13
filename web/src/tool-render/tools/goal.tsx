/** `goal` — goal-mode lifecycle: set/check/complete/resume/drop an objective with an optional token budget. */
import type { ReactNode } from "react";
import { formatDuration, formatTokens } from "../../lib/format";
import type { Tone } from "../parts";
import {
	Badge,
	InvalidArg,
	Kv,
	KvGrid,
	Note,
	Output,
	ResultText,
} from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import {
	detailsRecord,
	isRecord,
	normalizeWs,
	num,
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

/** Mirrors the TUI's describeOp: "create" reads as "set", "get" as "check". */
function describeOp(op: string | null): string {
	switch (op) {
		case "create":
			return "set";
		case "get":
			return "check";
		default:
			return op ?? "?";
	}
}

function statusTone(status: string): Tone | undefined {
	switch (status) {
		case "complete":
			return "ok";
		case "budget-limited":
			return "warn";
		case "paused":
		case "dropped":
			return undefined;
		default:
			return "accent";
	}
}

/** "12k / 100k tokens (88k left)" or "12k tokens" without a budget. */
function tokensLine(goal: GoalView): string {
	const used = formatTokens(goal.tokensUsed ?? 0);
	if (goal.tokenBudget === null) return `${used} tokens`;
	const left = Math.max(0, goal.tokenBudget - (goal.tokensUsed ?? 0));
	return `${used} / ${formatTokens(goal.tokenBudget)} tokens (${formatTokens(left)} left)`;
}

function Summary({ args, result }: ToolRenderProps): ReactNode {
	const details = detailsRecord(result);
	const goal = goalOf(details);
	const op = str(details?.op) ?? str(args.op);
	const objective = goal?.objective ?? str(args.objective);
	const budget = num(args.token_budget);
	return (
		<>
			{op === null && args.op !== undefined ? (
				<InvalidArg what="op" />
			) : (
				<span>{describeOp(op)}</span>
			)}
			{goal && <Badge tone={statusTone(goal.status)}>{goal.status}</Badge>}
			{objective !== null && objective.trim() !== "" && (
				<span className="tv-muted">
					“{truncate(normalizeWs(objective), 64)}”
				</span>
			)}
			{budget !== null && (
				<span className="tv-faint">budget {formatTokens(budget)}</span>
			)}
		</>
	);
}

function Body({ args, result }: ToolRenderProps): ReactNode {
	const details = detailsRecord(result);
	const goal = goalOf(details);
	const op = str(details?.op) ?? str(args.op);
	const objective = goal?.objective ?? str(args.objective);
	const budgetArg = num(args.token_budget);
	const report = str(details?.completionBudgetReport);
	const hasTokens =
		goal !== null && (goal.tokensUsed !== null || goal.tokenBudget !== null);
	return (
		<>
			<KvGrid>
				<Kv k="op">{describeOp(op)}</Kv>
				{goal && (
					<Kv k="status">
						<Badge tone={statusTone(goal.status)}>{goal.status}</Badge>
					</Kv>
				)}
				{objective !== null && objective.trim() !== "" && (
					<Kv k="objective">{objective.trim()}</Kv>
				)}
				{hasTokens && goal ? (
					<Kv k="tokens">{tokensLine(goal)}</Kv>
				) : (
					budgetArg !== null && (
						<Kv k="budget">{formatTokens(budgetArg)} tokens</Kv>
					)
				)}
				{goal !== null &&
					goal.timeUsedSeconds !== null &&
					goal.timeUsedSeconds > 0 && (
						<Kv k="elapsed">{formatDuration(goal.timeUsedSeconds * 1000)}</Kv>
					)}
			</KvGrid>
			{details !== null && goal === null && !result?.isError && (
				<Note tone="warn">no active goal</Note>
			)}
			{report !== null && report !== "" && (
				<Output text={report} title="Report" maxLines={12} />
			)}
			{(goal === null || result?.isError) && (
				<ResultText result={result} maxLines={10} />
			)}
		</>
	);
}

export const goalRenderer: ToolRenderer = { Summary, Body };
