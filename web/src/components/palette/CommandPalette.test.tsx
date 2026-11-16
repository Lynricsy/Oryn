import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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

function Where() {
	const loc = useLocation();
	return <p data-testid="where">{loc.pathname}</p>;
}

function mount() {
	const qc = new QueryClient({
		defaultOptions: { queries: { retry: false, staleTime: Infinity } },
	});
	qc.setQueryData(["projects"], { projects: [] });
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
});
