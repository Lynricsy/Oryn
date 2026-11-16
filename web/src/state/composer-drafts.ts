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
	/**
	 * 正在发送、尚未应答的那份草稿（正文 + 附件 key）。跨断点重新挂载后输入区实例换了，但发送仍在途：
	 * 存在组件外，重新挂载后再按 Enter 不会把同一份草稿再发一遍。按对象身份清除，旧实例的应答也能清掉它
	 */
	inFlight: InFlightDraft | null;
	/**
	 * 引用回复（转录里选中 agent 输出后点「引用回复」）：已规整的引用原文，输入区上方显示成可移除的引用条，
	 * 发送普通消息时拼成 Markdown 引用块放在正文前面（`lib/quote.ts`）。按对象身份区分每次引用，同一段再引用一次也会重新聚焦
	 */
	quote: ComposerQuote | null;
}

export interface ComposerQuote {
	text: string;
}

export interface InFlightDraft {
	text: string;
	keys: readonly string[];
}

/** 这份草稿是否就是在途的那份：正文相同，附件是在途那批的子集（附件列表在输入区内，重新挂载后会清空）。 */
export function isInFlightDraft(
	pending: InFlightDraft | null,
	text: string,
	keys: readonly string[],
): boolean {
	return (
		pending !== null &&
		pending.text === text &&
		keys.every((k) => pending.keys.includes(k))
	);
}

const EMPTY: ComposerDraft = {
	text: "",
	error: null,
	selection: null,
	editorText: null,
	inFlight: null,
	quote: null,
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
