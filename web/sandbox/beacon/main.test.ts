import { afterEach, describe, expect, it, vi } from "vitest";
import { BEAT_MS, startBeacon, visitorId } from "./main";

describe("访客心跳", () => {
	afterEach(() => {
		vi.useRealTimers();
	});

	it("立即发一次，之后每 20 秒一次，id 不变、地址落在发布根下", () => {
		vi.useFakeTimers();
		const send = vi.fn();
		const stop = startBeacon("/p/abc/~g.s/", send);
		expect(send).toHaveBeenCalledTimes(1);
		const [url, id] = send.mock.calls[0] as [string, string];
		expect(new URL(url).pathname).toBe("/p/abc/~g.s/_oryn/beat");
		expect(id).toMatch(/^[0-9a-f]{24}$/);
		vi.advanceTimersByTime(BEAT_MS);
		expect(send).toHaveBeenCalledTimes(2);
		expect(send.mock.calls[1]?.[1]).toBe(id);
		stop();
		vi.advanceTimersByTime(BEAT_MS * 3);
		expect(send).toHaveBeenCalledTimes(2);
	});

	it("发送失败不抛出", () => {
		const stop = startBeacon("/", () => {
			throw new Error("blocked");
		});
		stop();
	});

	it("每次生成的实例 id 不同", () => {
		expect(visitorId()).not.toBe(visitorId());
	});
});
