/**
 * 右侧面板：换线程时 Changes 面板按线程重建，不把上一个线程的范围（所选 commit）带到新 orb；
 * 「改动」标签在有未查看的新改动时亮点，切到改动标签即清除；
 * Portal 标签只在 orb 有 portal 时出现（终端之后），portal 消失时退回信息标签；
 * Desktop 标签只在 orb 有桌面时出现（Portal 之后），消失时退回信息标签，Profile 被占用或装载失败时带提醒点；
 * 窄屏 `peer` 面板：medium 下同级标签 agent … info、侧栏开关、不能关闭 / 最大化，Agent 标签在线程等你回答时亮注意点；
 * 手机（compact）下没有标签条，页头是「返回对话 + 面板名 + 视图菜单」，菜单列出全部视图并标出当前视图与提醒点。
 * 桌面 App 无系统标题栏：三栏面板顶部多一行可拖动的标题行放右侧窗口按钮；同级面板把按钮放进标签条两端；浏览器里都没有。
 * iOS 原生页头：转录页头里的视图菜单（`round`）登记 `trail` 部件，面板标签行里那个不登记。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Orb, Thread } from "../../api/client";
import i18n from "../../i18n";
import type * as NativeHeaderModule from "../../lib/native-header";
import type { HeaderParts } from "../../lib/native-header";
import { setWindowChrome, type WindowChrome } from "../../lib/window-chrome";
import { handleOrbFetch, resetOrbFs } from "../../mock/orbfs";
import { getThreadStore } from "../../state/stores";
import type { ThreadViewState } from "../../state/thread-reducer";
import { uiStore } from "../../state/ui";
import type { GitFileChange } from "../changes/git";
import { RightPanel, ThreadViewMenu } from "./RightPanel";

const term = vi.hoisted(() => ({ attention: false }));
vi.mock("../../state/terminals", () => ({
	terminalHub: { onExit: vi.fn(() => () => {}) },
	useTerminals: () => term.attention,
}));

const nh = vi.hoisted(() => ({
	active: false,
	setPart: vi.fn(),
	frame: { x: 337, y: 68, width: 44, height: 44 },
}));
// 换掉控制器实例与钩子（矩形固定），其余导出（nativeItems）用真的
vi.mock("../../lib/native-header", async (orig) => {
	// vi.mock 的工厂被提升到所有 import 之前，只能在这里取 React
	const { useEffect } = await import("react");
	return {
		...(await orig<typeof NativeHeaderModule>()),
		nativeHeader: { setPart: nh.setPart },
		useNativeHeaderActive: () => nh.active,
		useNativeHeaderEngaged: () => nh.active,
		useNativeHeaderMenu: () => null,
		useNativeHeaderPart: (part: string, value: unknown, on = true) => {
			useEffect(() => {
				if (on) nh.setPart(part, value);
			});
			useEffect(
				() => (on ? () => nh.setPart(part, null) : undefined),
				[part, on],
			);
		},
		useSlotFrame: (_ref: unknown, on = true) => (on ? nh.frame : null),
	};
});

const ORB_A = "orb-rp00000000000000000000a1";
const ORB_B = "orb-rp00000000000000000000b2";
const SHA = "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b";
let statusQueries: { orb: string; query: URLSearchParams }[];
// 非空时，后台（bg）状态请求直接返回这些文件
let bgFiles: GitFileChange[] | null;

function withQuery(node: ReactNode) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return <QueryClientProvider client={client}>{node}</QueryClientProvider>;
}

function file(path: string, additions: number): GitFileChange {
	return {
		path,
		status: "modified",
		additions,
		deletions: 0,
		binary: false,
		staged: "none",
	};
}

beforeEach(() => {
	resetOrbFs();
	localStorage.clear();
	statusQueries = [];
	bgFiles = null;
	uiStore.setState({ changesSig: {} });
	uiStore.setState({ artifactSel: {}, artifactsSig: {} });
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const url = new URL(req.url);
			if (url.pathname.endsWith("/git/status"))
				statusQueries.push({
					orb: url.pathname.split("/")[4] ?? "",
					query: url.searchParams,
				});
			if (
				bgFiles &&
				url.pathname.endsWith("/git/status") &&
				url.searchParams.has("bg")
			)
				return Response.json({
					repo: true,
					scope: "all",
					files: bgFiles,
					truncated: false,
				});
			return (await handleOrbFetch(req)) ?? new Response(null, { status: 404 });
		}),
	);
	for (const [id, orbId] of [
		["T-rpA", ORB_A],
		["T-rpB", ORB_B],
	] as const)
		getThreadStore(id)
			.getState()
			.patchLocal({ thread: { id, orbId } as Thread });
	uiStore.getState().setPanelTab("changes");
});

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

describe("RightPanel", () => {
	it("在线程 A 选了 commit，切到线程 B：B 的请求按自己的默认范围，不带 A 的 commit", async () => {
		// 改动面板的「推送并开 PR」（M14）读项目列表，需要 QueryClient；rerender 沿用同一个 wrapper
		const client = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		const { rerender } = render(<RightPanel threadId="T-rpA" />, {
			wrapper: ({ children }) => (
				<QueryClientProvider client={client}>{children}</QueryClientProvider>
			),
		});
		const picker = await screen.findByRole(
			"combobox",
			{ name: i18n.t("changes.scope.label") },
			{ timeout: 5000 },
		);
		await waitFor(() =>
			expect(
				screen.getByRole("option", { name: /^1a2b3c4 docs: 更新 README/ }),
			).toBeInTheDocument(),
		);
		fireEvent.change(picker, { target: { value: `commit:${SHA}` } });
		await waitFor(() =>
			expect(statusQueries.at(-1)?.query.get("commit")).toBe(SHA),
		);

		await act(async () => {
			rerender(<RightPanel threadId="T-rpB" />);
		});
		await waitFor(() => expect(statusQueries.at(-1)?.orb).toBe(ORB_B));
		const forB = statusQueries.filter((q) => q.orb === ORB_B);
		expect(forB.map((q) => q.query.get("scope"))).toEqual(["all"]);
		expect(forB.some((q) => q.query.has("commit"))).toBe(false);
		expect(
			screen.getByRole("combobox", { name: i18n.t("changes.scope.label") }),
		).toHaveValue("all");
	});

	it("新改动亮点，切到改动标签即清除并记为已看", async () => {
		uiStore.getState().noteChanges("T-rpA", "a", false);
		uiStore.getState().noteChanges("T-rpA", "b", false);
		uiStore.getState().setPanelTab("info");
		render(withQuery(<RightPanel threadId="T-rpA" />));
		const tab = screen.getByRole("tab", {
			name: i18n.t("rightPanel.tabs.changes"),
		});
		expect(tab).toHaveAttribute("data-panel-tab", "changes");
		expect(screen.getByTestId("changes-unseen")).toBeInTheDocument();
		await act(async () => {
			fireEvent.click(tab);
		});
		expect(tab).toHaveAttribute("aria-selected", "true");
		expect(screen.queryByTestId("changes-unseen")).toBeNull();
		expect(uiStore.getState().changesSig["T-rpA"]).toEqual({
			current: "b",
			seen: "b",
		});
	});

	it("其它标签下后台检查改动：首次为基线，agent 活动后出现新改动则亮点", async () => {
		const id = "T-rpC";
		const orbId = "orb-rp00000000000000000000c3";
		getThreadStore(id)
			.getState()
			.patchLocal({
				thread: { id, orbId } as Thread,
				orb: { id: orbId, state: "ready", stateChangedAt: 0 } as Orb,
			});
		bgFiles = [file("a.ts", 1)];
		uiStore.getState().setPanelTab("info");
		render(withQuery(<RightPanel threadId={id} />));
		await waitFor(() =>
			expect(uiStore.getState().changesSig[id]?.current).toContain("a.ts"),
		);
		expect(statusQueries.filter((q) => q.orb === orbId)).toHaveLength(1);
		expect(statusQueries[0]?.query.get("scope")).toBe("all");
		expect(screen.queryByTestId("changes-unseen")).toBeNull();

		// agent 回合里新增条目 → 1s 防抖后再查
		bgFiles = [file("a.ts", 1), file("b.ts", 3)];
		act(() => {
			const s = getThreadStore(id).getState();
			s.patchLocal({
				entries: [
					...s.entries,
					{
						seq: 999,
						file: "x.jsonl",
						entryId: "x",
						type: "message",
						payload: {},
					},
				],
			});
		});
		expect(
			await screen.findByTestId("changes-unseen", {}, { timeout: 3000 }),
		).toBeInTheDocument();
	});

	it("orb 有 portal 时在终端之后出现 Portal 标签，点开加载 Portal 面板", async () => {
		const id = "T-rpP";
		const orbId = "orb-rp00000000000000000000p1";
		getThreadStore(id)
			.getState()
			.patchLocal({
				thread: {
					id,
					orbId,
					portalUrlTemplate: "https://t-3f9a1c0b7e2d-p{port}.orb.example.com/",
				} as Thread,
				orb: {
					id: orbId,
					state: "frozen",
					stateChangedAt: 0,
					threadIds: [id],
					portals: [
						{
							port: 5173,
							service: "web",
							links: [{ title: "web", path: "/" }],
						},
					],
				} as unknown as Orb,
			});
		uiStore.getState().setPanelTab("info");
		render(withQuery(<RightPanel threadId={id} />));
		const tabs = screen
			.getAllByRole("tab")
			.map((b) => b.getAttribute("data-panel-tab"));
		expect(tabs).toEqual(["info", "changes", "files", "terminal", "portal"]);
		const tab = screen.getByRole("tab", {
			name: i18n.t("rightPanel.tabs.portal"),
		});
		await act(async () => {
			fireEvent.click(tab);
		});
		expect(tab).toHaveAttribute("aria-selected", "true");
		const frame = await screen.findByTestId(
			"portal-frame",
			{},
			{ timeout: 5000 },
		);
		expect(frame.getAttribute("src")).toBe(
			"https://t-3f9a1c0b7e2d-p5173.orb.example.com/__oryn/login?next=%2F",
		);
	});

	it("没有 portal 时没有 Portal 标签；正看着 Portal 时 portal 消失则退回信息标签", async () => {
		const id = "T-rpQ";
		const orbId = "orb-rp00000000000000000000q1";
		const base = {
			id: orbId,
			state: "frozen",
			stateChangedAt: 0,
			threadIds: [id],
		} as unknown as Orb;
		const store = getThreadStore(id).getState();
		store.patchLocal({
			thread: {
				id,
				orbId,
				portalUrlTemplate: "https://x-p{port}.e/",
			} as Thread,
			orb: {
				...base,
				portals: [
					{ port: 80, service: "web", links: [{ title: "w", path: "/" }] },
				],
			},
		});
		uiStore.getState().setPanelTab("portal");
		render(withQuery(<RightPanel threadId={id} />));
		expect(
			screen.getByRole("tab", { name: i18n.t("rightPanel.tabs.portal") }),
		).toHaveAttribute("aria-selected", "true");

		act(() => {
			getThreadStore(id)
				.getState()
				.patchLocal({ orb: { ...base, services: [], portals: [] } });
		});
		await waitFor(() => expect(uiStore.getState().panelTab).toBe("info"));
		expect(document.querySelector("[data-panel-tab=portal]")).toBeNull();
		expect(
			screen.getByRole("tab", { name: i18n.t("rightPanel.tabs.info") }),
		).toHaveAttribute("aria-selected", "true");
	});

	it("标签条放不下全部文字时只有选中的标签保留文字，其余只显示图标（保留读屏名）", () => {
		layoutTabs(250, 80);
		uiStore.getState().setPanelTab("info");
		render(withQuery(<RightPanel threadId="T-rpA" />));
		const info = screen.getByRole("tab", {
			name: i18n.t("rightPanel.tabs.info"),
		});
		const files = screen.getByRole("tab", {
			name: i18n.t("rightPanel.tabs.files"),
		});
		expect(info).toHaveTextContent(i18n.t("rightPanel.tabs.info"));
		expect(files.textContent).toBe("");
		expect(files).toHaveAttribute("title", i18n.t("rightPanel.tabs.files"));
	});

	it("按标签内容宽度判断：放得下（含预留的「最大化」宽度）就不折叠", () => {
		// expanded + 信息标签 → 预留 36px；4 个 60px 标签共 243px，远小于 400 − 36
		layoutTabs(400, 60);
		uiStore.getState().setPanelTab("info");
		render(withQuery(<RightPanel threadId="T-rpA" />));
		const files = screen.getByRole("tab", {
			name: i18n.t("rightPanel.tabs.files"),
		});
		expect(files).toHaveTextContent(i18n.t("rightPanel.tabs.files"));
		expect(files).not.toHaveAttribute("title");
	});

	it("线程有 artifact 时在内容标签之后、Portal 之前出现 Artifacts 标签，点开加载 Artifacts 面板", async () => {
		const id = "T-rpArt";
		const orbId = "orb-rp0000000000000000000art";
		const art = {
			id: "art_01JDNAWEB0000000000000000A",
			origin: "agent",
			kind: "web",
			title: "本周习惯打卡",
			version: 2,
			size: 10,
			pages: 1,
			createdAt: 1,
			updatedAt: 2,
			threadId: id,
		};
		let list = [art];
		const base = vi.mocked(fetch).getMockImplementation() as typeof fetch;
		vi.mocked(fetch).mockImplementation(async (input, init) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const url = new URL(req.url);
			if (url.pathname === "/api/v1/artifacts")
				return Response.json({
					artifacts: url.searchParams.get("threadId") === id ? list : [],
				});
			return base(input, init);
		});
		getThreadStore(id)
			.getState()
			.patchLocal({
				thread: {
					id,
					orbId,
					portalUrlTemplate: "https://x-p{port}.e/",
				} as Thread,
				orb: {
					id: orbId,
					state: "frozen",
					stateChangedAt: 0,
					threadIds: [id],
					portals: [
						{ port: 80, service: "web", links: [{ title: "w", path: "/" }] },
					],
				} as unknown as Orb,
			});
		uiStore.getState().setPanelTab("info");
		const client = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		render(
			<QueryClientProvider client={client}>
				<MemoryRouter>
					<RightPanel threadId={id} />
				</MemoryRouter>
			</QueryClientProvider>,
		);
		await waitFor(() =>
			expect(
				screen.getAllByRole("tab").map((b) => b.getAttribute("data-panel-tab")),
			).toEqual([
				"info",
				"changes",
				"files",
				"terminal",
				"artifacts",
				"portal",
			]),
		);
		// 首次拿到列表是基线，不亮点；之后出了新版本而标签不在显示时亮 accent 点
		expect(screen.queryByTestId("artifacts-unseen")).toBeNull();
		list = [{ ...art, version: 3 }];
		await act(async () => {
			await client.invalidateQueries({ queryKey: ["artifacts"] });
		});
		expect(await screen.findByTestId("artifacts-unseen")).toBeInTheDocument();

		const tab = screen.getByRole("tab", {
			name: i18n.t("rightPanel.tabs.artifacts"),
		});
		await act(async () => {
			fireEvent.click(tab);
		});
		expect(tab).toHaveAttribute("aria-selected", "true");
		expect(
			await screen.findByTestId("artifact-panel", {}, { timeout: 5000 }),
		).toBeInTheDocument();
		await waitFor(() =>
			expect(screen.queryByTestId("artifacts-unseen")).toBeNull(),
		);
	});

	it("没有 artifact 时没有 Artifacts 标签；从转录卡片打开一个后标签出现并选中", async () => {
		uiStore.getState().setPanelTab("info");
		render(
			withQuery(
				<MemoryRouter>
					<RightPanel threadId="T-rpA" />
				</MemoryRouter>,
			),
		);
		await waitFor(() =>
			expect(document.querySelector("[data-panel-tab=artifacts]")).toBeNull(),
		);
		act(() => {
			uiStore
				.getState()
				.openArtifact("T-rpA", "art_01JDNAWEB0000000000000000A");
		});
		expect(
			screen.getByRole("tab", { name: i18n.t("rightPanel.tabs.artifacts") }),
		).toHaveAttribute("aria-selected", "true");
		expect(uiStore.getState().panelOpen).toBe(true);
	});
});

/** 带桌面（与可选 portal）的线程。 */
function deskThread(
	id: string,
	desktop: Orb["desktop"],
	portals = false,
	state: Orb["state"] = "frozen",
): Orb {
	const orbId = `orb-${id.toLowerCase().padEnd(24, "0")}`;
	const orb = {
		id: orbId,
		state,
		stateChangedAt: 0,
		threadIds: [id],
		...(desktop ? { desktop } : {}),
		...(portals
			? {
					portals: [
						{ port: 80, service: "web", links: [{ title: "w", path: "/" }] },
					],
				}
			: {}),
	} as unknown as Orb;
	getThreadStore(id)
		.getState()
		.patchLocal({
			thread: {
				id,
				orbId,
				portalUrlTemplate: "https://x-p{port}.e/",
			} as Thread,
			orb,
		});
	return orb;
}

