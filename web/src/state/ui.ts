/** 界面状态（与协议无关）：当前打开的对话框、侧边栏 / 右侧面板（标签、宽度、最大化、改动标签的未看签名）、搜索聚焦请求。 */
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
	| { kind: "attachments" }
	| { kind: "setupLog"; orbId: string; run: Schemas["OrbSetupRun"] };

export type PanelTab = "info" | "changes" | "files" | "terminal";

export const PANEL_WIDTH_KEY = "oryn.panel.width";
/** 下限保证四个标签与关闭 / 最大化按钮在一行里放得下。 */
export const PANEL_MIN_WIDTH = 360;
const PANEL_DEFAULT_WIDTH = 420;

/** 面板宽度夹到 [PANEL_MIN_WIDTH, 视口 70%]（视口过窄时下限优先）。 */
export function clampPanelWidth(width: number, viewport: number): number {
	const max = Math.max(PANEL_MIN_WIDTH, Math.floor(viewport * 0.7));
	if (!Number.isFinite(width)) return Math.min(PANEL_DEFAULT_WIDTH, max);
	return Math.round(Math.min(max, Math.max(PANEL_MIN_WIDTH, width)));
}

function viewportWidth(): number {
	return typeof window === "undefined" ? 1280 : window.innerWidth;
}

function loadPanelWidth(): number {
	let stored = Number.NaN;
	try {
		const raw = globalThis.localStorage?.getItem(PANEL_WIDTH_KEY);
		if (raw) stored = Number(raw);
	} catch {
		// 隐私模式下 localStorage 不可用
	}
	return clampPanelWidth(
		Number.isFinite(stored) ? stored : PANEL_DEFAULT_WIDTH,
		viewportWidth(),
	);
}

/** 改动标签的签名（按线程）：`current` 是最近一次后台检查的结果，`seen` 是用户在改动标签看过的。 */
export interface ChangesSig {
	current: string;
	seen: string;
}

/** 有未查看的新改动：最新签名非空（有改动）且与看过的不同。 */
export function hasUnseenChanges(sig: ChangesSig | undefined): boolean {
	return !!sig && sig.current !== "" && sig.current !== sig.seen;
}

export interface UiState {
	dialog: DialogState | null;
	sidebarOpen: boolean;
	panelOpen: boolean;
	panelTab: PanelTab;
	/** expanded 布局下的面板列宽（px），所有标签共用。 */
	panelWidth: number;
	/** 面板占满内容区（转录列隐藏）。 */
	panelMax: boolean;
	/** 每次请求聚焦侧边栏搜索时递增。 */
	searchNonce: number;
	open(d: DialogState): void;
	close(): void;
	setSidebar(open: boolean): void;
	setPanel(open: boolean): void;
	setPanelTab(tab: PanelTab): void;
	setPanelWidth(width: number): void;
	togglePanelMax(): void;
	focusSearch(): void;
	changesSig: Record<string, ChangesSig>;
	/** 记录后台检查得到的改动签名；`seen` 为真或该线程首次记录时同时记为已看（只提示之后的新改动）。 */
	noteChanges(threadId: string, sig: string, seen: boolean): void;
	/** 用户正在看改动标签：最新签名记为已看。 */
	markChangesSeen(threadId: string): void;
}

export const uiStore = createStore<UiState>((set) => ({
	dialog: null,
	sidebarOpen: false,
	panelOpen: typeof window === "undefined" || window.innerWidth >= 840,
	panelTab: "info",
	panelWidth: loadPanelWidth(),
	panelMax: false,
	searchNonce: 0,
	open: (dialog) => set({ dialog }),
	close: () => set({ dialog: null }),
	setSidebar: (sidebarOpen) => set({ sidebarOpen }),
	setPanel: (panelOpen) => set({ panelOpen }),
	setPanelTab: (panelTab) =>
		set((s) => ({
			panelTab,
			// 最大化只对内容标签有意义
			panelMax: panelTab === "info" ? false : s.panelMax,
		})),
	setPanelWidth: (width) => {
		const panelWidth = clampPanelWidth(width, viewportWidth());
		try {
			globalThis.localStorage?.setItem(PANEL_WIDTH_KEY, String(panelWidth));
		} catch {
			// 忽略
		}
		set({ panelWidth });
	},
	togglePanelMax: () => set((s) => ({ panelMax: !s.panelMax })),
	focusSearch: () =>
		set((s) => ({ searchNonce: s.searchNonce + 1, sidebarOpen: true })),
	changesSig: {},
	noteChanges: (threadId, sig, seen) =>
		set((s) => {
			const prev = s.changesSig[threadId];
			const next = { current: sig, seen: seen || !prev ? sig : prev.seen };
			if (prev && prev.current === next.current && prev.seen === next.seen)
				return s;
			return { changesSig: { ...s.changesSig, [threadId]: next } };
		}),
	markChangesSeen: (threadId) =>
		set((s) => {
			const prev = s.changesSig[threadId];
			if (!prev || prev.seen === prev.current) return s;
			return {
				changesSig: {
					...s.changesSig,
					[threadId]: { ...prev, seen: prev.current },
				},
			};
		}),
}));

export function useUi<T>(selector: (s: UiState) => T): T {
	return useStore(uiStore, selector);
}
