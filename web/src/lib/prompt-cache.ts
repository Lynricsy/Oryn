/**
 * 输入坞缓存读数的推导。Core（`get_session_stats` 的 `promptCache`）给的是取数那一刻的状态，
 * 这里按当前时刻折算剩余时间并在本地判定到期，规则与 Core `promptCacheStatus` 一致：
 * 到期时若续期请求在途，先不算冷，等它的结果（下一次取数）。
 */
import type { PromptCacheColdReason, PromptCacheStatus } from "../omp/types";

/** 剩余时间低于存活时长的这个比例时转为警告色 */
const LOW_FRACTION = 0.2;

export type PromptCacheView =
	| {
			kind: "warm";
			remainingMs: number;
			ttlMs: number;
			expiresAt: number;
			/** 剩余不到两成 */
			low: boolean;
	  }
	/** 提供方从不让缓存过期 */
	| { kind: "persistent" }
	/** 已到期，但续期请求在途：结果出来前不判冷 */
	| { kind: "refreshing" }
	| {
			kind: "cold";
			reason?: PromptCacheColdReason;
			/**
			 * 下一条消息恰好重发全部 `nextTokens`（缓存整体失效、换模型或换工具集）；
			 * 其余变化保留它之前的缓存块，Core 看不到那部分占比，`nextTokens` 只是上限。
			 */
			exact: boolean;
	  };

const EXACT_RESEND: Record<PromptCacheColdReason["kind"], boolean> = {
	expired: true,
	"refresh-missed": true,
	model: true,
	tools: true,
	thinking: false,
	"system-prompt": false,
	retention: false,
	compaction: false,
};

function cold(reason: PromptCacheColdReason | undefined): PromptCacheView {
	return {
		kind: "cold",
		reason,
		exact: reason ? EXACT_RESEND[reason.kind] : false,
	};
}

export function promptCacheView(
	status: PromptCacheStatus,
	now: number,
): PromptCacheView {
	if (status.state === "cold") return cold(status.reason);
	if (status.ttlMs === undefined) return { kind: "persistent" };
	const expiresAt = status.touchedAt + status.ttlMs;
	const remainingMs = expiresAt - now;
	if (remainingMs > 0) {
		return {
			kind: "warm",
			remainingMs,
			ttlMs: status.ttlMs,
			expiresAt,
			low: remainingMs < status.ttlMs * LOW_FRACTION,
		};
	}
	if (status.warming?.state === "refreshing") return { kind: "refreshing" };
	return cold({ kind: "expired", at: expiresAt });
}

/** 会话的缓存命中率：读缓存占全部提示词的比例；没有任何缓存读写（模型不缓存）时为 undefined。 */
export function cacheHitRatio(tokens: {
	input: number;
	cacheRead: number;
	cacheWrite: number;
}): number | undefined {
	if (tokens.cacheRead + tokens.cacheWrite <= 0) return undefined;
	return (
		tokens.cacheRead / (tokens.input + tokens.cacheRead + tokens.cacheWrite)
	);
}

/** 倒计时：不足一小时 `m:ss`，否则 `h:mm:ss`；向上取整到秒（剩 0.4 秒仍显示 0:01，到 0 才算到期）。 */
export function formatCountdown(ms: number): string {
	const total = Math.max(0, Math.ceil(ms / 1000));
	const h = Math.floor(total / 3600);
	const m = Math.floor((total % 3600) / 60);
	const s = String(total % 60).padStart(2, "0");
	return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${s}` : `${m}:${s}`;
}
