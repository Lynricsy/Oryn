/** 全局助手的对话列表（M9.6）：助手页与「助手」分区侧栏共用。 */
import { useMemo } from "react";
import type { Thread } from "../api/client";
import { useIndex } from "../state/stores";

/** 未归档的助手对话，最近活动的在前。 */
export function useAssistantConversations(): Thread[] {
	const threads = useIndex((s) => s.threads);
	return useMemo(
		() =>
			Object.values(threads)
				.filter((t) => t.kind === "assistant" && !t.archivedAt)
				.sort(
					(a, b) =>
						(b.lastMessageAt ?? b.updatedAt) - (a.lastMessageAt ?? a.updatedAt),
				),
		[threads],
	);
}
