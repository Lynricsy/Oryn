/** 设置 → 桌面应用：各更新状态的呈现、保存快捷键、开关、令牌存储提示、更换服务器、壳推来 update 后重取。 */
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import {
	afterEach,
	beforeEach,
	describe,
	expect,
	it,
	type Mock,
	vi,
} from "vitest";
import { type DesktopHost, type DesktopInfo, host } from "../../lib/host";
import DesktopSettings from "./DesktopSettings";

function makeInfo(p: Partial<DesktopInfo> = {}): DesktopInfo {
	return {
		version: "0.1.0",
		settings: {
			shortcut: "Control+Alt+Space",
			closeToTray: true,
			autoUpdate: false,
		},
		tokenStore: "keyring",
		update: { state: "none" },
		...p,
	};
}

let info: DesktopInfo;
let fake: { [K in keyof DesktopHost]: Mock<DesktopHost[K]> };
let updateCb: (() => void) | undefined;
const h = host as { desktop: DesktopHost | null };

beforeEach(() => {
	info = makeInfo();
	fake = {
		info: vi.fn(async () => info),
		setSettings: vi.fn(async (patch: Partial<DesktopInfo["settings"]>) => {
			info = { ...info, settings: { ...info.settings, ...patch } };
			return info;
		}),
		checkUpdate: vi.fn(async () => null),
		installUpdate: vi.fn(async () => {}),
		changeServer: vi.fn(),
	};
	h.desktop = fake;
	vi.spyOn(host, "onUpdate").mockImplementation((cb) => {
		updateCb = cb;
		return () => {
			updateCb = undefined;
		};
	});
});

afterEach(() => {
	h.desktop = null;
	vi.restoreAllMocks();
});

describe("设置 → 桌面应用", () => {
	it("显示版本与令牌存储；检查更新没有新版本时说明已是最新", async () => {
		render(<DesktopSettings />);
		expect(await screen.findByText("0.1.0")).toBeInTheDocument();
		expect(screen.getByText("系统凭据库")).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "检查更新" }));
		expect(await screen.findByText("已是最新版本")).toBeInTheDocument();
		expect(fake.checkUpdate).toHaveBeenCalledTimes(1);
	});

	it("available / downloading：进度文字，不给按钮", async () => {
		info = makeInfo({ update: { state: "downloading", version: "0.2.0" } });
		const view = render(<DesktopSettings />);
		expect(await screen.findByText("正在下载 v0.2.0…")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "检查更新" })).toBeNull();
		view.unmount();
		info = makeInfo({ update: { state: "available", version: "0.2.0" } });
		render(<DesktopSettings />);
		expect(
			await screen.findByText("发现 v0.2.0，正在下载…"),
		).toBeInTheDocument();
	});

	it("ready：主按钮「重启以更新到 v…」调 installUpdate", async () => {
		info = makeInfo({ update: { state: "ready", version: "0.2.0" } });
		render(<DesktopSettings />);
		fireEvent.click(
			await screen.findByRole("button", { name: "重启以更新到 v0.2.0" }),
		);
		await waitFor(() => expect(fake.installUpdate).toHaveBeenCalledTimes(1));
	});

	it("error：显示原因，仍可再检查", async () => {
		info = makeInfo({ update: { state: "error", error: "network down" } });
		render(<DesktopSettings />);
		expect(
			await screen.findByText("检查更新失败：network down"),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "检查更新" }),
		).toBeInTheDocument();
	});

	it("保存快捷键：去掉首尾空白后提交；壳报告的注册失败原因显示在下方", async () => {
		fake.setSettings.mockImplementation(async (patch) => {
			info = {
				...info,
				settings: { ...info.settings, ...patch },
				shortcutError: "already registered",
			};
			return info;
		});
		render(<DesktopSettings />);
		const input = await screen.findByDisplayValue("Control+Alt+Space");
		const save = screen.getByRole("button", { name: "保存" });
		expect(save).toBeDisabled();
		fireEvent.change(input, { target: { value: "  Control+Alt+K " } });
		fireEvent.click(save);
		await waitFor(() =>
			expect(fake.setSettings).toHaveBeenCalledWith({
				shortcut: "Control+Alt+K",
			}),
		);
		expect(
			await screen.findByText("快捷键没有生效：already registered"),
		).toBeInTheDocument();
	});

	it("开关：关闭到托盘与自动安装各自提交一个字段", async () => {
		render(<DesktopSettings />);
		fireEvent.click(await screen.findByLabelText("关闭窗口时隐藏到托盘"));
		await waitFor(() =>
			expect(fake.setSettings).toHaveBeenCalledWith({ closeToTray: false }),
		);
		await waitFor(() =>
			expect(screen.getByLabelText("下载后自动安装并重启")).toBeEnabled(),
		);
		fireEvent.click(screen.getByLabelText("下载后自动安装并重启"));
		await waitFor(() =>
			expect(fake.setSettings).toHaveBeenLastCalledWith({ autoUpdate: true }),
		);
	});

	it("令牌存在文件里时给出警告；更换服务器交给壳", async () => {
		info = makeInfo({ tokenStore: "file" });
		render(<DesktopSettings />);
		expect(
			await screen.findByText("未找到系统凭据库，令牌以仅本人可读的文件保存。"),
		).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "更换服务器…" }));
		expect(fake.changeServer).toHaveBeenCalledTimes(1);
	});

	it("壳推来 update 事件后重新取 info", async () => {
		render(<DesktopSettings />);
		await screen.findByText("0.1.0");
		info = makeInfo({ update: { state: "ready", version: "0.3.0" } });
		updateCb?.();
		expect(
			await screen.findByRole("button", { name: "重启以更新到 v0.3.0" }),
		).toBeInTheDocument();
		expect(fake.info).toHaveBeenCalledTimes(2);
	});
});
