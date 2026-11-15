/**
 * 底栏（M9.6）：四项顺序与选中态、点击导航到记住的路径、滑动底板、桌面宽度下点已选中项不动、手机下沉侧栏里
 * 点已选中项收起侧栏而点别的分区不收起；助手项的文字与圆形头像按身份三种情况取值；助手未读点与断线状态点。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas, Thread } from "../../api/client";
import { connectionStore, indexStore } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { SectionTabBar } from "./SectionTabBar";

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
	const loc = useLocation();
	return <span data-testid="where">{loc.pathname + loc.search}</span>;
}

type Identity = Schemas["AgentIdentity"];
let identities: { agent: Identity; assistant: Identity };

function mount(path = "/t/T-1") {
	const qc = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={[path]}>
				<Routes>
					<Route
						path="*"
						element={
							<>
								<SectionTabBar tone="inset" />
								<Where />
							</>
						}
					/>
				</Routes>
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const tab = (key: string) =>
	document.querySelector(`[data-tab="${key}"]`) as HTMLElement;
const current = () =>
	Array.from(document.querySelectorAll("[data-tab][aria-current=true]")).map(
		(b) => b.getAttribute("data-tab"),
	);
const where = () => screen.getByTestId("where").textContent;
const plate = () => screen.getByTestId("section-tabbar-plate");

beforeEach(() => {
	window.innerWidth = 1280;
	identities = { agent: {}, assistant: {} };
	uiStore.setState({ sidebarOpen: false, sectionPaths: {} });
	connectionStore.setState({ status: "open" });
	indexStore.getState().setAll([], 1);
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL) => {
			const url = new URL(
				input instanceof Request ? input.url : String(input),
				"http://x",
			);
			if (url.pathname === "/api/v1/settings/agent-identity")
				return Response.json(identities.agent);
			if (url.pathname === "/api/v1/settings/assistant-identity")
				return Response.json(identities.assistant);
			return Response.json({});
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	indexStore.getState().setAll([], 0);
	window.innerWidth = 1024;
});

describe("SectionTabBar", () => {
	it("四项依次是 线程 / 全局助手 / 项目 / 更多，在主导航里；选中项随路由", () => {
		mount("/t/T-1");
		const nav = screen.getByRole("navigation", { name: "主导航" });
		expect(
			within(nav)
				.getAllByRole("button")
				.map((b) => b.textContent),
		).toEqual(["线程", "全局助手", "项目", "更多"]);
		expect(current()).toEqual(["threads"]);
		expect(tab("more").title).toBe("更多（Ctrl+4）");
	});

	it("点击导航到该分区记住的路径，选中项与底板随之移动", () => {
		uiStore.setState({
			sectionPaths: {
				more: "/settings/secrets",
				projects: "/projects/p1",
			},
		});
		mount("/t/T-1");
		expect(plate().style.left).toBe("0%");
		// 与分区轨道同步：200ms、--ease
		expect(plate().className).toContain("transition-[left]");
		expect(plate().className).toContain("duration-200");
		expect(plate().className).toContain("ease-[var(--ease)]");
		fireEvent.click(tab("more"));
		expect(where()).toBe("/settings/secrets");
		expect(current()).toEqual(["more"]);
		expect(plate().style.left).toBe("75%");
		fireEvent.click(tab("projects"));
		expect(where()).toBe("/projects/p1");
		expect(plate().style.left).toBe("50%");
		fireEvent.click(tab("assistant"));
		expect(where()).toBe("/assistant");
		expect(plate().style.left).toBe("25%");
	});

	it("桌面宽度下点已选中项不动", () => {
		uiStore.setState({ sectionPaths: { more: "/settings/about" } });
		mount("/settings/secrets");
		fireEvent.click(tab("more"));
		expect(where()).toBe("/settings/secrets");
		expect(uiStore.getState().sidebarOpen).toBe(false);
	});

	it("390 宽且侧栏打开：点别的分区不收起，点已选中项收起侧栏", () => {
		window.innerWidth = 390;
		uiStore.setState({ sidebarOpen: true });
		mount("/t/T-1");
		fireEvent.click(tab("assistant"));
		expect(where()).toBe("/assistant");
		expect(uiStore.getState().sidebarOpen).toBe(true);
		fireEvent.click(tab("assistant"));
		expect(where()).toBe("/assistant");
		expect(uiStore.getState().sidebarOpen).toBe(false);
	});

	it("medium 宽度的推入列里点已选中项不收起侧栏", () => {
		window.innerWidth = 700;
		uiStore.setState({ sidebarOpen: true });
		mount("/t/T-1");
		fireEvent.click(tab("threads"));
		expect(uiStore.getState().sidebarOpen).toBe(true);
	});

	it("助手项：都没设 → 「全局助手」+ sparkle 图标", async () => {
		mount();
		const item = tab("assistant");
		await waitFor(() => expect(fetch).toHaveBeenCalled());
		expect(item).toHaveTextContent("全局助手");
		expect(item.querySelector("[data-avatar-of]")).toBeNull();
		expect(item.querySelector("svg")).not.toBeNull();
	});

	it("助手项：只设了 agent → 仍是「全局助手」+ sparkle（不跟随 Agent 身份）", async () => {
		identities.agent = { name: "Oryn", avatarIcon: "🦊" };
		mount();
		const item = tab("assistant");
		await waitFor(() => expect(fetch).toHaveBeenCalled());
		expect(item).toHaveTextContent("全局助手");
		expect(item).not.toHaveTextContent("Oryn");
		expect(item.querySelector("[data-avatar-of]")).toBeNull();
		expect(item.querySelector("svg")).not.toBeNull();
	});

	it("助手项：助手单独设了 → 用助手自己的名字与头像", async () => {
		identities.agent = { name: "Oryn", avatarIcon: "🦊" };
		identities.assistant = {
			name: "小助手",
			avatarImage: "data:image/png;base64,AAAA",
		};
		mount();
		const item = tab("assistant");
		await waitFor(() => expect(item).toHaveTextContent("小助手"));
		expect(item).not.toHaveTextContent("Oryn");
		const avatar = item.querySelector("img[data-avatar-of]") as HTMLElement;
		expect(avatar).toHaveAttribute("src", "data:image/png;base64,AAAA");
		expect(avatar.className).toContain("rounded-full");
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
		expect(within(tab("more")).getByTestId("tabbar-conn")).toBeVisible();
		act(() => connectionStore.setState({ status: "open" }));
		expect(screen.queryByTestId("tabbar-conn")).toBeNull();
	});

	it("底栏不能拖动窗口（没有 data-window-drag）", () => {
		mount();
		const bar = screen.getByTestId("section-tabbar");
		expect(bar.closest("[data-window-drag]")).toBeNull();
		expect(bar.querySelector("[data-window-drag]")).toBeNull();
	});
});
