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
import {
	afterEach,
	beforeAll,
	beforeEach,
	describe,
	expect,
	it,
	vi,
} from "vitest";
import { identityKeys } from "../api/identity-queries";
import { qk } from "../api/queries";
import i18n from "../i18n";
import {
	type DesktopHost,
	host,
	type MobileHost,
	type MobileInfo,
} from "../lib/host";
import { pairUri } from "../lib/pair-qr";
import { type ProcessedWallpaper, processWallpaper } from "../lib/wallpaper";
import { installTheme, THEME_KEY, themeStore } from "../state/theme";
import { uiStore } from "../state/ui";
import { answerConfirm } from "../test/confirm";
import { SettingsPage } from "./SettingsPage";
import {
	SETTINGS_SECTIONS,
	type SettingsSection,
	visibleEntries,
} from "./settings/registry";

// 壁纸处理要真解码图片，jsdom 做不到：换成测试里手动兑现的 promise，其余（存取图片）用真实现
vi.mock("../lib/wallpaper", async (importOriginal) => ({
	...(await importOriginal<object>()),
	processWallpaper: vi.fn(),
}));

// 编辑器换成 textarea：这里测设置页接线，CodeMirror 本身另有测试
vi.mock("../components/editor/CodeEditor", () => ({
	default: ({
		value,
		path,
		onChange,
	}: {
		value: string;
		path: string;
		onChange?(v: string): void;
	}) => (
		<textarea
			aria-label={`editor:${path}`}
			value={value}
			onChange={(e) => onChange?.(e.target.value)}
		/>
	),
}));

interface Call {
	method: string;
	path: string;
	body: unknown;
	csrf: string | null;
}

let identity: { name: string; email: string } | undefined;
let language: "zh-CN" | "en" = "zh-CN";
let calls: Call[] = [];
let agentFiles: Record<string, string> = {};
let agentPuts: { path: string; body: string }[] = [];
let lastClient: QueryClient;

async function agentConfig(req: Request, url: URL): Promise<Response> {
	if (url.pathname === "/api/v1/agent-config/files")
		return Response.json({
			files: Object.entries(agentFiles).map(([path, c]) => ({
				path,
				size: c.length,
				mode: 420,
				sha256: "0".repeat(64),
				updatedAt: 1,
			})),
		});
	const path = decodeURIComponent(
		url.pathname.slice("/api/v1/agent-config/files/".length),
	);
	if (req.method === "PUT") {
		const body = await req.text();
		agentPuts.push({ path, body });
		agentFiles[path] = body;
		return Response.json({
			file: {
				path,
				size: body.length,
				mode: 420,
				sha256: "0".repeat(64),
				updatedAt: 2,
			},
			extractedSecrets: ["omp/models/acme/apiKey"],
			warnings: [
				{ path, line: 3, kind: "possible_secret", message: "looks like a key" },
			],
		});
	}
	const c = agentFiles[path];
	return c === undefined
		? Response.json({ code: "not_found", message: path }, { status: 404 })
		: new Response(c);
}

const fetchMock = vi.fn(
	async (input: RequestInfo | URL, init?: RequestInit) => {
		// rawFetch 传相对路径与 init；Node 的 Request 只收绝对地址
		const req =
			input instanceof Request
				? input
				: new Request(new URL(String(input), "http://x"), init);
		const url = new URL(req.url, "http://x");
		if (url.pathname.startsWith("/api/v1/agent-config/"))
			return agentConfig(req, url);
		const text = await req.text();
		calls.push({
			method: req.method,
			path: url.pathname,
			body: text ? JSON.parse(text) : undefined,
			csrf: req.headers.get("X-Oryn-CSRF"),
		});
		if (url.pathname === "/api/v1/settings/git-identity") {
			if (req.method === "PUT") {
				identity = JSON.parse(text);
			} else if (req.method === "DELETE") {
				identity = undefined;
				return new Response(null, { status: 204 });
			}
			return Response.json(identity ? { identity } : {});
		}
		if (url.pathname === "/api/v1/settings/language") {
			if (req.method === "PUT") {
				const parsed = JSON.parse(text) as { language: "zh-CN" | "en" };
				language = parsed.language;
			}
			return Response.json({ language });
		}
		// 身份与头像：各卡只读不写（保存语义见 IdentitySettings.test）
		if (
			/^\/api\/v1\/settings\/(agent|assistant|user)-identity$/.test(
				url.pathname,
			)
		)
			return Response.json({});
		if (url.pathname === "/api/v1/settings/role-identities")
			return Response.json({
				roles: [{ role: "default", inCycle: true }],
			});
		if (url.pathname === "/api/v1/browser-profiles")
			return Response.json({
				profiles: [
					{
						id: "bpf_A",
						name: "工作账号",
						revision: 1,
						sizeBytes: 1,
						createdAt: 1,
						updatedAt: 1,
					},
				],
			});
		return Response.json({ code: "not_found", message: "" }, { status: 404 });
	},
);

