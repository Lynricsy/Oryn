/** 托盘状态：归档不计、待处理不重复计入未读、排序与 5 条上限、空标题。 */
import { describe, expect, it } from "vitest";
import type { Thread } from "../api/client";
import { computeHostStatus } from "./host-status";

let n = 0;
function thread(p: Partial<Thread>): Thread {
	n++;
	return {
		id: `T-${n}`,
		title: `线程 ${n}`,
		titleSource: "auto",
		state: "idle",
		stateChangedAt: 1,
		pinned: false,
		labels: [],
		unread: false,
		createdAt: 1,
		updatedAt: n,
		...p,
	};
}

describe("computeHostStatus", () => {
	it("归档的不计；待处理同时未读只计入待处理", () => {
		const s = computeHostStatus(
			[
				thread({ id: "A", state: "awaiting_input", unread: true }),
				thread({ id: "B", unread: true }),
				thread({ id: "C", unread: true, archivedAt: 5 }),
				thread({ id: "D", state: "awaiting_input", archivedAt: 5 }),
				thread({ id: "E" }),
			],
			"未命名",
		);
		expect(s.awaiting).toBe(1);
		expect(s.unread).toBe(1);
		expect(s.items.map((i) => [i.threadId, i.kind])).toEqual([
			["A", "awaiting"],
			["B", "unread"],
		]);
	});

	it("先待处理后未读，各按 updatedAt 降序，共至多 5 条；计数不受上限影响", () => {
		const s = computeHostStatus(
			[
				thread({ id: "u1", unread: true, updatedAt: 10 }),
				thread({ id: "u2", unread: true, updatedAt: 30 }),
				thread({ id: "a1", state: "awaiting_input", updatedAt: 5 }),
				thread({ id: "a2", state: "awaiting_input", updatedAt: 50 }),
				thread({ id: "u3", unread: true, updatedAt: 20 }),
				thread({ id: "u4", unread: true, updatedAt: 40 }),
			],
			"未命名",
		);
		expect(s.awaiting).toBe(2);
		expect(s.unread).toBe(4);
		expect(s.items.map((i) => i.threadId)).toEqual([
			"a2",
			"a1",
			"u4",
			"u2",
			"u3",
		]);
	});

	it("空标题用给定的占位", () => {
		const s = computeHostStatus(
			[thread({ id: "x", title: "  ", unread: true })],
			"未命名线程",
		);
		expect(s.items).toEqual([
			{ threadId: "x", title: "未命名线程", kind: "unread" },
		]);
	});
});
