import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { Thread } from "../api/client";
import { threads as fixtureThreads } from "../mock/fixtures";
import { indexStore } from "../state/stores";
import { useAssistantConversations } from "./assistant-conversations";

const base = fixtureThreads[0] as Thread;

function thread(
	id: string,
	kind: Thread["kind"],
	at: number,
	extra: Partial<Thread> = {},
): Thread {
	return {
		...base,
		id,
		kind,
		updatedAt: at,
		lastMessageAt: at,
		archivedAt: undefined,
		...extra,
	};
}

afterEach(() => {
	indexStore.getState().setAll([], 0);
});

describe("useAssistantConversations", () => {
	it("只留未归档的助手对话，最近活动在前（没有 lastMessageAt 时按 updatedAt）", () => {
		indexStore.getState().setAll(
			[
				thread("T-a1", "assistant", 10),
				thread("T-a2", "assistant", 30),
				thread("T-a3", "assistant", 0, {
					lastMessageAt: undefined,
					updatedAt: 20,
				}),
				thread("T-old", "assistant", 99, { archivedAt: 50 }),
				thread("T-work", "work", 100),
				thread("T-chat", "space", 100),
			],
			1,
		);
		const { result } = renderHook(() => useAssistantConversations());
		expect(result.current.map((t) => t.id)).toEqual(["T-a2", "T-a3", "T-a1"]);
	});

	it("索引变化后重新计算；索引没变时返回同一个数组", () => {
		indexStore.getState().setAll([thread("T-a1", "assistant", 10)], 1);
		const { result, rerender } = renderHook(() => useAssistantConversations());
		const first = result.current;
		rerender();
		expect(result.current).toBe(first);
		act(() => {
			indexStore.getState().upsertThread(thread("T-a2", "assistant", 20));
		});
		expect(result.current.map((t) => t.id)).toEqual(["T-a2", "T-a1"]);
	});
});
