/** 手机底部胶囊（M9）：四项的选中条件、助手项跳转并收起侧栏、助手未读点与连接状态点。 */
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { Thread } from "../../api/client";
import { connectionStore, indexStore } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { SinkTabBar, selectedTab } from "./SinkTabBar";

function th(id: string, p: Partial<Thread> = {}): Thread {
	return {
		id,
		title: id,
		titleSource: "auto",
		state: "idle",
		stateChangedAt: 1,
		pinned: false,
		labels: [],
		unread: false,
		createdAt: 1,
		updatedAt: 1,
		...p,
	};
}

function Where() {
	return <span data-testid="where">{useLocation().pathname}</span>;
}

function mount(path = "/t/T-1") {
	return render(
		<MemoryRouter initialEntries={[path]}>
			<Routes>
				<Route
					path="*"
					element={
						<>
							<SinkTabBar />
							<Where />
						</>
					}
				/>
			</Routes>
		</MemoryRouter>,
	);
}

const tab = (key: string) =>
	document.querySelector(`[data-tab="${key}"]`) as HTMLElement;
const current = () =>
	Array.from(document.querySelectorAll("[data-tab][aria-current=true]")).map(
		(b) => b.getAttribute("data-tab"),
	);

beforeEach(() => {
	uiStore.setState({ sidebarOpen: true, sidebarView: "threads" });
	connectionStore.setState({ status: "open" });
	indexStore.getState().setAll([], 1);
});

afterEach(() => {
	indexStore.getState().setAll([], 0);
});

describe("selectedTab", () => {
	it("项目 / 更多视图优先，其次是助手路由，否则线程", () => {
		expect(selectedTab("threads", "/t/x")).toBe("threads");
		expect(selectedTab("threads", "/assistant/T-1")).toBe("assistant");
		expect(selectedTab("projects", "/assistant")).toBe("projects");
		expect(selectedTab("more", "/")).toBe("more");
	});
});

describe("SinkTabBar", () => {
	it("四项依次是 线程 / 全局助手 / 项目 / 更多，在主导航里", () => {
		mount();
		const nav = screen.getByRole("navigation", { name: "主导航" });
		expect(
			within(nav)
				.getAllByRole("button")
				.map((b) => b.textContent),
		).toEqual(["线程", "全局助手", "项目", "更多"]);
		expect(current()).toEqual(["threads"]);
	});

	it("项目 / 更多切换侧栏视图，选中项随之移动", () => {
		mount();
		fireEvent.click(tab("projects"));
		expect(uiStore.getState().sidebarView).toBe("projects");
		expect(current()).toEqual(["projects"]);
		expect(screen.getByTestId("sink-tabbar-plate").style.left).toBe("50%");
		fireEvent.click(tab("more"));
		expect(current()).toEqual(["more"]);
		fireEvent.click(tab("threads"));
		expect(uiStore.getState().sidebarView).toBe("threads");
		expect(current()).toEqual(["threads"]);
	});

	it("全局助手：跳到 /assistant 并收起侧栏；在助手路由上时它是选中项", () => {
		mount();
		fireEvent.click(tab("assistant"));
		expect(screen.getByTestId("where")).toHaveTextContent("/assistant");
		expect(uiStore.getState().sidebarOpen).toBe(false);
		expect(current()).toEqual(["assistant"]);
	});

	it("任一未归档助手对话未读时助手项带未读点", () => {
		mount();
		expect(screen.queryByTestId("tabbar-unread")).toBeNull();
		act(() =>
			indexStore
				.getState()
				.upsertThread(th("T-a", { kind: "assistant", unread: true })),
		);
		expect(screen.getByTestId("tabbar-unread")).toBeVisible();
		act(() =>
			indexStore
				.getState()
				.upsertThread(
					th("T-a", { kind: "assistant", unread: true, archivedAt: 2 }),
				),
		);
		expect(screen.queryByTestId("tabbar-unread")).toBeNull();
		// 工作线程未读不算
		act(() => indexStore.getState().upsertThread(th("T-w", { unread: true })));
		expect(screen.queryByTestId("tabbar-unread")).toBeNull();
	});

	it("连接不是 open 时「更多」带状态点", () => {
		mount();
		expect(screen.queryByTestId("tabbar-conn")).toBeNull();
		act(() => connectionStore.setState({ status: "reconnecting" }));
		expect(screen.getByTestId("tabbar-conn")).toBeVisible();
		act(() => connectionStore.setState({ status: "open" }));
		expect(screen.queryByTestId("tabbar-conn")).toBeNull();
	});

	it("侧栏关闭时视图复位为线程", () => {
		mount();
		fireEvent.click(tab("more"));
		act(() => uiStore.getState().setSidebar(false));
		expect(uiStore.getState().sidebarView).toBe("threads");
	});
});
