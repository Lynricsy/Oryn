import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import {
	afterEach,
	beforeAll,
	beforeEach,
	describe,
	expect,
	it,
	vi,
} from "vitest";
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
import type { SettingsSection } from "./SettingsPage";
import { SettingsPage } from "./SettingsPage";

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
let calls: Call[] = [];
let agentFiles: Record<string, string> = {};
let agentPuts: { path: string; body: string }[] = [];
let avatars: Record<string, { avatarIcon?: string; avatarImage?: string }> = {};
/** 非空时头像 GET 先取当时的值再等它兑现：模拟 PUT 之前发出、之后才到的旧读取。 */
let avatarGetGate: Promise<void> | null = null;
let avatarGetsServed = 0;

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

const PROJECT = {
	id: "p1",
	name: "oryn",
	repoUrl: "https://github.com/x/oryn",
	shallowClone: false,
	icon: "\u{f07d3}",
	createdAt: 1,
	updatedAt: 1,
};

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
		const avatarPath = /^\/api\/v1\/settings\/(agent|user)-identity$/.exec(
			url.pathname,
		);
		if (avatarPath) {
			const who = avatarPath[1] ?? "";
			if (req.method === "PUT") avatars[who] = JSON.parse(text);
			const snapshot = avatars[who] ?? {};
			if (req.method === "GET" && avatarGetGate) {
				await avatarGetGate;
				avatarGetsServed++;
			}
			return Response.json(snapshot);
		}
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
		if (url.pathname === "/api/v1/projects")
			return Response.json({ projects: [PROJECT] });
		if (url.pathname === `/api/v1/projects/${PROJECT.id}`)
			return Response.json({ ...PROJECT, ...JSON.parse(text) });
		return Response.json({ code: "not_found", message: "" }, { status: 404 });
	},
);

function setup(section: SettingsSection = "git-identity") {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<SettingsPage section={section} />
			</MemoryRouter>
		</QueryClientProvider>,
	);
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
		vi.spyOn(window, "confirm").mockReturnValue(true);
		setup();
		expect(
			await screen.findByText("当前：Grace <grace@example.com>"),
		).toBeInTheDocument();
		expect(screen.getByLabelText("名字")).toHaveValue("Grace");
		fireEvent.click(screen.getByRole("button", { name: "清除" }));
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
		vi.spyOn(window, "confirm").mockReturnValue(false);
		setup();
		fireEvent.click(await screen.findByRole("button", { name: "清除" }));
		await waitFor(() => expect(window.confirm).toHaveBeenCalled());
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
		avatars = {};
		avatarGetGate = null;
		avatarGetsServed = 0;
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

	// 外观里依次是 agent 头像与我的头像两张卡，交互相同、各写各的接口
	it.each([
		["agent", 0],
		["user", 1],
	] as const)(
		"%s 头像：选内置字形以 PUT 整体替换，恢复默认发送空对象",
		async (who, idx) => {
			setup("appearance");
			const grids = await screen.findAllByRole("group", {
				name: i18n.t("appearance.avatarGlyphs"),
			});
			expect(grids).toHaveLength(2);
			const grid = grids[idx] as HTMLElement;
			fireEvent.click(within(grid).getByRole("button", { name: "🦊" }));
			await waitFor(() =>
				expect(
					within(grid).getByRole("button", { name: "🦊" }),
				).toHaveAttribute("aria-pressed", "true"),
			);
			const reset = screen.getAllByRole("button", {
				name: i18n.t("appearance.avatarReset"),
			})[idx] as HTMLElement;
			await waitFor(() => expect(reset).toBeEnabled());
			fireEvent.click(reset);
			await waitFor(() => expect(reset).toBeDisabled());
			expect(writes().map((c) => [c.method, c.path, c.body])).toEqual([
				["PUT", `/api/v1/settings/${who}-identity`, { avatarIcon: "🦊" }],
				["PUT", `/api/v1/settings/${who}-identity`, {}],
			]);
		},
	);

	it("头像保存时在途的旧读取后到，也不会把头像还原成旧值", async () => {
		const gate = deferred<void>();
		avatarGetGate = gate.promise;
		setup("appearance");
		const grid = (
			await screen.findAllByRole("group", {
				name: i18n.t("appearance.avatarGlyphs"),
			})
		)[0] as HTMLElement;
		const fox = () => within(grid).getByRole("button", { name: "🦊" });
		fireEvent.click(fox());
		await waitFor(() => expect(fox()).toHaveAttribute("aria-pressed", "true"));
		// 页面打开时发出的两个 GET（读到的是保存前的 {}）这时才到
		await act(async () => {
			gate.resolve();
			await gate.promise;
		});
		await waitFor(() => expect(avatarGetsServed).toBe(2));
		await act(async () => {});
		expect(fox()).toHaveAttribute("aria-pressed", "true");
		expect(writes().map((c) => c.body)).toEqual([{ avatarIcon: "🦊" }]);
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
});

