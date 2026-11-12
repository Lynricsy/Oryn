import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import type { Thread } from "../../api/client";
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
		expect(links.map((a) => a.textContent)).toEqual([
			"parent",
			"child",
			"grandchild",
		]);
		expect(links.map((a) => a.style.paddingLeft)).toEqual([
			"0.5rem",
			"1.5rem",
			"2.5rem",
		]);
	});
});