describe("RightPanel Desktop 标签", () => {
	it("orb 有桌面时出现 Desktop 标签，排在 Portal 之后", () => {
		deskThread("T-rpD1", { displayReady: true, browser: "stopped" }, true);
		uiStore.getState().setPanelTab("info");
		render(withQuery(<RightPanel threadId="T-rpD1" />));
		const tabs = screen
			.getAllByRole("tab")
			.map((b) => b.getAttribute("data-panel-tab"));
		expect(tabs).toEqual([
			"info",
			"changes",
			"files",
			"terminal",
			"portal",
			"desktop",
		]);
		expect(
			screen.getByRole("tab", { name: i18n.t("rightPanel.tabs.desktop") }),
		).toBeInTheDocument();
	});

	it("没有桌面（旧镜像）没有 Desktop 标签；已删除的 orb 也没有", () => {
		deskThread("T-rpD2", undefined);
		uiStore.getState().setPanelTab("info");
		const { unmount } = render(withQuery(<RightPanel threadId="T-rpD2" />));
		expect(document.querySelector("[data-panel-tab=desktop]")).toBeNull();
		unmount();
		deskThread(
			"T-rpD3",
			{ displayReady: true, browser: "stopped" },
			false,
			"deleted",
		);
		render(withQuery(<RightPanel threadId="T-rpD3" />));
		expect(document.querySelector("[data-panel-tab=desktop]")).toBeNull();
	});

	it("正看着 Desktop 时桌面消失则退回信息标签", async () => {
		const orb = deskThread("T-rpD4", {
			displayReady: true,
			browser: "stopped",
		});
		uiStore.getState().setPanelTab("desktop");
		render(withQuery(<RightPanel threadId="T-rpD4" />));
		expect(
			screen.getByRole("tab", { name: i18n.t("rightPanel.tabs.desktop") }),
		).toHaveAttribute("aria-selected", "true");
		const { desktop: _, ...rest } = orb;
		act(() => {
			getThreadStore("T-rpD4")
				.getState()
				.patchLocal({ orb: rest as Orb });
		});
		await waitFor(() => expect(uiStore.getState().panelTab).toBe("info"));
		expect(document.querySelector("[data-panel-tab=desktop]")).toBeNull();
	});

	it("Profile 被占用或装载失败时标签带提醒点；独占使用时没有", () => {
		const profile = {
			id: "bpf_1",
			name: "工作账号",
			mode: "exclusive" as const,
			revision: 1,
		};
		const orb = deskThread("T-rpD5", {
			displayReady: true,
			browser: "running",
			profile: { ...profile, status: "attached" },
		});
		uiStore.getState().setPanelTab("info");
		render(withQuery(<RightPanel threadId="T-rpD5" />));
		expect(screen.queryByTestId("desktop-attention")).toBeNull();
		for (const status of ["in_use", "error"] as const) {
			act(() => {
				getThreadStore("T-rpD5")
					.getState()
					.patchLocal({
						orb: {
							...orb,
							desktop: {
								displayReady: true,
								browser: "stopped",
								profile: { ...profile, status },
							},
						},
					});
			});
			const dot = screen.getByTestId("desktop-attention");
			expect(
				screen.getByRole("tab", { name: i18n.t("rightPanel.tabs.desktop") }),
			).toContainElement(dot);
		}
	});
});