describe("设置 → 项目图标", () => {
	beforeEach(() => {
		calls = [];
		vi.stubGlobal("fetch", fetchMock);
	});
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("选字形后保存发送该字形；选「自动」发送空串清除", async () => {
		setup("projects");
		fireEvent.click(await screen.findByRole("button", { name: "编辑" }));
		const grid = screen.getByRole("group", {
			name: i18n.t("projects.icon" as "common.save"),
		});
		expect(
			within(grid).getByRole("button", { name: "language go" }),
		).toHaveAttribute("aria-pressed", "true");
		fireEvent.click(
			within(grid).getByRole("button", { name: "language rust" }),
		);
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(writes()).toHaveLength(1));
		fireEvent.click(await screen.findByRole("button", { name: "编辑" }));
		fireEvent.click(
			screen.getByRole("button", {
				name: i18n.t("projects.iconAuto" as "common.save"),
			}),
		);
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(writes()).toHaveLength(2));
		expect(writes().map((c) => c.body)).toEqual([
			expect.objectContaining({ icon: "\u{f1617}" }),
			expect.objectContaining({ icon: "" }),
		]);
	});
});

describe("设置 → 项目默认浏览器 Profile（M8）", () => {
	beforeEach(() => {
		calls = [];
		vi.stubGlobal("fetch", fetchMock);
	});
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("下拉列出各 Profile；选中后保存带它的 id，改回「无」发空串清除", async () => {
		setup("projects");
		fireEvent.click(await screen.findByRole("button", { name: "编辑" }));
		const select = screen.getByRole("combobox", {
			name: new RegExp(`^${i18n.t("projects.defaultBrowserProfile")}`),
		});
		await screen.findByRole("option", { name: "工作账号" });
		fireEvent.change(select, { target: { value: "bpf_A" } });
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(writes()).toHaveLength(1));
		fireEvent.click(await screen.findByRole("button", { name: "编辑" }));
		fireEvent.change(
			screen.getByRole("combobox", {
				name: new RegExp(`^${i18n.t("projects.defaultBrowserProfile")}`),
			}),
			{ target: { value: "" } },
		);
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		await waitFor(() => expect(writes()).toHaveLength(2));
		expect(writes().map((c) => c.body)).toEqual([
			expect.objectContaining({ defaultBrowserProfileId: "bpf_A" }),
			expect.objectContaining({ defaultBrowserProfileId: "" }),
		]);
	});

	it("侧边导航有「浏览器 Profile」分区（排在密钥之后）", async () => {
		setup("browser-profiles");
		const nav = screen.getByRole("navigation");
		const links = within(nav)
			.getAllByRole("link")
			.map((a) => a.getAttribute("href"));
		expect(links.indexOf("/settings/browser-profiles")).toBe(
			links.indexOf("/settings/secrets") + 1,
		);
		expect(
			await screen.findByText(i18n.t("browserProfiles.new")),
		).toBeInTheDocument();
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

	it("侧边导航含「通知」与「公开分享」，通知页渲染三张卡片", async () => {
		setup("notifications");
		const nav = screen.getByRole("navigation");
		expect(within(nav).getByRole("link", { name: "通知" })).toHaveAttribute(
			"href",
			"/settings/notifications",
		);
		expect(within(nav).getByRole("link", { name: "公开分享" })).toHaveAttribute(
			"href",
			"/settings/shares",
		);
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
			changeServer: vi.fn(),
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
						<Route
							path="/settings/devices"
							element={
								section === "desktop" ? (
									<p>DEVICES</p>
								) : (
									<SettingsPage section="devices" />
								)
							}
						/>
						<Route
							path={`/settings/${section}`}
							element={<SettingsPage section={section} />}
						/>
					</Routes>
				</MemoryRouter>
			</QueryClientProvider>,
		);
	}

	it("「桌面应用」分区只在桌面壳里出现；浏览器里访问该路由回到设备页", () => {
		const web = mount("about");
		expect(
			within(screen.getByRole("navigation")).queryByRole("link", {
				name: "桌面应用",
			}),
		).toBeNull();
		web.unmount();
		mount("desktop");
		expect(screen.getByText("DEVICES")).toBeInTheDocument();
	});

	it("桌面壳里导航出现「桌面应用」，关于页没有「退出登录」", () => {
		h.desktop = fakeDesktop;
		mount("about");
		expect(
			within(screen.getByRole("navigation")).getByRole("link", {
				name: "桌面应用",
			}),
		).toHaveAttribute("href", "/settings/desktop");
		expect(screen.queryByRole("button", { name: "退出登录" })).toBeNull();
	});

	it("浏览器里关于页有「退出登录」", () => {
		mount("about");
		expect(
			screen.getByRole("button", { name: "退出登录" }),
		).toBeInTheDocument();
	});

	it("App 会话（设备换来的网页会话）：批准配对卡改为引导到浏览器", () => {
		mount("devices", { kind: "web", deviceId: "dev_1" });
		const link = screen.getByRole("link", { name: "在浏览器中打开" });
		expect(link).toHaveAttribute("href", "/pair");
		expect(link).toHaveAttribute("target", "_blank");
		expect(link).toHaveAttribute("rel", "noopener noreferrer");
		expect(screen.queryByLabelText("配对码")).toBeNull();
	});

	it("普通网页会话：批准配对卡照常显示配对码输入", () => {
		mount("devices");
		expect(screen.getByLabelText("配对码")).toBeInTheDocument();
		expect(screen.queryByRole("link", { name: "在浏览器中打开" })).toBeNull();
	});

	it("设备页：二维码内容是 oryn://pair?server=<编码后的 origin>，下方写服务器地址", () => {
		mount("devices");
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
		expect(
			screen.getByRole("heading", { name: "用手机扫码连接" }),
		).toBeInTheDocument();
	});

	it("移动壳：设备页没有二维码卡，导航没有「桌面应用」，desktop 路由回设备页", () => {
		h.mobile = fakeMobile();
		const view = mount("devices");
		expect(screen.queryByTestId("pair-qr")).toBeNull();
		expect(
			within(screen.getByRole("navigation")).queryByRole("link", {
				name: "桌面应用",
			}),
		).toBeNull();
		view.unmount();
		mount("desktop");
		expect(screen.getByText("DEVICES")).toBeInTheDocument();
	});

	it("移动壳关于页：App 版本、WebView、令牌存储与「更换服务器…」，没有「退出登录」", async () => {
		const mobile = fakeMobile();
		h.mobile = mobile;
		mount("about");
		expect(await screen.findByText("0.2.0")).toBeInTheDocument();
		expect(screen.getByText("141.0.7390.0")).toBeInTheDocument();
		expect(screen.getByText("Android Keystore")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "退出登录" })).toBeNull();
		fireEvent.click(screen.getByTestId("mobile-change-server"));
		expect(mobile.changeServer).toHaveBeenCalledTimes(1);
	});
});
