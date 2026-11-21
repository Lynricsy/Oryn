import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useDebounced } from "./use-debounced";

afterEach(() => vi.useRealTimers());

describe("useDebounced", () => {
	it("值停止变化 ms 毫秒后才更新，中途的变化重新计时", () => {
		vi.useFakeTimers();
		const { result, rerender } = renderHook(
			({ v }: { v: string }) => useDebounced(v, 200),
			{ initialProps: { v: "a" } },
		);
		expect(result.current).toBe("a");
		rerender({ v: "ab" });
		act(() => vi.advanceTimersByTime(150));
		rerender({ v: "abc" });
		act(() => vi.advanceTimersByTime(150));
		expect(result.current).toBe("a");
		act(() => vi.advanceTimersByTime(50));
		expect(result.current).toBe("abc");
	});
});
