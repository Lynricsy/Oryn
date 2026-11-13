/**
 * 托盘状态（M6 计划 6.1）：由线程索引算出「待你处理 / 未读」计数与至多 5 条线程，交给壳的 `host_set_status`。
 * 只看未归档线程；`awaiting_input` 计入待处理（即使同时未读也不再计入未读）；条目先待处理后未读，各按 `updatedAt` 降序。
 */
import type { Thread } from "../api/client";
import type { HostStatus, HostStatusItem } from "./host";

export const HOST_STATUS_ITEMS = 5;

export function computeHostStatus(
	threads: Thread[],
	untitled: string,
): HostStatus {
	const live = threads.filter((t) => !t.archivedAt);
	const newest = (a: Thread, b: Thread) => b.updatedAt - a.updatedAt;
	const awaiting = live
		.filter((t) => t.state === "awaiting_input")
		.sort(newest);
	const unread = live
		.filter((t) => t.state !== "awaiting_input" && t.unread)
		.sort(newest);
	const item =
		(kind: HostStatusItem["kind"]) =>
		(t: Thread): HostStatusItem => ({
			threadId: t.id,
			title: t.title.trim() || untitled,
			kind,
		});
	return {
		awaiting: awaiting.length,
		unread: unread.length,
		items: [
			...awaiting.map(item("awaiting")),
			...unread.map(item("unread")),
		].slice(0, HOST_STATUS_ITEMS),
	};
}
