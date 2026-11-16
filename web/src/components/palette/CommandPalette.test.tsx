import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { artifactQk } from "../../api/artifact-queries";
import type { Schemas, Thread } from "../../api/client";
import { indexStore } from "../../state/stores";
import { revealWhenReady } from "../transcript/reveal";
import { CommandPalette } from "./CommandPalette";
import { openPalette, paletteStore } from "./palette-state";

vi.mock("../transcript/reveal", () => ({ revealWhenReady: vi.fn() }));

function th(id: string, title: string, p: Partial<Thread> = {}): Thread {
	return {
		id,
		title,
		titleSource: "user",
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

const HIT: Schemas["SearchHit"] = {
	threadId: "T-old",
	threadTitle: "旧线程",
	threadKind: "work",
	archived: true,
	entryId: "e1",
	seq: 7,
	role: "assistant",
	ts: 1,
	snippet: "…把网关部署到生产环境…",
	highlights: [{ start: 2, end: 4 }],
	copies: 3,
};

function art(
	id: string,
	title: string,
	p: Partial<Schemas["Artifact"]> = {},
): Schemas["Artifact"] {
	return {
		id,
		origin: "agent",
		kind: "web",
		title,
		version: 1,
		size: 10,
		pages: 1,
		createdAt: 1,
		updatedAt: 1,
		...p,
	};
}

const ARTIFACTS: Schemas["Artifact"][] = [
	art("art_A", "季度报告", {
		kind: "document",
		projectId: "P-1",
		version: 3,
		updatedAt: 40,
	}),
	art("art_B", "路线图", { kind: "slides", updatedAt: 5, openedAt: 50 }),
	art("art_C", "落地页", { updatedAt: 30 }),
	art("art_D", "旧草图", { kind: "canvas", updatedAt: 2 }),
	art("art_E", "季度报告草稿", { updatedAt: 60, deletedAt: 70 }),
];

function Where() {
	const loc = useLocation();
	return <p data-testid="where">{loc.pathname}</p>;
}

function mount() {
	const qc = new QueryClient({
		defaultOptions: { queries: { retry: false, staleTime: Infinity } },
	});
	qc.setQueryData(["projects"], {
		projects: [{ id: "P-1", name: "增长", updatedAt: 1 }],
	});
	qc.setQueryData(artifactQk.list({}), { artifacts: ARTIFACTS });
	act(() => openPalette());
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={["/start"]}>
				<Routes>
					<Route path="*" element={<Where />} />
				</Routes>
				<CommandPalette />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const where = () => screen.getByTestId("where").textContent;
const input = () => screen.getByRole("combobox");
const options = () => screen.getAllByRole("option");
const type = (q: string) => fireEvent.change(input(), { target: { value: q } });

beforeEach(() => {
	indexStore
		.getState()
		.setAll(
			[
				th("T-1", "部署网关", { lastMessageAt: 30 }),
				th("T-2", "修复 Café 登录", { lastMessageAt: 20, labels: ["auth"] }),
				th("T-3", "整理文档", { lastMessageAt: 10 }),
			],
			1,
		);
	vi.stubGlobal("fetch", async (req: Request) => {
		const url = new URL(req.url);
		if (url.pathname === "/api/v1/search")
			return Response.json({ hits: [HIT], indexing: false });
		return Response.json({ spaces: [] });
	});
});

afterEach(() => {
	vi.unstubAllGlobals();
	paletteStore.setState({ open: false, opener: null });
	vi.mocked(revealWhenReady).mockClear();
});

describe("CommandPalette", () => {
	it("空查询列出最近线程；方向键移动、Enter 打开", () => {
		mount();
		expect(options()[0]).toHaveTextContent("部署网关");
		fireEvent.keyDown(input(), { key: "ArrowDown" });
		expect(options()[1]).toHaveAttribute("aria-selected", "true");
		expect(input()).toHaveAttribute("aria-activedescendant", options()[1]?.id);
		fireEvent.keyDown(input(), { key: "Enter" });
		expect(where()).toBe("/t/T-2");
		expect(paletteStore.getState().open).toBe(false);
	});

	it("输入法组字期间的 Enter 与方向键不处理", () => {
		mount();
		fireEvent.keyDown(input(), { key: "ArrowDown", isComposing: true });
		fireEvent.keyDown(input(), { key: "Enter", isComposing: true });
		fireEvent.keyDown(input(), { key: "Enter", keyCode: 229 });
		expect(options()[0]).toHaveAttribute("aria-selected", "true");
		expect(where()).toBe("/start");
		expect(paletteStore.getState().open).toBe(true);
	});

	it("多词 AND、折叠变音符，高亮落在原文上", () => {
		mount();
		type("cafe AUTH");
		const threads = screen.getByRole("group", { name: "线程" });
		expect(threads).toHaveTextContent("修复 Café 登录");
		expect(threads).not.toHaveTextContent("部署网关");
		const marks = threads.querySelectorAll("mark");
		expect([...marks].map((m) => m.textContent)).toEqual(["Café", "auth"]);
	});

	it("对话内容命中：打开线程并定位到那条消息；标出已归档与 fork 数", async () => {
		mount();
		type("网关");
		await screen.findByText("另有 2 个 fork");
		const group = screen.getByRole("group", { name: "对话内容" });
		expect(group).toHaveTextContent("已归档");
		expect(group).toHaveTextContent("另有 2 个 fork");
		expect(group.querySelector("mark")?.textContent).toBe("网关");
		const hit = [...group.querySelectorAll("[role=option]")][0] as HTMLElement;
		fireEvent.click(hit);
		expect(where()).toBe("/t/T-old");
		expect(revealWhenReady).toHaveBeenCalledWith("T-old", {
			entryId: "e1",
			seq: 7,
		});
	});

	it("Esc 先退出展开的分组，再关闭面板", () => {
		indexStore.getState().setAll(
			Array.from({ length: 5 }, (_, i) =>
				th(`T-${i}`, `网关 ${i}`, { lastMessageAt: i }),
			),
			1,
		);
		mount();
		type("网关");
		const more = screen.getByRole("option", { name: "显示全部 5 项" });
		fireEvent.click(more);
		expect(
			screen
				.getByRole("group", { name: "线程" })
				.querySelectorAll("[role=option]"),
		).toHaveLength(5);
		fireEvent.keyDown(input(), { key: "Escape" });
		expect(screen.getByRole("option", { name: "显示全部 5 项" })).toBeTruthy();
		fireEvent.keyDown(input(), { key: "Escape" });
		expect(paletteStore.getState().open).toBe(false);
	});

	it("Artifacts 组：标题片段命中并高亮，选中打开 /artifacts/<id>；回收站里的不出现", () => {
		indexStore.getState().setAll([th("T-9", "季度复盘")], 1);
		mount();
		type("季度");
		const groups = screen
			.getAllByRole("group")
			.map((g) => g.getAttribute("aria-label"));
		expect(groups.indexOf("Artifacts")).toBe(groups.indexOf("线程") + 1);
		const group = screen.getByRole("group", { name: "Artifacts" });
		const rows = group.querySelectorAll("[role=option]");
		expect(rows).toHaveLength(1);
		expect(rows[0]).toHaveTextContent("季度报告");
		expect(rows[0]).toHaveTextContent("增长 · 文档 · v3");
		expect(group).not.toHaveTextContent("季度报告草稿");
		expect(
			[...group.querySelectorAll("mark")].map((m) => m.textContent),
		).toEqual(["季度"]);
		fireEvent.click(rows[0] as HTMLElement);
		expect(where()).toBe("/artifacts/art_A");
		expect(paletteStore.getState().open).toBe(false);
	});

	it("类型名与项目名也能匹配，高亮只在标题上", () => {
		mount();
		type("幻灯片");
		let group = screen.getByRole("group", { name: "Artifacts" });
		expect(group.querySelectorAll("[role=option]")).toHaveLength(1);
		expect(group).toHaveTextContent("路线图");
		expect(group.querySelector("mark")).toBeNull();
		type("增长");
		group = screen.getByRole("group", { name: "Artifacts" });
		expect(group.querySelectorAll("[role=option]")).toHaveLength(1);
		expect(group).toHaveTextContent("季度报告");
	});

	it("空查询：最近线程之后是最近打开的至多 3 个 artifact，操作里有「打开 Artifacts」", () => {
		mount();
		const groups = screen
			.getAllByRole("group")
			.map((g) => g.getAttribute("aria-label"));
		expect(groups).toEqual(["最近线程", "最近的 Artifacts", "操作"]);
		const recent = screen.getByRole("group", { name: "最近的 Artifacts" });
		expect(
			[...recent.querySelectorAll("[role=option]")].map(
				(o) => o.querySelector(".text-fg")?.textContent,
			),
		).toEqual(["路线图", "季度报告", "落地页"]);
		fireEvent.click(screen.getByRole("option", { name: "打开 Artifacts" }));
		expect(where()).toBe("/artifacts");
	});
});
