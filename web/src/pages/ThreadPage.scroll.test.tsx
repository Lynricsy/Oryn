/**
 * 窄屏切标签保留阅读位置（M7 验收 A9）：对话 → 终端 → 对话，视口顶部那条转录条目和它的像素偏移不变；
 * 停在底部跟随时，切走期间来了新消息，切回仍在底部。
 *
 * jsdom 没有布局：这里给一个最小的布局模型，按 Chromium 的 display:none 语义——祖先带 `hidden`（Tailwind 的
 * display:none）时，元素尺寸为 0、ResizeObserver 报 0、scrollTop 读作 0 且写入无效，恢复显示时滚动偏移按新的
 * 最大值钳住；其余情况行高、视口高固定。真机上 display:none 让 react-virtual 把在屏行测成 0、总高缩水、
 * 恢复时偏移被钳住再被测高补偿推走（A9：scrollTop 3616 → 隐藏中 0/0 → 恢复 2365（总高 3204）→ 3705）；
 * 修复是切走时转录不 display:none，这里断言切走期间仍参与布局，并验证切回后锚点不变、贴底仍跟随。转录用真的 `Transcript`（含 react-virtual），其余子件换成桩。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../api/client";
import { qk } from "../api/queries";
import type * as RightPanelModule from "../components/thread/RightPanel";
import type * as ThreadHeaderModule from "../components/thread/ThreadHeader";
import { getThreadStore, resetThreadStores } from "../state/stores";
import type * as TranscriptModule from "../state/transcript";
import type { TranscriptItem } from "../state/transcript";
import { uiStore } from "../state/ui";
import { ThreadPage } from "./ThreadPage";

vi.mock("../realtime/socket", () => ({
	realtime: { subscribe: () => () => {}, focus: () => {} },
}));
vi.mock("../state/terminals", () => ({
	terminalHub: { attach: () => () => {} },
	useTerminals: () => false,
}));
vi.mock("../components/transcript/WorkingFooter", () => ({
	WorkingFooter: () => null,
}));
vi.mock("../components/composer/Composer", () => ({ Composer: () => null }));
vi.mock("../components/composer/DockTray", () => ({
	dockArrived: () => {},
	takeArrival: () => false,
	useDockMorph: () => "idle",
}));
vi.mock("../components/thread/Dialogs", () => ({
	PlanProposalDialog: () => null,
	ThreadDialogs: () => null,
}));
vi.mock("../components/thread/EnvBanner", () => ({ EnvBanner: () => null }));
vi.mock("../components/thread/ModeBar", () => ({
	AboveEditorWidgets: () => null,
	BelowEditorWidgets: () => null,
}));
vi.mock("../components/thread/QueueBar", () => ({ QueueBar: () => null }));
vi.mock("../components/thread/UiRequestCard", () => ({
	UiRequestCard: () => null,
}));
vi.mock("../components/thread/ThreadHeader", async (orig) => ({
	...(await orig<typeof ThreadHeaderModule>()),
	ThreadHeader: () => <header />,
}));
vi.mock("../components/thread/RightPanel", async (orig) => ({
	...(await orig<typeof RightPanelModule>()),
	RightPanel: () => <div data-testid="right-panel" />,
}));
// 转录内容由测试直接给出（不经条目重建）
let items: TranscriptItem[] = [];
vi.mock("../state/transcript", async (orig) => ({
	...(await orig<typeof TranscriptModule>()),
	buildTranscript: () => ({ items, toolResults: new Map(), liveTools: {} }),
}));

const ID = "T-scroll";
const VIEW = 800;
const rowHeight = (i: number) => 100 + (i % 4) * 40;

function message(i: number): TranscriptItem {
	return {
		kind: "message",
		key: `e${i}`,
		seq: i,
		entryId: `E${i}`,
		message: {
			role: i % 2 ? "assistant" : "user",
			content: [{ type: "text", text: `m${i}` }],
		},
		live: false,
	};
}

// ---- 布局模型

/** 自身或祖先带 `hidden`（display:none）即不渲染。 */
function displayed(el: Element): boolean {
	for (let e: Element | null = el; e; e = e.parentElement)
		if (e.classList.contains("hidden")) return false;
	return true;
}

const isScroller = (el: Element) =>
	el.getAttribute("data-testid") === "transcript";

function heightOf(el: HTMLElement): number {
	if (!displayed(el)) return 0;
	if (isScroller(el)) return VIEW;
	const index = el.getAttribute("data-index");
	if (index !== null) return rowHeight(Number(index));
	return 0;
}

