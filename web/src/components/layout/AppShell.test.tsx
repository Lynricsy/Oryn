/** 应用壳：桌面端侧栏收起 / 展开（按钮、页头开关、Ctrl+B）与持久化；桌面壳里的握手、跳转、重新鉴权与托盘状态（M6）。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../../api/client";
import { qk } from "../../api/queries";
import { host } from "../../lib/host";
import { indexStore } from "../../state/stores";
import { SIDEBAR_COLLAPSED_KEY, uiStore } from "../../state/ui";
import { ThreadHeader } from "../thread/ThreadHeader";
import { AppShell } from "./AppShell";

vi.mock("../../realtime/socket", () => ({
	realtime: { connect: vi.fn(), close: vi.fn(), send: vi.fn() },
}));

function setup() {
	const qc = new QueryClient({
		defaultOptions: { queries: { retry: false, staleTime: Infinity } },
	});
	qc.setQueryData(qk.me, {
		displayName: "Ada",
		createdAt: 1,
		auth: { kind: "web" },
	});
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={["/t/T-1"]}>
				<Routes>
					<Route element={<AppShell />}>
						<Route
							path="/t/:id"
							element={<ThreadHeader threadId="T-1" sidebarToggle={false} />}
						/>
					</Route>
				</Routes>
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const column = () =>
	document.querySelector<HTMLElement>("[data-sidebar]") as HTMLElement;
const headerToggle = () => screen.queryByRole("button", { name: "侧边栏" });

beforeEach(() => {
	window.innerWidth = 1280;
	localStorage.clear();
	uiStore.setState({ sidebarCollapsed: false, sidebarOpen: false });
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL) => {
			const url = new URL(
				input instanceof Request ? input.url : String(input),
				"http://x",
			);
			if (url.pathname === "/api/v1/projects")
				return Response.json({ projects: [] });
			if (url.pathname === "/api/v1/threads")
				return Response.json({ threads: [] });
			return Response.json({});
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	window.innerWidth = 1024;
});

describe("侧栏收起（expanded）", () => {
	it("点收起按钮：侧栏列变 inert，页头出现展开开关；点开关再展开", async () => {
		setup();
		expect(column()).not.toHaveAttribute("inert");
		expect(headerToggle()).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "收起侧边栏" }));
		expect(column()).toHaveAttribute("inert");
		expect(column().dataset.sidebar).toBe("collapsed");
		expect(localStorage.getItem(SIDEBAR_COLLAPSED_KEY)).toBe("1");
		const toggle = await waitFor(() => {
			const b = headerToggle();
			expect(b).not.toBeNull();
			return b as HTMLElement;
		});
		fireEvent.click(toggle);
		expect(column()).not.toHaveAttribute("inert");
		expect(headerToggle()).toBeNull();
		expect(localStorage.getItem(SIDEBAR_COLLAPSED_KEY)).toBe("0");
	});

	it("Ctrl+B 切换；焦点在输入框里时不抢", () => {
		setup();
		fireEvent.keyDown(window, { key: "b", ctrlKey: true });
		expect(column()).toHaveAttribute("inert");
		fireEvent.keyDown(window, { key: "B", metaKey: true });
		expect(column()).not.toHaveAttribute("inert");
		const search = screen.getByRole("textbox", { name: "搜索线程" });
		fireEvent.keyDown(search, { key: "b", ctrlKey: true });
		expect(column()).not.toHaveAttribute("inert");
	});

	it("收起状态在重新挂载后保持", () => {
		const first = setup();
		fireEvent.click(screen.getByRole("button", { name: "收起侧边栏" }));
		first.unmount();
		setup();
		expect(column()).toHaveAttribute("inert");
		expect(headerToggle()).not.toBeNull();
	});
});

function Where() {
	const loc = useLocation();
	return <p data-testid="where">{loc.pathname}</p>;
}

describe("桌面壳里（M6）", () => {
	const h = host as { kind: "web" | "tauri" };
	beforeEach(() => {
		h.kind = "tauri";
		indexStore.setState({ threads: {} });
	});
	afterEach(() => {
		h.kind = "web";
		vi.useRealTimers();
	});

	/** 不预置 me，由测试决定 `/api/v1/me` 的应答。 */
	function mount(me: Response | null) {
		if (me) {
			const base = fetch as unknown as (
				input: RequestInfo | URL,
			) => Promise<Response>;
			vi.stubGlobal(
				"fetch",
				vi.fn(async (input: RequestInfo | URL) => {
					const url = new URL(
						input instanceof Request ? input.url : String(input),
						"http://x",
					);
					if (url.pathname === "/api/v1/me") return me.clone();
					return base(input);
				}),
			);
		}
		const qc = new QueryClient({
			defaultOptions: { queries: { retry: false, staleTime: Infinity } },
		});
		if (!me)
			qc.setQueryData(qk.me, {
				displayName: "Ada",
				createdAt: 1,
				auth: { kind: "web", deviceId: "dev_1" },
			});
		return render(
			<QueryClientProvider client={qc}>
				<MemoryRouter initialEntries={["/t/T-1?tab=files"]}>
					<Routes>
						<Route path="/login" element={<p>LOGIN</p>} />
						<Route element={<AppShell />}>
							<Route path="*" element={<Where />} />
						</Route>
					</Routes>
				</MemoryRouter>
			</QueryClientProvider>,
		);
	}

	it("me 401：交给 host.reauth（带当前路径），不去 /login", async () => {
		const reauth = vi.spyOn(host, "reauth").mockImplementation(() => {});
		vi.spyOn(host, "ready").mockResolvedValue(null);
		mount(
			Response.json(
				{ code: "unauthorized", message: "no session" },
				{ status: 401 },
			),
		);
		await waitFor(() => expect(reauth).toHaveBeenCalled());
		expect(reauth).toHaveBeenCalledWith("/t/T-1?tab=files");
		expect(screen.queryByText("LOGIN")).toBeNull();
		expect(screen.getByRole("status")).toBeInTheDocument();
	});

	it("ready 握手带上语言与当前路径；返回的待开路径与壳推来的 navigate 都走路由跳转", async () => {
		let onNav: ((path: string) => void) | undefined;
		const ready = vi.spyOn(host, "ready").mockResolvedValue("/t/T-2");
		vi.spyOn(host, "onNavigate").mockImplementation((cb) => {
			onNav = cb;
			return () => {};
		});
		mount(null);
		expect(ready).toHaveBeenCalledWith("zh-CN", "/t/T-1?tab=files");
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent("/t/T-2"),
		);
		act(() => onNav?.("/settings/desktop"));
		expect(screen.getByTestId("where")).toHaveTextContent("/settings/desktop");
	});

	it("线程索引变化后去抖 300ms 把托盘状态交给 host.setStatus（相同状态不重发）", async () => {
		vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
		vi.spyOn(host, "ready").mockResolvedValue(null);
		const setStatus = vi.spyOn(host, "setStatus").mockImplementation(() => {});
		mount(null);
		const thread: Thread = {
			id: "T-a",
			title: "",
			titleSource: "auto",
			state: "awaiting_input",
			stateChangedAt: 1,
			pinned: false,
			labels: [],
			unread: true,
			createdAt: 1,
			updatedAt: 2,
		};
		act(() => {
			indexStore.getState().setAll([thread], 1);
		});
		await act(async () => {
			await vi.advanceTimersByTimeAsync(299);
		});
		expect(setStatus).toHaveBeenCalledTimes(0);
		await act(async () => {
			await vi.advanceTimersByTimeAsync(1);
		});
		expect(setStatus).toHaveBeenLastCalledWith({
			awaiting: 1,
			unread: 0,
			items: [{ threadId: "T-a", title: "未命名线程", kind: "awaiting" }],
		});
		const calls = setStatus.mock.calls.length;
		act(() => {
			indexStore.getState().setAll([{ ...thread }], 2);
		});
		await act(async () => {
			await vi.advanceTimersByTimeAsync(300);
		});
		expect(setStatus).toHaveBeenCalledTimes(calls);
	});
});
