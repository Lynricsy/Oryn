import { describe, expect, it } from "vitest";
import type { Thread } from "../../api/client";
import {
	matchesQuery,
	mergeProjectThreads,
	STATUS_FILTERS,
} from "./project-threads";

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

describe("mergeProjectThreads", () => {
	it("只留本项目的非协调线程，同 id 以索引为准，按活动降序", () => {
		const index = {
			a: th("a", { projectId: "p", lastMessageAt: 5, title: "live" }),
			b: th("b", { projectId: "q", lastMessageAt: 9 }),
			c: th("c", { projectId: "p", kind: "space", lastMessageAt: 9 }),
		};
		const archived = [
			th("a", { projectId: "p", lastMessageAt: 1, title: "stale" }),
			th("d", { projectId: "p", lastMessageAt: 7, archivedAt: 7 }),
		];
		const out = mergeProjectThreads(index, archived, "p");
		expect(out.map((t) => t.id)).toEqual(["d", "a"]);
		expect(out[1]?.title).toBe("live");
	});

	it("归档列表未到时只用索引", () => {
		expect(
			mergeProjectThreads({ a: th("a", { projectId: "p" }) }, undefined, "p"),
		).toHaveLength(1);
	});
});

describe("STATUS_FILTERS", () => {
	it("各档可以重叠", () => {
		const shippedLive = th("s", { shippedAt: 3 });
		const shippedArchived = th("t", { shippedAt: 3, archivedAt: 4 });
		expect(STATUS_FILTERS.active(shippedLive)).toBe(true);
		expect(STATUS_FILTERS.shipped(shippedLive)).toBe(true);
		expect(STATUS_FILTERS.archived(shippedArchived)).toBe(true);
		expect(STATUS_FILTERS.active(shippedArchived)).toBe(false);
		expect(STATUS_FILTERS.shipped(th("x"))).toBe(false);
		expect(STATUS_FILTERS.all(shippedArchived)).toBe(true);
	});
});

describe("matchesQuery", () => {
	const t = th("a", { title: "🐞 BUGX: Fix Login", labels: ["Urgent"] });
	it("每个词都要命中标题文字或标签，不分大小写", () => {
		expect(matchesQuery(t, "")).toBe(true);
		expect(matchesQuery(t, "  login  ")).toBe(true);
		expect(matchesQuery(t, "fix urgent")).toBe(true);
		expect(matchesQuery(t, "fix missing")).toBe(false);
	});
	it("不匹配标题卡的代码前缀", () => {
		expect(matchesQuery(t, "bugx")).toBe(false);
	});
});
