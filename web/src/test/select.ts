/**
 * 单测里经共享 `Select` 选一项：点开 combobox，再点 listbox 里按名称找到的选项。
 * jsdom 没有 `matchMedia`，`useCoarsePointer` 为 false，渲染的是自绘 combobox 而不是原生 select。
 * 选项的无障碍名是标签加 hint，带 hint 的选项用正则匹配。
 */
import { fireEvent, screen } from "@testing-library/react";

export function pickOption(combobox: HTMLElement, name: string | RegExp) {
	fireEvent.click(combobox);
	fireEvent.click(screen.getByRole("option", { name }));
}