function setup(section: SettingsSection = "git") {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	lastClient = client;
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<SettingsPage section={section} />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

function Where() {
	return <span data-testid="where">{useLocation().pathname}</span>;
}

const writes = () => calls.filter((c) => c.method !== "GET");

/** 手动兑现的 promise（tsconfig 的 lib 没有 `Promise.withResolvers`）。 */
function deferred<T>() {
	let resolve: (v: T) => void = () => undefined;
	const promise = new Promise<T>((r) => {
		resolve = r;
	});
	return { promise, resolve };
}

describe("设置 → Git 身份", () => {
	beforeEach(() => {
		identity = undefined;
		calls = [];
		vi.stubGlobal("fetch", fetchMock);
		fetchMock.mockClear();
	});
	afterEach(() => {
		vi.unstubAllGlobals();
		vi.restoreAllMocks();
	});

	it("未设置时填写并保存：以 PUT 发送去掉首尾空白的名字与邮箱", async () => {
		setup();
		expect(await screen.findByText(/尚未设置/)).toBeInTheDocument();
		const save = screen.getByRole("button", { name: "保存" });
		expect(save).toBeDisabled();
		fireEvent.change(screen.getByLabelText("名字"), {
			target: { value: "  Ada Lovelace " },
		});
		fireEvent.change(screen.getByLabelText("邮箱"), {
			target: { value: "ada@example.com" },
		});
		fireEvent.click(save);
		expect(
			await screen.findByText("当前：Ada Lovelace <ada@example.com>"),
		).toBeInTheDocument();
		expect(writes()).toEqual([
			{
				method: "PUT",
				path: "/api/v1/settings/git-identity",
				body: { name: "Ada Lovelace", email: "ada@example.com" },
				csrf: "1",
			},
		]);
		expect(screen.getByText("已保存")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
	});

	it("已设置时清除：确认后以 DELETE 清除并清空输入框", async () => {
		identity = { name: "Grace", email: "grace@example.com" };
		setup();
		expect(
			await screen.findByText("当前：Grace <grace@example.com>"),
		).toBeInTheDocument();
		expect(screen.getByLabelText("名字")).toHaveValue("Grace");
		fireEvent.click(screen.getByRole("button", { name: "清除" }));
		expect((await answerConfirm(true)).title).toBe("清除全局 Git 身份？");
		expect(await screen.findByText(/尚未设置/)).toBeInTheDocument();
		expect(writes()).toEqual([
			{
				method: "DELETE",
				path: "/api/v1/settings/git-identity",
				body: undefined,
				csrf: "1",
			},
		]);
		expect(screen.getByLabelText("名字")).toHaveValue("");
		expect(screen.getByLabelText("邮箱")).toHaveValue("");
		expect(screen.queryByRole("button", { name: "清除" })).toBeNull();
	});

	it("取消确认时不发送 DELETE", async () => {
		identity = { name: "Grace", email: "grace@example.com" };
		setup();
		fireEvent.click(await screen.findByRole("button", { name: "清除" }));
		await answerConfirm(false);
		expect(writes()).toEqual([]);
	});
});

describe("设置 → agent 配置", () => {
	beforeEach(() => {
		localStorage.clear();
		agentFiles = {
			"AGENTS.md": "# rules\n",
			"models.yml": "providers: {}\n",
			"skills/foo/SKILL.md": "skill",
		};
		agentPuts = [];
		calls = [];
		vi.stubGlobal("fetch", fetchMock);
	});
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("用同一个文件浏览器：目录由扁平列表合成，打开、编辑、保存后显示抽取的密钥与警告", async () => {
		setup("agent-config");
		expect(await screen.findByText("skills")).toBeInTheDocument();
		expect(screen.queryByText("SKILL.md")).toBeNull();
		expect(
			screen.queryByRole("button", {
				name: i18n.t("files.actions.newFolder" as "common.save"),
			}),
		).toBeNull();
		fireEvent.click(screen.getByText("models.yml"));
		const editor = await screen.findByLabelText("editor:models.yml");
		expect(editor).toHaveValue("providers: {}\n");
		fireEvent.change(editor, {
			target: { value: "providers:\n  acme: {apiKey: sk-1}\n" },
		});
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		expect(
			await screen.findByText("已抽取进密钥库：omp/models/acme/apiKey"),
		).toBeInTheDocument();
		expect(
			screen.getByText("models.yml:3 — looks like a key"),
		).toBeInTheDocument();
		expect(agentPuts).toEqual([
			{ path: "models.yml", body: "providers:\n  acme: {apiKey: sk-1}\n" },
		]);
		// 旧的 textarea 编辑器已删除
		expect(screen.queryByRole("textbox", { name: "models.yml" })).toBeNull();
	});

	it("保存 config.yml 后角色头像列表失效（role 来自它）；保存别的文件不失效", async () => {
		agentFiles["config.yml"] = "cycleOrder: [a]\n";
		setup("agent-config");
		lastClient.setQueryData(identityKeys.roles, { roles: [] });
		const stale = () =>
			lastClient.getQueryState(identityKeys.roles)?.isInvalidated;
		fireEvent.click(await screen.findByText("models.yml"));
		fireEvent.change(await screen.findByLabelText("editor:models.yml"), {
			target: { value: "providers: {a: {}}\n" },
		});
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(agentPuts).toHaveLength(1));
		await act(async () => {});
		expect(stale()).toBe(false);
		fireEvent.click(screen.getByText("config.yml"));
		fireEvent.change(await screen.findByLabelText("editor:config.yml"), {
			target: { value: "cycleOrder: [a, b]\n" },
		});
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(agentPuts).toHaveLength(2));
		await waitFor(() => expect(stale()).toBe(true));
	});
});

