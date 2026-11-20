/**
 * 当前打开的上下文菜单（全局只有一个）。宿主见 `components/ui/ContextMenu.tsx`。
 */
import { createStore } from "zustand/vanilla";
import type { ContextTrigger, MenuGroup } from "./types";

export interface OpenContextMenu {
	groups: MenuGroup[];
	x: number;
	y: number;
	trigger: ContextTrigger;
	/** 键盘触发时关闭后把焦点还给它 */
	restoreFocus: HTMLElement | null;
}

export const contextMenuStore = createStore<{ menu: OpenContextMenu | null }>(
	() => ({ menu: null }),
);

/** 打开菜单；已有打开的直接替换。 */
export function openContextMenu(menu: OpenContextMenu): void {
	contextMenuStore.setState({ menu });
}

/** 关闭菜单；`restoreFocus` 为真且记下的元素仍在文档里时把焦点还给它。 */
export function closeContextMenu(restoreFocus?: boolean): void {
	const menu = contextMenuStore.getState().menu;
	if (!menu) return;
	contextMenuStore.setState({ menu: null });
	const el = menu.restoreFocus;
	if (restoreFocus && el?.isConnected) el.focus({ preventScroll: true });
}