describe("RightPanel peer（窄屏同级标签，medium）", () => {
	const initialWidth = window.innerWidth;
	beforeEach(() => {
		window.innerWidth = 700;
		uiStore.setState({ sidebarOpen: false });
		uiStore.getState().setThreadTab("info");
	});
	afterEach(() => {
		window.innerWidth = initialWidth;
		uiStore.setState({ threadTab: "agent" });
	});

	it("标签依次是 agent / changes / files / terminal / info；左侧侧栏开关，没有关闭与最大化", () => {
		render(withQuery(<RightPanel threadId="T-rpA" peer onClose={vi.fn()} />));
		const strip = screen.getByTestId("thread-tabs");
		expect(
			Array.from(strip.querySelectorAll("[data-panel-tab]")).map((b) =>
				b.getAttribute("data-panel-tab"),
			),
		).toEqual(["agent", "changes", "files", "terminal", "info"]);
		expect(
			screen.getByRole("tab", { name: i18n.t("rightPanel.tabs.info") }),
		).toHaveAttribute("aria-selected", "true");
		expect(
			screen.queryByRole("button", { name: i18n.t("common.close") }),
		).toBeNull();
		expect(
			screen.queryByRole("button", { name: i18n.t("rightPanel.maximize") }),
		).toBeNull();
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("nav.sidebar") }),
		);
		expect(uiStore.getState().sidebarOpen).toBe(true);
	});

	it("点 agent 回到转录，点其它标签切同级标签与面板标签", async () => {
		render(withQuery(<RightPanel threadId="T-rpA" peer />));
		await act(async () => {
			fireEvent.click(
				screen.getByRole("tab", { name: i18n.t("rightPanel.tabs.files") }),
			);
		});
		expect(uiStore.getState()).toMatchObject({
			threadTab: "files",
			panelTab: "files",
		});
		fireEvent.click(
			screen.getByRole("tab", { name: i18n.t("rightPanel.tabs.agent") }),
		);
		expect(uiStore.getState().threadTab).toBe("agent");
	});

	it("线程等你回答（待答 UI 请求或待批计划）时 agent 标签亮注意点", () => {
		const id = "T-rpW";
		getThreadStore(id)
			.getState()
			.patchLocal({
				thread: { id } as Thread,
				pendingUi: [],
				planProposal: null,
			});
		render(withQuery(<RightPanel threadId={id} peer />));
		expect(screen.queryByTestId("agent-attention")).toBeNull();
		act(() => {
			getThreadStore(id)
				.getState()
				.patchLocal({
					pendingUi: [{ id: "ui-1" } as ThreadViewState["pendingUi"][number]],
				});
		});
		const dot = screen.getByTestId("agent-attention");
		expect(
			screen.getByRole("tab", { name: i18n.t("rightPanel.tabs.agent") }),
		).toContainElement(dot);
		expect(dot).toHaveAttribute("title", i18n.t("rightPanel.agentAttention"));
		act(() => {
			getThreadStore(id)
				.getState()
				.patchLocal({
					pendingUi: [],
					planProposal: {} as ThreadViewState["planProposal"],
				});
		});
		expect(screen.getByTestId("agent-attention")).toBeInTheDocument();
	});

	it("有 portal 与桌面时：agent / changes / files / terminal / portal / desktop / info", () => {
		deskThread("T-rpD6", { displayReady: true, browser: "stopped" }, true);
		render(withQuery(<RightPanel threadId="T-rpD6" peer />));
		expect(
			Array.from(
				screen.getByTestId("thread-tabs").querySelectorAll("[data-panel-tab]"),
			).map((b) => b.getAttribute("data-panel-tab")),
		).toEqual([
			"agent",
			"changes",
			"files",
			"terminal",
			"portal",
			"desktop",
			"info",
		]);
	});

	it("非 peer（expanded / 双段）没有 agent 标签", () => {
		window.innerWidth = 1280;
		render(withQuery(<RightPanel threadId="T-rpA" />));
		expect(document.querySelector('[data-panel-tab="agent"]')).toBeNull();
		expect(screen.getByTestId("thread-tabs")).toBeInTheDocument();
	});
});

