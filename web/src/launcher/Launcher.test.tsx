/**
 * 连接页：各状态的渲染、命令参数、壳推来的状态、错误码文案齐全（假 `window.__TAURI__`）；
 * 移动平台（假 `window.__ORYN_HOST__.platform`）的文案、复制配对码并打开浏览器、扫码、系统栏颜色。
 */
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../i18n";
import { HOST_EVENT } from "../lib/host";
import {
	LAUNCHER_ERROR_CODES,
	type LauncherInfo,
	type LauncherState,
} from "./bridge";
import { Launcher } from "./Launcher";

// 系统栏颜色：jsdom 算不出 body 底色，换成立刻回调一次固定颜色
const chrome = vi.hoisted(() => ({
	watchChrome: vi.fn(
		(_win: Window, cb: (c: { dark: boolean; color: string }) => void) => {
			cb({ dark: true, color: "#101418" });
			return () => undefined;
		},
	),
}));
vi.mock("../lib/host-chrome", () => chrome);

const SERVER = "https://oryn.example.com";

let info: LauncherInfo;
let replies: Record<string, LauncherState>;
/** 非 launcher 命令（扫码插件、host_set_chrome）的回复；函数 = 按调用计算（可抛错模拟 reject）。 */
let plugin: Record<string, unknown>;
const invoke = vi.fn(async (cmd: string, _args?: Record<string, unknown>) => {
	if (cmd === "launcher_state") return info;
	if (cmd in plugin) {
		const v = plugin[cmd];
		return typeof v === "function" ? v() : v;
	}
	const r = replies[cmd];
	if (!r) throw new Error(`unexpected ${cmd}`);
	return r;
});

function withState(state: LauncherState, p: Partial<LauncherInfo> = {}) {
	info = {
		state,
		lang: "zh-CN",
		version: "0.1.0",
		tokenStore: "keyring",
		...p,
	};
}

function push(state: LauncherState) {
	act(() => {
		window.dispatchEvent(
			new CustomEvent(HOST_EVENT, { detail: { type: "launcher", state } }),
		);
	});
}

const pairing: LauncherState = {
	step: "pairing",
	serverUrl: SERVER,
	code: "ABCD-EFGH",
	verifyUrl: `${SERVER}/pair`,
	expiresAt: Date.now() + 9 * 60_000 + 30_000,
};

beforeEach(() => {
	invoke.mockClear();
	chrome.watchChrome.mockClear();
	replies = {};
	plugin = {};
	window.__TAURI__ = { core: { invoke: invoke as never } };
	withState({ step: "connect", serverUrl: "" });
});

afterEach(async () => {
	delete window.__TAURI__;
	vi.restoreAllMocks();
	await i18n.changeLanguage("zh-CN");
});

