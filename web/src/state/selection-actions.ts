/**
 * 自定义选区快捷动作（选中文字后右键菜单里的动作，提示词连同选区交给模型）。数据存服务端，
 * 这里是给非 React 代码（菜单组构造）读的同步副本。
 */
import { createStore } from "zustand/vanilla";

export interface SelectionAction {
	id: string;
	name: string;
	prompt: string;
}

export const selectionActionsStore = createStore<{
	actions: SelectionAction[];
}>(() => ({ actions: [] }));
