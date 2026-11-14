/**
 * Space 页（M9）：expanded 聊天 + 右列（进度图 → 定时 → 线程三段）；窄屏「聊天 | 线程」标签；线程三段随索引实时变化；
 * 加入 / 移出空间。聊天本身（`ThreadScreen`）换成桩，只看页面自己的布局与数据。数据走 mock 后端。
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
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../api/client";
import { threads as fixtureThreads, M9_IDS } from "../mock/fixtures";
import { MockServer } from "../mock/server";
import { indexStore } from "../state/stores";
import { SpacePage } from "./SpacePage";
import type * as ThreadPageModule from "./ThreadPage";

vi.mock("./ThreadPage", async (orig) => ({
	...(await orig<typeof ThreadPageModule>()),
	ThreadScreen: ({
		threadId,
		variant,
		header,
		subheader,
	}: {
		threadId: string;
		variant: string;
		header?: (l: ThreadPageModule.ScreenHeaderLayout) => ReactNode;
		subheader?: ReactNode;
	}) => (
		<div data-testid="screen" data-thread={threadId} data-variant={variant}>
			{header?.({
				sidebarToggle: false,
				veil: 64,
				chromeLeft: false,
				chromeRight: false,
			})}
			{subheader}
		</div>
	),
}));

let server: MockServer;
const initialWidth = window.innerWidth;

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
});

afterEach(() => {
	vi.unstubAllGlobals();
	window.innerWidth = initialWidth;
	indexStore.getState().setAll([], 0);
});

function mount(width: number, id: string = M9_IDS.space) {
	window.innerWidth = width;
	const qc = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={[`/space/${id}`]}>
				<Routes>
					<Route path="/space/:spaceId" element={<SpacePage />} />
				</Routes>
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const sectionIds = (title: string) => {
	const h = screen
		.getAllByRole("heading", { level: 3 })
		.find((x) => x.textContent?.startsWith(title));
	if (!h) return [];
	return Array.from(
		h.parentElement?.querySelectorAll("[data-thread-card]") ?? [],
	).map((c) => c.getAttribute("data-thread-card"));
};

describe("SpacePage", () => {
	it("expanded：协调聊天 + 右列（进度图 → 定时 → 线程三段）；页头是名称与目标", async () => {
		mount(1280);
		const chat = await screen.findByTestId("screen");
		expect(chat).toHaveAttribute("data-thread", M9_IDS.spaceChat);
		expect(chat).toHaveAttribute("data-variant", "coordinator");
		expect(
			within(chat).getByRole("heading", { name: "发布 M9" }),
		).toBeVisible();
		expect(screen.getByTestId("space-goal")).toHaveTextContent(
			"Space、全局助手与定时三件事",
		);
		const side = screen.getByTestId("space-side");
		const progress = await within(side).findByTestId("space-progress");
		expect(progress.querySelectorAll("[data-day]")).toHaveLength(30);
		await waitFor(() =>
			expect(
				within(side)
					.getByTestId("space-schedules")
					.querySelectorAll("[data-schedule]"),
			).toHaveLength(1),
		);
		expect(sectionIds("需要你")).toEqual([M9_IDS.spaceNeedsYou]);
		expect(sectionIds("进行中")).toEqual([
			M9_IDS.spaceWorking,
			M9_IDS.spaceIdle,
		]);
		// 右列各节的顺序：进度图 → 定时 → 线程
		const order = Array.from(
			side.querySelectorAll(
				"[data-testid=space-progress],[data-testid=space-schedules],[data-testid=space-threads]",
			),
		).map((e) => e.getAttribute("data-testid"));
		expect(order).toEqual([
			"space-progress",
			"space-schedules",
			"space-threads",
		]);
	});

	it("线程三段随索引实时变化", async () => {
		mount(1280);
		await screen.findByTestId("space-threads");
		const cur = indexStore.getState().threads[M9_IDS.spaceWorking] as Thread;
		act(() =>
			indexStore.getState().upsertThread({ ...cur, state: "awaiting_input" }),
		);
		// 按最近活动排序
		expect(sectionIds("需要你")).toEqual([
			M9_IDS.spaceWorking,
			M9_IDS.spaceNeedsYou,
		]);
		act(() =>
			indexStore.getState().upsertThread({
				...cur,
				archivedAt: Date.now(),
				shippedAt: Date.now(),
			}),
		);
		expect(sectionIds("已结束")).toEqual([M9_IDS.spaceWorking]);
	});

	it("卡片菜单「移出空间」：PATCH spaceId 为空，卡片离开", async () => {
		mount(1280);
		await screen.findByTestId("space-threads");
		const card = document.querySelector(
			`[data-thread-card="${M9_IDS.spaceIdle}"]`,
		) as HTMLElement;
		fireEvent.click(within(card).getByRole("button", { name: "线程操作" }));
		fireEvent.click(await screen.findByRole("menuitem", { name: "移出空间" }));
		await waitFor(() =>
			expect(
				document.querySelector(`[data-thread-card="${M9_IDS.spaceIdle}"]`),
			).toBeNull(),
		);
		expect(server.threads.get(M9_IDS.spaceIdle)?.spaceId).toBeUndefined();
	});

	it("「加入线程」：搜索自己的工作线程，点选即加入", async () => {
		mount(1280);
		fireEvent.click(await screen.findByRole("button", { name: "加入线程" }));
		const search = screen.getByRole("textbox", { name: "搜索线程" });
		fireEvent.change(search, { target: { value: "上游" } });
		// 协调对话不是候选
		fireEvent.click(
			screen.getByRole("button", { name: /每天早上同步上游并跑测试/ }),
		);
		await waitFor(() =>
			expect(
				document.querySelector(
					`[data-thread-card="${M9_IDS.scheduledThread}"]`,
				),
			).not.toBeNull(),
		);
		expect(server.threads.get(M9_IDS.scheduledThread)?.spaceId).toBe(
			M9_IDS.space,
		);
	});

	it("窄屏：「聊天 | 线程」两项标签；切到线程时聊天保持挂载", async () => {
		mount(390);
		const chat = await screen.findByTestId("screen");
		const tabs = within(chat).getByTestId("space-tabs");
		expect(
			within(tabs)
				.getAllByRole("tab")
				.map((x) => x.textContent),
		).toEqual(["聊天", "线程"]);
		expect(screen.queryByTestId("space-side")).toBeNull();
		fireEvent.click(within(tabs).getByRole("tab", { name: "线程" }));
		expect(screen.getByTestId("space-side")).toBeVisible();
		expect(screen.getByTestId("screen")).toBe(chat);
		expect(chat.parentElement).toHaveClass("hidden");
	});

	it("找不到 Space：空状态", async () => {
		mount(1280, "spc_01JD0000000000000000NOPE00");
		expect(await screen.findByText("找不到这个空间")).toBeVisible();
	});
});