describe("连接页", () => {
	it("connect：提交地址调用 launcher_connect，返回 pairing 后显示配对码与剩余时间", async () => {
		vi.spyOn(Date, "now").mockReturnValue(1_000_000);
		replies.launcher_connect = {
			...pairing,
			expiresAt: 1_000_000 + 9 * 60_000 + 30_000,
		};
		render(<Launcher />);
		const input = await screen.findByLabelText(/服务器地址/);
		expect(input).toHaveAttribute("placeholder", "https://oryn.example.com");
		fireEvent.change(input, { target: { value: "  oryn.example.com " } });
		fireEvent.submit(input.closest("form") as HTMLFormElement);
		expect(
			await screen.findByRole("heading", { name: "在浏览器中批准" }),
		).toBeInTheDocument();
		expect(invoke).toHaveBeenCalledWith("launcher_connect", {
			url: "oryn.example.com",
		});
		expect(screen.getByLabelText("配对码")).toHaveTextContent("ABCD-EFGH");
		expect(
			screen.getByText(/已在浏览器打开 oryn\.example\.com/),
		).toBeInTheDocument();
		expect(screen.getByText("9:30 后失效")).toBeInTheDocument();
	});

	it("pairing：重新打开浏览器 / 取消", async () => {
		withState(pairing);
		replies.launcher_open_browser = pairing;
		replies.launcher_cancel = { step: "connect", serverUrl: SERVER };
		render(<Launcher />);
		fireEvent.click(
			await screen.findByRole("button", { name: "重新打开浏览器" }),
		);
		expect(invoke).toHaveBeenCalledWith("launcher_open_browser", undefined);
		fireEvent.click(await screen.findByRole("button", { name: "取消" }));
		// 回到 connect 时预填壳记住的地址
		expect(await screen.findByDisplayValue(SERVER)).toBeInTheDocument();
	});

	it("壳推来的 launcher 事件切换状态；connecting 显示主机名", async () => {
		render(<Launcher />);
		await screen.findByLabelText(/服务器地址/);
		push({ step: "connecting", serverUrl: SERVER });
		expect(screen.getByText("正在进入 oryn.example.com…")).toBeInTheDocument();
		push({
			step: "connect",
			serverUrl: SERVER,
			error: { code: "pair_expired", message: "expired" },
		});
		expect(screen.getByRole("alert")).toHaveTextContent("配对码已过期。");
	});

	it("error：原因与建议；unreachable 显示正在重连；重试调 launcher_retry", async () => {
		withState({
			step: "error",
			serverUrl: SERVER,
			error: { code: "unreachable", message: "connect refused" },
		});
		replies.launcher_retry = { step: "connecting", serverUrl: SERVER };
		render(<Launcher />);
		expect(await screen.findByRole("alert")).toHaveTextContent(
			"连不上服务器。",
		);
		expect(screen.getByText("正在重连…")).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "重试" }));
		expect(
			await screen.findByText("正在进入 oryn.example.com…"),
		).toBeInTheDocument();
		expect(invoke).toHaveBeenCalledWith("launcher_retry", undefined);
	});

	it("error → 更换服务器：先在本页确认，确认后 launcher_change_server{confirm:true}；取消回到错误页", async () => {
		withState({
			step: "error",
			serverUrl: SERVER,
			error: { code: "server_too_old", message: "404" },
		});
		replies.launcher_change_server = { step: "connect", serverUrl: "" };
		render(<Launcher />);
		fireEvent.click(await screen.findByRole("button", { name: "更换服务器…" }));
		expect(
			screen.getByText(/将在这台设备上移除 oryn\.example\.com 的登录/),
		).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "取消" }));
		expect(screen.getByRole("button", { name: "重试" })).toBeInTheDocument();
		expect(invoke).not.toHaveBeenCalledWith(
			"launcher_change_server",
			expect.anything(),
		);
		fireEvent.click(screen.getByRole("button", { name: "更换服务器…" }));
		fireEvent.click(screen.getByRole("button", { name: "移除并更换" }));
		expect(await screen.findByLabelText(/服务器地址/)).toBeInTheDocument();
		expect(invoke).toHaveBeenCalledWith("launcher_change_server", {
			confirm: true,
		});
	});

	it("confirmChange（壳发起）：取消调 launcher_change_server{confirm:false}", async () => {
		withState({ step: "confirmChange", serverUrl: SERVER });
		replies.launcher_change_server = { step: "connecting", serverUrl: SERVER };
		render(<Launcher />);
		fireEvent.click(await screen.findByRole("button", { name: "取消" }));
		expect(
			await screen.findByText("正在进入 oryn.example.com…"),
		).toBeInTheDocument();
		expect(invoke).toHaveBeenCalledWith("launcher_change_server", {
			confirm: false,
		});
	});

	it("令牌存在文件里时卡片底部给出警告，并显示版本", async () => {
		withState({ step: "connect", serverUrl: "" }, { tokenStore: "file" });
		render(<Launcher />);
		expect(
			await screen.findByText("未找到系统凭据库，令牌以仅本人可读的文件保存。"),
		).toBeInTheDocument();
		expect(screen.getByText("Oryn Desktop 0.1.0")).toBeInTheDocument();
	});

	it("按壳给出的语言切换界面", async () => {
		withState({ step: "connect", serverUrl: "" }, { lang: "en" });
		render(<Launcher />);
		expect(
			await screen.findByRole("heading", { name: "Connect to a server" }),
		).toBeInTheDocument();
	});

	it("IPC 失败：显示错误而不是一直转圈", async () => {
		invoke.mockRejectedValueOnce(new Error("ipc down"));
		render(<Launcher />);
		expect(await screen.findByRole("alert")).toHaveTextContent("ipc down");
	});

	it("每个错误码在两种语言里都有原因与下一步；未知码走通用文案", async () => {
		for (const lng of ["zh-CN", "en"])
			for (const code of [...LAUNCHER_ERROR_CODES, "unknown"])
				for (const part of ["reason", "hint"])
					expect(
						i18n.exists(`launcher.error.${code}.${part}`, { lng }),
						`${lng} ${code}.${part}`,
					).toBe(true);
		withState({
			step: "error",
			serverUrl: SERVER,
			error: { code: "something_new", message: "?" },
		});
		render(<Launcher />);
		expect(await screen.findByRole("alert")).toHaveTextContent("连接失败。");
	});
});

