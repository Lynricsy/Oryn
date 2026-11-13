/**
 * 模型速度档位：按 omp 会话状态里的能力字段列出可选档位，并把「切到某档」翻译成 RPC 命令序列。
 *
 * omp 的三个开关共用 service tier：`/fast`（priority）、`/fast ultra`（OpenAI ultrafast）、`/slow`（OpenAI/Google 的 flex；
 * Anthropic 订阅是独立的全局低优先级配置）。Anthropic 上快、慢可以同时开着，所以换档时先关掉另一侧，再开目标档。
 */

import type { IconName } from "../components/ui/icons";
import type { OmpCommand, SessionState } from "../omp/types";

export type SpeedTier = "standard" | "fast" | "ultrafast" | "slow";

/** 档位图标：标准是仪表，快速快进，极速火箭，慢速沙漏 */
export const SPEED_ICON: Record<SpeedTier, IconName> = {
	standard: "gauge",
	fast: "fastForward",
	ultrafast: "rocket",
	slow: "hourglass",
};

type SpeedState = Pick<
	SessionState,
	| "fastModeSupported"
	| "fastModeEnabled"
	| "ultrafastSupported"
	| "ultrafastEnabled"
	| "slowModeSupported"
	| "slowModeEnabled"
>;

/** 当前模型可选的档位（按从快到慢）；只有标准一档时返回 `["standard"]`。 */
export function speedTiers(s: SpeedState): SpeedTier[] {
	const tiers: SpeedTier[] = [];
	if (s.ultrafastSupported) tiers.push("ultrafast");
	if (s.fastModeSupported) tiers.push("fast");
	tiers.push("standard");
	if (s.slowModeSupported) tiers.push("slow");
	return tiers;
}

/** 当前生效的档位；ultrafast 也会让 fastModeEnabled 为真，所以先判断它。 */
export function currentSpeed(s: SpeedState): SpeedTier {
	if (s.ultrafastEnabled) return "ultrafast";
	if (s.fastModeEnabled) return "fast";
	if (s.slowModeEnabled) return "slow";
	return "standard";
}

/** 从当前状态切到 `to` 要依次发的命令：先关掉冲突的一侧，再开目标档；已在该档时为空。 */
export function speedCommands(s: SpeedState, to: SpeedTier): OmpCommand[] {
	const from = currentSpeed(s);
	if (from === to) return [];
	const frames: OmpCommand[] = [];
	// set_fast_mode(false) 同时清掉 priority 与 ultrafast
	if (to !== "fast" && to !== "ultrafast" && s.fastModeEnabled)
		frames.push({ type: "set_fast_mode", enabled: false });
	if (to !== "slow" && s.slowModeEnabled)
		frames.push({ type: "set_slow_mode", enabled: false });
	if (to === "fast") frames.push({ type: "set_fast_mode", enabled: true });
	if (to === "ultrafast")
		frames.push({ type: "set_ultrafast_mode", enabled: true });
	if (to === "slow") frames.push({ type: "set_slow_mode", enabled: true });
	return frames;
}
