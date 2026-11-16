/**
 * 命令面板的开关状态（面板本体按需加载，状态留在主包里，快捷键与入口按钮都经它打开）。
 * 打开时记下当时的焦点元素；面板卸载（撤掉背景 inert）之后把焦点还原过去——因选中结果而跳转时不还原，
 * 焦点留给新页面。
 */
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

interface PaletteState {
	open: boolean;
	/** 关闭后要还原焦点的元素（打开面板之前的焦点；跳转关闭时为 null） */
	opener: HTMLElement | null;
}

export const paletteStore = createStore<PaletteState>(() => ({
	open: false,
	opener: null,
}));

export function openPalette(): void {
	if (paletteStore.getState().open) return;
	const active = document.activeElement;
	paletteStore.setState({
		open: true,
		opener: active instanceof HTMLElement ? active : null,
	});
}

/** 关闭面板；`restoreFocus` 为假（选中结果、即将跳转）时不把焦点还给打开前的元素。 */
export function closePalette(restoreFocus = true): void {
	const { open, opener } = paletteStore.getState();
	if (open)
		paletteStore.setState({
			open: false,
			opener: restoreFocus ? opener : null,
		});
}

export function togglePalette(): void {
	if (paletteStore.getState().open) closePalette();
	else openPalette();
}

export function usePaletteOpen(): boolean {
	return useStore(paletteStore, (s) => s.open);
}
