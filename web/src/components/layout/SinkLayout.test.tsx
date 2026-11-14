/** 手机下沉式侧栏：开关状态、inert、焦点移入与还原、Esc / 返回键、触摸手势与吸附、同心圆角、减少动效。 */
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { concentricRadius } from "../../lib/device-corners";
import { uiStore } from "../../state/ui";
import { SINK_EDGE, SinkLayout, settleSink, sinkProgress } from "./SinkLayout";

function mount() {
	return render(
		<SinkLayout sidebar={<button type="button">线程甲</button>}>
			<button type="button">正文按钮</button>
		</SinkLayout>,
	);
}

const card = () => screen.getByTestId("sink-card");
const side = () => screen.getByTestId("sink-sidebar");
const content = () => screen.getByTestId("sink-content");

function touch(
	el: Element,
	type: "touchStart" | "touchMove" | "touchEnd",
	x: number,
	y = 300,
	timeStamp?: number,
) {
	const list = type === "touchEnd" ? [] : [{ clientX: x, clientY: y }];
	const ev = new Event(type.toLowerCase(), {
		bubbles: true,
		cancelable: true,
	});
	Object.assign(ev, {
		touches: list,
		changedTouches: [{ clientX: x, clientY: y }],
	});
	if (timeStamp !== undefined)
		Object.defineProperty(ev, "timeStamp", { value: timeStamp });
	act(() => {
		el.dispatchEvent(ev);
	});
	return ev;
}

beforeEach(() => {
	uiStore.setState({ sidebarOpen: false });
	window.history.replaceState(null, "");
});

afterEach(() => {
	uiStore.setState({ sidebarOpen: false });
	window.history.replaceState(null, "");
	vi.unstubAllGlobals();
});

describe("吸附与进度", () => {
	it("快速一甩按方向，否则按过半", () => {
		expect(settleSink(0.2, 0.5)).toBe(true);
		expect(settleSink(0.9, -0.5)).toBe(false);
		expect(settleSink(0.6, 0)).toBe(true);
		expect(settleSink(0.4, 0.1)).toBe(false);
	});
	it("进度 = 起点 + 位移 / 宽度，夹到 [0, 1]", () => {
		expect(sinkProgress(0, 160, 320)).toBe(0.5);
		expect(sinkProgress(1, -80, 320)).toBe(0.75);
		expect(sinkProgress(0, -50, 320)).toBe(0);
		expect(sinkProgress(1, 50, 320)).toBe(1);
	});
	it("卡片圆角：贴满屏幕（间距 0）取 max(2xl 令牌, 屏幕角)；屏幕角为 0 退回令牌", () => {
		expect(concentricRadius(36, 47, 0)).toBe(47);
		expect(concentricRadius(36, 0, 0)).toBe(36);
	});
});

