import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import type { Thread } from "../../api/client";
import { parseTitleCard } from "../../lib/title";
import { groupThreads, ThreadRow } from "./Sidebar";

const NOW = 1_800_000_000_000;

function th(id: string, p: Partial<Thread> = {}): Thread {
	return {
		id,
		title: id,
		titleSource: "auto",
		state: "idle",
		stateChangedAt: NOW,
		pinned: false,
		labels: [],
		unread: false,
		createdAt: NOW,
		updatedAt: NOW,
		lastMessageAt: NOW,
		...p,
	};
}

describe("侧边栏线程树", () => {
	it("fork 再 fork：孙线程也渲染并逐级缩进（W10）", () => {
		const g = groupThreads(
			[
				th("parent"),
				th("child", { parentThreadId: "parent" }),
				th("grandchild", { parentThreadId: "child" }),
			],
			"",
			NOW,
		);
		const root = g.byProject[0]?.threads[0] as Thread;
		render(
			<MemoryRouter>
				<ThreadRow thread={root} childMap={g.children} />
			</MemoryRouter>,
		);
		const links = screen.getAllByRole("link");
		// 契约：链接 textContent 只等于标题卡解析后的标题文字
		expect(links.map((a) => a.textContent)).toEqual(
			["parent", "child", "grandchild"].map((s) => parseTitleCard(s).text),
		);
		expect(links.map((a) => a.style.paddingLeft)).toEqual([
			"0.5rem",
			"1.5rem",
			"2.5rem",
		]);
	});

	it("标题卡图标、时间、子线程计数、活动行都不进链接文字", () => {
		const g = groupThreads(
			[
				th("p", {
					title: "🔌 SOCK: 重构 WebSocket 重连",
					state: "working",
					activity: "读取 socket.ts",
					childCount: 2,
					childDoneCount: 1,
				}),
				th("c", { title: "fork：共用 orb", parentThreadId: "p", unread: true }),
			],
			"",
			NOW,
		);
		const root = g.byProject[0]?.threads[0] as Thread;
		const { container } = render(
			<MemoryRouter>
				<ThreadRow thread={root} childMap={g.children} now={NOW} />
			</MemoryRouter>,
		);
		const links = screen.getAllByRole("link");
		expect(links.map((a) => a.textContent)).toEqual([
			"重构 WebSocket 重连",
			"fork：共用 orb",
		]);
		expect(links.map((a) => a.style.paddingLeft)).toEqual(["0.5rem", "1.5rem"]);
		expect(
			container.querySelector("[data-glyph]")?.getAttribute("data-glyph"),
		).toBe("🔌");
		expect(
			container.querySelector("[data-count]")?.getAttribute("data-count"),
		).toBe("1/2");
		expect(
			container.querySelector("[data-activity]")?.getAttribute("data-activity"),
		).toBe("读取 socket.ts");
	});
});