describe("设置页外框与导航", () => {
	beforeEach(() => {
		calls = [];
		vi.stubGlobal("fetch", fetchMock);
	});
	afterEach(() => {
		vi.unstubAllGlobals();
		window.innerWidth = 1024;
	});

	/** 与 App 的路由一样：`/settings` 是首页，`/settings/<id>` 是各分区。 */
	function mountAt(path: string) {
		const client = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		return render(
			<QueryClientProvider client={client}>
				<MemoryRouter initialEntries={[path]}>
					<Routes>
						<Route path="/settings" element={<SettingsPage />} />
						{SETTINGS_SECTIONS.map((s) => (
							<Route
								key={s}
								path={`/settings/${s}`}
								element={<SettingsPage section={s} />}
							/>
						))}
					</Routes>
					<Where />
				</MemoryRouter>
			</QueryClientProvider>,
		);
	}

	const where = () => screen.getByTestId("where").textContent;
	const entries = () =>
		Array.from(
			screen
				.getByRole("navigation", { name: "设置" })
				.querySelectorAll<HTMLElement>("[data-settings-entry]"),
		).map((a) => a.dataset.settingsEntry);

	it("宽屏：/settings 落到「账户与设备」，左侧是四组导航，当前分区高亮", () => {
		mountAt("/settings");
		expect(where()).toBe("/settings/account");
		const nav = screen.getByRole("navigation", { name: "设置" });
		expect(
			within(nav)
				.getAllByRole("heading", { level: 2 })
				.map((h) => h.textContent),
		).toEqual(["个人", "助手与 Agent", "工作区", "其它"]);
		// 浏览器里没有「桌面应用」
		expect(entries()).toEqual(visibleEntries(false).map((e) => e.id));
		expect(entries()).not.toContain("desktop");
		expect(
			within(nav).getByRole("link", { name: "账户与设备" }),
		).toHaveAttribute("aria-current", "page");
		expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("设置");
		expect(screen.queryByRole("button", { name: "返回设置" })).toBeNull();
	});

	it("宽屏：点导航条目切换分区", () => {
		mountAt("/settings/account");
		fireEvent.click(screen.getByRole("link", { name: "通知与预算" }));
		expect(where()).toBe("/settings/notifications");
		expect(screen.getByRole("link", { name: "通知与预算" })).toHaveAttribute(
			"aria-current",
			"page",
		);
		expect(screen.getByRole("heading", { name: "渠道" })).toBeInTheDocument();
	});

	it("搜索按分区名与关键词匹配：「预算」只剩「通知与预算」；无匹配时提示；Esc 清空", () => {
		mountAt("/settings/account");
		const search = screen.getByRole("searchbox", { name: "搜索设置" });
		fireEvent.change(search, { target: { value: "预算" } });
		expect(entries()).toEqual(["notifications"]);
		fireEvent.change(search, { target: { value: "BUDGET" } });
		expect(entries()).toEqual(["notifications"]);
		fireEvent.change(search, { target: { value: "zzz-没有这个" } });
		expect(entries()).toEqual([]);
		expect(screen.getByText("没有匹配的设置")).toBeInTheDocument();
		fireEvent.keyDown(search, { key: "Escape" });
		expect(search).toHaveValue("");
		expect(entries()).toEqual(visibleEntries(false).map((e) => e.id));
	});

	it("手机：/settings 是分组首页；点进分区后页头是「返回设置」，点它回到首页", () => {
		window.innerWidth = 390;
		mountAt("/settings");
		expect(where()).toBe("/settings");
		expect(entries()).toEqual(visibleEntries(false).map((e) => e.id));
		for (const g of ["个人", "助手与 Agent", "工作区", "其它"])
			expect(screen.getByText(g)).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "返回设置" })).toBeNull();
		fireEvent.click(screen.getByRole("link", { name: "通知与预算" }));
		expect(where()).toBe("/settings/notifications");
		// 分区页只有分区内容，没有导航
		expect(screen.queryByRole("navigation", { name: "设置" })).toBeNull();
		expect(screen.getByText("通知与预算")).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "返回设置" }));
		expect(where()).toBe("/settings");
		expect(
			screen.getByRole("searchbox", { name: "搜索设置" }),
		).toBeInTheDocument();
	});

	it("身份卡分到各自的分区：账户有我的头像，输入有新对话默认对象，助手有全局助手，模型与角色有默认 Agent 身份", () => {
		const firstCard = (section: SettingsSection) => {
			const view = setup(section);
			const title = screen.getAllByRole("heading", { level: 3 })[0]
				?.textContent;
			view.unmount();
			return title;
		};
		expect(firstCard("account")).toBe("我的头像");
		expect(firstCard("input")).toBe("新对话默认对象");
		expect(firstCard("assistant")).toBe("全局助手");
		expect(firstCard("agent-models")).toBe("默认 Agent 身份");
	});
});