describe("RightPanel peer（手机视图菜单）", () => {
	const initialWidth = window.innerWidth;
	beforeEach(() => {
		window.innerWidth = 390;
		uiStore.getState().setThreadTab("files");
	});
	afterEach(() => {
		window.innerWidth = initialWidth;
		uiStore.setState({ threadTab: "agent" });
	});

	it("没有标签条：页头是返回、面板名与视图菜单；菜单切换视图，返回回到对话", async () => {
		render(withQuery(<RightPanel threadId="T-rpA" peer />));
		expect(screen.queryByTestId("thread-tabs")).toBeNull();
		expect(screen.getByTestId("peer-panel-title")).toHaveTextContent(
			i18n.t("rightPanel.tabs.files"),
		);
		fireEvent.click(screen.getByTestId("thread-view-menu"));
		const items = screen.getAllByRole("menuitemradio");
		expect(items.map((b) => b.getAttribute("data-view"))).toEqual([
			"agent",
			"changes",
			"files",
			"terminal",
			"info",
		]);
		expect(
			items.filter((b) => b.getAttribute("aria-checked") === "true"),
		).toEqual([items[2]]);
		await act(async () => {
			fireEvent.click(items[3] as HTMLElement);
		});
		expect(uiStore.getState()).toMatchObject({
			threadTab: "terminal",
			panelTab: "terminal",
		});
		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("rightPanel.backToAgent") }),
		);
		expect(uiStore.getState().threadTab).toBe("agent");
	});

	it("线程等你回答时视图菜单按钮亮点，菜单里对话一项带注意点", () => {
		const id = "T-rpM";
		getThreadStore(id)
			.getState()
			.patchLocal({
				thread: { id } as Thread,
				pendingUi: [],
				planProposal: null,
			});
		render(withQuery(<RightPanel threadId={id} peer />));
		const trigger = screen.getByTestId("thread-view-menu");
		expect(screen.queryByTestId("view-menu-dot")).toBeNull();
		act(() => {
			getThreadStore(id)
				.getState()
				.patchLocal({
					pendingUi: [{ id: "ui-1" } as ThreadViewState["pendingUi"][number]],
				});
		});
		expect(trigger).toContainElement(screen.getByTestId("view-menu-dot"));
		fireEvent.click(trigger);
		expect(document.querySelector('[data-view="agent"]')).toContainElement(
			screen.getByTestId("agent-attention"),
		);
	});
});

