/**
 * 「助手」分区侧栏（M9.6）：会话按最近活动排列、未读点；首行「新对话」（协调宿主不可用时禁用并提示）；
 * 归档当前对话后回到 /assistant（按完成那一刻的路由判断）；链接的 textContent 只等于标题；「已归档」折叠组按
 * kind=assistant 拉取，归档后立即同步。
 * M19：最上面是主线的置顶行（助手的名字与头像、未读点），主线不在对话列表里；索引里还没有主线时调一次
 * `GET assistant/main` 让服务端建出来（协调宿主不可用时不调）。
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
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../../api/client";
import { installContextMenu } from "../../lib/context-menu/dispatch";
import { closeContextMenu } from "../../lib/context-menu/store";
import { indexStore } from "../../state/stores";
import { ContextMenuHost } from "../ui/ContextMenu";
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
/** 助手身份的名字（`GET settings/assistant-identity`）；空串 = 没设 */
let assistantName = "";
const requests: { method: string; url: URL; body?: unknown }[] = [];
/** 非空时，下一个 PATCH 先等它放行（手动 resolve）。 */
let holdPatch: Promise<void> | null = null;
/** 本测试里已归档的对话：`GET threads` 一并返回（服务端视角）。 */
const archivedNow: Thread[] = [];

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
	assistantName = "";
	holdPatch = null;
	archivedNow.length = 0;
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
			if (url.pathname === "/api/v1/settings/assistant-identity")
				return Response.json(assistantName ? { name: assistantName } : {});
			if (url.pathname === "/api/v1/assistant/main")
				return Response.json({
					thread: th("M-main", {
						title: "Main",
						titleSource: "user",
						main: true,
						unread: true,
						lastMessageAt: 60,
					}),
				});
			if (url.pathname === "/api/v1/assistant/conversations")
				return Response.json(
					{ thread: th("A-created", { title: "", lastMessageAt: 50 }) },
					{ status: 201 },
				);
			const patch = /^\/api\/v1\/threads\/([^/]+)$/.exec(url.pathname);
			if (patch?.[1] && req.method === "PATCH") {
				const id = patch[1];
				if (holdPatch) {
					const gate = holdPatch;
					holdPatch = null;
					await gate;
				}
				const cur = indexStore.getState().threads[id] ?? th(id);
				archivedNow.push({ ...cur, archivedAt: 99 });
				return Response.json(th(id, { archivedAt: 99 }));
			}
			if (url.pathname === "/api/v1/threads")
				return Response.json({
					threads: [
						...archivedNow,
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
		.filter(
			(a) =>
				a.getAttribute("href")?.startsWith("/assistant/") &&
				a.dataset.testid !== "assistant-main-row" &&
				a.dataset.testid !== "assistant-computer-row",
		);
const mainRow = () => screen.getByTestId("assistant-main-row");
const mainRequests = () =>
	requests.filter((r) => r.url.pathname === "/api/v1/assistant/main");

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

	it("没有分区标题行：第一行是主线，其下是「助手的电脑」，再下是「新对话」", async () => {
		mount();
		await waitFor(() => expect(pane().firstElementChild).toBe(mainRow()));
		const computerRow = screen.getByTestId("assistant-computer-row");
		expect(mainRow().nextElementSibling).toBe(computerRow);
		expect(computerRow).toHaveAttribute("href", "/assistant/computer");
		expect(computerRow).toHaveTextContent("助手的电脑");
		expect(computerRow.nextElementSibling).toBe(newButton());
	});

	it("主线：索引里没有时调一次 GET assistant/main；置顶行带助手的名字与未读点，不在对话列表里", async () => {
		assistantName = "小管家";
		mount();
		await waitFor(() => expect(mainRow()).toHaveTextContent("小管家"));
		expect(mainRequests()).toHaveLength(1);
		expect(mainRow()).toHaveAttribute("href", "/assistant/M-main");
		expect(within(mainRow()).getByRole("img", { name: "未读" })).toBeTruthy();
		expect(convLinks().map((a) => a.getAttribute("href"))).not.toContain(
			"/assistant/M-main",
		);
	});

	it("主线：没设名字时用「全局助手」；已在索引里时不请求", async () => {
		indexStore
			.getState()
			.upsertThread(
				th("M-main", { title: "Main", main: true, lastMessageAt: 60 }),
			);
		mount();
		expect(mainRow()).toHaveTextContent("全局助手");
		await waitFor(() => expect(newButton()).toBeEnabled());
		expect(mainRequests()).toHaveLength(0);
	});

	it("主线：协调宿主不可用时不建", async () => {
		available = false;
		mount();
		await waitFor(() => expect(newButton()).toBeDisabled());
		expect(mainRequests()).toHaveLength(0);
		expect(screen.queryByTestId("assistant-main-row")).toBeNull();
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

	it("归档请求未完成时已切到别的对话：完成后留在新选的对话上，不回 landing", async () => {
		let release = () => {};
		holdPatch = new Promise<void>((r) => {
			release = r;
		});
		mount("/assistant/A-old");
		fireEvent.click(screen.getByRole("button", { name: "归档「旧的对话」" }));
		// 链接的可及名带未读点（「新的对话 未读」）
		fireEvent.click(within(pane()).getByRole("link", { name: /^新的对话/ }));
		expect(where()).toBe("/assistant/A-new");
		await act(async () => release());
		await waitFor(() =>
			expect(convLinks().map((a) => a.getAttribute("href"))).not.toContain(
				"/assistant/A-old",
			),
		);
		await act(async () => {});
		expect(where()).toBe("/assistant/A-new");
	});

	it("「已归档」展开着时归档一个对话：它立即出现在组里（不折叠、不重新挂载）", async () => {
		mount("/assistant/A-old");
		fireEvent.click(within(pane()).getByRole("button", { name: /已归档/ }));
		await within(pane()).findByRole("link", { name: "归档过的" });
		fireEvent.click(screen.getByRole("button", { name: "归档「新的对话」" }));
		// 只剩一个指向 A-new 的链接，且它在已归档组里（行上没有归档按钮）
		await waitFor(() => {
			const rows = within(pane())
				.getAllByRole("link")
				.filter((a) => a.getAttribute("href") === "/assistant/A-new");
			expect(rows).toHaveLength(1);
			expect(
				within(rows[0]?.parentElement as HTMLElement).queryByRole("button"),
			).toBeNull();
		});
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

	it("右键菜单：主线只有打开 / 已读 / 复制链接；对话行另有改名、归档、删除，归档走侧栏同一路径", async () => {
		const off = installContextMenu();
		try {
			indexStore
				.getState()
				.upsertThread(
					th("M-main", { title: "Main", main: true, lastMessageAt: 60 }),
				);
			mount("/assistant/A-old");
			render(<ContextMenuHost />);
			const menuItems = () =>
				within(screen.getByTestId("context-menu"))
					.getAllByRole("menuitem")
					.map((b) => b.textContent);
			fireEvent.contextMenu(mainRow(), { clientX: 10, clientY: 10 });
			expect(menuItems()).toEqual([
				"打开",
				"在新标签页打开",
				"标为未读",
				"复制链接",
			]);
			act(() => closeContextMenu());
			const old = convLinks().find(
				(a) => a.getAttribute("href") === "/assistant/A-old",
			) as HTMLElement;
			fireEvent.contextMenu(old, { clientX: 10, clientY: 10 });
			for (const name of ["打开", "标为未读", "归档", "改名", "删除对话"])
				expect(menuItems()).toContain(name);
			fireEvent.click(screen.getByRole("menuitem", { name: "归档" }));
			// 归档当前对话：与行尾按钮一样回 landing
			await waitFor(() => expect(where()).toBe("/assistant"));
			expect(
				requests
					.filter((r) => r.method === "PATCH")
					.map((r) => [r.url.pathname, r.body]),
			).toEqual([["/api/v1/threads/A-old", { archived: true }]]);
		} finally {
			off();
			act(() => closeContextMenu());
		}
	});
});
