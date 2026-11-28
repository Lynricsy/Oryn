/**
 * 写作块改稿（C5）：`GET /api/v1/threads/{id}/drafts` 的查询。
 * 写入走 `state/draft-saves.ts`（防抖保存后直接改这份缓存）。
 */
import { type UseQueryResult, useQuery } from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";

export type ThreadDraft = Schemas["ThreadDraft"];

export const threadDraftsKey = (threadId: string) =>
	["threads", threadId, "drafts"] as const;

/** 缓存键：`<entryId>/<block>`。 */
export function draftMapKey(entryId: string, block: string): string {
	return `${entryId}/${block}`;
}

/** 线程里的全部改稿，按 `<entryId>/<block>` 索引；多设备共享，窗口重新聚焦时刷新。 */
export function useThreadDrafts(
	threadId: string,
	enabled: boolean,
): UseQueryResult<Map<string, ThreadDraft>> {
	return useQuery({
		queryKey: threadDraftsKey(threadId),
		queryFn: () =>
			unwrap(
				api.GET("/api/v1/threads/{id}/drafts", {
					params: { path: { id: threadId } },
				}),
			),
		enabled,
		staleTime: 0,
		refetchOnWindowFocus: true,
		select: (data) =>
			new Map(data.drafts.map((d) => [draftMapKey(d.entryId, d.block), d])),
	});
}
