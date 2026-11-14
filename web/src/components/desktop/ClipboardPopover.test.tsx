/**
 * 剪贴板弹层：发送到桌面、「复制到本机」；没有自动同步（安全审查 F3）——打开弹层、收到桌面内容都不读写本机剪贴板。
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
import { ClipboardBody, ClipboardPopover } from "./ClipboardPopover";

const clip = { readText: vi.fn(), writeText: vi.fn() };

beforeEach(() => {
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

	it("显示最近一次桌面剪贴板内容，只有点「复制到本机」才写本机剪贴板；没有内容时给说明", async () => {
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
		expect(clip.writeText).not.toHaveBeenCalled();
		await act(async () => {
			fireEvent.click(screen.getByTestId("desktop-clip-copy"));
		});
		expect(screen.getByTestId("desktop-clip-copy")).toHaveAccessibleName(
			i18n.t("deskPanel.clip.copyLocal"),
		);
		expect(clip.writeText).toHaveBeenCalledWith("来自桌面-2");
	});

	it("没有自动同步开关；说明剪贴板不会自动同步，也不读本机剪贴板", () => {
		render(<ClipboardBody remoteText="x" onSend={vi.fn()} />);
		expect(screen.queryByRole("checkbox")).toBeNull();
		expect(
			screen.getByText(i18n.t("deskPanel.clip.manualNote")),
		).toBeInTheDocument();
		expect(clip.readText).not.toHaveBeenCalled();
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
