/**
 * 转录里 mode_change 条目的可读说明。
 * Core 写入的 mode：none / plan / plan_paused / vibe / goal / goal_paused；目标每次改动（目标正文、预算、状态）
 * 都会再写一条 goal（data.goal 是当时的目标），所以要和前一条 mode_change 对照才能说清这次变了什么。
 */
import { formatTokens } from "../../lib/format";
import type { JsonRecord } from "../../state/message-builder";
import { isRecord } from "../../tool-render/util";

type ModeName =
	| "goalBudget"
	| "goalBudgetCleared"
	| "goalBudgetLimited"
	| "goalChanged"
	| "goalCompleted"
	| "goalEnded"
	| "goalPaused"
	| "goalResumed"
	| "goalSet"
	| "goalUpdated"
	| "normal"
	| "other"
	| "plan"
	| "planExited"
	| "planPaused"
	| "planResumed"
	| "vibe"
	| "vibeExited";

export interface ModeChangeText {
	key: `transcript.meta.modes.${ModeName}`;
	params?: { objective?: string; budget?: string; mode?: string };
}

function goalOf(entry: JsonRecord | undefined): JsonRecord | undefined {
	const data = entry?.data;
	return isRecord(data) && isRecord(data.goal) ? data.goal : undefined;
}

function objectiveOf(goal: JsonRecord | undefined): string {
	return typeof goal?.objective === "string" ? goal.objective.trim() : "";
}

function budgetOf(goal: JsonRecord | undefined): number | undefined {
	return typeof goal?.tokenBudget === "number" ? goal.tokenBudget : undefined;
}

export function describeModeChange(
	entry: JsonRecord,
	prev: JsonRecord | undefined,
): ModeChangeText {
	const mode = String(entry.mode ?? "");
	const before = String(prev?.mode ?? "none");
	const k = (name: ModeName, params?: ModeChangeText["params"]) => ({
		key: `transcript.meta.modes.${name}` as const,
		...(params ? { params } : {}),
	});
	switch (mode) {
		case "plan":
			return k(before === "plan_paused" ? "planResumed" : "plan");
		case "plan_paused":
			return k("planPaused");
		case "vibe":
			return k("vibe");
		case "goal":
		case "goal_paused": {
			const paused = mode === "goal_paused";
			const wasGoal = before === "goal" || before === "goal_paused";
			const goal = goalOf(entry);
			const objective = objectiveOf(goal);
			if (!wasGoal)
				return paused ? k("goalPaused") : k("goalSet", { objective });
			// 先比目标内容（完成、改正文、改预算、触顶），再看暂停 / 继续：暂停中改预算仍写 goal_paused
			const old = goalOf(prev);
			if (goal?.status === "complete" && old?.status !== "complete")
				return k("goalCompleted");
			if (goal && old && objectiveOf(old) !== objective)
				return k("goalChanged", { objective });
			const budget = budgetOf(goal);
			if (goal && old && budgetOf(old) !== budget)
				return budget === undefined
					? k("goalBudgetCleared")
					: k("goalBudget", { budget: formatTokens(budget) });
			if (goal?.status === "budget-limited" && old?.status !== "budget-limited")
				return k("goalBudgetLimited");
			if (paused && before === "goal") return k("goalPaused");
			if (!paused && before === "goal_paused") return k("goalResumed");
			return k("goalUpdated");
		}
		case "none":
			if (before === "goal" || before === "goal_paused") return k("goalEnded");
			if (before === "plan" || before === "plan_paused") return k("planExited");
			if (before === "vibe") return k("vibeExited");
			return k("normal");
		default:
			return k("other", { mode });
	}
}
