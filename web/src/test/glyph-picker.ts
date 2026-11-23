/**
 * 单测里经头像编辑器的「内置图标」弹层点选一个字形。弹层网格是虚拟滚动，jsdom 量不出尺寸：
 * 点选期间给元素一个可见的 client 尺寸，结束即还原。按按钮文字找字形——目录加载前后无障碍名会从字符本身变成名字。
 */
import { fireEvent, screen, within } from "@testing-library/react";
import { vi } from "vitest";
import i18n from "../i18n";

export function pickAvatarGlyph(scope: HTMLElement, char: string) {
	const h = vi
		.spyOn(HTMLElement.prototype, "clientHeight", "get")
		.mockReturnValue(288);
	const w = vi
		.spyOn(HTMLElement.prototype, "clientWidth", "get")
		.mockReturnValue(340);
	try {
		const name = i18n.t("appearance.avatarGlyphs");
		fireEvent.click(within(scope).getByRole("button", { name }));
		const dialog = screen.getByRole("dialog", { name });
		fireEvent.click(within(dialog).getByText(char, { selector: "button" }));
	} finally {
		h.mockRestore();
		w.mockRestore();
	}
}
