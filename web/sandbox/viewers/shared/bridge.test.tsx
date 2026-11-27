import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type {
	AnchorPin,
	SelectMode,
	ViewerAnchor,
} from "../../runtime/messages";
import { useViewerBridge } from "./bridge";

const anchor: ViewerAnchor = {
	page: { number: 2, name: "明细" },
	x: null,
	y: null,
	range: "B2:C3",
	block: null,
	text: "",
};

afterEach(() => {
	Reflect.deleteProperty(window, "__orynViewer");
});

describe("useViewerBridge", () => {
	it("没有运行时的桥时全部为空", () => {
		const { result } = renderHook(() => useViewerBridge());
		expect(result.current).toEqual({
			mode: null,
			pins: [],
			locate: null,
			pick: null,
		});
	});

	it("订阅模式、图钉与定位，卸载时注销；pick 转给桥", () => {
		const subs: {
			mode?: (m: SelectMode) => void;
			pins?: (p: readonly AnchorPin[]) => void;
			locate?: (a: ViewerAnchor) => void;
		} = {};
		const off = vi.fn();
		const bridge = {
			onMode: (cb: (m: SelectMode) => void) => {
				subs.mode = cb;
				cb("comment");
				return off;
			},
			onPins: (cb: (p: readonly AnchorPin[]) => void) => {
				subs.pins = cb;
				cb([]);
				return off;
			},
			onLocate: (cb: (a: ViewerAnchor) => void) => {
				subs.locate = cb;
				return off;
			},
			pick: vi.fn(),
		};
		Object.defineProperty(window, "__orynViewer", {
			value: bridge,
			configurable: true,
		});
		const { result, unmount } = renderHook(() => useViewerBridge());
		expect(result.current.mode).toBe("comment");
		const pins = [{ n: 1, answered: true, anchor }];
		act(() => {
			subs.pins?.(pins);
			subs.locate?.(anchor);
		});
		expect(result.current.pins).toBe(pins);
		expect(result.current.locate).toBe(anchor);
		result.current.pick?.(anchor, { x: 1, y: 2 });
		expect(bridge.pick).toHaveBeenCalledWith(anchor, { x: 1, y: 2 });
		unmount();
		expect(off).toHaveBeenCalledTimes(3);
	});
});
