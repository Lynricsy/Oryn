/**
 * 视图作答的在途记录（M33，docs/plan-m33.md 的 C10）：已发出、对应条目还没回来的作答，视图据此立即锁定。
 * 键是 `threadId:toolCallId`，其下按块路径记；发送失败时清掉。只在本页内存里：条目到达后转录推导出的作答
 * 接管（同一块取最早，内容相同），刷新页面后以转录为准。
 */
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { ViewAnswer } from "../lib/view/answer";

type Blocks = Readonly<Record<string, ViewAnswer>>;

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
): void {
	const key = callKey(threadId, toolCallId);
	viewInFlightStore.setState((s) => ({
		pending: {
			...s.pending,
			[key]: { ...s.pending[key], [answer.block]: answer },
		},
	}));
}

/** 发送失败：清掉这一块的在途作答，视图解锁。 */
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

/** 这次调用的在途作答（块路径 → 作答）；没有时为 undefined。 */
export function useViewInFlight(
	threadId: string,
	toolCallId: string,
): Blocks | undefined {
	return useStore(
		viewInFlightStore,
		(s) => s.pending[callKey(threadId, toolCallId)],
	);
}
