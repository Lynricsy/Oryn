import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import { installContextMenu } from "../../lib/context-menu/dispatch";
import { closeContextMenu } from "../../lib/context-menu/store";
import { type TerminalSession, terminalHub } from "../../state/terminals";
import { THEME_EVENT } from "../../state/theme";
import { ContextMenuHost } from "../ui/ContextMenu";
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

	describe("上下文菜单", () => {
		let off: () => void;
		beforeEach(() => {
			off = installContextMenu();
			Object.defineProperty(navigator, "clipboard", {
				configurable: true,
				value: { readText: vi.fn(async () => "") },
			});
		});
		afterEach(() => {
			off();
			act(() => closeContextMenu());
			Reflect.deleteProperty(navigator, "clipboard");
			vi.restoreAllMocks();
		});

		function mount(selection: string, tracking = false) {
			const term = {
				cols: 80,
				rows: 24,
				write: vi.fn(),
				reset: vi.fn(),
				resize: vi.fn(),
				dispose: vi.fn(),
				text: () => "",
				mount: vi.fn(),
				getSelection: () => selection,
				selectAll: vi.fn(),
				clear: vi.fn(),
				mouseTracking: () => tracking,
			};
			const session = { term, refit: null } as unknown as TerminalSession;
			vi.spyOn(terminalHub, "ensure").mockReturnValue(session);
			vi.spyOn(terminalHub, "session").mockReturnValue(session);
			vi.spyOn(terminalHub, "claimSize").mockImplementation(() => {});
			render(
				<>
					<ContextMenuHost />
					<TerminalView threadId="T-1" terminalId="t1" autoFocus={false} />
				</>,
			);
			let prevented = false;
			act(() => {
				prevented = !fireEvent.contextMenu(
					screen.getByTestId("terminal-view"),
					{
						clientX: 5,
						clientY: 5,
					},
				);
			});
			return { term, prevented };
		}

		const keys = () =>
			[
				...screen
					.getByTestId("context-menu")
					.querySelectorAll("[data-menu-key]"),
			].map((b) => b.getAttribute("data-menu-key"));

		it("没有选区：粘贴（终端不在线时置灰）、全选、清屏", () => {
			const { term, prevented } = mount("");
			expect(prevented).toBe(true);
			expect(keys()).toEqual(["paste", "selectAll", "clear"]);
			const menu = screen.getByTestId("context-menu");
			expect(
				within(menu).getByRole("menuitem", {
					name: i18n.t("contextMenu.paste"),
				}),
			).toBeDisabled();
			fireEvent.click(
				within(menu).getByRole("menuitem", {
					name: i18n.t("contextMenu.selectAll"),
				}),
			);
			expect(term.selectAll).toHaveBeenCalledTimes(1);
		});

		it("有选区：先给复制，再给终端组", () => {
			mount("ls -la");
			expect(keys()).toEqual(["copy", "paste", "selectAll", "clear"]);
		});

		it("程序开了鼠标上报：拦下原生菜单但不出菜单（右键交给程序）", () => {
			const { prevented } = mount("x", true);
			expect(prevented).toBe(true);
			expect(screen.queryByTestId("context-menu")).toBeNull();
		});
	});
});
