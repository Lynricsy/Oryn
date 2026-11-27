import { describe, expect, it, vi } from "vitest";
import { installFrameTimerShim } from "./frame-timer-shim";

describe("installFrameTimerShim", () => {
	it("rAF 换成 16ms 的 setTimeout，回调拿到 performance.now()；cAF 取消它", () => {
		vi.useFakeTimers();
		try {
			const win = {
				requestAnimationFrame: window.requestAnimationFrame,
				cancelAnimationFrame: window.cancelAnimationFrame,
				setTimeout: ((cb: () => void, ms: number) =>
					setTimeout(cb, ms)) as unknown as Window["setTimeout"],
				clearTimeout: ((id: number) =>
					clearTimeout(id)) as unknown as Window["clearTimeout"],
				performance: { now: () => 42 },
			};
			installFrameTimerShim(win);
			const ran = vi.fn();
			const kept = vi.fn();
			const id = win.requestAnimationFrame(ran);
			win.requestAnimationFrame(kept);
			win.cancelAnimationFrame(id);
			vi.advanceTimersByTime(15);
			expect(kept).not.toHaveBeenCalled();
			vi.advanceTimersByTime(1);
			expect(kept).toHaveBeenCalledWith(42);
			expect(ran).not.toHaveBeenCalled();
		} finally {
			vi.useRealTimers();
		}
	});
});
