import { describe, expect, it } from "vitest";
import type { PromptCacheStatus } from "../omp/types";
import {
	cacheHitRatio,
	formatCountdown,
	promptCacheView,
} from "./prompt-cache";

const T0 = 1_700_000_000_000;

function status(patch: Partial<PromptCacheStatus> = {}): PromptCacheStatus {
	return {
		state: "warm",
		inferred: false,
		tier: "short",
		touchedAt: T0,
		ttlMs: 300_000,
		cachedTokens: 80_000,
		nextTokens: 82_000,
		...patch,
	};
}

describe("promptCacheView", () => {
	it("按当前时刻折算剩余时间，不到两成时标为将过期", () => {
		expect(promptCacheView(status(), T0 + 28_000)).toMatchObject({
			kind: "warm",
			remainingMs: 272_000,
			low: false,
		});
		expect(promptCacheView(status(), T0 + 250_000)).toMatchObject({
			kind: "warm",
			low: true,
		});
	});

	it("取数后本地倒计时到期即判冷，原因是存活期到期", () => {
		expect(promptCacheView(status(), T0 + 300_000)).toEqual({
			kind: "cold",
			reason: { kind: "expired", at: T0 + 300_000 },
			exact: true,
		});
	});

	it("到期时续期请求在途：先不判冷，等它的结果", () => {
		const s = status({ warming: { state: "refreshing" } });
		expect(promptCacheView(s, T0 + 301_000)).toEqual({ kind: "refreshing" });
	});

	it("Core 判冷时照用它的原因；只改了思考级别等时重写量只是上限", () => {
		const s = status({
			state: "cold",
			reason: { kind: "thinking", from: "high", to: "low" },
		});
		expect(promptCacheView(s, T0 + 1_000)).toMatchObject({
			kind: "cold",
			exact: false,
		});
		const model = status({
			state: "cold",
			reason: { kind: "model", from: "a", to: "b" },
		});
		expect(promptCacheView(model, T0 + 1_000)).toMatchObject({ exact: true });
	});

	it("提供方不让缓存过期（没有存活时长）时不倒计时", () => {
		const s = status({ ttlMs: undefined });
		expect(promptCacheView(s, T0 + 10 * 3_600_000)).toEqual({
			kind: "persistent",
		});
	});
});

describe("cacheHitRatio", () => {
	it("读缓存占全部提示词的比例；没有任何缓存读写时不给比例", () => {
		expect(cacheHitRatio({ input: 10, cacheRead: 80, cacheWrite: 10 })).toBe(
			0.8,
		);
		expect(
			cacheHitRatio({ input: 100, cacheRead: 0, cacheWrite: 0 }),
		).toBeUndefined();
	});
});

describe("formatCountdown", () => {
	it("向上取整到秒，一小时及以上带小时位", () => {
		expect(formatCountdown(272_000)).toBe("4:32");
		expect(formatCountdown(400)).toBe("0:01");
		expect(formatCountdown(3_600_000)).toBe("1:00:00");
		expect(formatCountdown(-5)).toBe("0:00");
	});
});