/** 滚动容器自己存的偏移（不渲染期间保留，恢复显示时按新最大值钳住）。 */
const offsets = new WeakMap<HTMLElement, number>();
function scrollHeightOf(el: HTMLElement): number {
	if (!displayed(el)) return 0;
	const inner = el.firstElementChild as HTMLElement | null;
	return Math.max(VIEW, Number.parseFloat(inner?.style.height ?? "0") || 0);
}
const maxTop = (el: HTMLElement) =>
	Math.max(0, scrollHeightOf(el) - heightOf(el));

/** 假 ResizeObserver：`flush` 时把尺寸变了的被观察元素报给回调（首次观察也报一次）。 */
const observers = new Set<FakeResizeObserver>();
class FakeResizeObserver {
	private seen = new Map<Element, number>();
	private cb: ResizeObserverCallback;
	constructor(cb: ResizeObserverCallback) {
		this.cb = cb;
		observers.add(this);
	}
	observe(el: Element) {
		if (!this.seen.has(el)) this.seen.set(el, -1);
	}
	unobserve(el: Element) {
		this.seen.delete(el);
	}
	disconnect() {
		this.seen.clear();
		observers.delete(this);
	}
	/** 收集尺寸变了的被观察元素（先全部收集、再统一回调，同浏览器的一轮观察）。 */
	gather(): (() => void) | null {
		const entries: ResizeObserverEntry[] = [];
		for (const [el, last] of this.seen) {
			const h = heightOf(el as HTMLElement);
			if (h === last) continue;
			this.seen.set(el, h);
			entries.push({
				target: el,
				contentRect: { width: 390, height: h } as DOMRectReadOnly,
				borderBoxSize: [{ inlineSize: 390, blockSize: h }],
				contentBoxSize: [{ inlineSize: 390, blockSize: h }],
				devicePixelContentBoxSize: [],
			});
		}
		if (!entries.length) return null;
		return () => this.cb(entries, this as unknown as ResizeObserver);
	}
}

/** 跑到布局稳定：反复投递尺寸变化（渲染出的新行再测量），浏览器在偏移被钳住时会派发滚动事件。 */
function settle() {
	for (let round = 0; round < 20; round++) {
		let changed = false;
		act(() => {
			const calls = [...observers].map((o) => o.gather());
			for (const c of calls) {
				if (!c) continue;
				changed = true;
				c();
			}
		});
		const el = scroller();
		const top = el.scrollTop;
		if (top !== lastReported.get(el)) {
			lastReported.set(el, top);
			fireEvent.scroll(el);
			changed = true;
		}
		if (!changed) return;
	}
	throw new Error("布局没有稳定");
}
const lastReported = new WeakMap<HTMLElement, number>();

const scroller = () => screen.getByTestId("transcript");

/** 视口顶部那条转录行：序号与它顶边相对视口顶的偏移。 */
function anchor() {
	const el = scroller();
	const top = el.scrollTop;
	for (const row of el.querySelectorAll<HTMLElement>("[data-index]")) {
		const start = Number(
			/translateY\(([-\d.]+)px\)/.exec(row.style.transform)?.[1],
		);
		const i = Number(row.dataset.index);
		if (start <= top && top < start + rowHeight(i))
			return { index: i, offset: start - top };
	}
	return null;
}

function switchTab(tab: "terminal" | "agent") {
	act(() => uiStore.getState().setThreadTab(tab));
	settle();
	// 切走期间转录仍参与布局（不 display:none）：行高、视口高与滚动偏移都还在，只是不可见、不可交互
	const pane = screen.getByTestId("transcript-pane");
	expect(displayed(scroller())).toBe(true);
	expect(scroller().clientHeight).toBe(VIEW);
	if (tab === "agent") expect(pane).not.toHaveAttribute("inert");
	else expect(pane).toHaveAttribute("inert");
}

/** 新消息到达：换转录内容并让线程页重建（条目数组换新引用）。 */
function grow(n: number) {
	items = [
		...items,
		...Array.from({ length: n }, (_, k) => message(items.length + k)),
	];
	act(() => getThreadStore(ID).getState().patchLocal({ entries: [] }));
	settle();
}

