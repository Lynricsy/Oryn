/** 应用壳：桌面端侧栏收起 / 展开（按钮、页头开关、Ctrl+B）与持久化；桌面壳里的握手、跳转、重新鉴权与托盘状态（M6）；移动壳的系统栏、分享与连接状态（M7）。 */
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
import { qk, useMe } from "../../api/queries";
import {
	createHost,
	host,
	type MobileHost,
	type ShareIntake,
} from "../../lib/host";
import { realtime } from "../../realtime/socket";
import { shareIntakeStore } from "../../state/share-intake";
import { connectionStore, indexStore } from "../../state/stores";
import { SIDEBAR_COLLAPSED_KEY, uiStore } from "../../state/ui";
import { ThreadHeader } from "../thread/ThreadHeader";
import { AppShell } from "./AppShell";

vi.mock("../../realtime/socket", () => ({
	realtime: {
		connect: vi.fn(),
		close: vi.fn(),
		send: vi.fn(),
		onSchedulesChanged: vi.fn(() => () => undefined),
	},
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
	// me 查询状态：TanStack 的通知经 setTimeout 派发，测试等它变化来确认壳已随查询结果重新渲染
	const me = useMe();
	return (
		<p data-testid="where" data-me={me.status}>
			{loc.pathname}
		</p>
	);
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
		vi.restoreAllMocks();
	});

	/**
	 * `me` 为 null：预置身份、不拦 `/api/v1/me`；否则由 `me()` 决定每次 `/api/v1/me` 的应答（抛错 = fetch 拒绝），
	 * `cached` 时另外预置身份（模拟后台重取）。
	 */
	function mount(
		me: (() => Response | Promise<Response>) | null,
		{ cached = false }: { cached?: boolean } = {},
	) {
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
					if (url.pathname === "/api/v1/me") return me();
					return base(input);
				}),
			);
		}
		const qc = new QueryClient({
			defaultOptions: { queries: { retry: false, staleTime: Infinity } },
		});
		if (!me || cached)
			qc.setQueryData(qk.me, {
				displayName: "Ada",
				createdAt: 1,
				auth: { kind: "web", deviceId: "dev_1" },
			});
		const view = render(
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
		return { ...view, qc };
	}

	/** 壳里的真实 reauth（含每页一次的去重），invoke 记录发给壳的命令。 */
	function shellReauth() {
		const invoke = vi.fn(async () => undefined);
		const doc = Object.assign(new EventTarget(), {
			visibilityState: "visible",
		});
		const shell = createHost(
			Object.assign(new EventTarget(), {
				document: doc,
				__ORYN_HOST__: { kind: "tauri", platform: "linux", bridge: 1 },
				__TAURI__: { core: { invoke } },
			}) as unknown as Window,
		);
		const reauth = vi
			.spyOn(host, "reauth")
			.mockImplementation((p) => shell.reauth(p));
		const sent = () =>
			invoke.mock.calls.filter((c: unknown[]) => c[0] === "host_reauth");
		return { reauth, sent };
	}

	const unauthorized = () =>
		Response.json(
			{ code: "unauthorized", message: "no session" },
			{ status: 401 },
		);

	it("me 401：交给 host.reauth（带当前路径），不去 /login；多个入口报上来也只向壳发一次", async () => {
		const { reauth, sent } = shellReauth();
		vi.spyOn(host, "ready").mockResolvedValue(null);
		mount(unauthorized);
		await waitFor(() => expect(sent()).toHaveLength(1));
		expect(reauth).toHaveBeenCalledWith("/t/T-1?tab=files");
		expect(sent()).toEqual([["host_reauth", { path: "/t/T-1?tab=files" }]]);
		expect(screen.queryByText("LOGIN")).toBeNull();
		expect(screen.getByRole("status")).toBeInTheDocument();
	});

	it("me 500：不 reauth，显示服务器出错与重试；重试成功后进入界面", async () => {
		const reauth = vi.spyOn(host, "reauth").mockImplementation(() => {});
		vi.spyOn(host, "ready").mockResolvedValue(null);
		let reply: () => Response = () =>
			Response.json({ code: "internal", message: "boom" }, { status: 500 });
		mount(() => reply());
		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("读取登录信息失败：服务器出错");
		expect(reauth).not.toHaveBeenCalled();
		expect(screen.queryByText("LOGIN")).toBeNull();
		reply = () =>
			Response.json({
				displayName: "Ada",
				createdAt: 1,
				auth: { kind: "web", deviceId: "dev_1" },
			});
		fireEvent.click(screen.getByRole("button", { name: "重试" }));
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent("/t/T-1"),
		);
		expect(reauth).not.toHaveBeenCalled();
	});

	it("me 请求被拒（连不上服务器）：不 reauth，提示连不上", async () => {
		const reauth = vi.spyOn(host, "reauth").mockImplementation(() => {});
		vi.spyOn(host, "ready").mockResolvedValue(null);
		mount(() => {
			throw new TypeError("Failed to fetch");
		});
		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("连不上 Oryn 服务器");
		expect(reauth).not.toHaveBeenCalled();
	});

	it("已有身份时后台重取 502：不 reauth，保留当前界面；之后重取 401 才 reauth", async () => {
		const { reauth, sent } = shellReauth();
		vi.spyOn(host, "ready").mockResolvedValue(null);
		let reply: () => Response = () =>
			Response.json({ code: "bad_gateway", message: "" }, { status: 502 });
		const { qc } = mount(() => reply(), { cached: true });
		expect(screen.getByTestId("where")).toHaveTextContent("/t/T-1");
		await act(async () => {
			await qc.refetchQueries({ queryKey: qk.me });
		});
		await waitFor(() =>
			expect(screen.getByTestId("where").dataset.me).toBe("error"),
		);
		expect(screen.getByTestId("where")).toHaveTextContent("/t/T-1");
		expect(reauth).not.toHaveBeenCalled();
		reply = unauthorized;
		await act(async () => {
			await qc.refetchQueries({ queryKey: qk.me });
		});
		await waitFor(() => expect(sent()).toHaveLength(1));
	});

	it("浏览器里（非壳）行为不变：me 500 仍回登录页", async () => {
		h.kind = "web";
		const reauth = vi.spyOn(host, "reauth").mockImplementation(() => {});
		mount(() =>
			Response.json({ code: "internal", message: "boom" }, { status: 500 }),
		);
		await screen.findByText("LOGIN");
		expect(reauth).not.toHaveBeenCalled();
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

describe("移动壳里（M7）", () => {
	const h = host as { kind: "web" | "tauri"; mobile: MobileHost | null };
	let mobile: MobileHost;
	let onShare: ((s: ShareIntake) => void) | undefined;
	beforeEach(() => {
		h.kind = "tauri";
		mobile = {
			info: vi.fn(),
			enablePush: vi.fn(),
			disablePush: vi.fn(),
			openExternal: vi.fn(),
			shareAck: vi.fn(),
			setChrome: vi.fn(),
			openNotificationSettings: vi.fn(),
			changeServer: vi.fn(),
		};
		h.mobile = mobile;
		vi.spyOn(host, "ready").mockResolvedValue(null);
		vi.spyOn(host, "onShare").mockImplementation((cb) => {
			onShare = cb;
			return () => {
				onShare = undefined;
			};
		});
		shareIntakeStore.setState({ pending: null });
		document.body.style.backgroundColor = "rgb(238, 240, 244)";
	});
	afterEach(() => {
		h.kind = "web";
		h.mobile = null;
		document.body.style.backgroundColor = "";
		delete document.documentElement.dataset.conn;
		vi.restoreAllMocks();
	});

	function mount() {
		const qc = new QueryClient({
			defaultOptions: { queries: { retry: false, staleTime: Infinity } },
		});
		qc.setQueryData(qk.me, {
			displayName: "Ada",
			createdAt: 1,
			auth: { kind: "web", deviceId: "dev_1" },
		});
		return render(
			<QueryClientProvider client={qc}>
				<MemoryRouter initialEntries={["/t/T-1"]}>
					<Routes>
						<Route element={<AppShell />}>
							<Route path="*" element={<Where />} />
						</Route>
					</Routes>
				</MemoryRouter>
			</QueryClientProvider>,
		);
	}

	it("系统栏跟随页面底色", () => {
		mount();
		expect(mobile.setChrome).toHaveBeenCalledWith({
			dark: false,
			color: "#eef0f4",
		});
	});

	it("分享进来：存入 share-intake、跳到首页、shareAck", async () => {
		mount();
		const share: ShareIntake = {
			id: "s1",
			text: "m7 share",
			attachments: [],
			failed: [],
		};
		act(() => onShare?.(share));
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent(/^\/$/),
		);
		expect(shareIntakeStore.getState().pending).toEqual(share);
		expect(mobile.shareAck).toHaveBeenCalledWith("s1");
	});

	it("连接状态写到 <html data-conn>；侧栏玻璃带屏幕同心圆角类", () => {
		// 不用 open：连上会去拉通知列表（本测试的假 fetch 不应答它）
		connectionStore.setState({ status: "reconnecting" });
		mount();
		expect(document.documentElement.dataset.conn).toBe("reconnecting");
		act(() => connectionStore.setState({ status: "idle" }));
		expect(document.documentElement.dataset.conn).toBe("idle");
		const glass = document.querySelector(".glass-sidebar");
		expect(glass?.className).toContain("dc-tl");
		expect(glass?.className).toContain("dc-bl");
	});
});

describe("手机下沉式侧栏（compact）", () => {
	afterEach(() => {
		uiStore.setState({ sidebarOpen: false });
		window.history.replaceState(null, "");
	});

	it("compact 下侧栏在底层（无玻璃外框）、内容是卡片；页头开关打开后卡片 inert", () => {
		window.innerWidth = 390;
		setup();
		expect(document.querySelector(".glass-sidebar")).toBeNull();
		expect(screen.getByTestId("sink-sidebar")).toHaveAttribute("inert");
		expect(
			screen.getByTestId("sink-sidebar").querySelector("nav"),
		).not.toBeNull();
		act(() => uiStore.getState().toggleSidebar());
		expect(screen.getByTestId("sink-card")).toHaveAttribute("data-open");
		expect(screen.getByTestId("sink-content")).toHaveAttribute("inert");
	});
});

describe("定时查询对账（M9 审查 W6）", () => {
	it("应用壳订阅一次 realtime.onSchedulesChanged，卸载时取消", () => {
		const off = vi.fn();
		const on = vi.mocked(realtime.onSchedulesChanged);
		on.mockClear();
		on.mockReturnValueOnce(off);
		const { unmount } = setup();
		expect(on).toHaveBeenCalledTimes(1);
		expect(off).not.toHaveBeenCalled();
		unmount();
		expect(off).toHaveBeenCalledTimes(1);
	});
});
