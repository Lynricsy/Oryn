/** 连接页：各状态的渲染、命令参数、壳推来的状态、错误码文案齐全（假 `window.__TAURI__`）。 */
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../i18n";
import { HOST_EVENT } from "../lib/host";
import {
	LAUNCHER_ERROR_CODES,
	type LauncherInfo,
	type LauncherState,
} from "./bridge";
import { Launcher } from "./Launcher";

const SERVER = "https://oryn.example.com";

let info: LauncherInfo;
let replies: Record<string, LauncherState>;
const invoke = vi.fn(async (cmd: string, _args?: Record<string, unknown>) => {
	if (cmd === "launcher_state") return info;
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
	replies = {};
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
