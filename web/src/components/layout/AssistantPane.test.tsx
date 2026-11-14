/**
 * 「助手」分区侧栏（M9.6）：会话按最近活动排列、未读点；首行「新对话」（协调宿主不可用时禁用并提示）；
 * 归档当前对话后回到 /assistant；链接的 textContent 只等于标题；「已归档」折叠组按 kind=assistant 拉取。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../../api/client";
import { indexStore } from "../../state/stores";
import { AssistantPane } from "./AssistantPane";

function th(id: string, p: Partial<Thread> = {}): Thread {
	return {
		id,
		title: id,
		titleSource: "auto",
		state: "idle",
		stateChangedAt: 1,
		pinned: false,
		labels: [],
		unread: false,
		createdAt: 1,
		updatedAt: 1,
		kind: "assistant",
		...p,
	};
}

function Where() {
	const loc = useLocation();
	return <span data-testid="where">{loc.pathname}</span>;
}

let available = true;
const requests: { method: string; url: URL; body?: unknown }[] = [];

function mount(path = "/assistant/A-old") {
	const qc = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={[path]}>
				<Routes>
					<Route
						path="*"
						element={
							<>
								<AssistantPane />
								<Where />
							</>
						}
					/>
				</Routes>
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const where = () => screen.getByTestId("where").textContent;
const pane = () => screen.getByTestId("sidebar-assistant");
const newButton = () => screen.getByRole("button", { name: "新对话" });

beforeEach(() => {
	available = true;
	requests.length = 0;
	indexStore.getState().setAll(
		[
			th("A-old", { title: "旧的对话", lastMessageAt: 10 }),
			th("A-new", {
				title: "🧭 NAV: 新的对话",
				lastMessageAt: 30,
				unread: true,
			}),
			th("A-mid", { title: "", lastMessageAt: 20 }),
			th("A-gone", { title: "归档过的", archivedAt: 5 }),
			th("W-1", { title: "工作线程", kind: "work", lastMessageAt: 40 }),
		],
		1,
	);
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			const url = new URL(req.url, "http://x");
			const body = req.method === "GET" ? undefined : await req.text();
			requests.push({
				method: req.method,
				url,
				body: body ? JSON.parse(body) : undefined,
			});
			if (url.pathname === "/api/v1/coordinator")
				return Response.json(
					available
						? { available: true }
						: { available: false, reason: "缺 omp" },
				);
			if (url.pathname === "/api/v1/assistant/conversations")
				return Response.json(
					{ thread: th("A-created", { title: "", lastMessageAt: 50 }) },
					{ status: 201 },
				);
			const patch = /^\/api\/v1\/threads\/([^/]+)$/.exec(url.pathname);
			if (patch?.[1] && req.method === "PATCH")
				return Response.json(th(patch[1], { archivedAt: 99 }));
			if (url.pathname === "/api/v1/threads")
				return Response.json({
					threads: [
						th("A-gone", { title: "归档过的", archivedAt: 5 }),
						th("W-arch", {
							title: "归档的工作线程",
							kind: "work",
							archivedAt: 6,
						}),
					],
				});
			return Response.json({});
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	indexStore.getState().setAll([], 0);
	window.innerWidth = 1024;
});

const convLinks = () =>
	within(pane())
		.getAllByRole("link")
		.filter((a) => a.getAttribute("href")?.startsWith("/assistant/"));

describe("AssistantPane", () => {
	it("只列未归档的助手对话、最近活动在前；链接文字只等于标题；未读的带未读点", () => {
		mount();
		const links = convLinks();
		expect(links.map((a) => a.getAttribute("href"))).toEqual([
			"/assistant/A-new",
			"/assistant/A-mid",
			"/assistant/A-old",
		]);
		expect(links.map((a) => a.textContent)).toEqual([
			"新的对话",
			"新对话",
			"旧的对话",
		]);
		expect(within(links[0] as HTMLElement).getByRole("img", { name: "未读" }));
		expect(within(links[2] as HTMLElement).queryByRole("img")).toBeNull();
		// 当前对话是选中行
		expect(links[2]).toHaveAttribute("aria-current", "page");
	});

	it("没有分区标题行：第一行就是「新对话」", () => {
		mount();
		expect(pane().firstElementChild).toBe(newButton());
	});

	it("新建：创建后跳到新对话、列表多一行", async () => {
		mount();
		await waitFor(() => expect(newButton()).toBeEnabled());
		fireEvent.click(newButton());
		await waitFor(() => expect(where()).toBe("/assistant/A-created"));
		expect(convLinks()[0]).toHaveAttribute("href", "/assistant/A-created");
		expect(
			requests.some(
				(r) =>
					r.method === "POST" &&
					r.url.pathname === "/api/v1/assistant/conversations",
			),
		).toBe(true);
	});

	it("协调宿主不可用：「新对话」禁用并出现警告", async () => {
		available = false;
		mount();
		await waitFor(() => expect(newButton()).toBeDisabled());
		expect(screen.getByText("全局助手暂不可用")).toBeVisible();
		expect(screen.getByText("缺 omp")).toBeVisible();
	});

	it("归档当前对话后回到 /assistant；归档别的对话不跳转", async () => {
		mount("/assistant/A-old");
		fireEvent.click(screen.getByRole("button", { name: "归档「新的对话」" }));
		await waitFor(() =>
			expect(convLinks().map((a) => a.getAttribute("href"))).not.toContain(
				"/assistant/A-new",
			),
		);
		expect(where()).toBe("/assistant/A-old");
		fireEvent.click(screen.getByRole("button", { name: "归档「旧的对话」" }));
		await waitFor(() => expect(where()).toBe("/assistant"));
		expect(
			requests
				.filter((r) => r.method === "PATCH")
				.map((r) => [r.url.pathname, r.body]),
		).toEqual([
			["/api/v1/threads/A-new", { archived: true }],
			["/api/v1/threads/A-old", { archived: true }],
		]);
	});

	it("空列表提示", () => {
		indexStore.getState().setAll([], 1);
		mount("/assistant");
		expect(within(pane()).getByText("还没有对话")).toBeVisible();
	});

	it("「已归档」展开才拉取，只按 kind=assistant 取、只列助手对话，行上没有归档按钮", async () => {
		mount();
		const fold = within(pane()).getByRole("button", { name: /已归档/ });
		expect(requests.some((r) => r.url.pathname === "/api/v1/threads")).toBe(
			false,
		);
		fireEvent.click(fold);
		const gone = await within(pane()).findByRole("link", { name: "归档过的" });
		expect(gone).toHaveAttribute("href", "/assistant/A-gone");
		const list = requests.find((r) => r.url.pathname === "/api/v1/threads");
		expect(list?.url.searchParams.get("archived")).toBe("true");
		expect(list?.url.searchParams.get("kind")).toBe("assistant");
		expect(within(pane()).queryByText("归档的工作线程")).toBeNull();
		expect(
			within(pane()).queryByRole("button", { name: "归档「归档过的」" }),
		).toBeNull();
	});
});
