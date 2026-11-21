/**
 * 项目活动（M24）：按项目统计线程数与最后活跃时刻，项目总览与侧栏项目行共用。
 * 只算工作线程：已归档线程与协调对话（Space 聊天、助手对话）不计。
 */
import type { Project, Thread } from "../api/client";
import { activityOf } from "./inbox";
import { isCoordinatorThread } from "./thread-kind";

export interface ProjectActivity {
	threads: number;
	/** 最近一个线程的最后活动时刻；没有线程时省略 */
	lastActiveAt?: number;
}

/** 项目 id → 活动；没有线程的项目不在表里。 */
export function projectActivity(
	threads: readonly Thread[],
): Map<string, ProjectActivity> {
	const out = new Map<string, ProjectActivity>();
	for (const t of threads) {
		if (!t.projectId || t.archivedAt || isCoordinatorThread(t)) continue;
		const at = activityOf(t);
		const cur = out.get(t.projectId);
		if (!cur) out.set(t.projectId, { threads: 1, lastActiveAt: at });
		else {
			cur.threads += 1;
			if (cur.lastActiveAt === undefined || at > cur.lastActiveAt)
				cur.lastActiveAt = at;
		}
	}
	return out;
}

/** 项目的最后活跃时刻：没有线程时取项目的创建时间。 */
export function lastActive(p: Project, a: ProjectActivity | undefined): number {
	return a?.lastActiveAt ?? p.createdAt;
}

/** 排序比较器：最近活跃的在前，同值按名称。 */
export function byActivity(
	map: ReadonlyMap<string, ProjectActivity>,
): (a: Project, b: Project) => number {
	return (a, b) =>
		lastActive(b, map.get(b.id)) - lastActive(a, map.get(a.id)) ||
		a.name.localeCompare(b.name);
}
