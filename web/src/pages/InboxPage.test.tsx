/**
 * 收件箱页：分节顺序与行内容取实时索引；分组方式切换并存 localStorage；空状态。项目名走 mock 后端。
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
import type { Thread } from "../api/client";
import { projects } from "../mock/fixtures";
import { MockServer } from "../mock/server";
import {
	type AppNotification,
	notificationsStore,
} from "../state/notifications";
import { indexStore } from "../state/stores";
import { InboxPage } from "./InboxPage";

const NOW = Date.now();

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

const sections = () =>
	[...document.querySelectorAll<HTMLElement>("[data-inbox-section]")].map(
		(s) => s.dataset.inboxSection,
	);

describe("收件箱页", () => {
	beforeEach(() => {
		const server = new MockServer();
		vi.stubGlobal(
			"fetch",
			async (input: RequestInfo | URL, init?: RequestInit) => {
				const req = input instanceof Request ? input : new Request(input, init);
				return (
					(await server.handleFetch(req)) ?? new Response(null, { status: 404 })
				);
			},
		);
	});

	afterEach(() => {
		vi.unstubAllGlobals();
		indexStore.getState().setAll([], 0);
		notificationsStore.setState({ items: [] });
		localStorage.clear();
	});

	function mount() {
		const qc = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		return render(
			<QueryClientProvider client={qc}>
				<MemoryRouter>
					<InboxPage />
				</MemoryRouter>
			</QueryClientProvider>,
		);
	}

	it("按状态分节，行带意图行与派生来源，点行进线程；切到按项目并记住", async () => {
		const oryn = projects[0];
		indexStore.getState().setAll(
			[
				th("parent", { title: "🔌 SOCK: 父线程", projectId: oryn?.id }),
				th("run", {
					state: "working",
					activity: "读 socket.ts",
					projectId: oryn?.id,
				}),
				th("wait", {
					state: "awaiting_input",
					parentThreadId: "parent",
					projectId: oryn?.id,
				}),
				th("done", { unread: true, lastMessageAt: NOW - 60_000 }),
				th("err", { state: "error", archivedAt: NOW }),
			],
			1,
		);
		mount();
		expect(sections()).toEqual(["you", "ready", "running"]);
		const wait = document.querySelector(
			'[data-inbox-row="wait"]',
		) as HTMLElement;
		expect(wait).toHaveAttribute("href", "/t/wait");
		expect(wait).toHaveTextContent("派生自「父线程」");
		await waitFor(() => expect(wait).toHaveTextContent(oryn?.name ?? ""));
		const run = document.querySelector('[data-inbox-row="run"]') as HTMLElement;
		expect(
			within(run).getByText("读 socket.ts", {
				selector: "[data-inbox-activity]",
			}),
		).toBeVisible();
		// 空闲已读的父线程、已归档的出错线程都不列
		expect(document.querySelector('[data-inbox-row="parent"]')).toBeNull();
		expect(document.querySelector('[data-inbox-row="err"]')).toBeNull();

		fireEvent.click(screen.getByRole("button", { name: "按项目" }));
		expect(screen.getByRole("button", { name: "按项目" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(sections()).toEqual([oryn?.id, "_none"]);
		expect(localStorage.getItem("oryn.inbox.groupBy")).toBe("project");
	});

	it("索引变化实时反映；没有条目时是空状态", () => {
		indexStore.getState().setAll([th("quiet")], 1);
		mount();
		expect(screen.getByTestId("inbox-empty")).toHaveTextContent(
			"没有需要你处理的线程",
		);
		act(() =>
			indexStore
				.getState()
				.upsertThread(th("quiet", { state: "awaiting_input" })),
		);
		expect(screen.queryByTestId("inbox-empty")).toBeNull();
		expect(sections()).toEqual(["you"]);
	});

	it("行内预览取该线程未作答的提问与失败原因；状态筛选只留那一类，那一类清空后回到全部", () => {
		const note = (
			id: string,
			threadId: string,
			p: Partial<AppNotification>,
		): AppNotification => ({
			id,
			kind: "input",
			threadId,
			title: threadId,
			body: "",
			url: `/t/${threadId}`,
			createdAt: NOW,
			deliveries: [],
			...p,
		});
		// 新的在前：已作答的那条较新，预览要跳过它取未作答的
		notificationsStore.setState({
			items: [
				note("n2", "wait", { body: "已经答过的旧问题", resolvedAt: NOW }),
				note("n1", "wait", { body: "允许执行 `rm -rf dist`？" }),
			],
		});
		indexStore.getState().setAll(
			[
				th("wait", { state: "awaiting_input" }),
				th("err", {
					state: "error",
					error: { kind: "agent_failed", reason: "omp exited with status 1" },
				}),
				th("run", { state: "working", activity: "跑测试" }),
			],
			1,
		);
		mount();
		const preview = (id: string) =>
			document.querySelector(`[data-inbox-row="${id}"] [data-inbox-preview]`);
		expect(preview("wait")).toHaveTextContent("允许执行 rm -rf dist？");
		expect(preview("wait")).not.toHaveTextContent("已经答过");
		expect(preview("err")).toHaveTextContent("omp exited with status 1");

		fireEvent.click(
			document.querySelector("[data-inbox-filter=stopped]") as HTMLElement,
		);
		expect(sections()).toEqual(["stopped"]);
		act(() => indexStore.getState().upsertThread(th("err", { state: "idle" })));
		expect(sections()).toEqual(["you", "running"]);
	});

	it("行内归档走归档入口：带 worktree 的线程先弹出是否一并移除工作树", () => {
		indexStore.getState().setAll(
			[
				th("wt", {
					state: "error",
					worktree: { repo: "/r", dir: "/r/.wt/x", branch: "oryn/x" },
				}),
			],
			1,
		);
		mount();
		const row = document.querySelector('[data-inbox-row="wt"]')
			?.parentElement as HTMLElement;
		fireEvent.click(within(row).getByRole("button", { name: "归档" }));
		expect(screen.getByTestId("archive-dialog")).toHaveTextContent("/r/.wt/x");
		// 对话框确认前不归档
		expect(indexStore.getState().threads.wt?.archivedAt).toBeUndefined();
	});
});
