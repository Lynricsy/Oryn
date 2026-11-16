/**
 * Artifacts 总览页：类型标签与显示选项进 URL、按项目与会话分组、卡片网格的方向键导航、空态磁贴交给首屏、
 * 筛不出结果时清除筛选、回收站的剩余天数与恢复。
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
import type { Schemas } from "../api/client";
import i18n from "../i18n";
import { indexStore } from "../state/stores";
import { uiStore } from "../state/ui";
import { ArtifactsPage } from "./ArtifactsPage";

const NOW = Date.now();
const DAY = 86_400_000;

function art(
	id: string,
	title: string,
	patch: Partial<Schemas["Artifact"]> = {},
): Schemas["Artifact"] {
	return {
		id,
		origin: "agent",
		kind: "web",
		title,
		version: 1,
		size: 1,
		pages: 1,
		createdAt: NOW - 10 * DAY,
		updatedAt: NOW - DAY,
		...patch,
	};
}

const A = art("art_01JDNA000000000000000000A1", "本周习惯打卡", {
	projectId: "prj_1",
	threadId: "T-1",
	updatedAt: NOW - 1000,
});
const B = art("art_01JDNA000000000000000000B2", "Alpha 幻灯片", {
	kind: "slides",
	type: "slides",
	projectId: "prj_1",
	threadId: "T-2",
	updatedAt: NOW - 2000,
});
const C = art("art_01JDNA000000000000000000C3", "导入的落地页", {
	origin: "import",
	updatedAt: NOW - 3000,
});

let live: Schemas["Artifact"][];
let trashed: Schemas["Artifact"][];
let calls: { method: string; path: string }[];

function Where() {
	const l = useLocation();
	return <span data-testid="where">{l.pathname + l.search}</span>;
}

function mount(path: string) {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={[path]}>
				<Routes>
					<Route path="/artifacts" element={<ArtifactsPage />} />
					<Route path="/artifacts/trash" element={<ArtifactsPage trash />} />
					<Route path="*" element={null} />
				</Routes>
				<Where />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

function search(): URLSearchParams {
	const text = screen.getByTestId("where").textContent ?? "";
	return new URLSearchParams(text.split("?")[1] ?? "");
}

function cardTitles(): string[] {
	return screen
		.getAllByTestId("artifact-card")
		.map((c) => c.getAttribute("aria-label") ?? "");
}

const open = (title: string) => i18n.t("artifacts.card.open", { title });

beforeEach(() => {
	live = [A, B, C];
	trashed = [];
	calls = [];
	uiStore.setState({ homeSeed: null });
	indexStore.setState({
		threads: {
			"T-1": { id: "T-1", title: "打卡线程" } as never,
			"T-2": { id: "T-2", title: "汇报线程" } as never,
		},
	});
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const url = new URL(req.url);
			calls.push({ method: req.method, path: url.pathname });
			if (url.pathname === "/api/v1/artifacts")
				return Response.json({
					artifacts:
						url.searchParams.get("deleted") === "true" ? trashed : live,
				});
			if (url.pathname === "/api/v1/projects")
				return Response.json({
					projects: [{ id: "prj_1", name: "Oryn" }],
				});
			if (url.pathname === "/api/v1/artifacts/capabilities")
				return Response.json({ frames: "host", render: false, sample: false });
			if (url.pathname.endsWith("/restore")) return Response.json(trashed[0]);
			return new Response(null, { status: 204 });
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("ArtifactsPage", () => {
	it("类型标签与排序都写进 URL，列表随之筛选、排序", async () => {
		mount("/artifacts");
		await screen.findByRole("button", { name: open(A.title) });
		expect(cardTitles()).toEqual([open(A.title), open(B.title), open(C.title)]);

		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("artifacts.kinds.slides") }),
		);
		expect(search().get("kind")).toBe("slides");
		expect(cardTitles()).toEqual([open(B.title)]);

		fireEvent.click(
			screen.getByRole("button", { name: i18n.t("artifacts.page.all") }),
		);
		expect(search().get("kind")).toBeNull();

		fireEvent.click(screen.getByTestId("artifacts-display"));
		fireEvent.click(screen.getByTestId("sort-name"));
		expect(search().get("sort")).toBe("name");
		// 名称自然序：Alpha 在前，中文按区域排序
		expect(cardTitles()[0]).toBe(open(B.title));
	});

	it("来源筛选与标题搜索进 URL；筛不出结果时清除筛选", async () => {
		mount("/artifacts");
		await screen.findByRole("button", { name: open(A.title) });
		fireEvent.click(screen.getByTestId("artifacts-filter"));
		fireEvent.click(screen.getByTestId("origin-import"));
		expect(search().getAll("origin")).toEqual(["import"]);
		expect(cardTitles()).toEqual([open(C.title)]);

		// 宽屏的搜索平时收成放大镜：点开后聚焦；有搜索词时失焦也不收回
		expect(screen.queryByRole("searchbox")).toBeNull();
		fireEvent.click(screen.getByTestId("artifacts-search"));
		const box = screen.getByRole("searchbox", {
			name: i18n.t("artifacts.page.search"),
		});
		expect(document.activeElement).toBe(box);
		fireEvent.change(box, { target: { value: "没有这个" } });
		fireEvent.blur(box);
		expect(screen.getByRole("searchbox")).toBeVisible();
		expect(search().get("q")).toBe("没有这个");
		expect(screen.getByText(i18n.t("artifacts.page.noMatch"))).toBeVisible();
		fireEvent.click(
			screen.getByRole("button", {
				name: i18n.t("artifacts.page.clearFilters"),
			}),
		);
		expect(search().get("q")).toBeNull();
		expect(search().getAll("origin")).toEqual([]);
		expect(cardTitles()).toHaveLength(3);
	});

	it("地址还没提交时紧接着的第二次改动不会盖掉第一次（换类型后马上搜索）", async () => {
		mount("/artifacts?kind=slides");
		await screen.findByTestId("artifacts-search");
		fireEvent.click(screen.getByTestId("artifacts-search"));
		const box = screen.getByRole("searchbox", {
			name: i18n.t("artifacts.page.search"),
		});
		// 两个事件之间不渲染：第二次改动若取渲染里的旧 view，kind=slides 会被写回去
		act(() => {
			fireEvent.click(
				screen.getByRole("button", { name: i18n.t("artifacts.page.all") }),
			);
			fireEvent.change(box, { target: { value: "汇报" } });
		});
		expect(search().get("kind")).toBeNull();
		expect(search().get("q")).toBe("汇报");
		expect(box).toHaveValue("汇报");
	});

	it("Esc 在有筛选时清除筛选", async () => {
		mount("/artifacts?kind=web&q=%E6%89%93%E5%8D%A1");
		await screen.findByRole("button", { name: open(A.title) });
		expect(cardTitles()).toEqual([open(A.title)]);
		act(() => {
			window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
		});
		expect(search().get("kind")).toBeNull();
		expect(search().get("q")).toBeNull();
	});

	it("Mod+F 聚焦标题搜索", async () => {
		mount("/artifacts");
		await screen.findByRole("button", { name: open(A.title) });
		act(() => {
			window.dispatchEvent(
				new KeyboardEvent("keydown", { key: "f", ctrlKey: true }),
			);
		});
		expect(document.activeElement).toBe(
			screen.getByRole("searchbox", { name: i18n.t("artifacts.page.search") }),
		);
	});

	it("按项目与会话分组：项目名、线程标题、导入的", async () => {
		mount("/artifacts?layout=project-thread");
		await screen.findByRole("button", { name: open(A.title) });
		const heads = screen
			.getAllByTestId("artifacts-group-head")
			.map((h) => h.textContent);
		expect(heads).toEqual(["Oryn", i18n.t("artifacts.page.noProject")]);
		const sections = screen
			.getAllByTestId("artifacts-section")
			.map((s) => s.textContent);
		expect(sections).toEqual([
			"打卡线程",
			"汇报线程",
			i18n.t("artifacts.page.imported"),
		]);
		// 线程行可点开线程
		expect(
			within(
				screen.getAllByTestId("artifacts-section")[0] as HTMLElement,
			).getByRole("link"),
		).toHaveAttribute("href", "/t/T-1");
	});

	it("卡片网格是一个 Tab 停靠点：方向键与 Home / End 移动焦点", async () => {
		mount("/artifacts");
		const first = await screen.findByRole("button", { name: open(A.title) });
		const cards = screen.getAllByTestId("artifact-card");
		expect(cards.map((c) => c.tabIndex)).toEqual([0, -1, -1]);
		first.focus();
		fireEvent.keyDown(first, { key: "ArrowRight" });
		expect(document.activeElement).toBe(cards[1]);
		expect(
			screen.getAllByTestId("artifact-card").map((c) => c.tabIndex),
		).toEqual([-1, 0, -1]);
		fireEvent.keyDown(cards[1] as HTMLElement, { key: "End" });
		expect(document.activeElement).toBe(cards[2]);
		fireEvent.keyDown(cards[2] as HTMLElement, { key: "ArrowRight" });
		expect(document.activeElement).toBe(cards[2]);
		fireEvent.keyDown(cards[2] as HTMLElement, { key: "Home" });
		expect(document.activeElement).toBe(cards[0]);
		// 卡片上的菜单按钮不是 Tab 停靠点
		for (const b of screen.getAllByTestId("artifact-menu"))
			expect(b.tabIndex).toBe(-1);
	});

	it("空态：四个磁贴，点击把一行提示交给首屏输入框", async () => {
		live = [];
		mount("/artifacts");
		await screen.findByTestId("artifacts-empty");
		fireEvent.click(
			screen
				.getAllByRole("button")
				.find((b) => b.dataset.seed === "slides") as HTMLElement,
		);
		expect(uiStore.getState().homeSeed).toEqual({
			text: i18n.t("artifacts.page.seeds.slides"),
		});
		expect(screen.getByTestId("where")).toHaveTextContent(/^\/$/);
	});

	it("回收站：卡片显示剩余天数，可以恢复", async () => {
		trashed = [
			art("art_01JDNA000000000000000000D4", "旧版落地页", {
				deletedAt: NOW - 3 * DAY,
			}),
		];
		mount("/artifacts/trash");
		expect(await screen.findByTestId("days-left")).toHaveTextContent(
			i18n.t("artifacts.page.daysLeft", { count: 27 }),
		);
		fireEvent.click(screen.getByTestId("trash-restore"));
		await waitFor(() =>
			expect(calls).toContainEqual({
				method: "POST",
				path: "/api/v1/artifacts/art_01JDNA000000000000000000D4/restore",
			}),
		);
	});

	it("总览页底部有「最近删除 N」入口", async () => {
		trashed = [art("art_01JDNA000000000000000000D4", "旧", { deletedAt: NOW })];
		mount("/artifacts");
		await waitFor(() =>
			expect(screen.getByTestId("artifacts-trash-link")).toHaveTextContent(
				i18n.t("artifacts.page.trashLink", { n: 1 }),
			),
		);
		expect(screen.getByTestId("artifacts-trash-link")).toHaveAttribute(
			"href",
			"/artifacts/trash",
		);
	});

	it("手机：筛选、显示与导入收进一个菜单；浏览器不支持选文件夹时没有那一项", async () => {
		const width = window.innerWidth;
		window.innerWidth = 390;
		try {
			mount("/artifacts");
			await waitFor(() => expect(cardTitles()).toHaveLength(3));
			expect(screen.queryByTestId("artifacts-filter")).toBeNull();
			expect(screen.queryByTestId("artifacts-display")).toBeNull();
			expect(screen.queryByTestId("artifacts-import")).toBeNull();
			fireEvent.click(screen.getByTestId("artifacts-options"));
			// 一个菜单里有筛选与显示的全部选项，还有导入
			fireEvent.click(screen.getByTestId("origin-import"));
			expect(search().get("origin")).toBe("import");
			expect(screen.getByTestId("artifacts-options")).toHaveTextContent(
				i18n.t("artifacts.page.optionsN", { n: 1 }),
			);
			fireEvent.click(screen.getByTestId("sort-name"));
			expect(search().get("sort")).toBe("name");
			// jsdom 的 <input> 没有 webkitdirectory
			expect(screen.queryByTestId("artifacts-import-folder")).toBeNull();
			const pick = vi
				.spyOn(HTMLInputElement.prototype, "click")
				.mockImplementation(() => {});
			fireEvent.click(screen.getByTestId("artifacts-import-file"));
			expect(pick).toHaveBeenCalledTimes(1);
			expect(pick.mock.contexts[0]).toBe(screen.getByTestId("import-file"));
			// 选了导入就收起菜单
			expect(screen.queryByTestId("artifacts-import-file")).toBeNull();
		} finally {
			window.innerWidth = width;
			vi.restoreAllMocks();
		}
	});

	it("手机：浏览器能选文件夹时菜单里有「文件夹」", async () => {
		const width = window.innerWidth;
		window.innerWidth = 390;
		Object.defineProperty(HTMLInputElement.prototype, "webkitdirectory", {
			configurable: true,
			value: false,
			writable: true,
		});
		try {
			mount("/artifacts");
			await waitFor(() => expect(cardTitles()).toHaveLength(3));
			fireEvent.click(screen.getByTestId("artifacts-options"));
			expect(screen.getByTestId("artifacts-import-folder")).toHaveTextContent(
				i18n.t("artifacts.import.folder"),
			);
		} finally {
			window.innerWidth = width;
			Reflect.deleteProperty(HTMLInputElement.prototype, "webkitdirectory");
		}
	});
});
