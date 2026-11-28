/** 应用壳：桌面端侧栏收起 / 展开（按钮、页头开关、Ctrl+B）与持久化；桌面壳里的握手、跳转、重新鉴权与托盘状态（M6）；移动壳的系统栏、分享与连接状态（M7）。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { type ReactNode, useEffect } from "react";
import {
	BrowserRouter,
	MemoryRouter,
	Navigate,
	Route,
	Routes,
	useLocation,
} from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../../api/client";
import { qk, useMe } from "../../api/queries";
import {
	createHost,
	host,
	type MobileHost,
	type NativeRailTap,
	type ShareIntake,
} from "../../lib/host";
import { applyRail } from "../../lib/rail";
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
		onGitHubChanged: vi.fn(() => () => undefined),
		onSshChanged: vi.fn(() => () => undefined),
		onProjectRepoChanged: vi.fn(() => () => undefined),
		onBoxesChanged: vi.fn(() => () => undefined),
		onSandboxesChanged: vi.fn(() => () => undefined),
		onArtifactsChanged: vi.fn(() => () => undefined),
		onArtifactConsent: vi.fn(() => () => undefined),
		onArtifactWatchesChanged: vi.fn(() => () => undefined),
	},
}));

function setup(
	page: ReactNode = <ThreadHeader threadId="T-1" sidebarToggle={false} />,
) {
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
						<Route path="/t/:id" element={page} />
						{/* 切分区会导航到别的页面：壳仍要留在原地 */}
						<Route path="*" element={null} />
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
			// AI 数据共享已同意：同意框不出现
			if (url.pathname === "/api/v1/settings/ai-data-consent")
				return Response.json({
					recipients: [],
					consentedAt: null,
					consentedRecipientIds: [],
					required: false,
				});
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
		// 页面里的任何输入框：Ctrl+B 在里面是光标操作，不切侧栏
		const field = document.body.appendChild(document.createElement("input"));
		fireEvent.keyDown(field, { key: "b", ctrlKey: true });
		expect(column()).not.toHaveAttribute("inert");
		field.remove();
	});

	it("收起状态在重新挂载后保持", () => {
		const first = setup();
		fireEvent.click(screen.getByRole("button", { name: "收起侧边栏" }));
		first.unmount();
		setup();
		expect(column()).toHaveAttribute("inert");
		expect(headerToggle()).not.toBeNull();
	});

	it("1280 宽：侧栏底部是底栏（内嵌底色），没有旧的「已连接 + 图标」底部行", () => {
		setup();
		const bar = screen.getByTestId("section-tabbar");
		expect(column()).toContainElement(bar);
		expect(bar.dataset.tone).toBe("inset");
		expect(screen.queryByTestId("sidebar-footer")).toBeNull();
		// 底栏是侧栏 nav 的最后一个子元素
		expect(column().querySelector("nav")?.lastElementChild).toBe(bar);
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
			syncAssistantAvatar: vi.fn(),
			changeServer: vi.fn(),
			pushTransport: false,
			sink: null,
			tabBar: null,
			header: null,
			rail: null,
			appIcon: null,
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

	it("390 宽：底栏（玻璃胶囊）在下沉侧栏层里、不在卡片里；点别的分区侧栏保持打开，再点已选中的收起", () => {
		window.innerWidth = 390;
		setup();
		const bar = screen.getByTestId("section-tabbar");
		expect(screen.getByTestId("sink-sidebar")).toContainElement(bar);
		expect(screen.getByTestId("sink-card")).not.toContainElement(bar);
		expect(bar.dataset.tone).toBe("glass");
		expect(screen.queryByTestId("sidebar-footer")).toBeNull();
		act(() => uiStore.getState().setSidebar(true));
		const more = bar.querySelector<HTMLElement>('[data-tab="more"]');
		fireEvent.click(more as HTMLElement);
		expect(uiStore.getState().sidebarOpen).toBe(true);
		expect(more).toHaveAttribute("aria-current", "true");
		fireEvent.click(more as HTMLElement);
		expect(uiStore.getState().sidebarOpen).toBe(false);
	});

	it("iPhone Duo 竖栏：开关侧栏、页头操作与分区底栏都在竖栏里，不在卡片与侧栏层；页面不重挂载，竖栏撤掉后各回原位", () => {
		window.innerWidth = 466;
		pageMounts = 0;
		setup(
			<>
				<ThreadHeader threadId="T-1" sidebarToggle={false} />
				<MountProbe />
			</>,
		);
		const root = document.documentElement;
		const more = () =>
			screen.getByRole("button", { name: /^更多(操作|动作)$/ });
		act(() =>
			applyRail(root, {
				side: "right",
				frame: { x: 402, y: 54, width: 64, height: 624 },
				safeArea: { top: 0, right: 64, bottom: 21, left: 0 },
			}),
		);
		expect(root.dataset.rail).toBe("right");
		const rail = screen.getByTestId("rail");
		const bar = screen.getByTestId("section-tabbar");
		expect(bar.dataset.tone).toBe("rail");
		expect(rail).toContainElement(bar);
		expect(screen.getByTestId("rail-actions")).toContainElement(more());
		expect(screen.getByTestId("sink-sidebar")).not.toContainElement(rail);
		expect(screen.getByTestId("sink-card")).not.toContainElement(rail);
		fireEvent.click(screen.getByTestId("rail-sidebar"));
		expect(uiStore.getState().sidebarOpen).toBe(true);
		act(() => uiStore.getState().setSidebar(false));

		act(() => applyRail(root, null));
		expect(root.dataset.rail).toBeUndefined();
		expect(root.style.getPropertyValue("--rail-safe-right")).toBe("");
		expect(screen.queryByTestId("rail")).toBeNull();
		expect(screen.getByTestId("sink-card")).toContainElement(more());
		const back = screen.getByTestId("section-tabbar");
		expect(back.dataset.tone).toBe("glass");
		expect(screen.getByTestId("sink-sidebar")).toContainElement(back);
		expect(pageMounts).toBe(1);
	});

	it("iPhone Duo 竖栏：侧栏收着时点分区 = 切到该分区并展开侧栏露出它的列表，再点当前分区收起", () => {
		window.innerWidth = 466;
		setup(<ThreadHeader threadId="T-1" sidebarToggle={false} />);
		act(() =>
			applyRail(document.documentElement, {
				side: "right",
				frame: { x: 402, y: 54, width: 64, height: 624 },
				safeArea: { top: 0, right: 64, bottom: 21, left: 0 },
			}),
		);
		const projects = () =>
			screen
				.getByTestId("rail")
				.querySelector<HTMLElement>('[data-tab="projects"]') as HTMLElement;
		expect(uiStore.getState().sidebarOpen).toBe(false);
		fireEvent.click(projects());
		expect(uiStore.getState().sidebarOpen).toBe(true);
		expect(projects()).toHaveAttribute("aria-current", "true");
		fireEvent.click(projects());
		expect(uiStore.getState().sidebarOpen).toBe(false);
		act(() => applyRail(document.documentElement, null));
	});

	it("iPhone Duo 原生竖栏的点按：开关、页头操作、菜单项、分区走与网页竖栏同一套回调；换页前的旧 key 丢弃", () => {
		window.innerWidth = 466;
		let tap: ((e: NativeRailTap) => void) | undefined;
		const spy = vi.spyOn(host, "onRailTap").mockImplementation((cb) => {
			tap = cb;
			return () => {};
		});
		setup(<ThreadHeader threadId="T-1" sidebarToggle={false} />);
		act(() =>
			applyRail(document.documentElement, {
				side: "right",
				frame: { x: 402, y: 54, width: 64, height: 624 },
				safeArea: { top: 0, right: 64, bottom: 21, left: 0 },
			}),
		);
		const key = "/t/T-1";
		act(() => tap?.({ part: "lead", key: "/t/T-0" }));
		expect(uiStore.getState().sidebarOpen).toBe(false);
		act(() => tap?.({ part: "lead", key }));
		expect(uiStore.getState().sidebarOpen).toBe(true);
		act(() => uiStore.getState().setSidebar(false));
		act(() => tap?.({ part: "action", key, id: "tree" }));
		expect(uiStore.getState().dialog).toEqual({ kind: "tree" });
		act(() => tap?.({ part: "item", key, id: "more", item: "rename" }));
		expect(uiStore.getState().dialog).toEqual({ kind: "rename" });
		act(() => uiStore.getState().close());
		act(() => tap?.({ part: "tab", key, id: "projects" }));
		expect(uiStore.getState().sidebarOpen).toBe(true);
		expect(
			screen.getByTestId("rail").querySelector('[data-tab="projects"]'),
		).toHaveAttribute("aria-current", "true");
		act(() => applyRail(document.documentElement, null));
		spy.mockRestore();
	});

	it("iPhone Duo 竖栏（真实历史）：首次点「助手」经 replace 重定向后，收起侧栏再按返回回到原线程，侧栏保持收起", async () => {
		window.innerWidth = 466;
		window.history.replaceState(null, "", "/t/T-1");
		const qc = new QueryClient({
			defaultOptions: { queries: { retry: false, staleTime: Infinity } },
		});
		qc.setQueryData(qk.me, {
			displayName: "Ada",
			createdAt: 1,
			auth: { kind: "web" },
		});
		render(
			<QueryClientProvider client={qc}>
				<BrowserRouter>
					<Where />
					<Routes>
						<Route element={<AppShell />}>
							<Route path="/t/:id" element={null} />
							{/* 与全局助手页一样：没有记住的对话时 replace 到具体对话 */}
							<Route
								path="/assistant"
								element={<Navigate replace to="/assistant/A-1" />}
							/>
							<Route path="*" element={null} />
						</Route>
					</Routes>
				</BrowserRouter>
			</QueryClientProvider>,
		);
		act(() =>
			applyRail(document.documentElement, {
				side: "right",
				frame: { x: 402, y: 54, width: 64, height: 624 },
				safeArea: { top: 0, right: 64, bottom: 21, left: 0 },
			}),
		);
		const where = () => screen.getByTestId("where").textContent;
		const assistant = () =>
			screen
				.getByTestId("rail")
				.querySelector<HTMLElement>('[data-tab="assistant"]') as HTMLElement;
		fireEvent.click(assistant());
		await waitFor(() => expect(where()).toBe("/assistant/A-1"));
		expect(uiStore.getState().sidebarOpen).toBe(true);
		// 再点当前分区收起侧栏，然后按返回
		fireEvent.click(assistant());
		expect(uiStore.getState().sidebarOpen).toBe(false);
		act(() => window.history.back());
		await waitFor(() => expect(where()).toBe("/t/T-1"));
		expect(uiStore.getState().sidebarOpen).toBe(false);
		act(() => applyRail(document.documentElement, null));
	});
});

