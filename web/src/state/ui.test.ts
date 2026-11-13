/** 界面状态：改动标签的未看签名（首次记录即为基线，之后的新签名才算未看；在看改动标签时记为已看）与侧栏收起。 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { hasUnseenChanges, SIDEBAR_COLLAPSED_KEY, uiStore } from "./ui";

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
