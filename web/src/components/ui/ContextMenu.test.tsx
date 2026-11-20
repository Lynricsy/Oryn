import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	closeContextMenu,
	openContextMenu,
} from "../../lib/context-menu/store";
import type { MenuGroup } from "../../lib/context-menu/types";
import { ContextMenuHost } from "./ContextMenu";
import { Dialog } from "./primitives";

afterEach(() => {
	act(() => closeContextMenu());
});

function open(groups: MenuGroup[], restoreFocus: HTMLElement | null = null) {
	act(() =>
		openContextMenu({
			groups,
			x: 10,
			y: 10,
			trigger: "keyboard",
			restoreFocus,
		}),
	);
}

function key(k: string) {
	fireEvent.keyDown(document.activeElement ?? document.body, { key: k });
}

function activeKey(): string | null {
	const menu = screen.getByTestId("context-menu");
	const id = menu.getAttribute("aria-activedescendant");
	return id ? (document.getElementById(id)?.dataset.menuKey ?? null) : null;
}

describe("ContextMenuHost", () => {
	it("方向键、Home、End 跳过禁用项并循环", () => {
		render(<ContextMenuHost />);
		open([
			{
				key: "a",
				items: [
					{ key: "one", label: "one" },
					{ key: "two", label: "two", disabled: true },
				],
			},
			{ key: "b", items: [{ key: "three", label: "three" }] },
		]);
		expect(activeKey()).toBe("one");
		key("ArrowDown");
		expect(activeKey()).toBe("three");
		key("ArrowDown");
		expect(activeKey()).toBe("one");
		key("ArrowUp");
		expect(activeKey()).toBe("three");
		key("Home");
		expect(activeKey()).toBe("one");
		key("End");
		expect(activeKey()).toBe("three");
		expect(screen.getAllByRole("separator")).toHaveLength(1);
	});

	it("Enter 执行高亮项并关闭，焦点回到触发元素", () => {
		const onSelect = vi.fn();
		render(
			<>
				<button type="button">trigger</button>
				<ContextMenuHost />
			</>,
		);
		const trigger = screen.getByRole("button", { name: "trigger" });
		trigger.focus();
		open(
			[{ key: "a", items: [{ key: "go", label: "go", onSelect }] }],
			trigger,
		);
		key("Enter");
		expect(onSelect).toHaveBeenCalledOnce();
		expect(screen.queryByTestId("context-menu")).toBeNull();
		expect(document.activeElement).toBe(trigger);
	});

	it("带 children 的项进入子菜单，← 返回", () => {
		const onSelect = vi.fn();
		render(<ContextMenuHost />);
		open([
			{
				key: "a",
				items: [
					{
						key: "more",
						label: "more",
						children: [{ key: "inner", label: "inner", onSelect }],
					},
				],
			},
		]);
		key("ArrowRight");
		expect(screen.getByRole("menuitem", { name: "inner" })).toBeTruthy();
		expect(activeKey()).toBe("inner");
		key("ArrowLeft");
		expect(screen.getByRole("menuitem", { name: "more" })).toBeTruthy();
		expect(screen.queryByRole("menuitem", { name: "inner" })).toBeNull();
		fireEvent.click(screen.getByRole("menuitem", { name: "more" }));
		fireEvent.click(screen.getByRole("menuitem", { name: "inner" }));
		expect(onSelect).toHaveBeenCalledOnce();
	});

	it("菜单外按下关闭，菜单内按下不关", () => {
		render(
			<>
				<p>outside</p>
				<ContextMenuHost />
			</>,
		);
		open([{ key: "a", items: [{ key: "x", label: "x" }] }]);
		fireEvent.pointerDown(screen.getByRole("menuitem", { name: "x" }));
		expect(screen.getByTestId("context-menu")).toBeTruthy();
		fireEvent.pointerDown(screen.getByText("outside"));
		expect(screen.queryByTestId("context-menu")).toBeNull();
	});

	it("菜单里的 Esc 不会连带关掉底下的对话框", () => {
		const onClose = vi.fn();
		render(
			<>
				<Dialog open onClose={onClose} title="dlg">
					<button type="button">inside</button>
				</Dialog>
				<ContextMenuHost />
			</>,
		);
		open([{ key: "a", items: [{ key: "x", label: "x" }] }]);
		key("Escape");
		expect(screen.queryByTestId("context-menu")).toBeNull();
		expect(onClose).not.toHaveBeenCalled();
	});
});
