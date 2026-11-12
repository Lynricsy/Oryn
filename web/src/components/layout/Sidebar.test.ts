import { describe, expect, it } from "vitest";
import type { Thread } from "../../api/client";
import { groupThreads } from "./Sidebar";

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

describe("侧边栏分区", () => {
	it("Pinned → 按项目 → Inactive 72h → Snoozed，子线程挂在父线程下", () => {
		const g = groupThreads(
			[
				th("pinned", { pinned: true }),
				th("a", { projectId: "p1" }),
				th("b"),
				th("child", { parentThreadId: "a" }),
				th("old", { lastMessageAt: NOW - 80 * 3600_000 }),
				th("oldBusy", { lastMessageAt: NOW - 80 * 3600_000, state: "working" }),
				th("snoozed", { snoozedUntil: NOW + 1000 }),
				th("archived", { archivedAt: NOW }),
			],
			"",
			NOW,
		);
		expect(g.pinned.map((t) => t.id)).toEqual(["pinned"]);
		expect(
			g.byProject.map((x) => [x.projectId, x.threads.map((t) => t.id)]),
		).toEqual([
			["p1", ["a"]],
			[null, ["b", "oldBusy"]],
		]);
		expect(g.children.get("a")?.map((t) => t.id)).toEqual(["child"]);
		expect(g.inactive.map((t) => t.id)).toEqual(["old"]);
		expect(g.snoozed.map((t) => t.id)).toEqual(["snoozed"]);
	});

	it("搜索匹配标题与标签", () => {
		const g = groupThreads(
			[
				th("x", { title: "重连", labels: [] }),
				th("y", { title: "z", labels: ["realtime"] }),
				th("w"),
			],
			"real",
			NOW,
		);
		expect(g.byProject.flatMap((x) => x.threads.map((t) => t.id))).toEqual([
			"y",
		]);
	});
});
