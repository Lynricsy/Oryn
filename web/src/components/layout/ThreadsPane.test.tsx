/**
 * 「线程」分区侧栏：线程树与链接文字契约；空间分组、线程内定时的时钟标记、定时运行的折叠行（M9，数据走 mock 后端，
 * 夹具同截图）；M9.6 起不再有「全局助手」固定行与底部「已连接 + 图标」行，项目过滤签仍在搜索框下。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../../api/client";
import { inboxBadgeCount } from "../../lib/inbox";
import { parseTitleCard } from "../../lib/title";
import {
	threads as fixtureThreads,
	M9_IDS,
	MAIN_THREAD,
	projects,
} from "../../mock/fixtures";
import { MockServer } from "../../mock/server";
import { connectionStore, indexStore } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { groupThreads, ThreadRow, ThreadsPane } from "./ThreadsPane";

const NOW = 1_800_000_000_000;

function th(id: string, p: Partial<Thread> = {}): Thread {
	return {
		id,
		title: id,
		titleSource: "auto",
		state: "idle",
		stateChangedAt: NOW,
		pinned: false,
		labels: [],
		unread: false,
		createdAt: NOW,
		updatedAt: NOW,
		lastMessageAt: NOW,
		...p,
	};
}

describe("侧边栏线程树", () => {
	it("fork 再 fork：孙线程也渲染并逐级缩进（W10）", () => {
		const g = groupThreads(
			[
				th("parent"),
				th("child", { parentThreadId: "parent" }),
				th("grandchild", { parentThreadId: "child" }),
			],
			"",
			NOW,
		);
		const root = g.byProject[0]?.threads[0] as Thread;
		render(
			<MemoryRouter>
				<ThreadRow thread={root} childMap={g.children} />
			</MemoryRouter>,
		);
		const links = screen.getAllByRole("link");
		// 契约：链接 textContent 只等于标题卡解析后的标题文字
		expect(links.map((a) => a.textContent)).toEqual(
			["parent", "child", "grandchild"].map((s) => parseTitleCard(s).text),
		);
		expect(links.map((a) => a.style.paddingLeft)).toEqual([
			"0.5rem",
			"1.5rem",
			"2.5rem",
		]);
	});

	it("标题卡图标、时间、子线程计数、活动行都不进链接文字", () => {
		const g = groupThreads(
			[
				th("p", {
					title: "🔌 SOCK: 重构 WebSocket 重连",
					state: "working",
					activity: "读取 socket.ts",
					childCount: 2,
					childDoneCount: 1,
				}),
				th("c", { title: "fork：共用 orb", parentThreadId: "p", unread: true }),
			],
			"",
			NOW,
		);
		const root = g.byProject[0]?.threads[0] as Thread;
		const { container } = render(
			<MemoryRouter>
				<ThreadRow thread={root} childMap={g.children} now={NOW} />
			</MemoryRouter>,
		);
		const links = screen.getAllByRole("link");
		expect(links.map((a) => a.textContent)).toEqual([
			"重构 WebSocket 重连",
			"fork：共用 orb",
		]);
		expect(links.map((a) => a.style.paddingLeft)).toEqual(["0.5rem", "1.5rem"]);
		expect(
			container.querySelector("[data-glyph]")?.getAttribute("data-glyph"),
		).toBe("🔌");
		expect(
			container.querySelector("[data-count]")?.getAttribute("data-count"),
		).toBe("1/2");
		expect(
			container.querySelector("[data-activity]")?.getAttribute("data-activity"),
		).toBe("读取 socket.ts");
	});
});

describe("线程分区（M9 / M9.6）", () => {
	let server: MockServer;

	beforeEach(() => {
		server = new MockServer();
		vi.stubGlobal(
			"fetch",
			async (input: RequestInfo | URL, init?: RequestInit) => {
				const req = input instanceof Request ? input : new Request(input, init);
				return (
					(await server.handleFetch(req)) ?? new Response(null, { status: 404 })
				);
			},
		);
		indexStore.getState().setAll(
			fixtureThreads.filter((t) => !t.archivedAt),
			1,
		);
		connectionStore.setState({ status: "open" });
		uiStore.setState({
			sidebarOpen: true,
			projectFilter: undefined,
			expandedStacks: {},
		});
	});

	afterEach(() => {
		vi.unstubAllGlobals();
		indexStore.getState().setAll([], 0);
		uiStore.setState({ projectFilter: undefined });
	});

	function mount(active = true) {
		const qc = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		return render(
			<QueryClientProvider client={qc}>
				<MemoryRouter>
					<ThreadsPane active={active} />
				</MemoryRouter>
			</QueryClientProvider>,
		);
	}

	const stack = () =>
		document.querySelector<HTMLElement>(
			`[data-schedule-stack="${M9_IDS.projectSchedule}"]`,
		) as HTMLElement;

	it("空间分组：Space 名链到 Space 页，计数 = 未归档工作线程数，聊天未读带未读点", async () => {
		mount();
		const link = await screen.findByRole("link", { name: /发布 M9/ });
		expect(link).toHaveAttribute("href", `/space/${M9_IDS.space}`);
		expect(link).toHaveTextContent("3");
		expect(within(link).getByRole("img", { name: "未读" })).toBeVisible();
		// 协调对话本身不作为线程行出现
		expect(
			screen
				.queryAllByRole("link")
				.some((a) => a.getAttribute("href") === `/t/${M9_IDS.spaceChat}`),
		).toBe(false);
	});

	it("没有「全局助手」固定行与底部连接行；助手对话也不作为线程行出现", async () => {
		mount();
		await screen.findByRole("link", { name: /发布 M9/ });
		expect(screen.queryByRole("link", { name: /全局助手/ })).toBeNull();
		expect(
			screen
				.queryAllByRole("link")
				.some(
					(a) =>
						a.getAttribute("href") === "/assistant" ||
						a.getAttribute("href") === `/t/${M9_IDS.assistant}`,
				),
		).toBe(false);
		expect(screen.queryByTestId("sidebar-footer")).toBeNull();
		expect(screen.queryByText("已连接")).toBeNull();
	});

	it("线程内定时：行尾时钟标记（不进链接文字）", () => {
		mount();
		const row = screen
			.getAllByRole("link")
			.find(
				(a) => a.getAttribute("href") === `/t/${M9_IDS.scheduledThread}`,
			) as HTMLElement;
		expect(within(row).getByRole("img", { name: "有定时" })).toBeVisible();
		expect(row.textContent).toBe("每天早上同步上游并跑测试");
	});

	it("定时折叠：一行 = 定时名 + 汇总状态 + 未读点 + 运行数；点开是各次运行与「全部运行」", async () => {
		mount();
		await waitFor(() => expect(stack()).toHaveTextContent("nightly"));
		const head = within(stack()).getByRole("button");
		expect(head).toHaveAttribute("aria-expanded", "false");
		// 第 1 次运行已归档，不在索引里
		expect(head).toHaveTextContent("2");
		expect(within(head).getByTitle("工作中")).toBeVisible();
		expect(within(head).getByRole("img", { name: "未读" })).toBeVisible();
		// 运行线程本身不作为项目里的普通行出现
		expect(
			screen
				.queryAllByRole("link")
				.some((a) => a.getAttribute("href") === `/t/${M9_IDS.run2}`),
		).toBe(false);
		fireEvent.click(head);
		expect(head).toHaveAttribute("aria-expanded", "true");
		const links = within(stack()).getAllByRole("link");
		expect(links.map((a) => a.getAttribute("href"))).toEqual([
			`/t/${M9_IDS.run3}`,
			`/t/${M9_IDS.run2}`,
			`/settings/automations?schedule=${M9_IDS.projectSchedule}`,
		]);
		expect(links[2]).toHaveTextContent("全部运行");
		fireEvent.click(head);
		expect(within(stack()).queryAllByRole("link")).toEqual([]);
	});

	it("项目过滤签：过滤后只剩该项目的线程，可清除", async () => {
		const oryn = projects[0];
		uiStore.setState({ projectFilter: oryn?.id });
		mount();
		const chip = screen.getByTestId("project-filter");
		await waitFor(() => expect(chip).toHaveTextContent(`项目：${oryn?.name}`));
		expect(
			screen
				.queryAllByRole("link")
				.some((a) => a.getAttribute("href") === `/t/${M9_IDS.scheduledThread}`),
		).toBe(false);
		fireEvent.click(within(chip).getByRole("button", { name: "清除项目过滤" }));
		expect(uiStore.getState().projectFilter).toBeUndefined();
		expect(screen.queryByTestId("project-filter")).toBeNull();
		expect(
			screen
				.queryAllByRole("link")
				.some((a) => a.getAttribute("href") === `/t/${M9_IDS.scheduledThread}`),
		).toBe(true);
	});

	it("「无项目」过滤签", () => {
		uiStore.setState({ projectFilter: null });
		mount();
		expect(screen.getByTestId("project-filter")).toHaveTextContent(
			"项目：无项目",
		);
	});

	it("搜索聚焦请求：非当前分区时不聚焦，变成当前分区后聚焦一次", () => {
		uiStore.setState({ searchNonce: 0 });
		const qc = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		const pane = (active: boolean) => (
			<QueryClientProvider client={qc}>
				<MemoryRouter>
					<ThreadsPane active={active} />
				</MemoryRouter>
			</QueryClientProvider>
		);
		const view = render(pane(false));
		const search = screen.getByRole("textbox", { name: "搜索线程" });
		act(() => uiStore.setState({ searchNonce: 1 }));
		expect(document.activeElement).not.toBe(search);
		view.rerender(pane(true));
		expect(document.activeElement).toBe(search);
		// 同一个 nonce 只处理一次：切走再切回不再抢焦点
		act(() => search.blur());
		view.rerender(pane(false));
		view.rerender(pane(true));
		expect(document.activeElement).not.toBe(search);
		// 新的请求在当前分区上立即聚焦
		act(() => uiStore.setState({ searchNonce: 2 }));
		expect(document.activeElement).toBe(search);
	});

	it("「需要你」分组在最上、子线程平铺；父行状态图标上卷；收件箱入口角标", () => {
		mount();
		const group = screen
			.getByRole("button", { name: /需要你/ })
			.closest("div.mb-2") as HTMLElement;
		const hrefs = within(group)
			.getAllByRole("link")
			.map((a) => a.getAttribute("href"));
		// 等待输入在前（置顶线程与 Space 派生的线程），出错在后（含孙线程）
		expect(hrefs).toEqual(
			[
				M9_IDS.spaceNeedsYou,
				"T-0193a1b2-0000-7000-8000-00000000a004",
				"T-0193a1b2-0000-7000-8000-00000000a0f1",
				"T-0193a1b2-0000-7000-8000-00000000a005",
			].map((id) => `/t/${id}`),
		);
		// 分组排在空间分组之前
		const scroll = screen.getByTestId("threads-scroll");
		expect(scroll.firstElementChild).toBe(group);
		// 孙线程出错 → 主线程（祖父）行在项目分组里显示出错
		const main = within(scroll)
			.getAllByRole("link")
			.find(
				(a) => a.getAttribute("href") === `/t/${MAIN_THREAD}`,
			) as HTMLElement;
		expect(within(main).getByTitle("出错")).toBeVisible();
		const entry = screen.getByTestId("inbox-entry");
		expect(entry).toHaveAttribute("href", "/inbox");
		const n = inboxBadgeCount(
			fixtureThreads.filter((t) => !t.archivedAt),
			Date.now(),
		);
		expect(n).toBeGreaterThan(0);
		expect(entry).toHaveAccessibleName(`收件箱，${n} 项待处理`);
	});
});
