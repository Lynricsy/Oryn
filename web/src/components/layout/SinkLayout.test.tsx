/** 手机下沉式侧栏：开关状态、inert、焦点移入与还原、Esc / 返回键、触摸手势与吸附、同心圆角、减少动效。 */
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { concentricRadius } from "../../lib/device-corners";
import { uiStore } from "../../state/ui";
import {
	blocksSwipe,
	SINK_EDGE,
	SinkLayout,
	settleSink,
	sinkProgress,
} from "./SinkLayout";

function mount() {
	return render(
		<SinkLayout sidebar={<button type="button">线程甲</button>}>
			<button type="button">正文按钮</button>
			<p data-testid="prose">正文段落</p>
			<pre data-testid="code">
				<span data-testid="code-text">const x = 1;</span>
			</pre>
			<table>
				<tbody>
					<tr>
						<td data-testid="cell">格</td>
					</tr>
				</tbody>
			</table>
			<div data-testid="thread-tabs">
				<span data-testid="tab">终端</span>
			</div>
			<input data-testid="field" />
			<div data-testid="scroller" style={{ overflowX: "auto" }}>
				<span data-testid="scroll-item">很宽的内容</span>
			</div>
		</SinkLayout>,
	);
}

/** jsdom 不做布局：给横滚容器补上内容宽度与滚动位置。 */
function makeScrollable(el: HTMLElement, scrollLeft: number) {
	Object.defineProperty(el, "scrollWidth", { configurable: true, value: 800 });
	Object.defineProperty(el, "clientWidth", { configurable: true, value: 300 });
	el.scrollLeft = scrollLeft;
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

	it("footer（底部胶囊）渲染在侧栏层底部、不在卡片层里：关闭时随侧栏 inert，打开时可用", () => {
		render(
			<SinkLayout
				sidebar={<button type="button">线程甲</button>}
				footer={<nav data-testid="footer">胶囊</nav>}
			>
				<p>正文</p>
			</SinkLayout>,
		);
		const footer = screen.getByTestId("footer");
		expect(side()).toContainElement(footer);
		expect(card()).not.toContainElement(footer);
		// 在侧栏内容之后（侧栏层是纵向排布，胶囊贴底）
		expect(side().lastElementChild).toBe(footer);
		expect(side()).toHaveAttribute("inert");
		act(() => uiStore.getState().setSidebar(true));
		expect(side()).not.toHaveAttribute("inert");
	});
});

describe("触摸手势", () => {
	beforeEach(() => {
		window.innerWidth = 390;
	});
	afterEach(() => {
		window.innerWidth = 1024;
	});

	/** 从 `el` 起手横滑：起点 x0、终点 x1，纵向漂移 dy。 */
	function swipe(el: Element, x0: number, x1: number, dy = 0, t = 0) {
		touch(el, "touchStart", x0, 300, t);
		const move = touch(el, "touchMove", (x0 + x1) / 2, 300 + dy / 2, t + 100);
		touch(el, "touchMove", x1, 300 + dy, t + 400);
		touch(el, "touchEnd", x1);
		return move;
	}

	it("左缘右滑跟手写进度，松手过半吸附打开", () => {
		mount();
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

	it("关闭时在卡片中部右滑也能打开（横向明显大于纵向）", () => {
		mount();
		const move = swipe(screen.getByTestId("prose"), 120, 340, 20);
		expect(move.defaultPrevented).toBe(true);
		expect(uiStore.getState().sidebarOpen).toBe(true);
	});

	it("卡片中部：纵向为主、横纵相近或向左滑都不打开", () => {
		mount();
		const prose = screen.getByTestId("prose");
		expect(swipe(prose, 120, 140, 200).defaultPrevented).toBe(false);
		expect(swipe(prose, 120, 300, 160, 1000).defaultPrevented).toBe(false);
		expect(swipe(prose, 300, 100, 0, 2000).defaultPrevented).toBe(false);
		expect(uiStore.getState().sidebarOpen).toBe(false);
		expect(card()).not.toHaveAttribute("data-dragging");
	});

	it("起手在代码块、表格、同级标签条、输入框、没滚到最左的横滚容器里不接管；滚到最左的容器照常", () => {
		mount();
		for (const id of ["code-text", "cell", "tab", "field"]) {
			swipe(screen.getByTestId(id), 120, 340);
			expect(uiStore.getState().sidebarOpen).toBe(false);
		}
		const scroller = screen.getByTestId("scroller");
		makeScrollable(scroller, 40);
		swipe(screen.getByTestId("scroll-item"), 120, 340);
		expect(uiStore.getState().sidebarOpen).toBe(false);

		makeScrollable(scroller, 0);
		swipe(screen.getByTestId("scroll-item"), 120, 340);
		expect(uiStore.getState().sidebarOpen).toBe(true);
	});

	it("左缘起手不受可横滚元素限制；有文本选择时卡片中部不接管", () => {
		mount();
		const sel = vi
			.spyOn(window, "getSelection")
			.mockReturnValue({ isCollapsed: false } as Selection);
		swipe(screen.getByTestId("prose"), 120, 340);
		expect(uiStore.getState().sidebarOpen).toBe(false);
		sel.mockRestore();
		swipe(screen.getByTestId("code-text"), SINK_EDGE - 4, 300);
		expect(uiStore.getState().sidebarOpen).toBe(true);
	});

	it("blocksSwipe：卡片外的祖先不算", () => {
		const outer = document.createElement("pre");
		const stop = document.createElement("div");
		const inner = document.createElement("span");
		outer.append(stop);
		stop.append(inner);
		expect(blocksSwipe(inner, stop)).toBe(false);
		expect(blocksSwipe(null, stop)).toBe(false);
	});
});
