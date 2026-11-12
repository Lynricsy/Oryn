import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { type TerminalSession, terminalHub } from "../../state/terminals";
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
});
