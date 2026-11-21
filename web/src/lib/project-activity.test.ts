import { describe, expect, it } from "vitest";
import type { Project, Thread } from "../api/client";
import { byActivity, lastActive, projectActivity } from "./project-activity";

function th(id: string, p: Partial<Thread> = {}): Thread {
	return {
		id,
		title: id,
		titleSource: "auto",
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

function prj(id: string, name: string, createdAt = 1): Project {
	return {
		id,
		name,
		shallowClone: false,
		snapshots: true,
		warmOrbs: 0,
		createdAt,
		updatedAt: createdAt,
	};
}

describe("projectActivity", () => {
	it("只算未归档的工作线程，取最后活动的最大值", () => {
		const m = projectActivity([
			th("a", { projectId: "p", lastMessageAt: 5 }),
			th("b", { projectId: "p", kind: "space", lastMessageAt: 9 }),
			th("c", { projectId: "p", archivedAt: 1, lastMessageAt: 8 }),
			th("d", { lastMessageAt: 20 }),
		]);
		expect(m.get("p")).toEqual({ threads: 1, lastActiveAt: 5 });
		expect(m.size).toBe(1);
	});

	it("多个线程累加，没有消息时取 updatedAt", () => {
		const m = projectActivity([
			th("a", { projectId: "p", lastMessageAt: 5 }),
			th("b", { projectId: "p", updatedAt: 7 }),
		]);
		expect(m.get("p")).toEqual({ threads: 2, lastActiveAt: 7 });
	});
});

describe("lastActive / byActivity", () => {
	it("没有线程时回落到创建时间；最近活跃的在前，同值按名称", () => {
		const a = prj("a", "alpha", 3);
		const b = prj("b", "beta", 3);
		const c = prj("c", "gamma", 1);
		const m = new Map([["c", { threads: 1, lastActiveAt: 10 }]]);
		expect(lastActive(a, m.get("a"))).toBe(3);
		expect(lastActive(c, m.get("c"))).toBe(10);
		expect([b, a, c].toSorted(byActivity(m)).map((p) => p.id)).toEqual([
			"c",
			"a",
			"b",
		]);
	});
});