describe("ThreadViewMenu iOS 原生页头", () => {
	const TABS = ["agent", "changes", "files", "terminal", "info"] as const;
	const trail = () => {
		const call = nh.setPart.mock.calls.filter((c) => c[0] === "trail").at(-1);
		if (!call?.[1]) throw new Error("没有登记 trail");
		return call[1] as HeaderParts["trail"];
	};
	beforeEach(() => {
		nh.setPart.mockClear();
		nh.active = false;
		term.attention = false;
		getThreadStore("T-rpN")
			.getState()
			.patchLocal({ thread: { id: "T-rpN" } as Thread });
	});
	afterEach(() => {
		nh.active = false;
		term.attention = false;
		uiStore.setState({ threadTab: "agent" });
	});

	it("round：登记 trail，当前视图打勾；终端等待输入时该项与按钮都带橙点；选中终端切到终端", () => {
		term.attention = true;
		render(
			withQuery(
				<ThreadViewMenu threadId="T-rpN" tabs={TABS} shown="agent" round />,
			),
		);
		const t = trail();
		expect(t.frame).toEqual(nh.frame);
		expect(t.label).toBe(i18n.t("rightPanel.views"));
		expect(t.dot).toBe("warn");
		expect(t.menu.dim).toBe(false);
		const items = t.menu.groups[0] ?? [];
		expect(items.map((i) => i.id)).toEqual(TABS);
		expect(items.find((i) => i.checked)?.id).toBe("agent");
		expect(items.filter((i) => i.checked === false)).toHaveLength(4);
		expect(items.find((i) => i.id === "terminal")).toMatchObject({
			label: i18n.t("rightPanel.tabs.terminal"),
			dot: "warn",
			pulse: true,
		});
		act(() => t.onSelect("terminal"));
		expect(uiStore.getState().threadTab).toBe("terminal");
		// 不是视图的 id 什么也不做
		act(() => t.onSelect("nope"));
		expect(uiStore.getState().threadTab).toBe("terminal");
	});

	it("原生显示稳了：round 按钮透明、对读屏隐藏，仍带 data-native-slot", () => {
		nh.active = true;
		render(
			withQuery(
				<ThreadViewMenu threadId="T-rpN" tabs={TABS} shown="agent" round />,
			),
		);
		const btn = screen.getByTestId("thread-view-menu");
		expect(btn).toHaveAttribute("data-native-slot", "trail");
		expect(btn.className).toMatch(/(^|\s)opacity-0(\s|$)/);
		expect(btn.className).not.toMatch(/(^|\s)glass(\s|$)/);
		expect(btn).toHaveAttribute("aria-hidden", "true");
		expect(btn).toHaveAttribute("tabindex", "-1");
	});

	it("不带 round（面板标签行里的那个）：从不登记，也不受原生接管影响", () => {
		nh.active = true;
		const { unmount } = render(
			withQuery(<ThreadViewMenu threadId="T-rpN" tabs={TABS} shown="files" />),
		);
		const btn = screen.getByTestId("thread-view-menu");
		expect(btn).not.toHaveAttribute("data-native-slot");
		expect(btn).not.toHaveAttribute("aria-hidden");
		unmount();
		expect(nh.setPart).not.toHaveBeenCalled();
	});
});

