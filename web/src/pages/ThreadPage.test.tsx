/**
 * 线程页布局（M7 4.2 / 4.3）：窄屏转录与面板各标签是同级标签（没有右抽屉），切走时转录纸面透明、inert 但保持挂载与布局；
 * expanded 三栏不变；折叠屏双段视口左段转录、右段面板常驻；compact 下输入坞托盘底角与屏幕圆角同心。
 * 转录、输入坞各节、页头与面板内容换成桩，只看页面自己的布局；标签条用真的 `ThreadTabStrip`。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../api/client";
import type * as RightPanelModule from "../components/thread/RightPanel";
import type * as ThreadHeaderModule from "../components/thread/ThreadHeader";
import { getThreadStore, indexStore, resetThreadStores } from "../state/stores";
import { uiStore } from "../state/ui";
import { ThreadPage } from "./ThreadPage";

vi.mock("../realtime/socket", () => ({
	realtime: { subscribe: () => () => {}, focus: () => {} },
}));
vi.mock("../state/terminals", () => ({
	terminalHub: { attach: () => () => {} },
	useTerminals: () => false,
}));
vi.mock("../components/transcript/Transcript", () => ({
	Transcript: ({ insetTop }: { insetTop: number }) => (
		<div data-testid="transcript" data-inset-top={insetTop} />
	),
}));
vi.mock("../components/transcript/WorkingFooter", () => ({
	WorkingFooter: () => null,
}));
vi.mock("../components/composer/Composer", () => ({ Composer: () => null }));
vi.mock("../components/composer/DockTray", () => ({
	dockArrived: () => {},
	takeArrival: () => false,
	useDockMorph: () => "idle",
}));
vi.mock("../components/thread/Dialogs", () => ({
	PlanProposalDialog: () => null,
	ThreadDialogs: () => null,
}));
vi.mock("../components/thread/EnvBanner", () => ({ EnvBanner: () => null }));
vi.mock("../components/thread/ModeBar", () => ({
	AboveEditorWidgets: () => null,
	BelowEditorWidgets: () => null,
}));
vi.mock("../components/thread/QueueBar", () => ({ QueueBar: () => null }));
vi.mock("../components/thread/UiRequestCard", () => ({
	UiRequestCard: () => null,
}));
vi.mock("../components/thread/ThreadHeader", async (orig) => ({
	...(await orig<typeof ThreadHeaderModule>()),
	ThreadHeader: ({
		sidebarToggle,
		veil,
	}: {
		sidebarToggle: boolean;
		veil?: number;
	}) => (
		<header
			data-testid="thread-header"
			data-sidebar-toggle={String(sidebarToggle)}
			data-veil={veil}
		/>
	),
}));
vi.mock("../components/thread/RightPanel", async (orig) => ({
	...(await orig<typeof RightPanelModule>()),
	RightPanel: ({ peer, onClose }: { peer?: boolean; onClose?: () => void }) => (
		<div
			data-testid="right-panel"
			data-peer={String(!!peer)}
			data-closable={String(!!onClose)}
		/>
	),
}));

const ID = "T-tp";
const initialWidth = window.innerWidth;

function rect(x: number, y: number, width: number, height: number) {
	return { x, y, left: x, top: y, width, height } as DOMRectReadOnly;
}

function renderPage(width: number) {
	window.innerWidth = width;
	return render(
		<MemoryRouter initialEntries={[`/t/${ID}`]}>
			<Routes>
				<Route path="/t/:threadId" element={<ThreadPage />} />
			</Routes>
		</MemoryRouter>,
	);
}

const pane = () => screen.getByTestId("transcript-pane");
const tabIds = () =>
	Array.from(
		screen.getByTestId("thread-tabs").querySelectorAll("[data-panel-tab]"),
	).map((b) => b.getAttribute("data-panel-tab"));

beforeEach(() => {
	getThreadStore(ID)
		.getState()
		.patchLocal({ thread: { id: ID } as Thread, snapshotReceived: true });
	uiStore.setState({
		threadTab: "agent",
		panelTab: "info",
		panelOpen: true,
		panelMax: false,
	});
});

afterEach(() => {
	window.innerWidth = initialWidth;
	delete window.viewport;
	resetThreadStores();
});

describe("ThreadPage 窄屏同级标签", () => {
	it("compact：页头下一行标签条依次是 agent / changes / files / terminal / info，转录留白加上这一行", () => {
		renderPage(390);
		expect(tabIds()).toEqual(["agent", "changes", "files", "terminal", "info"]);
		expect(document.querySelector('[data-panel-tab="agent"]')).toHaveAttribute(
			"aria-selected",
			"true",
		);
		expect(screen.getByTestId("transcript")).toHaveAttribute(
			"data-inset-top",
			"104",
		);
		expect(screen.getByTestId("thread-header")).toHaveAttribute(
			"data-veil",
			"104",
		);
		// 窄屏没有面板列，面板开关状态不渲染面板
		expect(screen.queryByTestId("right-panel")).toBeNull();
	});

	it("点终端：转录纸面 inert（透明、仍在布局）且保持挂载、面板作为同级页出现、没有抽屉遮罩；切回 agent 后还是同一个转录", () => {
		renderPage(390);
		const transcript = screen.getByTestId("transcript");
		fireEvent.click(
			document.querySelector('[data-panel-tab="terminal"]') as HTMLElement,
		);
		expect(uiStore.getState()).toMatchObject({
			threadTab: "terminal",
			panelTab: "terminal",
		});
		expect(pane()).toHaveAttribute("inert");
		expect(screen.getByTestId("transcript")).toBe(transcript);
		const panel = screen.getByTestId("right-panel");
		expect(panel).toHaveAttribute("data-peer", "true");
		expect(panel).toHaveAttribute("data-closable", "false");
		expect(document.querySelector(".fixed.inset-0")).toBeNull();
		// 面板里的标签条接手，转录页头里那一条卸掉
		expect(screen.queryByTestId("thread-tabs")).toBeNull();

		act(() => uiStore.getState().setThreadTab("agent"));
		expect(pane()).not.toHaveAttribute("inert");
		expect(screen.getByTestId("transcript")).toBe(transcript);
		expect(screen.queryByTestId("right-panel")).toBeNull();
		expect(screen.getByTestId("thread-tabs")).toBeInTheDocument();
	});

	it("medium 同样是同级标签，打开面板（如终端入口）直接切到该标签", () => {
		renderPage(700);
		expect(tabIds()[0]).toBe("agent");
		act(() => {
			const ui = uiStore.getState();
			ui.setPanel(true);
			ui.setPanelTab("terminal");
		});
		expect(uiStore.getState().threadTab).toBe("terminal");
		expect(pane()).toHaveAttribute("inert");
	});

	it("进入线程回到 agent 标签", () => {
		uiStore.setState({ threadTab: "files", panelTab: "files" });
		renderPage(390);
		expect(uiStore.getState().threadTab).toBe("agent");
		expect(pane()).not.toHaveAttribute("inert");
	});

	it("compact 下输入坞托盘底角与屏幕圆角同心，medium 不贴边不加", () => {
		const { unmount } = renderPage(390);
		const tray = () => document.querySelector(".glass-dock") as HTMLElement;
		expect(tray()).toHaveClass("dc-bl", "dc-br");
		unmount();
		renderPage(700);
		expect(tray()).not.toHaveClass("dc-bl");
	});
});

describe("ThreadPage expanded", () => {
	it("三栏不变：没有同级标签条，面板列可关闭，最右侧纸面（面板）右侧两角同心", () => {
		renderPage(1280);
		expect(screen.queryByTestId("thread-tabs")).toBeNull();
		expect(document.querySelector('[data-panel-tab="agent"]')).toBeNull();
		expect(screen.getByTestId("transcript")).toHaveAttribute(
			"data-inset-top",
			"64",
		);
		const panel = screen.getByTestId("right-panel");
		expect(panel).toHaveAttribute("data-peer", "false");
		expect(panel).toHaveAttribute("data-closable", "true");
		expect(panel.parentElement).toHaveClass("sheet", "dc-tr", "dc-br");
		expect(pane().firstElementChild).not.toHaveClass("dc-tr");
		expect(screen.getByTestId("thread-header")).toHaveAttribute(
			"data-sidebar-toggle",
			"false",
		);
	});

	it("面板列关闭时转录纸面是最右侧，右侧两角同心", () => {
		uiStore.setState({ panelOpen: false });
		renderPage(1280);
		expect(screen.queryByTestId("right-panel")).toBeNull();
		expect(pane().firstElementChild).toHaveClass("sheet", "dc-tr", "dc-br");
	});
});

describe("ThreadPage 双段视口", () => {
	it("左段转录按段宽固定，右段面板隔着铰链常驻、不可关闭；不走同级标签", () => {
		window.viewport = {
			segments: [rect(0, 0, 500, 800), rect(530, 0, 500, 800)],
		};
		renderPage(1030);
		const primary = document.querySelector('[data-pane="primary"]');
		const secondary = document.querySelector(
			'[data-pane="secondary"]',
		) as HTMLElement;
		expect(primary).toBe(pane());
		expect(primary).toHaveStyle({ width: "500px" });
		expect((primary as HTMLElement).style.flexGrow).toBe("0");
		expect(secondary).toHaveStyle({ marginLeft: "30px" });
		expect(secondary.style.flexGrow).toBe("1");
		const panel = screen.getByTestId("right-panel");
		expect(secondary).toContainElement(panel);
		expect(panel).toHaveAttribute("data-peer", "false");
		expect(panel).toHaveAttribute("data-closable", "false");
		expect(screen.getAllByTestId("right-panel")).toHaveLength(1);
		expect(screen.queryByTestId("thread-tabs")).toBeNull();
		expect(screen.getByTestId("thread-header")).toHaveAttribute(
			"data-sidebar-toggle",
			"true",
		);
	});
});

function Where() {
	return <span data-testid="where">{useLocation().pathname}</span>;
}

describe("ThreadPage：协调对话经 /t/<id> 打开时重定向（M9）", () => {
	function open(thread: Partial<Thread>, spaces: unknown = { spaces: [] }) {
		indexStore
			.getState()
			.setAll([{ id: "T-c", title: "c", ...thread } as Thread], 1);
		const qc = new QueryClient({
			defaultOptions: { queries: { retry: false, staleTime: Infinity } },
		});
		qc.setQueryData(["spaces", "list", { archived: false }], spaces);
		return render(
			<QueryClientProvider client={qc}>
				<MemoryRouter initialEntries={["/t/T-c"]}>
					<Routes>
						<Route path="/t/:threadId" element={<ThreadPage />} />
						<Route path="*" element={<Where />} />
					</Routes>
				</MemoryRouter>
			</QueryClientProvider>,
		);
	}

	afterEach(() => indexStore.getState().setAll([], 0));

	it("助手对话 → /assistant/<id>", () => {
		open({ kind: "assistant" });
		expect(screen.getByTestId("where")).toHaveTextContent("/assistant/T-c");
	});

	it("Space 聊天 → /space/<spaceId>（取线程的 spaceId）", () => {
		open({ kind: "space", spaceId: "spc_X" });
		expect(screen.getByTestId("where")).toHaveTextContent("/space/spc_X");
	});

	it("Space 聊天没带 spaceId 时按 Space 列表反查", () => {
		open(
			{ kind: "space" },
			{ spaces: [{ id: "spc_Y", name: "y", goal: "", threadId: "T-c" }] },
		);
		expect(screen.getByTestId("where")).toHaveTextContent("/space/spc_Y");
	});

	it("工作线程不重定向", () => {
		getThreadStore("T-c")
			.getState()
			.patchLocal({ thread: { id: "T-c" } as Thread, snapshotReceived: true });
		open({ kind: "work" });
		expect(screen.queryByTestId("where")).toBeNull();
		expect(screen.getByTestId("transcript-pane")).toBeInTheDocument();
	});
});
