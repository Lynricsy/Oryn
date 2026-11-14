import { describe, expect, it } from "vitest";
import type { Thread } from "../../api/client";
import { filterByProject, groupThreads, stackState } from "./Sidebar";

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

describe("侧边栏分区（M9）", () => {
	it("协调对话（Space 聊天、助手对话）不进任何分组", () => {
		const g = groupThreads(
			[
				th("work"),
				th("chat", { kind: "space", spaceId: "spc" }),
				th("helper", { kind: "assistant", pinned: true }),
				th("child", { kind: "work", parentThreadId: "chat" }),
			],
			"",
			NOW,
		);
		expect(g.pinned).toEqual([]);
		// 父线程是协调对话（不可见）时，子线程作为根出现
		expect(g.byProject.flatMap((x) => x.threads.map((t) => t.id))).toEqual([
			"work",
			"child",
		]);
	});

	it("定时运行按定时折叠：新的在前、不进 72 小时未活动；置顶与稍后提醒照旧；项目计数含折叠", () => {
		const run = (id: string, scheduleId: string, n: number, p = {}) =>
			th(id, {
				projectId: "p1",
				scheduleRun: { scheduleId, run: n },
				...p,
			});
		const g = groupThreads(
			[
				th("plain", { projectId: "p1" }),
				run("a1", "sch_a", 1, { lastMessageAt: NOW - 80 * 3600_000 }),
				run("a3", "sch_a", 3),
				run("a2", "sch_a", 2),
				run("b1", "sch_b", 1),
				run("pinned", "sch_a", 4, { pinned: true }),
				run("snoozed", "sch_a", 5, { snoozedUntil: NOW + 1000 }),
				th("nostack", { scheduleRun: { scheduleId: "sch_c", run: 1 } }),
			],
			"",
			NOW,
		);
		const p1 = g.byProject.find((x) => x.projectId === "p1");
		expect(p1?.threads.map((t) => t.id)).toEqual(["plain"]);
		expect(
			p1?.stacks.map((s) => [s.scheduleId, s.threads.map((t) => t.id)]),
		).toEqual([
			["sch_a", ["a3", "a2", "a1"]],
			["sch_b", ["b1"]],
		]);
		expect(g.inactive).toEqual([]);
		expect(g.pinned.map((t) => t.id)).toEqual(["pinned"]);
		expect(g.snoozed.map((t) => t.id)).toEqual(["snoozed"]);
		// 只有折叠、没有普通线程的项目也有分组
		const none = g.byProject.find((x) => x.projectId === null);
		expect(none?.threads).toEqual([]);
		expect(none?.stacks.map((s) => s.scheduleId)).toEqual(["sch_c"]);
	});

	it("折叠行的汇总状态：等待输入 > 出错 > 工作中 > 准备中 > 空闲", () => {
		expect(stackState([th("a"), th("b", { state: "working" })])).toBe(
			"working",
		);
		expect(
			stackState([
				th("a", { state: "error" }),
				th("b", { state: "awaiting_input" }),
				th("c", { state: "working" }),
			]),
		).toBe("awaiting_input");
		expect(
			stackState([
				th("a", { state: "provisioning" }),
				th("b", { state: "error" }),
			]),
		).toBe("error");
		expect(stackState([])).toBe("idle");
	});

	it("项目过滤：undefined 不过滤，null 只留无项目，否则只留该项目", () => {
		const list = [
			th("a", { projectId: "p1" }),
			th("b"),
			th("c", { projectId: "p2" }),
		];
		expect(filterByProject(list, undefined).map((t) => t.id)).toEqual([
			"a",
			"b",
			"c",
		]);
		expect(filterByProject(list, null).map((t) => t.id)).toEqual(["b"]);
		expect(filterByProject(list, "p2").map((t) => t.id)).toEqual(["c"]);
	});
});
