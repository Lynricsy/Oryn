/** HTML 视图运行时的高度上报：没有上限地跟随内容；页面随帧高变化（100vh 一类）时不一路撑高。 */
import { describe, expect, it, vi } from "vitest";
import { VIEW_FILL_HEIGHT } from "./messages";
import { install } from "./runtime";

describe("视图运行时的高度", () => {
	// extra：100vh 的子元素之外不随视口变化的部分（body 内边距、页边距）；24 超过耦合判定的余量 16
	it.each([4, 24])(
		"内容高度跟着帧高走时停止撑高（视口外 %ipx），之后内容真的变多或变少仍会上报",
		(initialExtra) => {
			// 每个用例一个新窗口：install 在窗口上定义的全局只能定义一次。视口与内容高度由测试控制：jsdom 没有布局
			const view = document.createElement("iframe");
			document.body.appendChild(view);
			// 帧窗口带自己 realm 的 Event / MessageEvent 构造器（jsdom 只认本 realm 的事件）；Window 类型里没有声明它们
			const win = view.contentWindow as Window & typeof globalThis;
			const body = win.document.body;
			let viewport = 160;
			let extra = initialExtra;
			let fixedContent: number | null = null;
			Object.defineProperty(win, "innerHeight", {
				get: () => viewport,
				configurable: true,
			});
			const content = () => fixedContent ?? viewport + extra;
			Object.defineProperty(body, "scrollHeight", {
				get: content,
				configurable: true,
			});
			body.getBoundingClientRect = () => ({ height: content() }) as DOMRect;
			win.requestAnimationFrame = (cb: FrameRequestCallback) => {
				cb(0);
				return 0;
			};

			const host = document.createElement("iframe");
			document.body.appendChild(host);
			const parent = host.contentWindow as Window;
			const heights: number[] = [];
			// 父页面：收到 resize 就把帧（视口）设成这个高度，帧变高后页面重新布局
			vi.spyOn(parent, "postMessage").mockImplementation((msg: unknown) => {
				const m = msg as { kind: string; height?: number };
				if (m.kind !== "resize" || m.height === undefined) return;
				heights.push(m.height);
				if (heights.length > 50) throw new Error("一路撑高");
				viewport = m.height;
				win.dispatchEvent(new win.Event("resize"));
			});
			install(win, parent, "");
			win.dispatchEvent(
				new win.MessageEvent("message", {
					data: { v: 1, channel: "0123456789abcdef", kind: "hello" },
					source: parent,
				}),
			);

			expect(heights.length).toBeLessThan(10);
			expect(heights.at(-1)).toBe(VIEW_FILL_HEIGHT);
			expect(viewport).toBe(VIEW_FILL_HEIGHT);

			// 耦合之后：视口外的部分小幅变化不上报；明显变多时报一次就停，不再每轮再涨
			let before = heights.length;
			extra = initialExtra + 10;
			win.dispatchEvent(new win.Event("resize"));
			expect(heights.length).toBe(before);
			extra = initialExtra + 60;
			win.dispatchEvent(new win.Event("resize"));
			expect(heights.length).toBe(before + 1);
			expect(viewport).toBe(VIEW_FILL_HEIGHT + initialExtra + 60);

			// 不随视口变化的内容长到 2400：照常上报
			before = heights.length;
			fixedContent = 2400;
			win.dispatchEvent(new win.Event("resize"));
			expect(heights.at(-1)).toBe(2400);
			expect(heights.length).toBe(before + 1);
			// 之后再长 200 也照常上报（基线随帧变高回落到 0）
			fixedContent = 2600;
			win.dispatchEvent(new win.Event("resize"));
			expect(heights.at(-1)).toBe(2600);
			// 内容缩到比视口矮：帧跟着变矮
			fixedContent = 900;
			win.dispatchEvent(new win.Event("resize"));
			expect(heights.at(-1)).toBe(900);
			expect(heights.length).toBe(before + 3);
		},
	);
});