const proto = HTMLElement.prototype;
const saved = {
	clientHeight: Object.getOwnPropertyDescriptor(
		Element.prototype,
		"clientHeight",
	),
	clientWidth: Object.getOwnPropertyDescriptor(
		Element.prototype,
		"clientWidth",
	),
	offsetHeight: Object.getOwnPropertyDescriptor(proto, "offsetHeight"),
	scrollHeight: Object.getOwnPropertyDescriptor(
		Element.prototype,
		"scrollHeight",
	),
	scrollTop: Object.getOwnPropertyDescriptor(Element.prototype, "scrollTop"),
};
const initialWidth = window.innerWidth;

beforeEach(() => {
	vi.stubGlobal("ResizeObserver", FakeResizeObserver);
	Object.defineProperties(Element.prototype, {
		clientHeight: {
			configurable: true,
			get(this: HTMLElement) {
				return heightOf(this);
			},
		},
		clientWidth: {
			configurable: true,
			get(this: HTMLElement) {
				return displayed(this) ? 390 : 0;
			},
		},
		scrollHeight: {
			configurable: true,
			get(this: HTMLElement) {
				return scrollHeightOf(this);
			},
		},
		scrollTop: {
			configurable: true,
			get(this: HTMLElement) {
				if (!displayed(this)) return 0;
				return Math.min(offsets.get(this) ?? 0, maxTop(this));
			},
			set(this: HTMLElement, v: number) {
				// 不渲染的元素没有滚动盒，写入无效
				if (!displayed(this)) return;
				offsets.set(this, Math.max(0, Math.min(v, maxTop(this))));
			},
		},
	});
	Object.defineProperty(proto, "offsetHeight", {
		configurable: true,
		get(this: HTMLElement) {
			return heightOf(this);
		},
	});
	Element.prototype.scrollTo = function (
		this: HTMLElement,
		opts?: ScrollToOptions | number,
	) {
		if (typeof opts === "object" && opts.top !== undefined)
			this.scrollTop = opts.top;
	} as Element["scrollTo"];

	items = Array.from({ length: 60 }, (_, i) => message(i));
	getThreadStore(ID)
		.getState()
		.patchLocal({ thread: { id: ID } as Thread, snapshotReceived: true });
	uiStore.setState({ threadTab: "agent", panelTab: "info", panelOpen: true });
	window.innerWidth = 390;
	const qc = new QueryClient({
		defaultOptions: { queries: { retry: false, staleTime: Infinity } },
	});
	qc.setQueryData(["spaces", "list", { archived: false }], { spaces: [] });
	qc.setQueryData(qk.agentIdentity, {});
	qc.setQueryData(qk.userIdentity, {});
	qc.setQueryData(qk.gitIdentity, {});
	render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={[`/t/${ID}`]}>
				<Routes>
					<Route path="/t/:threadId" element={<ThreadPage />} />
				</Routes>
			</MemoryRouter>
		</QueryClientProvider>,
	);
	settle();
});

afterEach(() => {
	for (const [k, d] of Object.entries(saved)) {
		const target = k === "offsetHeight" ? proto : Element.prototype;
		if (d) Object.defineProperty(target, k, d);
		else delete (target as unknown as Record<string, unknown>)[k];
	}
	vi.unstubAllGlobals();
	observers.clear();
	window.innerWidth = initialWidth;
	resetThreadStores();
});

describe("窄屏切标签保留阅读位置", () => {
	it("往上翻一点（离底部不到一屏），切到终端再切回：视口顶部还是同一条、偏移不变", () => {
		const el = scroller();
		expect(el.scrollTop).toBe(maxTop(el));
		// 用户往上翻（滚轮意图 + 滚动）
		fireEvent.wheel(el, { deltaY: -120 });
		el.scrollTop = maxTop(el) - 500;
		settle();
		const before = anchor();
		expect(before).not.toBeNull();
		expect(maxTop(el) - el.scrollTop).toBeGreaterThan(100);

		switchTab("terminal");
		switchTab("agent");
		expect(anchor()).toEqual(before);
	});

	it("切走期间来了新消息：往上翻过的阅读位置不变", () => {
		const el = scroller();
		fireEvent.wheel(el, { deltaY: -120 });
		el.scrollTop = maxTop(el) - 500;
		settle();
		const before = anchor();

		switchTab("terminal");
		grow(5);
		switchTab("agent");
		expect(anchor()).toEqual(before);
	});

	it("停在底部跟随：切走期间来了新消息，切回仍在底部、最后一条可见", () => {
		const el = scroller();
		switchTab("terminal");
		grow(5);
		switchTab("agent");
		expect(el.scrollTop).toBe(maxTop(el));
		expect(
			el.querySelector(`[data-index="${items.length - 1}"]`),
		).not.toBeNull();
	});
});
