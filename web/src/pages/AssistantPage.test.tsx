/**
 * 全局助手页（M9）：`/assistant` 跳到最近的对话、没有就新建；对话菜单切换 / 新对话 / 归档；
 * 协调宿主不可用时不创建对话、显示原因与指向设置 → 关于，已有对话只读。`ThreadScreen` 换成桩。数据走 mock 后端。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { threads as fixtureThreads, M9_IDS } from "../mock/fixtures";
import { MockServer } from "../mock/server";
import { indexStore } from "../state/stores";
import { AssistantPage } from "./AssistantPage";
import type * as ThreadPageModule from "./ThreadPage";

vi.mock("./ThreadPage", async (orig) => ({
	...(await orig<typeof ThreadPageModule>()),
	ThreadScreen: ({
		threadId,
		variant,
		header,
		disabled,
		dockNotice,
	}: {
		threadId: string;
		variant: string;
		header?: (l: ThreadPageModule.ScreenHeaderLayout) => ReactNode;
		disabled?: boolean;
		dockNotice?: ReactNode;
	}) => (
		<div
			data-testid="screen"
			data-thread={threadId}
			data-variant={variant}
			data-disabled={String(Boolean(disabled))}
		>
			{header?.({
				sidebarToggle: false,
				veil: 64,
				chromeLeft: false,
				chromeRight: false,
			})}
			{dockNotice}
		</div>
	),
}));

let server: MockServer;

function Where() {
	return <span data-testid="where">{useLocation().pathname}</span>;
}

beforeEach(() => {
	localStorage.removeItem("oryn.mock.coordinator");
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
	localStorage.removeItem("oryn.mock.coordinator");
	indexStore.getState().setAll([], 0);
});

function mount(path: string) {
	const qc = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={[path]}>
				<Routes>
					<Route path="/assistant" element={<AssistantPage />} />
					<Route path="/assistant/:threadId" element={<AssistantPage />} />
				</Routes>
				<Where />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

/** 只留工作线程（没有任何助手对话）。 */
function withoutConversations() {
	indexStore.getState().setAll(
		fixtureThreads.filter((t) => !t.archivedAt && t.kind !== "assistant"),
		1,
	);
}

describe("AssistantPage", () => {
	it("/assistant 跳到最近一个未归档的助手对话", async () => {
		mount("/assistant");
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent(
				`/assistant/${M9_IDS.assistant}`,
			),
		);
		const s = screen.getByTestId("screen");
		expect(s).toHaveAttribute("data-variant", "coordinator");
		expect(s).toHaveAttribute("data-disabled", "false");
		expect(
			within(s).getByRole("heading", { name: "今天有哪些线程在等我" }),
		).toBeVisible();
	});

	it("没有对话时新建一个并进入", async () => {
		withoutConversations();
		mount("/assistant");
		await waitFor(() =>
			expect(screen.getByTestId("where").textContent).toMatch(
				/^\/assistant\/T-mock-coord-/,
			),
		);
		const created = Object.values(indexStore.getState().threads).find(
			(t) => t.kind === "assistant",
		);
		expect(created?.orbId).toBe(M9_IDS.coordOrb);
	});

	it("对话菜单：列出对话、新对话、归档此对话", async () => {
		mount(`/assistant/${M9_IDS.assistant}`);
		fireEvent.click(await screen.findByRole("button", { name: "对话" }));
		expect(
			screen.getByRole("menuitemradio", { name: /今天有哪些线程在等我/ }),
		).toHaveAttribute("aria-checked", "true");
		fireEvent.click(screen.getByRole("menuitem", { name: "新对话" }));
		await waitFor(() =>
			expect(screen.getByTestId("where").textContent).toMatch(
				/^\/assistant\/T-mock-coord-/,
			),
		);
		const fresh = screen.getByTestId("where").textContent?.split("/").pop();
		fireEvent.click(screen.getByRole("button", { name: "对话" }));
		fireEvent.click(screen.getByRole("menuitem", { name: "归档此对话" }));
		await waitFor(() =>
			expect(server.threads.get(fresh ?? "")?.archivedAt).toBeDefined(),
		);
		// 归档后回到 /assistant，再落到剩下的那个对话
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent(
				`/assistant/${M9_IDS.assistant}`,
			),
		);
	});

	it("协调宿主不可用：不创建对话，显示原因与设置 → 关于的链接", async () => {
		localStorage.setItem("oryn.mock.coordinator", "down");
		withoutConversations();
		mount("/assistant");
		const landing = await screen.findByTestId("assistant-landing");
		// 加载中的 Spinner 同样是 role=status：按标题找到提示块
		const callout = (
			await within(landing).findByText("全局助手暂不可用")
		).closest("[role=status]") as HTMLElement;
		expect(callout).toHaveTextContent("全局助手暂不可用");
		expect(callout).toHaveTextContent("does not match core/source.json");
		expect(within(callout).getByRole("link")).toHaveAttribute(
			"href",
			"/settings/about",
		);
		expect(screen.getByTestId("where")).toHaveTextContent(/^\/assistant$/);
		expect(
			Object.values(indexStore.getState().threads).some(
				(t) => t.kind === "assistant",
			),
		).toBe(false);
	});

	it("协调宿主不可用：已有对话只读浏览，输入区禁用并提示", async () => {
		localStorage.setItem("oryn.mock.coordinator", "down");
		mount(`/assistant/${M9_IDS.assistant}`);
		const s = screen.getByTestId("screen");
		await waitFor(() => expect(s).toHaveAttribute("data-disabled", "true"));
		expect(within(s).getByRole("status")).toHaveTextContent("全局助手暂不可用");
		fireEvent.click(screen.getByRole("button", { name: "对话" }));
		expect(screen.getByRole("menuitem", { name: "新对话" })).toBeDisabled();
	});
});
