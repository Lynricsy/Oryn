/**
 * 侧栏（M9）：空间分组与「助手」行、线程内定时的时钟标记、定时运行的折叠行（汇总状态、未读点、展开），
 * 以及手机下沉侧栏（`sink`）的三种视图与项目过滤签。数据走 mock 后端（夹具同截图）。
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
import {
	threads as fixtureThreads,
	M9_IDS,
	projects,
} from "../../mock/fixtures";
import { MockServer } from "../../mock/server";
import { connectionStore, indexStore } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { Sidebar } from "./Sidebar";

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
		sidebarView: "threads",
		projectFilter: undefined,
		expandedStacks: {},
	});
});

afterEach(() => {
	vi.unstubAllGlobals();
	indexStore.getState().setAll([], 0);
});

function mount(sink = false) {
	const qc = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter>
				<Sidebar sink={sink} />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const stack = () =>
	document.querySelector<HTMLElement>(
		`[data-schedule-stack="${M9_IDS.projectSchedule}"]`,
	) as HTMLElement;

describe("侧栏：空间、助手、定时（M9）", () => {
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

	it("「助手」固定行链到 /assistant；非下沉侧栏保留底部的连接行", () => {
		mount();
		expect(screen.getByRole("link", { name: /全局助手/ })).toHaveAttribute(
			"href",
			"/assistant",
		);
		expect(screen.getByTestId("sidebar-footer")).toHaveTextContent("已连接");
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
});

describe("下沉侧栏（sink）", () => {
	it("不渲染底部连接行与「助手」行（胶囊接管）", () => {
		mount(true);
		expect(screen.queryByTestId("sidebar-footer")).toBeNull();
		expect(screen.queryByRole("link", { name: /全局助手/ })).toBeNull();
	});

	it("项目视图：点项目 = 设过滤并回到线程视图，出现可关闭的项目过滤签", async () => {
		mount(true);
		act(() => uiStore.getState().setSidebarView("projects"));
		const list = screen.getByTestId("sidebar-projects");
		const oryn = projects[0];
		fireEvent.click(
			await within(list).findByRole("button", {
				name: new RegExp(`^${oryn?.name}`),
			}),
		);
		expect(uiStore.getState()).toMatchObject({
			sidebarView: "threads",
			projectFilter: oryn?.id,
		});
		const chip = screen.getByTestId("project-filter");
		expect(chip).toHaveTextContent(`项目：${oryn?.name}`);
		// 过滤后只剩该项目的线程
		expect(
			screen
				.queryAllByRole("link")
				.some((a) => a.getAttribute("href") === `/t/${M9_IDS.scheduledThread}`),
		).toBe(false);
		fireEvent.click(within(chip).getByRole("button", { name: "清除项目过滤" }));
		expect(uiStore.getState().projectFilter).toBeUndefined();
		expect(screen.queryByTestId("project-filter")).toBeNull();
	});

	it("项目视图的「无项目」= 只看无项目线程", () => {
		mount(true);
		act(() => uiStore.getState().setSidebarView("projects"));
		fireEvent.click(
			within(screen.getByTestId("sidebar-projects")).getByRole("button", {
				name: /^无项目/,
			}),
		);
		expect(uiStore.getState().projectFilter).toBeNull();
		expect(screen.getByTestId("project-filter")).toHaveTextContent(
			"项目：无项目",
		);
	});

	it("更多视图：第一行是连接状态，其后是外观与设置入口", () => {
		connectionStore.setState({ status: "connecting" });
		mount(true);
		act(() => uiStore.getState().setSidebarView("more"));
		const more = screen.getByTestId("sidebar-more");
		expect(more.firstElementChild).toBe(
			screen.getByTestId("sidebar-connection"),
		);
		expect(screen.getByTestId("sidebar-connection")).toHaveTextContent(
			"正在连接…",
		);
		expect(
			within(more)
				.getAllByRole("button")
				.map((b) => b.textContent?.trim()),
		).toEqual(expect.arrayContaining(["自动化", "通知", "关于"]));
	});

	it("在搜索框里输入时回到线程视图", () => {
		mount(true);
		act(() => uiStore.getState().setSidebarView("more"));
		fireEvent.change(screen.getByRole("textbox", { name: "搜索线程" }), {
			target: { value: "nightly" },
		});
		expect(uiStore.getState().sidebarView).toBe("threads");
	});
});
