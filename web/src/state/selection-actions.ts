/**
 * 自定义选区快捷动作（选中文字后右键菜单里的动作，提示词连同选区交给模型）。数据存服务端，
 * 这里是给非 React 代码（菜单组构造）读的同步副本，由 AppShell 里的 `useSelectionActionsSync` 跟查询缓存对齐。
 */
import { useEffect } from "react";
import { createStore } from "zustand/vanilla";
import { useSelectionActions } from "../api/selection-actions-queries";

export interface SelectionAction {
	id: string;
	name: string;
	prompt: string;
}

export const selectionActionsStore = createStore<{
	actions: SelectionAction[];
}>(() => ({ actions: [] }));

/** 把查询到的动作列表写进 `selectionActionsStore`；`enabled=false`（尚未鉴权）时不发请求。 */
export function useSelectionActionsSync(enabled: boolean): void {
	const data = useSelectionActions(enabled).data;
	useEffect(() => {
		if (data) selectionActionsStore.setState({ actions: data.actions });
	}, [data]);
}
