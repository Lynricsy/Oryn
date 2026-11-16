import { afterEach, describe, expect, it } from "vitest";
import type { Thread } from "../api/client";
import {
	groupByProject,
	groupByState,
	INBOX_GROUP_KEY,
	inboxBadgeCount,
	inboxItems,
	inboxStateOf,
	loadInboxGroupBy,
	saveInboxGroupBy,
} from "./inbox";

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

afterEach(() => localStorage.clear());

describe("收件箱分类", () => {
	it("四种状态；空闲已读不列；准备环境算运行中", () => {
		expect(inboxStateOf(th("a", { state: "awaiting_input" }), NOW)).toBe("you");
		expect(inboxStateOf(th("b", { unread: true }), NOW)).toBe("ready");
		expect(inboxStateOf(th("c", { state: "error" }), NOW)).toBe("stopped");
		expect(inboxStateOf(th("d", { state: "working" }), NOW)).toBe("running");
		expect(inboxStateOf(th("e", { state: "provisioning" }), NOW)).toBe(
			"running",
		);
		expect(inboxStateOf(th("f"), NOW)).toBeNull();
		// 等待输入同时未读：仍是「等你处理」
		expect(
			inboxStateOf(th("g", { state: "awaiting_input", unread: true }), NOW),
		).toBe("you");
	});

	it("已归档、协调对话、未到期的稍后提醒不进；到期后照常分类；子线程各自分类", () => {
		expect(
			inboxStateOf(th("a", { state: "error", archivedAt: NOW }), NOW),
		).toBeNull();
		expect(
			inboxStateOf(th("b", { kind: "assistant", unread: true }), NOW),
		).toBeNull();
		expect(
			inboxStateOf(th("c", { kind: "space", state: "awaiting_input" }), NOW),
		).toBeNull();
		expect(
			inboxStateOf(
				th("d", { state: "awaiting_input", snoozedUntil: NOW + 1 }),
				NOW,
			),
		).toBeNull();
		expect(
			inboxStateOf(
				th("e", { state: "awaiting_input", snoozedUntil: NOW - 1 }),
				NOW,
			),
		).toBe("you");
		expect(
			inboxStateOf(th("f", { parentThreadId: "p", state: "error" }), NOW),
		).toBe("stopped");
	});

	it("按状态分节：固定顺序、空节省略、节内新的在前（无 lastMessageAt 时取 updatedAt）", () => {
		const items = inboxItems(
			[
				th("run", { state: "working" }),
				th("old", { state: "awaiting_input", lastMessageAt: NOW - 10 }),
				th("new", {
					state: "awaiting_input",
					lastMessageAt: undefined,
					updatedAt: NOW + 10,
				}),
				th("err", { state: "error" }),
				th("quiet"),
			],
			NOW,
		);
		expect(
			groupByState(items).map((g) => [
				g.state,
				g.items.map((i) => i.thread.id),
			]),
		).toEqual([
			["you", ["new", "old"]],
			["stopped", ["err"]],
			["running", ["run"]],
		]);
	});

	it("按项目分组：组按最新活动排序，组内先按状态次序；无项目也是一组", () => {
		const items = inboxItems(
			[
				th("a1", { projectId: "a", state: "error", lastMessageAt: NOW - 50 }),
				th("n1", { state: "working", lastMessageAt: NOW - 20 }),
				th("b1", { projectId: "b", unread: true, lastMessageAt: NOW - 10 }),
				th("a2", { projectId: "a", state: "working", lastMessageAt: NOW }),
			],
			NOW,
		);
		expect(
			groupByProject(items).map((g) => [
				g.projectId,
				g.items.map((i) => i.thread.id),
			]),
		).toEqual([
			["a", ["a1", "a2"]],
			["b", ["b1"]],
			[null, ["n1"]],
		]);
	});

	it("角标 = 等你处理 + 已完成未读 + 出错（不含运行中与被排除的）", () => {
		expect(
			inboxBadgeCount(
				[
					th("a", { state: "awaiting_input" }),
					th("b", { unread: true }),
					th("c", { state: "error" }),
					th("d", { state: "working" }),
					th("e", { state: "error", archivedAt: NOW }),
					th("f", { state: "error", snoozedUntil: NOW + 1 }),
					th("g"),
				],
				NOW,
			),
		).toBe(3);
	});

	it("分组方式存 localStorage，非法值回落按状态", () => {
		expect(loadInboxGroupBy()).toBe("state");
		saveInboxGroupBy("project");
		expect(loadInboxGroupBy()).toBe("project");
		localStorage.setItem(INBOX_GROUP_KEY, "bogus");
		expect(loadInboxGroupBy()).toBe("state");
	});
});
