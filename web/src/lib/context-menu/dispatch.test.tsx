import { act, fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ContextMenuHost } from "../../components/ui/ContextMenu";
import { type DesktopHost, host } from "../host";
import { installContextMenu } from "./dispatch";
import { useContextTarget } from "./registry";
import { closeContextMenu } from "./store";
import type { ContextTargetSpec } from "./types";

const h = host as { desktop: DesktopHost | null };
let off: () => void;

beforeEach(() => {
	off = installContextMenu();
});

afterEach(() => {
	off();
	act(() => closeContextMenu());
	h.desktop = null;
	document.getSelection()?.removeAllRanges();
});

function Target({
	spec,
	children,
}: {
	spec: ContextTargetSpec;
	children: ReactNode;
}) {
	const ref = useContextTarget<HTMLButtonElement>(spec);
	return (
		<button ref={ref} type="button">
			{children}
		</button>
	);
}

const ITEMS: ContextTargetSpec = {
	items: () => [{ key: "obj", items: [{ key: "pin", label: "置顶" }] }],
};

/** 派发右键，返回事件是否被拦下（defaultPrevented） */
function rightClick(el: Element, init: MouseEventInit = {}): boolean {
	let prevented = false;
	act(() => {
		prevented = !fireEvent.contextMenu(el, {
			clientX: 5,
			clientY: 5,
			...init,
		});
	});
	return prevented;
}

describe("installContextMenu", () => {
	it("登记了菜单项的元素：拦下原生菜单并开出菜单", () => {
		render(
			<>
				<Target spec={ITEMS}>row</Target>
				<ContextMenuHost />
			</>,
		);
		expect(rightClick(screen.getByText("row"))).toBe(true);
		expect(screen.getByTestId("context-menu")).toBeTruthy();
		expect(screen.getByRole("menuitem", { name: "置顶" })).toBeTruthy();
	});

	it("普通输入框、Shift+右键一律放行原生菜单", () => {
		render(
			<>
				<Target spec={ITEMS}>
					row
					<textarea aria-label="box" />
				</Target>
				<ContextMenuHost />
			</>,
		);
		expect(rightClick(screen.getByLabelText("box"))).toBe(false);
		expect(rightClick(screen.getByText("row"), { shiftKey: true })).toBe(false);
		expect(screen.queryByTestId("context-menu")).toBeNull();
	});

	it("没有菜单项：浏览器放行，桌面壳屏蔽且不开菜单", () => {
		render(
			<>
				<p>plain</p>
				<ContextMenuHost />
			</>,
		);
		expect(rightClick(screen.getByText("plain"))).toBe(false);
		h.desktop = {} as DesktopHost;
		expect(rightClick(screen.getByText("plain"))).toBe(true);
		expect(screen.queryByTestId("context-menu")).toBeNull();
	});

	it("spec 返回 suppress：拦下原生菜单但不开菜单", () => {
		render(
			<>
				<Target spec={{ items: () => "suppress" }}>term</Target>
				<ContextMenuHost />
			</>,
		);
		expect(rightClick(screen.getByText("term"))).toBe(true);
		expect(screen.queryByTestId("context-menu")).toBeNull();
	});

	it("祖先处理器已 preventDefault：不再开菜单", () => {
		render(
			<>
				{/* biome-ignore lint/a11y/noStaticElementInteractions: 模拟祖先组件自己处理右键 */}
				<div onContextMenu={(e) => e.preventDefault()}>
					<Target spec={ITEMS}>row</Target>
				</div>
				<ContextMenuHost />
			</>,
		);
		rightClick(screen.getByText("row"));
		expect(screen.queryByTestId("context-menu")).toBeNull();
	});

	it("聚焦登记过的按钮按 Shift+F10 出菜单，Esc 后焦点回到按钮", () => {
		render(
			<>
				<Target spec={ITEMS}>row</Target>
				<ContextMenuHost />
			</>,
		);
		const row = screen.getByText("row");
		row.focus();
		act(() => {
			fireEvent.keyDown(row, { key: "F10", shiftKey: true });
		});
		expect(screen.getByTestId("context-menu")).toBeTruthy();
		act(() => {
			fireEvent.keyDown(document.activeElement as Element, { key: "Escape" });
		});
		expect(screen.queryByTestId("context-menu")).toBeNull();
		expect(document.activeElement).toBe(row);
	});

	it("页面别处有选区、焦点在按钮上按 Shift+F10：只有按钮自己的项", () => {
		render(
			<>
				<p>some selected words</p>
				<Target spec={ITEMS}>row</Target>
				<ContextMenuHost />
			</>,
		);
		document.getSelection()?.selectAllChildren(screen.getByText(/selected/));
		const row = screen.getByText("row");
		row.focus();
		act(() => {
			fireEvent.keyDown(row, { key: "F10", shiftKey: true });
		});
		expect(screen.getByRole("menuitem", { name: "置顶" })).toBeTruthy();
		expect(screen.queryByRole("menuitem", { name: "复制" })).toBeNull();
	});
});
