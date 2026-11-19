import { describe, expect, it } from "vitest";
import type { Thread } from "../../api/client";
import {
	filterByProject,
	groupThreads,
	rollupState,
	stackState,
} from "./ThreadsPane";

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

describe("「需要你」分组与状态上卷", () => {
	it("等待输入在前、出错在后，各按活动新的在前；子线程平铺进来，原分组照常保留", () => {
		const g = groupThreads(
			[
				th("idle"),
				th("errOld", { state: "error", lastMessageAt: NOW - 5000 }),
				th("waitOld", { state: "awaiting_input", lastMessageAt: NOW - 9000 }),
				th("parent", { projectId: "p1" }),
				th("kid", {
					projectId: "p1",
					parentThreadId: "parent",
					state: "awaiting_input",
				}),
				th("pinnedErr", { pinned: true, state: "error" }),
				th("busy", { state: "working" }),
			],
			NOW,
		);
		expect(g.needsYou.map((t) => t.id)).toEqual([
			"kid",
			"waitOld",
			"pinnedErr",
			"errOld",
		]);
		// 子线程仍挂在父线程下，置顶的仍在置顶
		expect(g.children.get("parent")?.map((t) => t.id)).toEqual(["kid"]);
		expect(g.pinned.map((t) => t.id)).toEqual(["pinnedErr"]);
	});

	it("已归档、协调对话不进；稍后提醒到期前不进，到期后进", () => {
		const threads = [
			th("archived", { state: "awaiting_input", archivedAt: NOW }),
			th("chat", { kind: "space", state: "awaiting_input" }),
			th("sleeping", { state: "error", snoozedUntil: NOW + 1000 }),
			th("woke", { state: "error", snoozedUntil: NOW - 1000 }),
			th("other", { title: "zzz", state: "awaiting_input" }),
		];
		expect(groupThreads(threads, NOW).needsYou.map((t) => t.id)).toEqual([
			"other",
			"woke",
		]);
	});

	it("子孙的等待输入 / 出错上卷到父行；工作中不上卷；自身更紧要时取自身；数据成环不死循环", () => {
		const g = groupThreads(
			[
				th("root"),
				th("mid", { parentThreadId: "root", state: "working" }),
				th("leaf", { parentThreadId: "mid", state: "error" }),
				th("busyParent"),
				th("busyKid", { parentThreadId: "busyParent", state: "working" }),
				th("waiting", { state: "awaiting_input" }),
				th("errKid", { parentThreadId: "waiting", state: "error" }),
			],
			NOW,
		);
		const byId = (id: string) =>
			[...g.byProject.flatMap((x) => x.threads), ...g.children.values()]
				.flat()
				.find((t) => t.id === id) as Thread;
		expect(rollupState(byId("root"), g.children)).toBe("error");
		expect(rollupState(byId("mid"), g.children)).toBe("error");
		expect(rollupState(byId("busyParent"), g.children)).toBe("idle");
		expect(rollupState(byId("waiting"), g.children)).toBe("awaiting_input");
		// 不给 childMap（「需要你」分组的平铺行）只看自身
		expect(rollupState(byId("root"), undefined)).toBe("idle");
		const a = th("a");
		const b = th("b", { parentThreadId: "a", state: "awaiting_input" });
		const loop = new Map([
			["a", [b]],
			["b", [a]],
		]);
		expect(rollupState(a, loop)).toBe("awaiting_input");
	});

	it("折叠行汇总计入运行线程子孙里需要你的状态", () => {
		const g = groupThreads(
			[
				th("run", { scheduleRun: { scheduleId: "s", run: 1 } }),
				th("kid", { parentThreadId: "run", state: "awaiting_input" }),
			],
			NOW,
		);
		const runs = g.byProject[0]?.stacks[0]?.threads ?? [];
		expect(stackState(runs)).toBe("idle");
		expect(
			stackState(runs.map((r) => ({ state: rollupState(r, g.children) }))),
		).toBe("awaiting_input");
	});
});
