/**
 * 视图作答的在途记录（M33，docs/plan-m33.md 的 C10）：已发出、对应条目还没回来的作答，视图据此立即锁定。
 * 键是 `threadId:toolCallId`，其下按块路径记；发送失败时清掉。只在本页内存里，并且只在确认之前有效：
 * - 转录里出现了这一块的作答（回执或工具结果）就由转录接管，在途记录作废——之后回退到回执之前，块随转录解锁；
 * - 阻塞作答（`await`）的调用已经有了结果、结果里却没有它（与超时撤回赛跑落败、或流忙被丢），说明没有送到，
 *   在途记录作废、块解锁，之后的作答按非阻塞通道发出。
 */
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { ViewAnswer } from "../lib/view/answer";

export interface ViewInFlight {
	answer: ViewAnswer;
	/** 经阻塞通道（`extension_ui_response`）发出。 */
	awaited: boolean;
}

type Blocks = Readonly<Record<string, ViewInFlight>>;

interface ViewInFlightState {
	pending: Readonly<Record<string, Blocks>>;
}

export const viewInFlightStore = createStore<ViewInFlightState>(() => ({
	pending: {},
}));

function callKey(threadId: string, toolCallId: string): string {
	return `${threadId}:${toolCallId}`;
}

/** 记下一次在途作答。 */
export function markViewInFlight(
	threadId: string,
	toolCallId: string,
	answer: ViewAnswer,
	awaited: boolean,
): void {
	const key = callKey(threadId, toolCallId);
	viewInFlightStore.setState((s) => ({
		pending: {
			...s.pending,
			[key]: { ...s.pending[key], [answer.block]: { answer, awaited } },
		},
	}));
}

/** 清掉这一块的在途作答（发送失败或已作废），视图按转录解锁。 */
export function clearViewInFlight(
	threadId: string,
	toolCallId: string,
	block: string,
): void {
	const key = callKey(threadId, toolCallId);
	viewInFlightStore.setState((s) => {
		const blocks = s.pending[key];
		if (!blocks || !Object.hasOwn(blocks, block)) return s;
		const { [block]: _, ...rest } = blocks;
		if (Object.keys(rest).length > 0)
			return { pending: { ...s.pending, [key]: rest } };
		const { [key]: __, ...others } = s.pending;
		return { pending: others };
	});
}

/** 这次调用的在途作答（块路径 → 记录）；没有时为 undefined。 */
export function useViewInFlight(
	threadId: string,
	toolCallId: string,
): Blocks | undefined {
	return useStore(
		viewInFlightStore,
		(s) => s.pending[callKey(threadId, toolCallId)],
	);
}

/**
 * 按转录现状划分在途作答：`live` 仍应锁定块的作答，`stale` 应清掉的块路径（规则见文件头）。
 * `known` 是转录推导出的作答，`ended` 表示这次调用已经有结果。
 */
export function partitionViewInFlight(
	blocks: Blocks | undefined,
	known: ReadonlyMap<string, ViewAnswer> | undefined,
	ended: boolean,
): { live: [string, ViewAnswer][]; stale: string[] } {
	const live: [string, ViewAnswer][] = [];
	const stale: string[] = [];
	for (const [block, f] of Object.entries(blocks ?? {})) {
		if (known?.has(block) || (f.awaited && ended)) stale.push(block);
		else live.push([block, f.answer]);
	}
	return { live, stale };
}
