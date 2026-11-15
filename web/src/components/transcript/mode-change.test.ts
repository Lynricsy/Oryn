import { describe, expect, it } from "vitest";
import { describeModeChange } from "./mode-change";

const goal = (g: Record<string, unknown>) => ({
	mode: "goal",
	data: { goal: { objective: "修好登录", status: "active", ...g } },
});
const key = (
	entry: Record<string, unknown>,
	prev?: Record<string, unknown>,
) => {
	const d = describeModeChange(entry, prev);
	return [d.key.replace("transcript.meta.modes.", ""), d.params];
};

describe("describeModeChange", () => {
	it("首次设定目标带上目标正文", () => {
		expect(key(goal({}))).toEqual(["goalSet", { objective: "修好登录" }]);
		expect(key(goal({}), { mode: "none" })).toEqual([
			"goalSet",
			{ objective: "修好登录" },
		]);
	});

	it("目标期间的连续 goal 条目说清变了什么：正文、预算、触顶，否则是一般更新", () => {
		expect(key(goal({ objective: "改需求" }), goal({}))).toEqual([
			"goalChanged",
			{ objective: "改需求" },
		]);
		expect(key(goal({ tokenBudget: 200_000 }), goal({}))).toEqual([
			"goalBudget",
			{ budget: "200k" },
		]);
		expect(key(goal({}), goal({ tokenBudget: 5000 }))).toEqual([
			"goalBudgetCleared",
			undefined,
		]);
		expect(
			key(
				goal({ tokenBudget: 10, status: "budget-limited" }),
				goal({ tokenBudget: 10 }),
			),
		).toEqual(["goalBudgetLimited", undefined]);
		expect(key(goal({ tokensUsed: 9 }), goal({}))).toEqual([
			"goalUpdated",
			undefined,
		]);
	});

	it("暂停 / 继续 / 结束按前一条模式区分，不显示内部值", () => {
		expect(key({ mode: "goal_paused" }, goal({}))[0]).toBe("goalPaused");
		expect(key(goal({}), { mode: "goal_paused" })[0]).toBe("goalResumed");
		expect(key({ mode: "none" }, goal({}))[0]).toBe("goalEnded");
		expect(key({ mode: "none" }, { mode: "goal_paused" })[0]).toBe("goalEnded");
		expect(key({ mode: "none" }, { mode: "plan" })[0]).toBe("planExited");
		expect(key({ mode: "none" }, { mode: "vibe" })[0]).toBe("vibeExited");
		expect(key({ mode: "none" })[0]).toBe("normal");
		expect(key({ mode: "plan" }, { mode: "plan_paused" })[0]).toBe(
			"planResumed",
		);
	});

	it("未知模式保留原值", () => {
		expect(key({ mode: "turbo" })).toEqual(["other", { mode: "turbo" }]);
	});
});

describe("describeModeChange：Core 的完成与暂停中改预算", () => {
	it("complete_goal 写的是 goal + status=complete：显示完成，不是一般更新", () => {
		expect(key(goal({ status: "complete" }), goal({}))[0]).toBe(
			"goalCompleted",
		);
	});

	it("暂停中改预算仍写 goal_paused：显示预算变化，不再报「已暂停」", () => {
		const pausedPrev = { ...goal({}), mode: "goal_paused" };
		const paused = { ...goal({ tokenBudget: 80_000 }), mode: "goal_paused" };
		expect(key(paused, pausedPrev)).toEqual(["goalBudget", { budget: "80k" }]);
		// 真正从进行中转暂停：仍报已暂停
		expect(key({ ...goal({}), mode: "goal_paused" }, goal({}))[0]).toBe(
			"goalPaused",
		);
	});
});
