/**
 * 收件箱（跨线程「需要你」）的分类规则：纯函数，侧栏入口的角标、`/inbox` 页面与测试共用。
 * 只看工作线程：已归档、协调对话（Space 聊天、助手对话）、尚未醒来的「稍后提醒」线程都不进收件箱。
 * 四种状态：you = 等待输入；ready = 空闲且未读（做完了还没看）；stopped = 出错；running = 工作中 / 准备环境中。
 * 空闲且已读的线程不列出。fork / 协调者派生的子线程各自按自己的状态分类（不并入父线程）。
 */
import type { Thread } from "../api/client";
import { isCoordinatorThread } from "./thread-kind";

export type InboxState = "you" | "ready" | "stopped" | "running";

/** 页面上的分节顺序；也是按项目分组时组内的状态次序。 */
export const INBOX_ORDER: readonly InboxState[] = [
	"you",
	"ready",
	"stopped",
	"running",
];

/** 线程的最后活动时刻（与侧栏一致：最后一条消息，没有则取更新时间）。 */
export function activityOf(t: Thread): number {
	return t.lastMessageAt ?? t.updatedAt;
}

/** 线程在收件箱里的状态；不进收件箱时为 null。`now` 用来判断稍后提醒是否已到期。 */
export function inboxStateOf(t: Thread, now: number): InboxState | null {
	if (t.archivedAt || isCoordinatorThread(t)) return null;
	if (t.snoozedUntil && t.snoozedUntil > now) return null;
	switch (t.state) {
		case "awaiting_input":
			return "you";
		case "error":
			return "stopped";
		case "working":
		case "provisioning":
			return "running";
		case "idle":
			return t.unread ? "ready" : null;
	}
}

export interface InboxItem {
	thread: Thread;
	state: InboxState;
}

/** 收件箱条目：按最后活动新的在前。 */
export function inboxItems(
	threads: readonly Thread[],
	now: number,
): InboxItem[] {
	const out: InboxItem[] = [];
	for (const thread of threads) {
		const state = inboxStateOf(thread, now);
		if (state) out.push({ thread, state });
	}
	return out.sort((a, b) => activityOf(b.thread) - activityOf(a.thread));
}

/** 按状态分节：顺序固定为 INBOX_ORDER，空节省略；节内新的在前。 */
export function groupByState(
	items: readonly InboxItem[],
): { state: InboxState; items: InboxItem[] }[] {
	return INBOX_ORDER.map((state) => ({
		state,
		items: items.filter((i) => i.state === state),
	})).filter((g) => g.items.length > 0);
}

/**
 * 按项目分组：组按其中最新的活动排序（无项目与普通项目一样参与排序）；组内先按 INBOX_ORDER 的状态次序，
 * 同一状态新的在前。`items` 须已按活动排好（`inboxItems` 的输出），排序是稳定的。
 */
export function groupByProject(
	items: readonly InboxItem[],
): { projectId: string | null; items: InboxItem[] }[] {
	const groups = new Map<string | null, InboxItem[]>();
	for (const item of items) {
		const key = item.thread.projectId ?? null;
		const list = groups.get(key) ?? [];
		list.push(item);
		groups.set(key, list);
	}
	return [...groups.entries()].map(([projectId, list]) => ({
		projectId,
		items: list.sort(
			(a, b) => INBOX_ORDER.indexOf(a.state) - INBOX_ORDER.indexOf(b.state),
		),
	}));
}

/** 入口角标：等你处理 + 已完成未读 + 出错的线程数（运行中只是告知，不算待处理）。 */
export function inboxBadgeCount(
	threads: readonly Thread[],
	now: number,
): number {
	let n = 0;
	for (const t of threads) {
		const s = inboxStateOf(t, now);
		if (s && s !== "running") n++;
	}
	return n;
}

export type InboxGroupBy = "state" | "project";

/** localStorage 键：收件箱的分组方式（`state` | `project`）。 */
export const INBOX_GROUP_KEY = "oryn.inbox.groupBy";

export function loadInboxGroupBy(): InboxGroupBy {
	try {
		return globalThis.localStorage?.getItem(INBOX_GROUP_KEY) === "project"
			? "project"
			: "state";
	} catch {
		return "state";
	}
}

export function saveInboxGroupBy(v: InboxGroupBy): void {
	try {
		globalThis.localStorage?.setItem(INBOX_GROUP_KEY, v);
	} catch {
		// 隐私模式下 localStorage 不可用：只在本次会话生效
	}
}
