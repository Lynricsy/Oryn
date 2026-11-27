/** HTML 视图运行时的高度上报：没有上限地跟随内容；页面随帧高变化（100vh 一类）时不一路撑高。 */
import { describe, expect, it, vi } from "vitest";
import { VIEW_FILL_HEIGHT } from "./messages";
import { install } from "./runtime";

describe("视图运行时的高度", () => {
	it("内容高度跟着帧高走时停止撑高，报一次填充高度；之后内容真的变多仍会上报", () => {
		// 视口与内容高度由测试控制：jsdom 没有布局
		let viewport = 160;
		let extra = 4; // 100vh 的子元素加上 body 的内边距
		let fixedContent: number | null = null;
		Object.defineProperty(window, "innerHeight", {
			get: () => viewport,
			configurable: true,
		});
		const content = () => fixedContent ?? viewport + extra;
		Object.defineProperty(document.body, "scrollHeight", {
			get: content,
			configurable: true,
		});
		document.body.getBoundingClientRect = () =>
			({ height: content() }) as DOMRect;
		vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
			cb(0);
			return 0;
		});

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
			window.dispatchEvent(new Event("resize"));
		});
		install(window, parent, "");
		window.dispatchEvent(
			new MessageEvent("message", {
				data: { v: 1, channel: "0123456789abcdef", kind: "hello" },
				source: parent,
			}),
		);

		expect(heights.length).toBeLessThan(10);
		expect(heights.at(-1)).toBe(VIEW_FILL_HEIGHT);
		expect(viewport).toBe(VIEW_FILL_HEIGHT);

		// 耦合之后：贴着视口的小变化不再上报；内容明显超出视口（不随视口变化的部分多了）照常上报，且不再一路撑高
		const before = heights.length;
		extra = 10;
		window.dispatchEvent(new Event("resize"));
		expect(heights.length).toBe(before);
		fixedContent = 2400;
		window.dispatchEvent(new Event("resize"));
		expect(heights.at(-1)).toBe(2400);
		expect(heights.length).toBe(before + 1);
		vi.unstubAllGlobals();
	});
});
