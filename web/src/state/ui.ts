/** 界面状态（与协议无关）：当前打开的对话框、侧边栏 / 信息面板开关、搜索聚焦请求。 */
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { Schemas } from "../api/client";
import type { ReportKind } from "../lib/slash";

export type DialogState =
	| { kind: "tree" }
	| { kind: "fork"; entryId?: string }
	| { kind: "report"; report: ReportKind }
	| { kind: "model" }
	| { kind: "subagent"; id: string }
	| { kind: "btw" }
	| { kind: "goal" }
	| { kind: "label"; entryId: string }
	| { kind: "labels" }
	| { kind: "rename" }
	| { kind: "debug" }
	| { kind: "setupLog"; orbId: string; run: Schemas["OrbSetupRun"] };

export interface UiState {
	dialog: DialogState | null;
	sidebarOpen: boolean;
	panelOpen: boolean;
	/** 每次请求聚焦侧边栏搜索时递增。 */
	searchNonce: number;
	open(d: DialogState): void;
	close(): void;
	setSidebar(open: boolean): void;
	setPanel(open: boolean): void;
	focusSearch(): void;
}

export const uiStore = createStore<UiState>((set) => ({
	dialog: null,
	sidebarOpen: false,
	panelOpen: typeof window === "undefined" || window.innerWidth >= 840,
	searchNonce: 0,
	open: (dialog) => set({ dialog }),
	close: () => set({ dialog: null }),
	setSidebar: (sidebarOpen) => set({ sidebarOpen }),
	setPanel: (panelOpen) => set({ panelOpen }),
	focusSearch: () =>
		set((s) => ({ searchNonce: s.searchNonce + 1, sidebarOpen: true })),
}));

export function useUi<T>(selector: (s: UiState) => T): T {
	return useStore(uiStore, selector);
}