describe("设置 → 外观", () => {
	beforeAll(() => installTheme());
	beforeEach(() => {
		localStorage.clear();
		themeStore.setState({
			mode: "system",
			accent: "teal",
			wallpaper: "aurora",
			tone: undefined,
		});
		calls = [];
		vi.stubGlobal("fetch", fetchMock);
	});
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("切到深色 + 狐橙：写到根元素并存进 localStorage", async () => {
		setup("appearance");
		const mode = screen.getByRole("radiogroup", { name: "明暗" });
		const dark = within(mode).getByRole("radio", { name: "深色" });
		expect(dark).toHaveAttribute("aria-checked", "false");
		fireEvent.click(dark);
		expect(dark).toHaveAttribute("aria-checked", "true");
		fireEvent.click(screen.getByRole("radio", { name: "狐橙" }));
		expect(screen.getByRole("radio", { name: "狐橙" })).toHaveAttribute(
			"aria-checked",
			"true",
		);
		expect({ ...document.documentElement.dataset }).toEqual({
			theme: "dark",
			accent: "fox",
			wallpaper: "aurora",
		});
		expect(JSON.parse(localStorage.getItem(THEME_KEY) ?? "null")).toEqual({
			mode: "dark",
			accent: "fox",
			wallpaper: "aurora",
		});
	});

	it("壁纸：选 Tahoe 波写到根元素并存进同一个 JSON；没有图片时自定义只是上传入口", () => {
		setup("appearance");
		const group = screen.getByRole("radiogroup", { name: "壁纸" });
		expect(
			within(group)
				.getAllByRole("radio")
				.map((r) => r.getAttribute("aria-label")),
		).toEqual(["极光", "Tahoe 波", "纸纹"]);
		expect(
			within(group).getByRole("button", { name: "自定义：上传图片" }),
		).toBeInTheDocument();
		fireEvent.click(within(group).getByRole("radio", { name: "Tahoe 波" }));
		expect(document.documentElement.dataset.wallpaper).toBe("waves");
		expect(JSON.parse(localStorage.getItem(THEME_KEY) ?? "null")).toEqual({
			mode: "system",
			accent: "teal",
			wallpaper: "waves",
		});
		// 没有图片时上传只有「自定义」磁贴一个入口，不再另有上传 / 移除按钮
		expect(screen.queryByRole("button", { name: "移除图片" })).toBeNull();
		expect(screen.queryByRole("button", { name: "上传图片" })).toBeNull();
	});

	it("方向键在主题色之间移动并选中", () => {
		setup("appearance");
		const teal = screen.getByRole("radio", { name: "青碧" });
		teal.focus();
		fireEvent.keyDown(teal, { key: "ArrowRight" });
		const cobalt = screen.getByRole("radio", { name: "钴蓝" });
		expect(cobalt).toHaveFocus();
		expect(cobalt).toHaveAttribute("aria-checked", "true");
		expect(document.documentElement.dataset.accent).toBe("cobalt");
	});

	it("外观分三组：主题、语言、布局，没有头像卡", () => {
		setup("appearance");
		expect(
			screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent),
		).toEqual(["主题", "语言", "布局"]);
		expect(
			screen.getByRole("radiogroup", { name: "全局语言" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("group", { name: i18n.t("appearance.avatarGlyphs") }),
		).toBeNull();
	});

	it("重置界面布局：侧栏、面板宽度、卡片折叠、Desktop 缩放立刻回到默认，主题与壁纸不动", () => {
		window.innerWidth = 1280;
		const ui = uiStore.getState();
		ui.setSidebarCollapsed(true);
		ui.setPanelWidth(700);
		ui.toggleCard("usage");
		ui.setDesktopScale("fit");
		themeStore.getState().setMode("dark");
		setup("appearance");
		fireEvent.click(screen.getByRole("button", { name: "重置" }));
		expect(screen.getByRole("status")).toHaveTextContent("已恢复默认");
		const after = uiStore.getState();
		expect(after.sidebarCollapsed).toBe(false);
		expect(after.panelWidth).toBe(420);
		expect(after.collapsedCards).toEqual({});
		expect(after.desktopScale).toBeNull();
		expect(localStorage.getItem("oryn.sidebar.collapsed")).toBeNull();
		expect(localStorage.getItem("oryn.panel.width")).toBeNull();
		expect(themeStore.getState().mode).toBe("dark");
		expect(themeStore.getState().wallpaper).toBe("aurora");
	});

	const IMAGE = "data:image/png;base64,AAAA";
	const pick = (container: HTMLElement) => {
		const input = container.querySelector<HTMLInputElement>(
			'input[type="file"][accept="image/*"]',
		);
		if (!input) throw new Error("没有壁纸上传入口");
		fireEvent.change(input, {
			target: { files: [new File(["x"], "bg.png", { type: "image/png" })] },
		});
	};

	it("壁纸上传途中改选了内置壁纸：上传结果不再覆盖这次选择", async () => {
		const done = deferred<ProcessedWallpaper>();
		vi.mocked(processWallpaper).mockReturnValueOnce(done.promise);
		const { container } = setup("appearance");
		pick(container);
		fireEvent.click(screen.getByRole("radio", { name: "Tahoe 波" }));
		await act(async () => {
			done.resolve({ url: IMAGE, tone: [0.2, 0.8], width: 1, height: 1 });
			await done.promise;
		});
		expect(themeStore.getState().wallpaper).toBe("waves");
		expect(document.documentElement.dataset.wallpaper).toBe("waves");
	});

	it("上传途中离开设置页、期间没有别的选择：上传完成后仍换成自定义壁纸", async () => {
		const done = deferred<ProcessedWallpaper>();
		vi.mocked(processWallpaper).mockReturnValueOnce(done.promise);
		const view = setup("appearance");
		pick(view.container);
		view.unmount();
		await act(async () => {
			done.resolve({ url: IMAGE, tone: [0.2, 0.8], width: 1, height: 1 });
			await done.promise;
		});
		expect(themeStore.getState().wallpaper).toBe("custom");
		expect(document.documentElement.dataset.wallpaper).toBe("custom");
	});

	it("全局语言卡片切换语言后经 PUT /api/v1/settings/language 持久化并同步 i18n", async () => {
		language = "zh-CN";
		calls = [];
		vi.stubGlobal("fetch", fetchMock);
		fetchMock.mockClear();
		try {
			setup("appearance");
			const group = await screen.findByRole("radiogroup", { name: "全局语言" });
			fireEvent.click(within(group).getByRole("radio", { name: "English" }));
			await waitFor(() => {
				expect(calls).toContainEqual({
					method: "PUT",
					path: "/api/v1/settings/language",
					body: { language: "en" },
					csrf: "1",
				});
			});
			expect(i18n.language).toBe("en");
			expect(document.documentElement.lang).toBe("en");
		} finally {
			await act(() => i18n.changeLanguage("zh-CN"));
			vi.unstubAllGlobals();
		}
	});
});

describe("设置 → 通知 / 公开分享（M5）", () => {
	beforeEach(() => {
		calls = [];
		vi.stubGlobal("fetch", fetchMock);
		fetchMock.mockClear();
	});
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("通知页渲染三张卡片", async () => {
		setup("notifications");
		expect(screen.getByRole("heading", { name: "此设备" })).toBeInTheDocument();
		expect(screen.getByRole("heading", { name: "渠道" })).toBeInTheDocument();
		expect(
			screen.getByRole("heading", { name: "最近通知" }),
		).toBeInTheDocument();
	});
});

describe("设置 → 桌面壳（M6）/ 移动壳（M7）", () => {
	const h = host as {
		desktop: DesktopHost | null;
		mobile: MobileHost | null;
	};
	const fakeDesktop: DesktopHost = {
		info: vi.fn(),
		setSettings: vi.fn(),
		checkUpdate: vi.fn(),
		installUpdate: vi.fn(),
		changeServer: vi.fn(),
	};
	const mobileInfo: MobileInfo = {
		version: "0.2.0",
		platform: "android",
		tokenStore: "keystore",
		webview: "141.0.7390.0",
		push: { state: "disabled" },
	};
	function fakeMobile(): MobileHost {
		return {
			info: vi.fn(async () => mobileInfo),
			enablePush: vi.fn(async () => mobileInfo),
			disablePush: vi.fn(async () => mobileInfo),
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
	}
	beforeEach(() => {
		calls = [];
		vi.stubGlobal("fetch", fetchMock);
		fetchMock.mockClear();
	});
	afterEach(() => {
		h.desktop = null;
		h.mobile = null;
		vi.unstubAllGlobals();
	});

	function mount(
		section: SettingsSection,
		auth: { kind: "web" | "device" | "recovery"; deviceId?: string } = {
			kind: "web",
		},
	) {
		const client = new QueryClient({
			defaultOptions: { queries: { retry: false, staleTime: Infinity } },
		});
		client.setQueryData(qk.me, { displayName: "Ada", createdAt: 1, auth });
		return render(
			<QueryClientProvider client={client}>
				<MemoryRouter initialEntries={[`/settings/${section}`]}>
					<Routes>
						<Route path="/settings" element={<p>SETTINGS HOME</p>} />
						<Route
							path={`/settings/${section}`}
							element={<SettingsPage section={section} />}
						/>
					</Routes>
				</MemoryRouter>
			</QueryClientProvider>,
		);
	}

	it("「桌面应用」分区在浏览器里访问时回到设置首页", () => {
		mount("desktop");
		expect(screen.getByText("SETTINGS HOME")).toBeInTheDocument();
	});

	it("桌面壳：账户页「更换服务器…」交给桌面壳，没有「退出登录」；导航里有「桌面应用」", () => {
		h.desktop = fakeDesktop;
		mount("account");
		expect(screen.queryByRole("button", { name: "退出登录" })).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "更换服务器…" }));
		expect(fakeDesktop.changeServer).toHaveBeenCalledTimes(1);
		expect(screen.getByRole("link", { name: "桌面应用" })).toHaveAttribute(
			"href",
			"/settings/desktop",
		);
	});

	it("浏览器：账户页有「退出登录」（POST /auth/logout），没有「更换服务器…」；导航里没有「桌面应用」", async () => {
		mount("account");
		expect(screen.queryByRole("button", { name: "更换服务器…" })).toBeNull();
		expect(screen.queryByRole("link", { name: "桌面应用" })).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "退出登录" }));
		await waitFor(() =>
			expect(calls.map((c) => `${c.method} ${c.path}`)).toContain(
				"POST /api/v1/auth/logout",
			),
		);
	});

	it("关于页只放信息：没有语言、退出登录与更换服务器", () => {
		mount("about");
		expect(screen.getByText("Ada")).toBeInTheDocument();
		expect(screen.queryByRole("radiogroup", { name: "全局语言" })).toBeNull();
		expect(screen.queryByRole("combobox")).toBeNull();
		expect(screen.queryByRole("button", { name: "退出登录" })).toBeNull();
		expect(screen.queryByRole("button", { name: "更换服务器…" })).toBeNull();
	});

	it("App 会话（设备换来的网页会话）：批准配对卡改为引导到浏览器", () => {
		mount("account", { kind: "web", deviceId: "dev_1" });
		const link = screen.getByRole("link", { name: "在浏览器中打开" });
		expect(link).toHaveAttribute("href", "/pair");
		expect(link).toHaveAttribute("target", "_blank");
		expect(link).toHaveAttribute("rel", "noopener noreferrer");
		expect(screen.queryByLabelText("配对码")).toBeNull();
	});

	it("普通网页会话：批准配对卡照常显示配对码输入", () => {
		mount("account");
		expect(screen.getByLabelText("配对码")).toBeInTheDocument();
		expect(screen.queryByRole("link", { name: "在浏览器中打开" })).toBeNull();
	});

	it("账户与设备页：二维码内容是 oryn://pair?server=<编码后的 origin>，下方写服务器地址", () => {
		mount("account");
		const qr = screen.getByTestId("pair-qr");
		expect(qr).toHaveAttribute(
			"data-qr-payload",
			`oryn://pair?server=${encodeURIComponent(window.location.origin)}`,
		);
		expect(qr.getAttribute("data-qr-payload")).toBe(
			pairUri(window.location.origin),
		);
		expect(qr).toHaveAttribute("role", "img");
		expect(qr.querySelectorAll("rect").length).toBeGreaterThan(0);
		expect(screen.getByText(window.location.origin)).toBeInTheDocument();
		expect(screen.getByText("用手机扫码连接")).toBeInTheDocument();
	});

	it("移动壳：账户与设备页没有二维码卡，desktop 路由回设置首页", () => {
		h.mobile = fakeMobile();
		const view = mount("account");
		expect(screen.queryByTestId("pair-qr")).toBeNull();
		view.unmount();
		mount("desktop");
		expect(screen.getByText("SETTINGS HOME")).toBeInTheDocument();
	});

	it("移动壳：关于页列 App 版本、WebView、令牌存储；账户页「更换服务器…」交给移动壳，没有「退出登录」", async () => {
		const mobile = fakeMobile();
		h.mobile = mobile;
		const view = mount("about");
		expect(await screen.findByText("0.2.0")).toBeInTheDocument();
		expect(screen.getByText("141.0.7390.0")).toBeInTheDocument();
		expect(screen.getByText("Android Keystore")).toBeInTheDocument();
		view.unmount();
		mount("account");
		expect(screen.queryByRole("button", { name: "退出登录" })).toBeNull();
		fireEvent.click(screen.getByTestId("change-server"));
		expect(mobile.changeServer).toHaveBeenCalledTimes(1);
	});
});
