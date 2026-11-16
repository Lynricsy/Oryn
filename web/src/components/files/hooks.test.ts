import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../../api/client";
import {
	INDEXING_RETRY_MS,
	INDEXING_WAIT_MAX_MS,
	type RetryWait,
	WAKE_RETRY_MS,
	withWakeRetry,
} from "./hooks";

const indexing = (retryAfter?: number) =>
	new ApiError(503, "offline_indexing", "indexing: 1 entries", retryAfter);

describe("withWakeRetry", () => {
	let waits: (RetryWait | null)[];
	beforeEach(() => {
		vi.useFakeTimers();
		waits = [];
	});
	afterEach(() => vi.useRealTimers());

	it("offline_indexing：按 Retry-After 反复重试直到成功，期间报告 indexing", async () => {
		const fn = vi
			.fn<() => Promise<string>>()
			.mockRejectedValueOnce(indexing(5))
			.mockRejectedValueOnce(indexing())
			.mockResolvedValue("ok");
		const p = withWakeRetry(fn, (w) => waits.push(w));
		await vi.advanceTimersByTimeAsync(5000 - 1);
		expect(fn).toHaveBeenCalledTimes(1);
		await vi.advanceTimersByTimeAsync(1);
		expect(fn).toHaveBeenCalledTimes(2);
		// 没有 Retry-After：缺省间隔
		await vi.advanceTimersByTimeAsync(INDEXING_RETRY_MS);
		await expect(p).resolves.toBe("ok");
		expect(fn).toHaveBeenCalledTimes(3);
		expect(waits).toEqual(["indexing", "indexing", null]);
	});

	it("offline_indexing 累计等待超过上限：抛出最后一次的错误", async () => {
		const fn = vi.fn(() => Promise.reject(indexing()));
		const p = withWakeRetry(fn, (w) => waits.push(w));
		const settled = p.catch((e: unknown) => e);
		await vi.advanceTimersByTimeAsync(INDEXING_WAIT_MAX_MS + INDEXING_RETRY_MS);
		const e = await settled;
		expect(e).toBeInstanceOf(ApiError);
		expect((e as ApiError).code).toBe("offline_indexing");
		expect(fn).toHaveBeenCalledTimes(
			INDEXING_WAIT_MAX_MS / INDEXING_RETRY_MS + 1,
		);
		expect(waits.at(-1)).toBeNull();
	});

	it("orb_unavailable：报告 waking，只重试一次", async () => {
		const unavailable = new ApiError(503, "orb_unavailable", "waking");
		const fn = vi.fn(() => Promise.reject(unavailable));
		const settled = withWakeRetry(fn, (w) => waits.push(w)).catch(
			(e: unknown) => e,
		);
		await vi.advanceTimersByTimeAsync(WAKE_RETRY_MS);
		expect(await settled).toBe(unavailable);
		expect(fn).toHaveBeenCalledTimes(2);
		expect(waits).toEqual(["waking", null]);
	});

	it("其它错误直接抛出，不报告等待", async () => {
		const err = new ApiError(409, "offline_unsupported", "no");
		await expect(
			withWakeRetry(
				() => Promise.reject(err),
				(w) => waits.push(w),
			),
		).rejects.toBe(err);
		expect(waits).toEqual([]);
	});
});
