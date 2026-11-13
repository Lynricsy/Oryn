/**
 * 新建线程时的首条任务文字（仅本页内存）。
 * 服务端的 Thread 不带 prompt 字段，任务要等 agent 启动后才作为 user 条目出现在转录里；
 * 首页在 `createThread` 成功后写入，转录在环境准备阶段先以「发送中」气泡显示它，
 * 出现第一条 user 消息即清除。刷新页面后丢失（已知并接受，不为此改协议）。
 */
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

interface PendingPromptState {
	prompts: Record<string, string>;
}

export const pendingPromptStore = createStore<PendingPromptState>(() => ({
	prompts: {},
}));

export function setPendingPrompt(threadId: string, text: string): void {
	if (!text) return;
	pendingPromptStore.setState((s) => ({
		prompts: { ...s.prompts, [threadId]: text },
	}));
}

export function clearPendingPrompt(threadId: string): void {
	pendingPromptStore.setState((s) => {
		if (!(threadId in s.prompts)) return s;
		const { [threadId]: _, ...rest } = s.prompts;
		return { prompts: rest };
	});
}

export function usePendingPrompt(threadId: string): string | undefined {
	return useStore(pendingPromptStore, (s) => s.prompts[threadId]);
}
