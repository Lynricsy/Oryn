/**
 * 已发出、尚未在转录里出现的 user 消息（仅本页内存），转录末尾先以「发送中」气泡显示：
 * - 新建线程的首条任务：服务端的 Thread 不带 prompt 字段，任务要等 agent 启动后才作为 user 条目出现（`afterSeq` 为 0）；
 * - 线程里 agent 未就绪（orb 冻结 / 唤醒中、agent 启动中）时从输入区发出的消息：命令在服务端排队，要等 agent 接下
 *   才应答、才有 user 条目。输入区记下发送时线程的 `lastSeq`，发送失败时自行清除。
 * 转录按发送先后把 `afterSeq` 之后到达的 user 行逐条认领给气泡（`waitingPendingPrompts`），只画没认领到的，
 * 同一句话不会出现两次，应答与条目到达之间也不会空一下。全部认领到才一并清除：先清掉前面的，它认领过的 user 行
 * 会被后面的气泡再认领一次。刷新页面后丢失（已知并接受，不为此改协议）。
 */
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

export interface PendingPrompt {
	id: number;
	text: string;
	/** 发送时线程已有条目的最大序号：此后到达的 user 行才可能是它。 */
	afterSeq: number;
}

interface PendingPromptState {
	prompts: Record<string, readonly PendingPrompt[]>;
}

export const pendingPromptStore = createStore<PendingPromptState>(() => ({
	prompts: {},
}));

let nextId = 0;
const NONE: readonly PendingPrompt[] = [];

/** 记下一条发送中的消息，返回它的 id（发送失败时据此清除）；空文字不记。 */
export function addPendingPrompt(
	threadId: string,
	text: string,
	afterSeq = 0,
): number | undefined {
	if (!text) return undefined;
	const id = ++nextId;
	pendingPromptStore.setState((s) => ({
		prompts: {
			...s.prompts,
			[threadId]: [...(s.prompts[threadId] ?? NONE), { id, text, afterSeq }],
		},
	}));
	return id;
}

export function removePendingPrompts(
	threadId: string,
	ids: readonly number[],
): void {
	pendingPromptStore.setState((s) => {
		const list = s.prompts[threadId];
		if (!list?.some((p) => ids.includes(p.id))) return s;
		const rest = list.filter((p) => !ids.includes(p.id));
		if (rest.length > 0) return { prompts: { ...s.prompts, [threadId]: rest } };
		const { [threadId]: _, ...others } = s.prompts;
		return { prompts: others };
	});
}

export function usePendingPrompts(threadId: string): readonly PendingPrompt[] {
	return useStore(pendingPromptStore, (s) => s.prompts[threadId] ?? NONE);
}

/**
 * 按发送先后认领：每条气泡取 `afterSeq` 之后第一条尚未被认领的 user 行（`userSeqs` 为转录顺序的 user 行序号）。
 * `afterSeq` 随发送先后不减，认领到的总是开头一段。返回仍在发送中（没认领到）的气泡；一条都没认领到时原样返回。
 */
export function waitingPendingPrompts(
	pending: readonly PendingPrompt[],
	userSeqs: readonly number[],
): readonly PendingPrompt[] {
	let j = 0;
	for (const [i, p] of pending.entries()) {
		while (j < userSeqs.length && (userSeqs[j] ?? 0) <= p.afterSeq) j++;
		if (j >= userSeqs.length) return i === 0 ? pending : pending.slice(i);
		j++;
	}
	return NONE;
}
