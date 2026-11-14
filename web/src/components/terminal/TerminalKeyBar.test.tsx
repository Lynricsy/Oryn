/**
 * 终端按键条的输入行（M7 计划 4.4）：回车整行发出 `value\r`、空回车只发 `\r`、粘滞 Ctrl 时发控制字符且不追加回车；
 * Android 下「粘贴」键不读剪贴板，而是打开并聚焦输入行。
 */
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TerminalKeyBar } from "./TerminalKeyBar";

const { fakeHost, session, sent } = vi.hoisted(() => ({
	fakeHost: { platform: null as string | null },
	session: { transform: null as ((d: string) => string) | null },
	sent: [] as string[],
}));

vi.mock("../../lib/host", () => ({ host: fakeHost }));
vi.mock("../../state/terminals", () => ({
	terminalHub: {
		ensure: () => session,
		session: () => session,
		// 与真实实现一致：经过粘滞改写再入队
		input: (_t: string, _id: string, data: string) => {
			sent.push(session.transform ? session.transform(data) : data);
		},
	},
}));

const readText = vi.fn(async () => "clip");
const clipboardDesc = Object.getOwnPropertyDescriptor(navigator, "clipboard");

beforeEach(() => {
	fakeHost.platform = null;
	session.transform = null;
	sent.length = 0;
	readText.mockClear();
	Object.defineProperty(navigator, "clipboard", {
		value: { readText },
		configurable: true,
	});
});

afterEach(() => {
	if (clipboardDesc)
		Object.defineProperty(navigator, "clipboard", clipboardDesc);
	else delete (navigator as { clipboard?: unknown }).clipboard;
});

function renderBar() {
	render(<TerminalKeyBar threadId="T-1" terminalId="term-1" />);
}

function openLine(): HTMLInputElement {
	fireEvent.click(screen.getByRole("button", { name: "输入行" }));
	return screen.getByTestId("terminal-line-input") as HTMLInputElement;
}

function submit(input: HTMLInputElement, value: string) {
	fireEvent.change(input, { target: { value } });
	fireEvent.submit(input.form as HTMLFormElement);
}

describe("终端输入行", () => {
	it("开关键带 aria-pressed，打开后聚焦等宽输入框", () => {
		renderBar();
		const toggle = screen.getByTestId("terminal-line-toggle");
		expect(toggle).toHaveAccessibleName("输入行");
		expect(toggle).toHaveAttribute("aria-pressed", "false");
		expect(screen.queryByTestId("terminal-line-input")).toBeNull();
		const input = openLine();
		expect(toggle).toHaveAttribute("aria-pressed", "true");
		expect(input).toHaveFocus();
		expect(input).toHaveAttribute("enterkeyhint", "send");
		expect(input).toHaveAttribute("autocapitalize", "off");
		expect(input).toHaveAttribute("spellcheck", "false");
	});

	it("回车发出 value + \\r，清空并保持焦点", () => {
		renderBar();
		const input = openLine();
		submit(input, "ls -la");
		expect(sent).toEqual(["ls -la\r"]);
		expect(input.value).toBe("");
		expect(input).toHaveFocus();
	});

	it("空回车只发 \\r", () => {
		renderBar();
		submit(openLine(), "");
		expect(sent).toEqual(["\r"]);
	});

	it("粘滞 Ctrl：发控制字符、不追加回车，且不被粘滞改写二次处理；发完解除", () => {
		renderBar();
		const input = openLine();
		const ctrl = screen.getByRole("button", { name: "Ctrl" });
		fireEvent.click(ctrl);
		expect(ctrl).toHaveAttribute("aria-pressed", "true");
		expect(session.transform).not.toBeNull();
		submit(input, "c");
		expect(sent).toEqual(["\x03"]);
		expect(ctrl).toHaveAttribute("aria-pressed", "false");
		expect(session.transform).toBeNull();
		submit(input, "c");
		expect(sent).toEqual(["\x03", "c\r"]);
	});

	it("非 Android：粘贴键读剪贴板", async () => {
		renderBar();
		await act(async () => {
			fireEvent.click(screen.getByRole("button", { name: "粘贴" }));
		});
		expect(sent).toEqual(["clip"]);
		expect(readText).toHaveBeenCalledTimes(1);
	});

	it("Android：粘贴键不读剪贴板，而是打开并聚焦输入行", () => {
		fakeHost.platform = "android";
		renderBar();
		fireEvent.click(screen.getByRole("button", { name: "粘贴" }));
		const input = screen.getByTestId("terminal-line-input");
		expect(input).toHaveFocus();
		expect(readText).not.toHaveBeenCalled();
		// 已打开时再按：只重新聚焦，不关闭
		input.blur();
		fireEvent.click(screen.getByRole("button", { name: "粘贴" }));
		expect(screen.getByTestId("terminal-line-input")).toHaveFocus();
		expect(sent).toEqual([]);
	});
});
