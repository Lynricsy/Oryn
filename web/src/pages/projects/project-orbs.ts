/** 项目的 orb 汇总（M24）：最近一次 setup 所在的 orb（主页状态条与设置「环境」分组）、orb 的最近活动（服务卡排序）。 */
import type { Orb } from "../../api/client";
import type { SetupRun } from "../../lib/env-banner";

/** 带最近一次 setup 记录的 orb。 */
export type SetupOrb = Orb & { lastSetup: SetupRun };

/** 这些 orb 里最近一次 setup 所在的那台；都没有记录时为 undefined。 */
export function latestSetupOrb(orbs: readonly Orb[]): SetupOrb | undefined {
	let best: SetupOrb | undefined;
	for (const o of orbs) {
		const run = o.lastSetup;
		if (run && (!best || run.finishedAt > best.lastSetup.finishedAt))
			best = { ...o, lastSetup: run };
	}
	return best;
}

/** 最近活动：用户交互、agent 活动、状态变化里最晚的一个。 */
export const orbActivity = (o: Orb): number =>
	Math.max(
		o.lastUserInteractionAt ?? 0,
		o.lastAgentActivityAt ?? 0,
		o.stateChangedAt,
	);
