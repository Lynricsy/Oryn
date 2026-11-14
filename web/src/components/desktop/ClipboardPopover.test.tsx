/**
 * 剪贴板弹层：发送到桌面、复制桌面内容、自动同步（打开时触发读取授权，被拒绝则开关回弹并提示改用手动）。
 */
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import { uiStore } from "../../state/ui";
import { ClipboardBody, ClipboardPopover } from "./ClipboardPopover";

const clip = { readText: vi.fn(), writeText: vi.fn() };

beforeEach(() => {
	localStorage.clear();
	uiStore.setState({ desktopClipSync: false });
	clip.readText.mockReset();
	clip.writeText.mockReset().mockResolvedValue(undefined);
	Object.defineProperty(navigator, "clipboard", {
		configurable: true,
		value: clip,
	});
});

afterEach(() => {
	vi.restoreAllMocks();
});

const syncToggle = () =>
	screen.getByRole("checkbox", { name: i18n.t("deskPanel.clip.autoSync") });

describe("ClipboardBody", () => {
	it("发送到桌面：把文本框内容交给 onSend 并提示去桌面粘贴", () => {
		const onSend = vi.fn();
		render(<ClipboardBody remoteText={null} onSend={onSend} />);
		const send = screen.getByRole("button", {
			name: i18n.t("deskPanel.clip.send"),
		});
		expect(send).toBeDisabled();
		fireEvent.change(
			screen.getByRole("textbox", { name: i18n.t("deskPanel.clip.toDesktop") }),
			{ target: { value: "发往桌面-1" } },
		);
		fireEvent.click(send);
		expect(onSend).toHaveBeenCalledWith("发往桌面-1");
		expect(screen.getByText(i18n.t("deskPanel.clip.sent"))).toBeInTheDocument();
	});

	it("显示最近一次桌面剪贴板内容，「复制」写入本机剪贴板；没有内容时给说明", async () => {
		const { rerender } = render(
			<ClipboardBody remoteText={null} onSend={vi.fn()} />,
		);
		expect(
			screen.getByText(i18n.t("deskPanel.clip.empty")),
		).toBeInTheDocument();
		rerender(<ClipboardBody remoteText="来自桌面-2" onSend={vi.fn()} />);
		expect(screen.getByTestId("desktop-clip-remote")).toHaveTextContent(
			"来自桌面-2",
		);
		await act(async () => {
			fireEvent.click(
				screen.getByRole("button", { name: i18n.t("common.copy") }),
			);
		});
		expect(clip.writeText).toHaveBeenCalledWith("来自桌面-2");
	});

	it("打开自动同步：先读一次剪贴板（触发授权），成功则开启并把内容发到桌面", async () => {
		clip.readText.mockResolvedValue("本机内容");
		const onSend = vi.fn();
		render(<ClipboardBody remoteText={null} onSend={onSend} />);
		await act(async () => {
			fireEvent.click(syncToggle());
		});
		expect(clip.readText).toHaveBeenCalledTimes(1);
		expect(uiStore.getState().desktopClipSync).toBe(true);
		expect(localStorage.getItem("oryn.desktop.clipsync")).toBe("1");
		expect(onSend).toHaveBeenCalledWith("本机内容");
		expect(syncToggle()).toBeChecked();
	});

	it("授权被拒：开关回弹，提示改用手动", async () => {
		clip.readText.mockRejectedValue(
			new DOMException("denied", "NotAllowedError"),
		);
		render(<ClipboardBody remoteText={null} onSend={vi.fn()} />);
		await act(async () => {
			fireEvent.click(syncToggle());
		});
		expect(uiStore.getState().desktopClipSync).toBe(false);
		expect(syncToggle()).not.toBeChecked();
		expect(screen.getByRole("alert")).toHaveTextContent(
			i18n.t("deskPanel.clip.denied"),
		);
	});

	it("关闭自动同步不再读剪贴板", async () => {
		uiStore.setState({ desktopClipSync: true });
		render(<ClipboardBody remoteText={null} onSend={vi.fn()} />);
		await act(async () => {
			fireEvent.click(syncToggle());
		});
		expect(clip.readText).not.toHaveBeenCalled();
		expect(uiStore.getState().desktopClipSync).toBe(false);
	});
});

describe("ClipboardPopover", () => {
	it("按钮开合弹层，Esc 关闭", async () => {
		render(<ClipboardPopover remoteText={null} onSend={vi.fn()} />);
		const btn = screen.getByRole("button", {
			name: i18n.t("deskPanel.clip.title"),
		});
		fireEvent.click(btn);
		expect(
			screen.getByRole("dialog", { name: i18n.t("deskPanel.clip.title") }),
		).toBeInTheDocument();
		fireEvent.keyDown(window, { key: "Escape" });
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});
});
