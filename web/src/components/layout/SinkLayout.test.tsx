/**
 * 手机下沉式侧栏：开关状态、inert、焦点移入与还原、Esc / 返回键、触摸手势与吸附、同心圆角、减少动效；
 * iOS 原生底栏的接入（`drag` / `settle` 与页头共用起步时刻、`data-native-bar`、遮挡探测）；
 * iOS 原生页头的接入（`cardDrag` / `cardSettle`、立即跟手、`data-native-head`）。
 */
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { concentricRadius } from "../../lib/device-corners";
import type * as NativeHeaderModule from "../../lib/native-header";
import type * as NativeTabBarModule from "../../lib/native-tab-bar";
import { uiStore } from "../../state/ui";
import {
	blocksSwipe,
	SINK_EDGE,
	SinkLayout,
	settleSink,
	sinkProgress,
} from "./SinkLayout";

const nb = vi.hoisted(() => ({
	engaged: false,
	bar: {
		drag: vi.fn(),
		settle: vi.fn(),
		setCovered: vi.fn(),
		setSpec: vi.fn(),
	},
}));

// 换掉控制器实例与钩子，其余导出（常量、slotCovered）用真的
vi.mock("../../lib/native-tab-bar", async (importOriginal) => ({
	...(await importOriginal<typeof NativeTabBarModule>()),
	nativeTabBar: nb.bar,
	useNativeTabBarEngaged: () => nb.engaged,
}));

const nh = vi.hoisted(() => {
	const s = {
		engaged: false,
		header: {
			engaged: () => s.engaged,
			cardDrag: vi.fn(),
			cardSettle: vi.fn(),
		},
	};
	return s;
});

// 同上：只换原生页头的控制器实例与钩子
vi.mock("../../lib/native-header", async (importOriginal) => ({
	...(await importOriginal<typeof NativeHeaderModule>()),
	nativeHeader: nh.header,
	useNativeHeaderEngaged: () => nh.engaged,
}));

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

