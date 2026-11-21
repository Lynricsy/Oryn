/**
 * 项目的线程（M24）：索引里的实时线程并上本项目的归档线程，供线程子页与项目主页的「最近线程」使用；
 * 以及线程子页的状态筛选与搜索。协调对话（Space 聊天、助手对话）不算项目线程。
 */
import type { Thread } from "../../api/client";
import { useArchivedThreads } from "../../api/queries";
import { activityOf } from "../../lib/inbox";
import { isCoordinatorThread } from "../../lib/thread-kind";
import { parseTitleCard } from "../../lib/title";
import { useIndex } from "../../state/stores";

/** 合并索引与归档列表：同一 id 以索引为准（实时），按最后活动降序。 */
export function mergeProjectThreads(
	index: Readonly<Record<string, Thread>>,
	archived: readonly Thread[] | undefined,
	projectId: string,
): Thread[] {
	const byId = new Map<string, Thread>();
	for (const t of archived ?? []) byId.set(t.id, t);
	for (const t of Object.values(index)) byId.set(t.id, t);
	return [...byId.values()]
		.filter((t) => t.projectId === projectId && !isCoordinatorThread(t))
		.sort((a, b) => activityOf(b) - activityOf(a));
}

/** 本项目的线程：索引 + 只拉本项目的归档线程（`GET /threads?archived=true&project=<id>`）。 */
export function useProjectThreads(projectId: string): {
	threads: Thread[];
	archivedPending: boolean;
	archivedError: unknown;
} {
	const index = useIndex((s) => s.threads);
	const archived = useArchivedThreads(true, projectId);
	return {
		threads: mergeProjectThreads(index, archived.data?.threads, projectId),
		archivedPending: archived.isPending,
		archivedError: archived.error,
	};
}

export type ThreadStatusFilter = "all" | "active" | "archived" | "shipped";

/** 各档允许重叠：已交付的线程同时出现在「活跃」或「已归档」里。 */
export const STATUS_FILTERS: Record<
	ThreadStatusFilter,
	(t: Thread) => boolean
> = {
	all: () => true,
	active: (t) => t.archivedAt === undefined,
	archived: (t) => t.archivedAt !== undefined,
	shipped: (t) => t.shippedAt !== undefined,
};

/** 按空白切词，每个词（不分大小写）都要命中标题文字或某个标签。 */
export function matchesQuery(t: Thread, q: string): boolean {
	const words = q.toLowerCase().split(/\s+/).filter(Boolean);
	if (words.length === 0) return true;
	const hay = [
		parseTitleCard(t.title).text.toLowerCase(),
		...t.labels.map((l) => l.toLowerCase()),
	];
	return words.every((w) => hay.some((h) => h.includes(w)));
}
