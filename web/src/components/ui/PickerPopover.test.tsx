import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { Popover } from "./PickerPopover";
import { Dialog } from "./primitives";

/** 胶囊 + 弹层（与首页新建线程框、定时对话框的模型选择同一用法）。 */
function Picker() {
	const [open, setOpen] = useState(false);
	return (
		<div className="relative">
			<button
				type="button"
				aria-expanded={open}
				onClick={() => setOpen((v) => !v)}
			>
				pill
			</button>
			{open && (
				<Popover onClose={() => setOpen(false)}>
					{/* biome-ignore lint/a11y/noAutofocus: 与 ModelOptions 的输入框一致 */}
					<input aria-label="filter" autoFocus />
				</Popover>
			)}
		</div>
	);
}

/** 定时对话框里的模型选择：弹层开在对话框内。 */
function InDialog() {
	const [open, setOpen] = useState(true);
	return (
		<Dialog open={open} onClose={() => setOpen(false)} title="Schedule">
			<Picker />
		</Dialog>
	);
}

describe("PickerPopover 键盘", () => {
	it("Escape 关闭弹层，焦点回到打开它的胶囊", () => {
		render(<Picker />);
		const pill = screen.getByRole("button", { name: "pill" });
		pill.focus();
		fireEvent.click(pill);
		const filter = screen.getByRole("textbox", { name: "filter" });
		expect(document.activeElement).toBe(filter);

		fireEvent.keyDown(filter, { key: "Escape" });
		expect(screen.queryByRole("textbox", { name: "filter" })).toBeNull();
		expect(pill).toHaveAttribute("aria-expanded", "false");
		expect(document.activeElement).toBe(pill);
	});

	it("弹层在对话框里时，Escape 只关弹层、不关对话框", () => {
		render(<InDialog />);
		fireEvent.click(screen.getByRole("button", { name: "pill" }));
		const filter = screen.getByRole("textbox", { name: "filter" });

		fireEvent.keyDown(filter, { key: "Escape" });
		expect(screen.queryByRole("textbox", { name: "filter" })).toBeNull();
		expect(screen.getByRole("dialog")).toBeInTheDocument();

		// 弹层关掉之后，Escape 照常关对话框
		fireEvent.keyDown(screen.getByRole("button", { name: "pill" }), {
			key: "Escape",
		});
		expect(screen.queryByRole("dialog")).toBeNull();
	});
});
