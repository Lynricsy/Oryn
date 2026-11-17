/**
 * 全局助手页（M9）：`/assistant` 跳到最近的对话、没有就新建；页头动作菜单（新对话 / 归档此对话，M9.6 起切换对话只在侧栏；
 * M17 起助手记忆可用时多一个「不进助手记忆」切换）；
 * 页头小头像跟随助手的生效头像（圆形）；协调宿主不可用时不创建对话、显示原因与指向设置 → 关于，已有对话只读。
 * `ThreadScreen` 换成桩。数据走 mock 后端，身份接口由本文件的桩应答。
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
import {
	MemoryRouter,
	type NavigateFunction,
	Route,
	Routes,
	useLocation,
	useNavigate,
} from "react-router";
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

let go: NavigateFunction = () => undefined;
/** 非空时，下一个 PATCH 先等它放行（手动 resolve）。 */
let holdPatch: Promise<void> | null = null;

function Where() {
	go = useNavigate();
	return <span data-testid="where">{useLocation().pathname}</span>;
}

/** 助手与 agent 身份（由桩应答，不依赖 mock 后端的身份实现）。 */
let identities: Record<"agent" | "assistant", Record<string, string>>;

beforeEach(() => {
	localStorage.removeItem("oryn.mock.coordinator");
	server = new MockServer();
	identities = { agent: {}, assistant: {} };
	holdPatch = null;
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			const who = /\/api\/v1\/settings\/(agent|assistant)-identity$/.exec(
				new URL(req.url, "http://x").pathname,
			)?.[1] as "agent" | "assistant" | undefined;
			if (who) return Response.json(identities[who]);
			if (req.method === "PATCH" && holdPatch) {
				const gate = holdPatch;
				holdPatch = null;
				await gate;
			}
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

	it("页头没有「对话」列表；动作菜单是新对话、归档此对话与「不进助手记忆」切换", async () => {
		mount(`/assistant/${M9_IDS.assistant}`);
		await screen.findByTestId("screen");
		expect(screen.queryByRole("button", { name: "对话" })).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "对话操作" }));
		expect(screen.getAllByRole("menuitem").map((m) => m.textContent)).toEqual([
			"新对话",
			"归档此对话",
		]);
		// 助手记忆可用时才出现，是唯一的勾选项（不是对话列表）
		expect(
			await screen.findByRole("menuitemradio", { name: "不进助手记忆" }),
		).toHaveAttribute("aria-checked", "false");
		expect(screen.getAllByRole("menuitemradio")).toHaveLength(1);
		fireEvent.click(screen.getByRole("menuitem", { name: "新对话" }));
		await waitFor(() =>
			expect(screen.getByTestId("where").textContent).toMatch(
				/^\/assistant\/T-mock-coord-/,
			),
		);
		const fresh = screen.getByTestId("where").textContent?.split("/").pop();
		fireEvent.click(screen.getByRole("button", { name: "对话操作" }));
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

	it("「不进助手记忆」切换本对话的排除", async () => {
		mount(`/assistant/${M9_IDS.assistant}`);
		await screen.findByTestId("screen");
		fireEvent.click(screen.getByRole("button", { name: "对话操作" }));
		fireEvent.click(
			await screen.findByRole("menuitemradio", { name: "不进助手记忆" }),
		);
		await waitFor(async () => {
			const res = await server.handleFetch(
				new Request("http://x/api/v1/owner-memory/exclusions"),
			);
			const body = (await res?.json()) as {
				items: { kind: string; targetId: string }[];
			};
			expect(body.items).toContainEqual(
				expect.objectContaining({ kind: "thread", targetId: M9_IDS.assistant }),
			);
		});
		fireEvent.click(screen.getByRole("button", { name: "对话操作" }));
		await waitFor(() =>
			expect(
				screen.getByRole("menuitemradio", { name: "不进助手记忆" }),
			).toHaveAttribute("aria-checked", "true"),
		);
		fireEvent.click(
			screen.getByRole("menuitemradio", { name: "不进助手记忆" }),
		);
		fireEvent.click(screen.getByRole("button", { name: "对话操作" }));
		await waitFor(() =>
			expect(
				screen.getByRole("menuitemradio", { name: "不进助手记忆" }),
			).toHaveAttribute("aria-checked", "false"),
		);
	});

	it("页头归档未完成时已切到别的对话：完成后留在新选的对话上", async () => {
		let release = () => {};
		holdPatch = new Promise<void>((r) => {
			release = r;
		});
		mount(`/assistant/${M9_IDS.assistant}`);
		await screen.findByTestId("screen");
		fireEvent.click(screen.getByRole("button", { name: "对话操作" }));
		fireEvent.click(screen.getByRole("menuitem", { name: "归档此对话" }));
		act(() => {
			go("/assistant/T-other");
		});
		expect(screen.getByTestId("where")).toHaveTextContent("/assistant/T-other");
		await act(async () => release());
		await waitFor(() =>
			expect(server.threads.get(M9_IDS.assistant)?.archivedAt).toBeDefined(),
		);
		await act(async () => {});
		expect(screen.getByTestId("where")).toHaveTextContent("/assistant/T-other");
	});

	it("页头小头像：未设置时是 sparkle 图标；设置了助手头像后是圆形头像", async () => {
		const view = mount(`/assistant/${M9_IDS.assistant}`);
		const heading = await screen.findByRole("heading", {
			name: "今天有哪些线程在等我",
		});
		const header = heading.parentElement as HTMLElement;
		await waitFor(() => expect(header.querySelector("svg")).not.toBeNull());
		expect(header.querySelector("[data-avatar-of]")).toBeNull();
		view.unmount();

		identities.assistant = { avatarIcon: "🦉" };
		mount(`/assistant/${M9_IDS.assistant}`);
		const h2 = await screen.findByRole("heading", {
			name: "今天有哪些线程在等我",
		});
		await waitFor(() =>
			expect(
				h2.parentElement?.querySelector('[data-avatar-of="assistant"]'),
			).not.toBeNull(),
		);
		const avatar = h2.parentElement?.querySelector(
			'[data-avatar-of="assistant"]',
		);
		expect(avatar).toHaveAttribute("data-avatar-shape", "circle");
		expect(avatar).toHaveClass("rounded-full");
		expect(avatar).toHaveTextContent("🦉");
	});

	it("落地页标题用助手自己的名字；只设了 agent 名字时不跟随", async () => {
		localStorage.setItem("oryn.mock.coordinator", "down");
		withoutConversations();
		identities.agent = { name: "Ling" };
		const view = mount("/assistant");
		expect(
			await screen.findByRole("heading", { level: 1, name: "全局助手" }),
		).toBeInTheDocument();
		view.unmount();
		identities.assistant = { name: "小助" };
		mount("/assistant");
		expect(
			await screen.findByRole("heading", { level: 1, name: "小助" }),
		).toBeInTheDocument();
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
		fireEvent.click(screen.getByRole("button", { name: "对话操作" }));
		expect(screen.getByRole("menuitem", { name: "新对话" })).toBeDisabled();
	});

	it("页头「文件库」打开文件库面板；面板里跳到别的对话后自动收起", async () => {
		mount(`/assistant/${M9_IDS.assistant}`);
		await screen.findByTestId("screen");
		fireEvent.click(screen.getByRole("button", { name: "文件库" }));
		const dialog = await screen.findByRole("dialog", { name: "文件库" });
		expect(
			await within(dialog).findByRole("listitem", {
				name: "周报-11月第2周.md",
			}),
		).toBeVisible();
		act(() => go("/assistant/T-elsewhere"));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});
});