/** 跨断点往返用的路由页：记挂载次数；不受控输入框的内容与焦点只活在 DOM 节点上，页面一重挂载就丢。 */
let pageMounts = 0;
function MountProbe(): ReactNode {
	useEffect(() => {
		pageMounts++;
	}, []);
	return <input aria-label="草稿" />;
}

describe("跨 compact 断点（折叠屏折叠 / 展开）", () => {
	afterEach(() => {
		uiStore.setState({ sidebarOpen: false });
		window.history.replaceState(null, "");
	});

	it("390 → 700 → 390 往返：路由页不重挂载，输入内容与焦点都留着", () => {
		pageMounts = 0;
		const resize = (width: number) =>
			act(() => {
				window.innerWidth = width;
				window.dispatchEvent(new Event("resize"));
			});
		window.innerWidth = 390;
		setup(<MountProbe />);
		const input = screen.getByRole<HTMLInputElement>("textbox", {
			name: "草稿",
		});
		expect(screen.getByTestId("sink-content")).toContainElement(input);
		input.value = "没发出去的半句话";
		input.focus();

		// 展开：内屏进入 medium，下沉侧栏换成列侧栏
		resize(700);
		expect(screen.queryByTestId("sink-content")).toBeNull();
		expect(column()).not.toBeNull();
		expect(screen.getByRole("textbox", { name: "草稿" })).toBe(input);
		expect(input.value).toBe("没发出去的半句话");
		expect(document.activeElement).toBe(input);

		// 合上：回到外屏的下沉侧栏
		resize(390);
		expect(screen.getByTestId("sink-content")).toContainElement(input);
		expect(input.value).toBe("没发出去的半句话");
		expect(document.activeElement).toBe(input);
		expect(pageMounts).toBe(1);
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
