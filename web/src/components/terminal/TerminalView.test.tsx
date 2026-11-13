import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { type TerminalSession, terminalHub } from "../../state/terminals";
import { THEME_EVENT } from "../../state/theme";
import { TerminalView } from "./TerminalView";

describe("TerminalView", () => {
	it("点容器空白处聚焦 xterm（非属主时 xterm 可能比容器小）", () => {
		const term = {
			cols: 80,
			rows: 24,
			write: vi.fn(),
			reset: vi.fn(),
			resize: vi.fn(),
			dispose: vi.fn(),
			text: () => "",
			mount: vi.fn(),
			focus: vi.fn(),
		};
		const session = { term, refit: null };
		vi.spyOn(terminalHub, "ensure").mockReturnValue(
			session as unknown as TerminalSession,
		);
		vi.spyOn(terminalHub, "claimSize").mockImplementation(() => {});
		render(<TerminalView threadId="T-1" terminalId="t1" autoFocus={false} />);
		expect(term.mount).toHaveBeenCalled();
		expect(term.focus).not.toHaveBeenCalled();
		fireEvent.click(screen.getByTestId("terminal-view"));
		expect(term.focus).toHaveBeenCalledTimes(1);
	});

	it("等宽字体到位后重新测量并 fit；主题切换时重取配色，卸载后不再响应", async () => {
		const term = {
			cols: 80,
			rows: 24,
			write: vi.fn(),
			reset: vi.fn(),
			resize: vi.fn(),
			dispose: vi.fn(),
			text: () => "",
			mount: vi.fn(),
			propose: vi.fn(() => ({ cols: 90, rows: 30 })),
			refont: vi.fn(),
			retheme: vi.fn(),
		};
		const session = { term, refit: null };
		vi.spyOn(terminalHub, "ensure").mockReturnValue(
			session as unknown as TerminalSession,
		);
		vi.spyOn(terminalHub, "claimSize").mockImplementation(() => {});
		const fitted = vi.spyOn(terminalHub, "fitted").mockImplementation(() => {});
		const load = vi.fn(() => Promise.resolve([]));
		Object.defineProperty(document, "fonts", {
			configurable: true,
			value: { load },
		});
		vi.useFakeTimers();
		try {
			const view = render(
				<TerminalView threadId="T-1" terminalId="t1" autoFocus={false} />,
			);
			expect(load).toHaveBeenCalledWith('13px "JetBrains Mono Variable"');
			// 挂载时对齐一次主题
			expect(term.retheme).toHaveBeenCalledTimes(1);
			await Promise.resolve();
			await Promise.resolve();
			expect(term.refont).toHaveBeenCalledTimes(1);
			vi.advanceTimersByTime(100);
			expect(fitted).toHaveBeenCalledWith("T-1", "t1", { cols: 90, rows: 30 });
			window.dispatchEvent(new Event(THEME_EVENT));
			expect(term.retheme).toHaveBeenCalledTimes(2);
			view.unmount();
			window.dispatchEvent(new Event(THEME_EVENT));
			expect(term.retheme).toHaveBeenCalledTimes(2);
		} finally {
			vi.useRealTimers();
			Reflect.deleteProperty(document, "fonts");
		}
	});
});
