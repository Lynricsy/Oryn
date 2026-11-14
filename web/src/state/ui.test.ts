/** 界面状态：改动标签的未看签名（首次记录即为基线，之后的新签名才算未看；在看改动标签时记为已看）、侧栏收起与窄屏同级标签。 */
import { renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	hasUnseenChanges,
	SIDEBAR_COLLAPSED_KEY,
	uiStore,
	useSidebarToggleShown,
} from "./ui";

const T = "T-ui-changes";
const sig = () => uiStore.getState().changesSig[T];

beforeEach(() => uiStore.setState({ changesSig: {} }));

describe("changesSig", () => {
	it("首次记录作为基线，不提示", () => {
		uiStore.getState().noteChanges(T, "a", false);
		expect(sig()).toEqual({ current: "a", seen: "a" });
		expect(hasUnseenChanges(sig())).toBe(false);
	});

	it("之后的新签名提示，标记已看后清除", () => {
		uiStore.getState().noteChanges(T, "a", false);
		uiStore.getState().noteChanges(T, "b", false);
		expect(hasUnseenChanges(sig())).toBe(true);
		uiStore.getState().markChangesSeen(T);
		expect(sig()).toEqual({ current: "b", seen: "b" });
		expect(hasUnseenChanges(sig())).toBe(false);
	});

	it("seen=true 的记录直接记为已看；改动清空（空签名）不提示", () => {
		uiStore.getState().noteChanges(T, "a", false);
		uiStore.getState().noteChanges(T, "b", true);
		expect(hasUnseenChanges(sig())).toBe(false);
		uiStore.getState().noteChanges(T, "", false);
		expect(sig()).toEqual({ current: "", seen: "b" });
		expect(hasUnseenChanges(sig())).toBe(false);
	});

	it("签名不变时不产生新状态", () => {
		uiStore.getState().noteChanges(T, "a", false);
		const before = uiStore.getState().changesSig;
		uiStore.getState().noteChanges(T, "a", false);
		uiStore.getState().markChangesSeen(T);
		expect(uiStore.getState().changesSig).toBe(before);
	});
});

describe("侧栏收起", () => {
	beforeEach(() => {
		localStorage.clear();
		uiStore.setState({ sidebarCollapsed: false, sidebarOpen: false });
	});

	it("expanded 宽度下 toggleSidebar 切换收起并写入 localStorage", () => {
		window.innerWidth = 1280;
		uiStore.getState().toggleSidebar();
		expect(uiStore.getState().sidebarCollapsed).toBe(true);
		expect(localStorage.getItem(SIDEBAR_COLLAPSED_KEY)).toBe("1");
		uiStore.getState().toggleSidebar();
		expect(uiStore.getState().sidebarCollapsed).toBe(false);
		expect(localStorage.getItem(SIDEBAR_COLLAPSED_KEY)).toBe("0");
	});

	it("compact / medium 宽度下 toggleSidebar 只开关抽屉 / 推入列，不动收起状态", () => {
		window.innerWidth = 390;
		uiStore.getState().toggleSidebar();
		expect(uiStore.getState()).toMatchObject({
			sidebarOpen: true,
			sidebarCollapsed: false,
		});
		window.innerWidth = 1024;
	});

	it("聚焦搜索时先展开收起的侧栏", () => {
		uiStore.getState().setSidebarCollapsed(true);
		uiStore.getState().focusSearch();
		expect(uiStore.getState().sidebarCollapsed).toBe(false);
	});

	it("重新加载模块（刷新页面）时同步读回收起状态", async () => {
		localStorage.setItem(SIDEBAR_COLLAPSED_KEY, "1");
		vi.resetModules();
		const fresh = await import("./ui");
		expect(fresh.uiStore.getState().sidebarCollapsed).toBe(true);
	});
});

function rect(x: number, y: number, width: number, height: number) {
	return { x, y, left: x, top: y, width, height } as DOMRectReadOnly;
}

describe("双段视口下的侧栏", () => {
	const initialWidth = window.innerWidth;
	beforeEach(() => {
		localStorage.clear();
		uiStore.setState({ sidebarCollapsed: false, sidebarOpen: false });
		window.innerWidth = 1030;
		window.viewport = {
			segments: [rect(0, 0, 500, 800), rect(530, 0, 500, 800)],
		};
	});
	afterEach(() => {
		window.innerWidth = initialWidth;
		delete window.viewport;
	});

	it("宽屏双段时 toggleSidebar 开关抽屉，不动收起状态", () => {
		uiStore.getState().toggleSidebar();
		expect(uiStore.getState()).toMatchObject({
			sidebarOpen: true,
			sidebarCollapsed: false,
		});
	});

	it("宽屏双段时侧栏开关总是显示；单段 expanded 只在收起后显示", () => {
		expect(renderHook(() => useSidebarToggleShown()).result.current).toBe(true);
		delete window.viewport;
		expect(renderHook(() => useSidebarToggleShown()).result.current).toBe(
			false,
		);
	});
});

describe("窄屏同级标签", () => {
	const initialWidth = window.innerWidth;
	beforeEach(() => {
		uiStore.setState({
			threadTab: "agent",
			panelTab: "info",
			panelOpen: false,
			panelMax: false,
		});
	});
	afterEach(() => {
		window.innerWidth = initialWidth;
	});

	it("setThreadTab 切到面板标签时同时切 panelTab；切回 agent 不动 panelTab", () => {
		uiStore.getState().setThreadTab("terminal");
		expect(uiStore.getState()).toMatchObject({
			threadTab: "terminal",
			panelTab: "terminal",
		});
		uiStore.getState().setThreadTab("agent");
		expect(uiStore.getState()).toMatchObject({
			threadTab: "agent",
			panelTab: "terminal",
		});
	});

	it("在看面板标签时 setPanelTab 同步同级标签；在 agent 上不切走", () => {
		uiStore.getState().setPanelTab("files");
		expect(uiStore.getState().threadTab).toBe("agent");
		uiStore.getState().setThreadTab("changes");
		uiStore.getState().setPanelTab("info");
		expect(uiStore.getState().threadTab).toBe("info");
	});

	it("窄屏下打开面板（终端、orb 胶囊等入口）切到面板当前标签；expanded 下只开面板列", () => {
		window.innerWidth = 390;
		uiStore.getState().setPanelTab("terminal");
		uiStore.getState().setPanel(true);
		expect(uiStore.getState()).toMatchObject({
			panelOpen: true,
			threadTab: "terminal",
		});

		uiStore.setState({ threadTab: "agent", panelOpen: false });
		window.innerWidth = 1280;
		uiStore.getState().setPanel(true);
		expect(uiStore.getState()).toMatchObject({
			panelOpen: true,
			threadTab: "agent",
		});
	});
});
