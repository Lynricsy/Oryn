/**
 * 输入区草稿：按线程存在组件外（只在内存，不落盘）。输入坞跨 compact 断点、切线程都会重新挂载 Composer，
 * 文字、错误行、光标位置不能跟着组件 state 一起丢；各线程各自保留，发送成功后由输入区清空。
 * 另记哪些线程的输入区正挂着：异步命令应答落地时据此判断用户是否还在这个线程（重新挂载不算离开）。
 */
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

export interface ComposerDraft {
	text: string;
	/** 上一次提交失败的错误（输入区的错误行）；下一次提交时清掉 */
	error: unknown;
	/** 输入区卸载时的选区与焦点，重新挂载后尽力恢复 */
	selection: { start: number; end: number; focused: boolean } | null;
	/** 已并入草稿的 set_editor_text（按对象身份）：重新挂载时不再并入一遍 */
	editorText: unknown;
}

const EMPTY: ComposerDraft = {
	text: "",
	error: null,
	selection: null,
	editorText: null,
};

const drafts = createStore<Record<string, ComposerDraft>>(() => ({}));

/** 线程 → 正挂着的输入区个数（重新挂载时新旧两份可能在同一次提交里先后进出）。 */
const mounted = new Map<string, number>();

export function getComposerDraft(threadId: string): ComposerDraft {
	return drafts.getState()[threadId] ?? EMPTY;
}

export function patchComposerDraft(
	threadId: string,
	patch: Partial<ComposerDraft>,
): void {
	drafts.setState({ [threadId]: { ...getComposerDraft(threadId), ...patch } });
}

export function useComposerDraft<T>(
	threadId: string,
	selector: (d: ComposerDraft) => T,
): T {
	return useStore(drafts, (s) => selector(s[threadId] ?? EMPTY));
}

/** 输入区挂载时登记，返回注销函数（直接作 effect 的清理函数）。 */
export function claimComposer(threadId: string): () => void {
	mounted.set(threadId, (mounted.get(threadId) ?? 0) + 1);
	return () => {
		const n = (mounted.get(threadId) ?? 1) - 1;
		if (n > 0) mounted.set(threadId, n);
		else mounted.delete(threadId);
	};
}

/** 这个线程的输入区眼下是否挂着（用户仍在这个线程）。 */
export function composerMounted(threadId: string): boolean {
	return mounted.has(threadId);
}

/** 仅测试用：清空所有草稿与挂载登记。 */
export function resetComposerDrafts(): void {
	drafts.setState({}, true);
	mounted.clear();
}
