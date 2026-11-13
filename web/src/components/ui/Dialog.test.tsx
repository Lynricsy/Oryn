import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { Dialog } from "./primitives";

function Harness() {
	const [open, setOpen] = useState(false);
	return (
		<div data-testid="app">
			<textarea aria-label="composer" />
			<button type="button" onClick={() => setOpen(true)}>
				open
			</button>
			<Dialog
				open={open}
				onClose={() => setOpen(false)}
				title="Tree"
				footer={<button type="button">last</button>}
			>
				<button type="button">first</button>
				<input aria-label="middle" />
			</Dialog>
		</div>
	);
}

describe("Dialog 焦点管理（W9）", () => {
	it("打开时焦点进入对话框、背景 inert、Tab 循环、关闭后焦点复原", () => {
		render(<Harness />);
		const composer = screen.getByRole("textbox", { name: "composer" });
		const opener = screen.getByRole("button", { name: "open" });
		opener.focus();
		fireEvent.click(opener);

		const first = screen.getByRole("button", { name: "first" });
		expect(document.activeElement).toBe(first);
		// 背景（应用根）被设为 inert，对话框宿主不是
		const appRoot = screen.getByTestId("app").parentElement as HTMLElement;
		expect(appRoot.closest("[inert]")).not.toBeNull();
		const dialog = screen.getByRole("dialog");
		expect(dialog.closest("[inert]")).toBeNull();
		expect(composer.closest("[inert]")).not.toBeNull();

		// Tab 从最后一个回到第一个；Shift+Tab 从第一个到最后一个
		const last = screen.getByRole("button", { name: "last" });
		last.focus();
		fireEvent.keyDown(last, { key: "Tab" });
		expect(document.activeElement).toBe(dialog.querySelector("button"));
		const closeBtn = dialog.querySelector("button") as HTMLElement;
		fireEvent.keyDown(closeBtn, { key: "Tab", shiftKey: true });
		expect(document.activeElement).toBe(last);

		fireEvent.keyDown(last, { key: "Escape" });
		expect(screen.queryByRole("dialog")).toBeNull();
		expect(appRoot.closest("[inert]")).toBeNull();
		expect(document.activeElement).toBe(opener);
	});
});

/** 对话框内容带 autoFocus（如模型对话框的搜索框）；`mountOnOpen` 时对话框随打开才挂载（`open` 恒为 true）。 */
function AutoFocusHarness({ mountOnOpen }: { mountOnOpen: boolean }) {
	const [open, setOpen] = useState(false);
	const dialog = (
		<Dialog
			open={mountOnOpen || open}
			onClose={() => setOpen(false)}
			title="Model"
		>
			<input aria-label="search" autoFocus />
		</Dialog>
	);
	return (
		<div>
			<button type="button" onClick={() => setOpen(true)}>
				open
			</button>
			{mountOnOpen ? open && dialog : dialog}
		</div>
	);
}

describe("Dialog 焦点还原（内容 autoFocus）", () => {
	it.each([
		["open 由假变真", false],
		["随打开挂载", true],
	])("%s：Esc 关闭后焦点回到触发按钮", (_name, mountOnOpen) => {
		render(<AutoFocusHarness mountOnOpen={mountOnOpen} />);
		const opener = screen.getByRole("button", { name: "open" });
		opener.focus();
		fireEvent.click(opener);
		expect(document.activeElement).toBe(
			screen.getByRole("textbox", { name: "search" }),
		);

		fireEvent.keyDown(document.activeElement as HTMLElement, {
			key: "Escape",
		});
		expect(screen.queryByRole("dialog")).toBeNull();
		expect(document.activeElement).toBe(opener);
	});
});