describe("开关", () => {
	it("关闭：侧栏 inert、卡片可交互且不带 data-open；打开：反之，并压入历史标记", () => {
		mount();
		expect(side()).toHaveAttribute("inert");
		expect(content()).not.toHaveAttribute("inert");
		expect(card()).not.toHaveAttribute("data-open");
		act(() => uiStore.getState().setSidebar(true));
		expect(card()).toHaveAttribute("data-open");
		expect(side()).not.toHaveAttribute("inert");
		expect(content()).toHaveAttribute("inert");
		expect((window.history.state as Record<string, unknown>).orynSidebar).toBe(
			true,
		);
	});

	it("打开时焦点移入侧栏，关闭后还原到打开前的元素", () => {
		mount();
		const opener = screen.getByRole("button", { name: "正文按钮" });
		opener.focus();
		act(() => uiStore.getState().setSidebar(true));
		expect(document.activeElement).toBe(side());
		act(() => uiStore.getState().setSidebar(false));
		expect(document.activeElement).toBe(opener);
	});

	it("Esc 与点露出的卡片条关闭；关闭时弹掉历史标记", () => {
		const back = vi.spyOn(window.history, "back").mockImplementation(() => {});
		mount();
		act(() => uiStore.getState().setSidebar(true));
		fireEvent.keyDown(window, { key: "Escape" });
		expect(uiStore.getState().sidebarOpen).toBe(false);
		expect(back).toHaveBeenCalledTimes(1);
		window.history.replaceState(null, "");
		act(() => uiStore.getState().setSidebar(true));
		fireEvent.click(screen.getByTestId("sink-close"));
		expect(uiStore.getState().sidebarOpen).toBe(false);
		back.mockRestore();
	});

	it("返回键（popstate 离开标记）关闭；关闭状态落到残留标记上自动再退一步", () => {
		const back = vi.spyOn(window.history, "back").mockImplementation(() => {});
		mount();
		act(() => uiStore.getState().setSidebar(true));
		// 返回键：浏览器弹出标记，回到原记录
		window.history.replaceState(null, "");
		act(() => {
			window.dispatchEvent(new PopStateEvent("popstate"));
		});
		expect(uiStore.getState().sidebarOpen).toBe(false);
		expect(back).not.toHaveBeenCalled();
		// 侧栏里导航后标记留在栈里：返回落到它上面 → 跳过
		window.history.replaceState({ orynSidebar: true }, "");
		act(() => {
			window.dispatchEvent(new PopStateEvent("popstate"));
		});
		expect(back).toHaveBeenCalledTimes(1);
		back.mockRestore();
	});

	it("减少动效：卡片带 data-instant（不做过渡）", () => {
		vi.stubGlobal("matchMedia", (q: string) => ({
			matches: q === "(prefers-reduced-motion: reduce)",
			addEventListener: () => {},
			removeEventListener: () => {},
		}));
		mount();
		expect(card()).toHaveAttribute("data-instant");
	});
});

describe("触摸手势", () => {
	beforeEach(() => {
		window.innerWidth = 390;
	});
	afterEach(() => {
		window.innerWidth = 1024;
	});

	it("左缘右滑跟手写进度，松手过半吸附打开；非左缘起手不拖", () => {
		mount();
		touch(card(), "touchStart", SINK_EDGE + 40, 300, 0);
		touch(card(), "touchMove", 300, 300, 400);
		touch(card(), "touchEnd", 300);
		expect(uiStore.getState().sidebarOpen).toBe(false);

		touch(card(), "touchStart", 4, 300, 1000);
		const move = touch(card(), "touchMove", 120, 302, 1400);
		expect(move.defaultPrevented).toBe(true);
		expect(card()).toHaveAttribute("data-dragging");
		expect(Number(card().style.getPropertyValue("--sink-p"))).toBeCloseTo(
			116 / 390,
		);
		touch(card(), "touchMove", 260, 302, 2000);
		touch(card(), "touchEnd", 260);
		expect(card()).not.toHaveAttribute("data-dragging");
		expect(card().style.getPropertyValue("--sink-p")).toBe("");
		expect(uiStore.getState().sidebarOpen).toBe(true);
	});

	it("竖向为主交给滚动；打开时在卡片上快速左甩关闭", () => {
		mount();
		touch(card(), "touchStart", 4, 300, 0);
		const v = touch(card(), "touchMove", 10, 360, 50);
		expect(v.defaultPrevented).toBe(false);
		touch(card(), "touchEnd", 10);
		expect(uiStore.getState().sidebarOpen).toBe(false);

		act(() => uiStore.getState().setSidebar(true));
		const strip = screen.getByTestId("sink-close");
		touch(strip, "touchStart", 340, 300, 3000);
		touch(strip, "touchMove", 320, 300, 3010);
		touch(strip, "touchMove", 300, 300, 3020);
		touch(strip, "touchEnd", 300);
		expect(uiStore.getState().sidebarOpen).toBe(false);
	});
});