/**
 * 模拟真实布局：tablist（flex-1）宽 `listWidth`，scrollWidth 不小于 clientWidth；
 * 每个标签宽 `tabWidth`、间距 1px 依次排开。
 */
function layoutTabs(listWidth: number, tabWidth: number) {
	const isTab = (el: HTMLElement) => el.getAttribute("role") === "tab";
	const index = (el: HTMLElement) =>
		Array.from(
			el.parentElement?.querySelectorAll('[role="tab"]') ?? [],
		).indexOf(el);
	vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(
		listWidth,
	);
	vi.spyOn(HTMLElement.prototype, "scrollWidth", "get").mockImplementation(
		function (this: HTMLElement) {
			return this.getAttribute("role") === "tablist"
				? Math.max(
						listWidth,
						this.querySelectorAll('[role="tab"]').length * (tabWidth + 1),
					)
				: 0;
		},
	);
	vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(
		function (this: HTMLElement) {
			return isTab(this) ? tabWidth : 0;
		},
	);
	vi.spyOn(HTMLElement.prototype, "offsetLeft", "get").mockImplementation(
		function (this: HTMLElement) {
			return isTab(this) ? index(this) * (tabWidth + 1) : 0;
		},
	);
}

describe("RightPanel 自绘标题栏", () => {
	function install(controls: "custom-right" | "custom-left") {
		// getFlags 要返回同一个对象（useSyncExternalStore 按引用比较快照）
		const flags = { maximized: false, fullscreen: false, focused: true };
		const wc: WindowChrome = {
			chrome: {
				platform: "windows",
				controls,
				buttons: ["minimize", "maximize", "close"],
				inset: { top: 48, left: 0, right: 0 },
				radius: 0,
			},
			getFlags: () => flags,
			subscribe: () => () => {},
			act: vi.fn(),
		};
		act(() => setWindowChrome(wc));
	}
	afterEach(() => {
		act(() => setWindowChrome(null));
	});

	it("三栏面板承载右侧按钮：按钮与标签条同一行、在标签条右侧，替掉 ×；这一行可拖动", () => {
		install("custom-right");
		render(
			withQuery(
				<RightPanel
					threadId="T-rpA"
					onClose={vi.fn()}
					chrome={{ left: false, right: true }}
				/>,
			),
		);
		const strip = screen.getByTestId("thread-tabs");
		const row = strip.parentElement as HTMLElement;
		expect(row).toHaveAttribute("data-chrome-row", "panel");
		expect(row).toHaveAttribute("data-window-drag", "");
		expect(row.lastElementChild).toHaveAttribute(
			"data-window-controls",
			"right",
		);
		expect(document.querySelectorAll("[data-chrome-row]")).toHaveLength(1);
		expect(
			screen.queryByRole("button", { name: i18n.t("common.close") }),
		).toBeNull();
		expect(
			screen.getByRole("button", { name: "关闭窗口" }),
		).toBeInTheDocument();
	});

	it("没有右侧窗口按钮（macOS / Linux 左侧 / 浏览器 / 面板不归它）时保留 ×", () => {
		for (const setup of [
			() => install("custom-left"),
			() => act(() => setWindowChrome(null)),
		]) {
			setup();
			const onClose = vi.fn();
			const r = render(
				withQuery(
					<RightPanel
						threadId="T-rpA"
						onClose={onClose}
						chrome={{ left: false, right: true }}
					/>,
				),
			);
			fireEvent.click(
				screen.getByRole("button", { name: i18n.t("common.close") }),
			);
			expect(onClose).toHaveBeenCalled();
			r.unmount();
		}
		install("custom-right");
		render(withQuery(<RightPanel threadId="T-rpA" onClose={vi.fn()} />));
		expect(
			screen.getByRole("button", { name: i18n.t("common.close") }),
		).toBeInTheDocument();
		expect(document.querySelector("[data-window-controls]")).toBeNull();
	});

	it("面板不归它（chrome 缺省）、布局是左侧按钮、或在浏览器里：没有标题行", () => {
		install("custom-right");
		const a = render(withQuery(<RightPanel threadId="T-rpA" />));
		expect(document.querySelector("[data-chrome-row]")).toBeNull();
		a.unmount();
		install("custom-left");
		const b = render(
			withQuery(
				<RightPanel threadId="T-rpA" chrome={{ left: false, right: true }} />,
			),
		);
		expect(document.querySelector("[data-chrome-row]")).toBeNull();
		b.unmount();
		act(() => setWindowChrome(null));
		render(
			withQuery(
				<RightPanel threadId="T-rpA" chrome={{ left: true, right: true }} />,
			),
		);
		expect(document.querySelector("[data-chrome-row]")).toBeNull();
		expect(document.querySelector("[data-window-controls]")).toBeNull();
	});

	it("同级面板：按钮直接放进标签条右端，不另起一行", () => {
		install("custom-right");
		render(
			withQuery(
				<RightPanel
					threadId="T-rpA"
					peer
					chrome={{ left: true, right: true }}
				/>,
			),
		);
		expect(document.querySelector("[data-chrome-row]")).toBeNull();
		const bar = screen.getByTestId("thread-tabs").closest("[data-window-drag]");
		expect(bar?.lastElementChild).toHaveAttribute(
			"data-window-controls",
			"right",
		);
	});
});
