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
import { answerConfirm } from "../test/confirm";
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

function setup(section: SettingsSection = "git-identity") {
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

describe("设置页外框与「身份与头像」", () => {
	beforeEach(() => {
		calls = [];
		vi.stubGlobal("fetch", fetchMock);
	});
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("页面里没有分区导航（导航在侧栏「更多」分区）", () => {
		setup("devices");
		expect(screen.queryByRole("navigation")).toBeNull();
		expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("设置");
	});

	it("身份与头像：依次是默认 Agent 身份、全局助手、新对话默认对象、角色头像、我的头像五张卡", async () => {
		setup("identity");
		expect(
			screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent),
		).toEqual([
			"默认 Agent 身份",
			"全局助手",
			"新对话默认对象",
			"角色头像",
			"我的头像",
		]);
		expect(await screen.findByTestId("identity-roles")).toBeInTheDocument();
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

	it("外观只有主题（明暗、主题色、壁纸），没有头像卡", () => {
		setup("appearance");
		expect(screen.getByRole("heading", { name: "主题" })).toBeInTheDocument();
		expect(screen.getAllByRole("heading", { level: 3 })).toHaveLength(1);
		expect(
			screen.queryByRole("group", { name: i18n.t("appearance.avatarGlyphs") }),
		).toBeNull();
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

	it("「桌面应用」分区在浏览器里访问时回到设备页", () => {
		mount("desktop");
		expect(screen.getByText("DEVICES")).toBeInTheDocument();
	});

	it("桌面壳里关于页没有「退出登录」", () => {
		h.desktop = fakeDesktop;
		mount("about");
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

	it("移动壳：设备页没有二维码卡，desktop 路由回设备页", () => {
		h.mobile = fakeMobile();
		const view = mount("devices");
		expect(screen.queryByTestId("pair-qr")).toBeNull();
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
