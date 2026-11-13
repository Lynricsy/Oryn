/**
 * 右侧面板：换线程时 Changes 面板按线程重建，不把上一个线程的范围（所选 commit）带到新 orb；
 * 「改动」标签在有未查看的新改动时亮点，切到改动标签即清除；
 * Portal 标签只在 orb 有 portal 时出现（终端之后），portal 消失时退回信息标签。
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
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Orb, Thread } from "../../api/client";
import i18n from "../../i18n";
import { handleOrbFetch, resetOrbFs } from "../../mock/orbfs";
import { getThreadStore } from "../../state/stores";
import type { ThreadViewState } from "../../state/thread-reducer";
import { uiStore } from "../../state/ui";
import type { GitFileChange } from "../changes/git";
import { RightPanel } from "./RightPanel";

vi.mock("../../state/terminals", () => ({
	terminalHub: { onExit: vi.fn(() => () => {}) },
	useTerminals: () => false,
}));

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
		const { rerender } = render(<RightPanel threadId="T-rpA" />);
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
				entries: [...s.entries, {} as ThreadViewState["entries"][number]],
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
		vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(250);
		vi.spyOn(HTMLElement.prototype, "scrollWidth", "get").mockReturnValue(330);
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
});