describe("连接页（移动平台）", () => {
	let writeText: ReturnType<typeof vi.fn>;

	beforeEach(() => {
		window.__ORYN_HOST__ = { kind: "tauri", platform: "android", bridge: 2 };
		writeText = vi.fn(async (_text: string) => undefined);
		Object.defineProperty(navigator, "clipboard", {
			value: { writeText },
			configurable: true,
		});
		plugin.host_set_chrome = null;
		plugin["plugin:barcode-scanner|open_app_settings"] = null;
	});
	afterEach(() => {
		delete window.__ORYN_HOST__;
		Reflect.deleteProperty(navigator, "clipboard");
	});

	it("不提 Oryn Desktop 与系统凭据库；版本写作 Oryn x.y.z；系统栏颜色交给 host_set_chrome", async () => {
		withState({ step: "connect", serverUrl: "" }, { tokenStore: "keystore" });
		const { container } = render(<Launcher />);
		expect(await screen.findByText("Oryn 0.1.0")).toBeInTheDocument();
		expect(container.textContent).not.toMatch(/Oryn Desktop|凭据库|钥匙串/);
		expect(screen.getByTestId("launcher-url")).toBeInTheDocument();
		expect(screen.getByTestId("launcher-connect")).toBeInTheDocument();
		expect(screen.getByTestId("launcher-scan")).toHaveTextContent("扫码");
		expect(chrome.watchChrome).toHaveBeenCalledWith(
			window,
			expect.any(Function),
		);
		expect(invoke).toHaveBeenCalledWith("host_set_chrome", {
			dark: true,
			color: "#101418",
		});
	});

	it("keyring 错误换成移动端措辞", async () => {
		withState({
			step: "error",
			serverUrl: SERVER,
			error: { code: "keyring", message: "x" },
		});
		const { container } = render(<Launcher />);
		expect(await screen.findByRole("alert")).toHaveTextContent(
			"无法把令牌存进系统安全存储。",
		);
		expect(container.textContent).not.toMatch(/凭据库|钥匙串/);
	});

	it("pairing：说明换成 pairingMobile；主按钮先复制配对码再 launcher_open_browser", async () => {
		withState(pairing);
		replies.launcher_open_browser = pairing;
		render(<Launcher />);
		expect(
			await screen.findByText(
				"在手机浏览器登录 Oryn 后输入此码；或到已登录的电脑「设置 → 设备」输入。",
			),
		).toBeInTheDocument();
		expect(screen.getByTestId("launcher-code")).toHaveTextContent("ABCD-EFGH");
		const open = screen.getByTestId("launcher-open-browser");
		expect(open).toHaveTextContent("复制配对码并打开浏览器");
		fireEvent.click(open);
		await waitFor(() =>
			expect(invoke).toHaveBeenCalledWith("launcher_open_browser", undefined),
		);
		expect(writeText).toHaveBeenCalledWith("ABCD-EFGH");
		const opened = invoke.mock.calls.findIndex(
			([cmd]) => cmd === "launcher_open_browser",
		);
		expect(writeText.mock.invocationCallOrder[0]).toBeLessThan(
			invoke.mock.invocationCallOrder[opened] ?? 0,
		);
	});

	it("扫码成功：权限 prompt → 申请得到 granted → 扫描，原样把内容交给 launcher_connect，输入框显示解析出的地址", async () => {
		const content = `oryn://pair?server=${encodeURIComponent(SERVER)}`;
		plugin["plugin:barcode-scanner|check_permissions"] = "prompt";
		plugin["plugin:barcode-scanner|request_permissions"] = {
			camera: "granted",
		};
		plugin["plugin:barcode-scanner|scan"] = { content, format: "QR_CODE" };
		replies.launcher_connect = { step: "connecting", serverUrl: SERVER };
		render(<Launcher />);
		fireEvent.click(await screen.findByTestId("launcher-scan"));
		expect(
			await screen.findByText("正在进入 oryn.example.com…"),
		).toBeInTheDocument();
		expect(invoke).toHaveBeenCalledWith("plugin:barcode-scanner|scan", {
			windowed: false,
			formats: ["QR_CODE"],
		});
		expect(invoke).toHaveBeenCalledWith("launcher_connect", { url: content });
	});

	it("扫码成功但壳判定地址非法：回到 connect 显示 invalid，输入框是扫到的 server", async () => {
		const content = `oryn://pair?server=${encodeURIComponent("ftp://bad")}`;
		plugin["plugin:barcode-scanner|check_permissions"] = { camera: "granted" };
		plugin["plugin:barcode-scanner|scan"] = { content };
		replies.launcher_connect = {
			step: "connect",
			serverUrl: "",
			error: { code: "invalid_url", message: "bad" },
		};
		render(<Launcher />);
		fireEvent.click(await screen.findByTestId("launcher-scan"));
		expect(await screen.findByRole("alert")).toHaveTextContent(
			"无法识别这个服务器地址。",
		);
		expect(screen.getByTestId("launcher-url")).toHaveValue("ftp://bad");
		expect(invoke).not.toHaveBeenCalledWith(
			"plugin:barcode-scanner|request_permissions",
			undefined,
		);
	});

	it("相机权限被拒：显示错误与「打开设置」，点它调 open_app_settings；不扫描", async () => {
		plugin["plugin:barcode-scanner|check_permissions"] = "denied";
		plugin["plugin:barcode-scanner|request_permissions"] = "denied";
		render(<Launcher />);
		fireEvent.click(await screen.findByTestId("launcher-scan"));
		expect(await screen.findByRole("alert")).toHaveTextContent(
			"没有相机权限，无法扫码。",
		);
		fireEvent.click(screen.getByRole("button", { name: "打开设置" }));
		expect(invoke).toHaveBeenCalledWith(
			"plugin:barcode-scanner|open_app_settings",
			undefined,
		);
		expect(invoke).not.toHaveBeenCalledWith(
			"plugin:barcode-scanner|scan",
			expect.anything(),
		);
	});

	it("取消扫描：静默回到原状，不报错也不连接", async () => {
		plugin["plugin:barcode-scanner|check_permissions"] = "granted";
		plugin["plugin:barcode-scanner|scan"] = () => {
			throw "cancelled";
		};
		render(<Launcher />);
		const scan = await screen.findByTestId("launcher-scan");
		fireEvent.click(scan);
		await waitFor(() => expect(scan).not.toBeDisabled());
		expect(invoke).toHaveBeenCalledWith("plugin:barcode-scanner|scan", {
			windowed: false,
			formats: ["QR_CODE"],
		});
		expect(screen.queryByRole("alert")).toBeNull();
		expect(invoke).not.toHaveBeenCalledWith(
			"launcher_connect",
			expect.anything(),
		);
	});

	it("扫描失败（非取消）：显示 scanFailed", async () => {
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		plugin["plugin:barcode-scanner|check_permissions"] = "granted";
		plugin["plugin:barcode-scanner|scan"] = () => {
			throw "camera unavailable";
		};
		render(<Launcher />);
		fireEvent.click(await screen.findByTestId("launcher-scan"));
		expect(await screen.findByRole("alert")).toHaveTextContent(
			"扫码失败，请重试或直接输入地址。",
		);
		expect(warn).toHaveBeenCalled();
	});
});

describe("连接页（桌面平台）", () => {
	beforeEach(() => {
		window.__ORYN_HOST__ = { kind: "tauri", platform: "linux", bridge: 2 };
	});
	afterEach(() => {
		delete window.__ORYN_HOST__;
	});

	it("文案不变、没有扫码按钮、不同步系统栏", async () => {
		withState(pairing);
		render(<Launcher />);
		expect(
			await screen.findByText(/已在浏览器打开 oryn\.example\.com/),
		).toBeInTheDocument();
		expect(screen.getByText("Oryn Desktop 0.1.0")).toBeInTheDocument();
		expect(screen.getByTestId("launcher-open-browser")).toHaveTextContent(
			"重新打开浏览器",
		);
		expect(screen.queryByTestId("launcher-scan")).toBeNull();
		expect(chrome.watchChrome).not.toHaveBeenCalled();
	});
});