/** 遮挡探测的武装延迟（ARM_MS = SINK_MS + NATIVE_LAG_MS + 60）：卡片吸附完才探测。 */
const ARMED = 260 + 34 + 60;

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
	nb.engaged = false;
	for (const f of Object.values(nb.bar)) f.mockClear();
	nh.engaged = false;
	nh.header.cardDrag.mockClear();
	nh.header.cardSettle.mockClear();
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

	it("触屏上关闭后不把焦点还原到输入框（会弹软键盘）；有鼠标的设备照常还原", () => {
		const media = (coarse: boolean) => (q: string) => ({
			matches: coarse && q === "(pointer: coarse)",
			addEventListener: () => {},
			removeEventListener: () => {},
		});
		vi.stubGlobal("matchMedia", media(true));
		const first = mount();
		const field = screen.getByTestId("field");
		field.focus();
		act(() => uiStore.getState().setSidebar(true));
		act(() => uiStore.getState().setSidebar(false));
		expect(document.activeElement).not.toBe(field);
		first.unmount();

		vi.stubGlobal("matchMedia", media(false));
		mount();
		const again = screen.getByTestId("field");
		again.focus();
		act(() => uiStore.getState().setSidebar(true));
		act(() => uiStore.getState().setSidebar(false));
		expect(document.activeElement).toBe(again);
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

	it("侧栏（含它底部的底栏）渲染在侧栏层、不在卡片层里：关闭时随侧栏 inert，打开时可用", () => {
		render(
			<SinkLayout sidebar={<nav data-testid="bar">底栏</nav>}>
				<p>正文</p>
			</SinkLayout>,
		);
		const bar = screen.getByTestId("bar");
		expect(side()).toContainElement(bar);
		expect(card()).not.toContainElement(bar);
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

describe("iOS 原生底栏", () => {
	let top: Element | null = null;
	beforeEach(() => {
		window.innerWidth = 390;
		Object.defineProperty(document, "elementFromPoint", {
			configurable: true,
			value: () => top,
		});
	});
	afterEach(() => {
		window.innerWidth = 1024;
		vi.useRealTimers();
		Reflect.deleteProperty(document, "elementFromPoint");
		top = null;
	});

	/** 底栏与页头每次吸附收到同一组参数、同一个起步 Promise（两边从同一时刻出发）。 */
	function expectSameSettles() {
		const bar = nb.bar.settle.mock.calls;
		const head = nh.header.cardSettle.mock.calls;
		expect(bar.length).toBeGreaterThan(0);
		expect(head).toHaveLength(bar.length);
		bar.forEach((c, i) => {
			expect(head[i]?.slice(0, 3)).toEqual(c.slice(0, 3));
			expect(head[i]?.[3]).toBe(c[3]);
		});
	}

	it("非手势开合：底栏 settle(开合, 侧栏宽, 减少动效, 起步时刻)，与页头共用同一个起步 Promise", () => {
		vi.useFakeTimers();
		mount();
		nb.bar.settle.mockClear();
		nh.header.cardSettle.mockClear();
		act(() => uiStore.getState().setSidebar(true));
		expect(nb.bar.settle).toHaveBeenLastCalledWith(
			true,
			390,
			false,
			expect.any(Promise),
		);
		act(() => uiStore.getState().setSidebar(false));
		expect(nb.bar.settle).toHaveBeenLastCalledWith(
			false,
			390,
			false,
			expect.any(Promise),
		);
		expectSameSettles();
	});

	it("松手吸附（含弹回原状态、store 不变）：底栏收到终点，与页头共用同一个起步 Promise", () => {
		vi.useFakeTimers();
		mount();
		nb.bar.settle.mockClear();
		nh.header.cardSettle.mockClear();
		touch(card(), "touchStart", 4, 300, 1000);
		touch(card(), "touchMove", 60, 300, 2000);
		touch(card(), "touchEnd", 60);
		expect(uiStore.getState().sidebarOpen).toBe(false);
		expect(nb.bar.settle).toHaveBeenLastCalledWith(
			false,
			390,
			false,
			expect.any(Promise),
		);
		touch(card(), "touchStart", 4, 300, 3000);
		touch(card(), "touchMove", 300, 300, 3100);
		touch(card(), "touchEnd", 300);
		expect(uiStore.getState().sidebarOpen).toBe(true);
		expect(nb.bar.settle).toHaveBeenCalledWith(
			true,
			390,
			false,
			expect.any(Promise),
		);
		expectSameSettles();
	});

	it("拖动中每次 touchMove 把进度、侧栏宽与速度交给底栏 drag（与页头同一组）；没过阈值不调", () => {
		mount();
		touch(card(), "touchStart", 4, 300, 1000);
		touch(card(), "touchMove", 8, 300, 1010);
		expect(nb.bar.drag).not.toHaveBeenCalled();
		touch(card(), "touchMove", 60, 300, 1100);
		touch(card(), "touchMove", 120, 300, 1200);
		expect(nb.bar.drag).toHaveBeenCalledTimes(2);
		const [p, w, v] = nb.bar.drag.mock.calls.at(-1) ?? [];
		expect(p).toBeCloseTo(116 / 390);
		expect(w).toBe(390);
		expect(v).toBeCloseTo(60 / 100);
		expect(nh.header.cardDrag.mock.calls).toEqual(nb.bar.drag.mock.calls);
		touch(card(), "touchEnd", 120);
	});

	it("视口尺寸变化：底栏与页头按当前开合 instant 就位（不等起步时刻）", () => {
		vi.useFakeTimers();
		mount();
		act(() => uiStore.getState().setSidebar(true));
		nb.bar.settle.mockClear();
		nh.header.cardSettle.mockClear();
		window.innerWidth = 400;
		act(() => {
			window.dispatchEvent(new Event("resize"));
		});
		expect(nb.bar.settle.mock.calls).toEqual([[true, 400, true, undefined]]);
		expectSameSettles();
	});

	it("减少动效：底栏 settle instant，不等起步时刻", () => {
		vi.stubGlobal("matchMedia", (q: string) => ({
			matches: q === "(prefers-reduced-motion: reduce)",
			addEventListener: () => {},
			removeEventListener: () => {},
		}));
		mount();
		act(() => uiStore.getState().setSidebar(true));
		expect(nb.bar.settle).toHaveBeenLastCalledWith(true, 390, true, undefined);
	});

	it("底栏接管时卡片带 data-native-bar（晚 34ms 起步），未接管不带", () => {
		const { unmount } = mount();
		expect(card()).not.toHaveAttribute("data-native-bar");
		unmount();
		nb.engaged = true;
		mount();
		expect(card()).toHaveAttribute("data-native-bar");
		expect(card()).not.toHaveAttribute("data-native-head");
	});

	it("只有底栏接管时起步时刻也加上 transition-delay；都没接管不加", async () => {
		const run = () => {
			const ev = Object.assign(new Event("transitionrun"), {
				propertyName: "translate",
			});
			card().dispatchEvent(ev);
			return performance.timeOrigin + ev.timeStamp;
		};
		nb.engaged = true;
		const first = mount();
		act(() => uiStore.getState().setSidebar(true));
		const lagged = nb.bar.settle.mock.calls.at(-1)?.[3];
		await expect(lagged).resolves.toBe(run() + 34);
		first.unmount();
		uiStore.setState({ sidebarOpen: false });
		window.history.replaceState(null, "");

		nb.engaged = false;
		mount();
		act(() => uiStore.getState().setSidebar(true));
		const plain = nb.bar.settle.mock.calls.at(-1)?.[3];
		await expect(plain).resolves.toBe(run());
	});

	it("打开且接管：吸附完才探测，被盖住 setCovered(true)、露出 false，抬手后下一帧补探；关闭时撤掉", () => {
		vi.useFakeTimers();
		nb.engaged = true;
		render(
			<SinkLayout
				sidebar={
					<div data-tabbar-slot data-testid="slot">
						<span data-testid="slot-inner">底栏</span>
					</div>
				}
			>
				<p>正文</p>
			</SinkLayout>,
		);
		const slot = screen.getByTestId("slot");
		vi.spyOn(slot, "getBoundingClientRect").mockReturnValue({
			left: 12,
			top: 700,
			width: 296,
			height: 52,
		} as DOMRect);
		const menu = document.createElement("div");
		top = menu;
		act(() => uiStore.getState().setSidebar(true));
		act(() => vi.advanceTimersByTime(ARMED - 10));
		expect(nb.bar.setCovered).not.toHaveBeenCalled();
		act(() => vi.advanceTimersByTime(10));
		expect(nb.bar.setCovered).toHaveBeenLastCalledWith(true);
		// 定时探测
		top = screen.getByTestId("slot-inner");
		act(() => vi.advanceTimersByTime(250));
		expect(nb.bar.setCovered).toHaveBeenLastCalledWith(false);
		// 抬手：下一帧补探，不等下一轮定时
		top = menu;
		fireEvent.pointerUp(document.body);
		act(() => vi.advanceTimersByTime(20));
		expect(nb.bar.setCovered).toHaveBeenLastCalledWith(true);
		nb.bar.setCovered.mockClear();
		act(() => uiStore.getState().setSidebar(false));
		expect(nb.bar.setCovered).toHaveBeenCalledWith(false);
		nb.bar.setCovered.mockClear();
		act(() => vi.advanceTimersByTime(1000));
		expect(nb.bar.setCovered).not.toHaveBeenCalled();
	});

	it("未接管时不探测", () => {
		vi.useFakeTimers();
		mount();
		act(() => uiStore.getState().setSidebar(true));
		act(() => vi.advanceTimersByTime(2000));
		expect(nb.bar.setCovered).not.toHaveBeenCalled();
	});

	/** 接管且侧栏里有底栏槽位；`top` 决定 elementFromPoint 命中谁。 */
	function mountSlot() {
		vi.useFakeTimers();
		nb.engaged = true;
		render(
			<SinkLayout
				sidebar={
					<div data-tabbar-slot data-testid="slot">
						<span data-testid="slot-inner">底栏</span>
					</div>
				}
			>
				<button
					type="button"
					onClick={() => uiStore.getState().setSidebar(true)}
				>
					侧栏
				</button>
			</SinkLayout>,
		);
		vi.spyOn(
			screen.getByTestId("slot"),
			"getBoundingClientRect",
		).mockReturnValue({
			left: 12,
			top: 700,
			width: 296,
			height: 52,
		} as DOMRect);
	}

	it("点按打开：抬手发生在武装前（卡片还盖着槽位），不判定为遮挡", () => {
		mountSlot();
		top = card();
		const btn = screen.getByRole("button", { name: "侧栏" });
		fireEvent.pointerDown(btn);
		fireEvent.click(btn);
		fireEvent.pointerUp(btn);
		act(() => vi.advanceTimersByTime(20));
		fireEvent.keyUp(document.body, { key: "Enter" });
		act(() => vi.advanceTimersByTime(20));
		expect(uiStore.getState().sidebarOpen).toBe(true);
		expect(nb.bar.setCovered).not.toHaveBeenCalledWith(true);
	});

	it("武装时卡片过渡还没走完（主线程忙）：不判定，轮询也不判定；过渡结束后补探", () => {
		mountSlot();
		let running = true;
		Object.defineProperty(card(), "getAnimations", {
			configurable: true,
			value: () => (running ? [{ playState: "running" }] : []),
		});
		top = card();
		act(() => uiStore.getState().setSidebar(true));
		act(() => vi.advanceTimersByTime(ARMED + 250 * 3));
		expect(nb.bar.setCovered).not.toHaveBeenCalled();
		running = false;
		top = screen.getByTestId("slot-inner");
		fireEvent.transitionEnd(card());
		act(() => vi.advanceTimersByTime(20));
		expect(nb.bar.setCovered).toHaveBeenLastCalledWith(false);
		expect(nb.bar.setCovered).not.toHaveBeenCalledWith(true);
	});

	it("拖动中不判定遮挡", () => {
		mountSlot();
		act(() => uiStore.getState().setSidebar(true));
		top = card();
		card().setAttribute("data-dragging", "");
		act(() => vi.advanceTimersByTime(ARMED + 250));
		expect(nb.bar.setCovered).not.toHaveBeenCalled();
	});
});

describe("iOS 原生页头", () => {
	beforeEach(() => {
		window.innerWidth = 390;
	});
	afterEach(() => {
		window.innerWidth = 1024;
		vi.useRealTimers();
	});

	it("非手势开合通知 cardSettle(开合, 侧栏宽, 减少动效, 起步时刻)；页头接不接管，底栏都收到同一个终点", () => {
		vi.useFakeTimers();
		const first = mount();
		act(() => uiStore.getState().setSidebar(true));
		expect(nh.header.cardSettle).toHaveBeenLastCalledWith(
			true,
			390,
			false,
			expect.any(Promise),
		);
		act(() => uiStore.getState().setSidebar(false));
		expect(nh.header.cardSettle).toHaveBeenLastCalledWith(
			false,
			390,
			false,
			expect.any(Promise),
		);
		expect(nb.bar.settle).toHaveBeenLastCalledWith(
			false,
			390,
			false,
			expect.any(Promise),
		);
		// 换成页头接管重新挂载（关闭时的 history.back() 异步到达，原地重开会被迟到的 popstate 再关掉）
		first.unmount();
		window.history.replaceState(null, "");

		nh.engaged = true;
		mount();
		act(() => uiStore.getState().setSidebar(true));
		expect(nh.header.cardSettle).toHaveBeenLastCalledWith(
			true,
			390,
			false,
			expect.any(Promise),
		);
		expect(nb.bar.settle).toHaveBeenLastCalledWith(
			true,
			390,
			false,
			expect.any(Promise),
		);
	});

	it("起步时刻取卡片 translate 过渡真正排上的时刻（接管时加 transition-delay）；200ms 没有过渡给 null", async () => {
		nh.engaged = true;
		mount();
		act(() => uiStore.getState().setSidebar(true));
		const started = nh.header.cardSettle.mock.calls.at(-1)?.[3];
		const run = Object.assign(new Event("transitionrun"), {
			propertyName: "translate",
		});
		card().dispatchEvent(run);
		await expect(started).resolves.toBe(
			performance.timeOrigin + run.timeStamp + 34,
		);

		vi.useFakeTimers();
		act(() => uiStore.getState().setSidebar(false));
		const none = nh.header.cardSettle.mock.calls.at(-1)?.[3];
		act(() => vi.advanceTimersByTime(200));
		await expect(none).resolves.toBeNull();
	});

	it("减少动效：cardSettle 第三个参数为 true，不等起步时刻", () => {
		vi.stubGlobal("matchMedia", (q: string) => ({
			matches: q === "(prefers-reduced-motion: reduce)",
			addEventListener: () => {},
			removeEventListener: () => {},
		}));
		mount();
		act(() => uiStore.getState().setSidebar(true));
		expect(nh.header.cardSettle).toHaveBeenLastCalledWith(
			true,
			390,
			true,
			undefined,
		);
	});

	it("左缘右滑过阈值后每次 touchMove 都把进度、侧栏宽与速度交给 cardDrag；没过阈值不调", () => {
		mount();
		touch(card(), "touchStart", 4, 300, 1000);
		touch(card(), "touchMove", 8, 300, 1010);
		expect(nh.header.cardDrag).not.toHaveBeenCalled();
		touch(card(), "touchMove", 60, 300, 1100);
		touch(card(), "touchMove", 120, 300, 1200);
		expect(nh.header.cardDrag).toHaveBeenCalledTimes(2);
		const [p, w, v] = nh.header.cardDrag.mock.calls.at(-1) ?? [];
		expect(p).toBeCloseTo(116 / 390);
		expect(w).toBe(390);
		expect(v).toBeCloseTo(60 / 100);
		touch(card(), "touchEnd", 120);
	});

	it("页头接管时卡片也立即跟手：第一次 touchMove 就写 --sink-p", () => {
		nh.engaged = true;
		mount();
		touch(card(), "touchStart", 4, 300, 1000);
		touch(card(), "touchMove", 60, 300, 1010);
		expect(Number(card().style.getPropertyValue("--sink-p"))).toBeCloseTo(
			56 / 390,
		);
		expect(nh.header.cardDrag).toHaveBeenCalledTimes(1);
		touch(card(), "touchEnd", 60);
	});

	it("松手：通知 cardSettle（弹回原状态也通知），卡片不带 data-released", () => {
		mount();
		touch(card(), "touchStart", 4, 300, 1000);
		touch(card(), "touchMove", 60, 300, 2000);
		touch(card(), "touchEnd", 60);
		expect(uiStore.getState().sidebarOpen).toBe(false);
		expect(nh.header.cardSettle).toHaveBeenLastCalledWith(
			false,
			390,
			false,
			expect.any(Promise),
		);
		expect(card()).not.toHaveAttribute("data-released");

		touch(card(), "touchStart", 4, 300, 3000);
		touch(card(), "touchMove", 300, 300, 3100);
		touch(card(), "touchEnd", 300);
		expect(uiStore.getState().sidebarOpen).toBe(true);
		expect(nh.header.cardSettle).toHaveBeenCalledWith(
			true,
			390,
			false,
			expect.any(Promise),
		);
		expect(card()).not.toHaveAttribute("data-released");
	});

	it("页头接管时卡片带 data-native-head，未接管不带", () => {
		const { unmount } = mount();
		expect(card()).not.toHaveAttribute("data-native-head");
		unmount();
		nh.engaged = true;
		mount();
		expect(card()).toHaveAttribute("data-native-head");
	});
});
