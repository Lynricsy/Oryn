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

/** 读屏跟随的当前项：焦点所在面板的 aria-activedescendant（必须指向该面板自己子树里的项） */
function activeKey(): string | null {
	const panel = document.activeElement as HTMLElement;
	expect(panel.getAttribute("role")).toBe("menu");
	const id = panel.getAttribute("aria-activedescendant");
	const el = id ? document.getElementById(id) : null;
	if (el) expect(panel.contains(el)).toBe(true);
	return el?.dataset.menuKey ?? null;
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
		// 键盘进子菜单：焦点挪到子面板，活动项挂在子面板上；主菜单只指向自己的父项
		expect(document.activeElement).toBe(screen.getByTestId("context-submenu"));
		expect(activeKey()).toBe("inner");
		const mainId = screen
			.getByTestId("context-menu")
			.getAttribute("aria-activedescendant");
		expect(mainId && document.getElementById(mainId)?.dataset.menuKey).toBe(
			"more",
		);
		key("ArrowLeft");
		expect(document.activeElement).toBe(screen.getByTestId("context-menu"));
		expect(activeKey()).toBe("more");
		expect(screen.getByRole("menuitem", { name: "more" })).toBeTruthy();
		expect(screen.queryByRole("menuitem", { name: "inner" })).toBeNull();
		fireEvent.click(screen.getByRole("menuitem", { name: "more" }));
		fireEvent.click(screen.getByRole("menuitem", { name: "inner" }));
		expect(onSelect).toHaveBeenCalledOnce();
	});

	it("悬停父项直接弹出子菜单；移到别的项停顿后收起，移进子菜单则保留", () => {
		vi.useFakeTimers();
		try {
			const onSelect = vi.fn();
			render(<ContextMenuHost />);
			open([
				{
					key: "a",
					items: [
						{ key: "plain", label: "plain" },
						{
							key: "more",
							label: "more",
							children: [{ key: "inner", label: "inner", onSelect }],
						},
					],
				},
			]);
			const more = screen.getByRole("menuitem", { name: "more" });
			fireEvent.mouseEnter(more);
			expect(more).toHaveAttribute("aria-expanded", "true");
			// 悬停打开不挪焦点：仍在主菜单
			expect(document.activeElement).toBe(screen.getByTestId("context-menu"));
			// 主菜单项都还在，子菜单是旁边的另一个面板
			expect(screen.getByRole("menuitem", { name: "plain" })).toBeTruthy();
			const submenu = screen.getByTestId("context-submenu");
			expect(submenu).toHaveTextContent("inner");
			// 斜着移向子菜单途经别的项：停顿内移进子菜单，不收起
			fireEvent.mouseEnter(screen.getByRole("menuitem", { name: "plain" }));
			fireEvent.mouseEnter(submenu);
			act(() => vi.advanceTimersByTime(500));
			expect(screen.getByTestId("context-submenu")).toBeTruthy();
			// 停在别的项上：停顿后收起
			fireEvent.mouseEnter(screen.getByRole("menuitem", { name: "plain" }));
			act(() => vi.advanceTimersByTime(500));
			expect(screen.queryByTestId("context-submenu")).toBeNull();
			fireEvent.mouseEnter(more);
			fireEvent.click(screen.getByRole("menuitem", { name: "inner" }));
			expect(onSelect).toHaveBeenCalledOnce();
			expect(screen.queryByTestId("context-menu")).toBeNull();
		} finally {
			vi.useRealTimers();
		}
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
