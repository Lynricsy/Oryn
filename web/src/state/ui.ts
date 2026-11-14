/** 界面状态（与协议无关）：当前打开的对话框、侧边栏（开关 / 收起）/ 右侧面板（标签、宽度、最大化、改动标签的未看签名）、窄屏线程页的同级标签、搜索聚焦请求。 */
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { Schemas } from "../api/client";
import type { ReportKind } from "../lib/slash";
import {
	breakpointFor,
	readSegments,
	useBreakpoint,
	useViewportSegments,
} from "../lib/viewport";

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
	| { kind: "share" }
	| { kind: "setupLog"; orbId: string; run: Schemas["OrbSetupRun"] };

export type PanelTab = "info" | "changes" | "files" | "terminal" | "portal";

/** 窄屏（compact / medium、非双段）线程页的同级标签：对话（转录）与面板各标签并列。 */
export type ThreadTab = "agent" | PanelTab;

export const PANEL_WIDTH_KEY = "oryn.panel.width";
/** 下限保证四个标签与关闭 / 最大化按钮在一行里放得下（第五个 Portal 标签出现且放不下时，未选中的标签只显示图标）。 */
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

/** 当前线程页布局：expanded 三栏、双段视口（左右两栏）、窄屏同级标签。 */
function threadLayout(): "expanded" | "dual" | "tabs" {
	if (typeof window !== "undefined" && readSegments(window)) return "dual";
	return breakpointFor(viewportWidth()) === "expanded" ? "expanded" : "tabs";
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

export const COLLAPSED_CARDS_KEY = "oryn.panel.collapsed";

/** 同步读取（store 创建时），首帧就按存储的收起状态渲染；非法值回落为全部展开。 */
export function loadCollapsedCards(): Record<string, boolean> {
	try {
		const raw = globalThis.localStorage?.getItem(COLLAPSED_CARDS_KEY);
		const parsed: unknown = raw ? JSON.parse(raw) : null;
		if (!parsed || typeof parsed !== "object" || Array.isArray(parsed))
			return {};
		return Object.fromEntries(
			Object.entries(parsed).filter(([, v]) => v === true),
		);
	} catch {
		return {};
	}
}

export const SIDEBAR_COLLAPSED_KEY = "oryn.sidebar.collapsed";

/** 同步读取（store 创建时），桌面端首帧就按存储的收起状态渲染。 */
function loadSidebarCollapsed(): boolean {
	try {
		return globalThis.localStorage?.getItem(SIDEBAR_COLLAPSED_KEY) === "1";
	} catch {
		return false;
	}
}

export interface UiState {
	dialog: DialogState | null;
	/** 侧边栏打开：medium 是推入式列、compact 是抽屉（不持久化）。 */
	sidebarOpen: boolean;
	/** expanded 布局下侧边栏收起（存 localStorage）。 */
	sidebarCollapsed: boolean;
	panelOpen: boolean;
	panelTab: PanelTab;
	/** 窄屏线程页当前的同级标签（不持久化，换线程回到 agent）；非 agent 时与 `panelTab` 同步。 */
	threadTab: ThreadTab;
	/** expanded 布局下的面板列宽（px），所有标签共用。 */
	panelWidth: number;
	/** 面板占满内容区（转录列隐藏）。 */
	panelMax: boolean;
	/** 每次请求聚焦侧边栏搜索时递增。 */
	searchNonce: number;
	open(d: DialogState): void;
	close(): void;
	setSidebar(open: boolean): void;
	setSidebarCollapsed(collapsed: boolean): void;
	/** 页头开关与 Ctrl/⌘+B：expanded 收起 / 展开，medium 推入 / 收回，compact 与双段视口开关抽屉。 */
	toggleSidebar(): void;
	/** 打开面板；窄屏同级标签布局下同时切到面板当前标签（终端、信息胶囊等入口在手机上也能直达）。 */
	setPanel(open: boolean): void;
	/** 切面板标签；正在看面板标签（threadTab 非 agent）时同级标签跟着切。 */
	setPanelTab(tab: PanelTab): void;
	/** 切窄屏同级标签；非 agent 时同时切面板标签。 */
	setThreadTab(tab: ThreadTab): void;
	setPanelWidth(width: number): void;
	togglePanelMax(): void;
	focusSearch(): void;
	changesSig: Record<string, ChangesSig>;
	/** 记录后台检查得到的改动签名；`seen` 为真或该线程首次记录时同时记为已看（只提示之后的新改动）。 */
	noteChanges(threadId: string, sig: string, seen: boolean): void;
	/** 用户正在看改动标签：最新签名记为已看。 */
	markChangesSeen(threadId: string): void;
	/** 信息面板里收起的卡片（按卡片 key，所有线程共用），存 localStorage。 */
	collapsedCards: Record<string, boolean>;
	toggleCard(key: string): void;
	/** Portal 标签里选中的端口（按线程，不持久化）；缺省 = 第一个 portal。 */
	portalPort: Record<string, number>;
	setPortalPort(threadId: string, port: number): void;
}

export const uiStore = createStore<UiState>((set, get) => ({
	dialog: null,
	sidebarOpen: false,
	sidebarCollapsed: loadSidebarCollapsed(),
	panelOpen: typeof window === "undefined" || window.innerWidth >= 840,
	panelTab: "info",
	threadTab: "agent",
	panelWidth: loadPanelWidth(),
	panelMax: false,
	searchNonce: 0,
	open: (dialog) => set({ dialog }),
	close: () => set({ dialog: null }),
	setSidebar: (sidebarOpen) => set({ sidebarOpen }),
	setSidebarCollapsed: (sidebarCollapsed) => {
		try {
			globalThis.localStorage?.setItem(
				SIDEBAR_COLLAPSED_KEY,
				sidebarCollapsed ? "1" : "0",
			);
		} catch {
			// 隐私模式下静默
		}
		set({ sidebarCollapsed });
	},
	toggleSidebar: () => {
		const s = get();
		// 双段视口里侧栏是抽屉（与 compact 相同），按开关抽屉处理
		if (threadLayout() === "expanded")
			s.setSidebarCollapsed(!s.sidebarCollapsed);
		else set({ sidebarOpen: !s.sidebarOpen });
	},
	setPanel: (panelOpen) =>
		set((s) =>
			panelOpen && threadLayout() === "tabs"
				? { panelOpen, threadTab: s.panelTab }
				: { panelOpen },
		),
	setPanelTab: (panelTab) =>
		set((s) => ({
			panelTab,
			threadTab: s.threadTab === "agent" ? "agent" : panelTab,
			// 最大化只对内容标签有意义
			panelMax: panelTab === "info" ? false : s.panelMax,
		})),
	setThreadTab: (threadTab) => {
		if (threadTab === "agent") set({ threadTab });
		else {
			get().setPanelTab(threadTab);
			set({ threadTab });
		}
	},
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
	focusSearch: () => {
		// 侧栏收起时先展开，搜索框才可聚焦
		if (get().sidebarCollapsed) get().setSidebarCollapsed(false);
		set((s) => ({ searchNonce: s.searchNonce + 1, sidebarOpen: true }));
	},
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
	collapsedCards: loadCollapsedCards(),
	toggleCard: (key) =>
		set((s) => {
			const collapsedCards = { ...s.collapsedCards };
			if (collapsedCards[key]) delete collapsedCards[key];
			else collapsedCards[key] = true;
			try {
				globalThis.localStorage?.setItem(
					COLLAPSED_CARDS_KEY,
					JSON.stringify(collapsedCards),
				);
			} catch {
				// 忽略
			}
			return { collapsedCards };
		}),
	portalPort: {},
	setPortalPort: (threadId, port) =>
		set((s) =>
			s.portalPort[threadId] === port
				? s
				: { portalPort: { ...s.portalPort, [threadId]: port } },
		),
}));

export function useUi<T>(selector: (s: UiState) => T): T {
	return useStore(uiStore, selector);
}

/**
 * 页头左上角的侧栏开关是否显示：非 expanded 时总在（开抽屉 / 推入列），双段视口里侧栏是抽屉也总在，
 * expanded 时只在侧栏收起后出现。
 */
export function useSidebarToggleShown(): boolean {
	const bp = useBreakpoint();
	const segments = useViewportSegments();
	const collapsed = useUi((s) => s.sidebarCollapsed);
	return bp !== "expanded" || segments !== null || collapsed;
}
