/**
 * 输入区草稿：按线程存在组件外（只在内存，不落盘）。输入坞跨 compact 断点、切线程都会重新挂载 Composer，
 * 文字、错误行、光标位置不能跟着组件 state 一起丢；各线程各自保留，发送成功后由输入区清空。
 * 另记哪些线程的输入区正挂着：异步命令应答落地时据此判断用户是否还在这个线程（重新挂载不算离开）。
 */

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { ArtifactSelectionRef } from "../lib/artifacts/prompt";
import type { Attachment } from "../lib/attachments";
import type { ImageContent } from "../omp/types";
import { uiStore } from "./ui";

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
	 * 存在组件外，重新挂载后再按 Enter 不会把同一份草稿再发一遍。按对象身份清除，旧实例的应答也能清掉它。
	 * agent 未就绪时发出的消息转成转录里的「发送中」气泡、草稿当即清空，在途标记随之清掉（草稿已不在输入区）
	 */
	inFlight: InFlightDraft | null;
	/**
	 * 引用回复（转录里选中 agent 输出后点「引用回复」）：已规整的引用原文，输入区上方显示成可移除的引用条，
	 * 发送普通消息时拼成 Markdown 引用块放在正文前面（`lib/quote.ts`）。按对象身份区分每次引用，同一段再引用一次也会重新聚焦
	 */
	quote: ComposerQuote | null;
	/**
	 * 右面板查看器在选择模式下选中的 artifact 元素（M14 3.5）：输入区上方显示成可移除的引用条，发送普通消息时用
	 * `selectionPrompt` 把它接在正文后面。清除选中、切走 artifact、关掉面板时由面板置空；按对象身份区分每次选中
	 */
	artifactSelection: ArtifactSelectionRef | null;
	/**
	 * 待并入正文的一段文字（右键菜单的「引用到输入框」）：输入区挂着时接到正文末尾（与原文空一行）后置空。
	 * 按对象身份区分每次插入
	 */
	insert: ComposerInsert | null;
}

export interface ComposerQuote {
	text: string;
}

export interface ComposerInsert {
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
	artifactSelection: null,
	insert: null,
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

/** 仅测试用：清空所有草稿、待加入的附件、待处理的附件恢复与挂载登记。 */
export function resetComposerDrafts(): void {
	drafts.setState({}, true);
	pendingFiles.setState({}, true);
	pendingRestores.setState({}, true);
	mounted.clear();
}

/** 把一段文字放进线程输入区（接在草稿末尾），并切到对话标签让输入区挂上。 */
export function insertIntoComposer(threadId: string, text: string): void {
	patchComposerDraft(threadId, { insert: { text } });
	uiStore.getState().setThreadTab("agent");
}

/** 待加入输入区附件列表的文件（附件列表在输入区组件里，挂上后由它取走）。 */
const pendingFiles = createStore<Record<string, File[]>>(() => ({}));
const NO_FILES: File[] = [];

export function enqueueComposerFiles(threadId: string, files: File[]): void {
	const prev = pendingFiles.getState()[threadId] ?? [];
	pendingFiles.setState({ [threadId]: [...prev, ...files] });
}

export function useComposerPendingFiles(threadId: string): File[] {
	return useStore(pendingFiles, (s) => s[threadId] ?? NO_FILES);
}

/** 取出并清空这个线程待加入的文件。 */
export function takeComposerFiles(threadId: string): File[] {
	const files = pendingFiles.getState()[threadId] ?? [];
	if (files.length > 0) pendingFiles.setState({ [threadId]: [] });
	return files;
}

/**
 * 回退（rewind）带回的一项附件：旧消息上已绑定到本线程的附件（`attachment`，直接复用 id，不重传），
 * 和 / 或它的内联图片副本（`image`，取自 Core 的 `editorImages`）。
 */
export interface RestoredAttachment {
	name: string;
	size: number;
	attachment?: Attachment;
	image?: ImageContent;
}

/**
 * 输入区附件列表的恢复操作：`add` 加入一次回退带回的附件（以 `tag` 标记这一批）；`drop` 撤销那次回退时去掉这一批。
 * 附件列表在输入区组件里，挂上后按顺序取走。
 */
export type ComposerRestoreOp =
	| { kind: "add"; tag: string; items: RestoredAttachment[] }
	| { kind: "drop"; tag: string };

const pendingRestores = createStore<Record<string, ComposerRestoreOp[]>>(
	() => ({}),
);
const NO_RESTORES: ComposerRestoreOp[] = [];

export function enqueueComposerRestore(
	threadId: string,
	op: ComposerRestoreOp,
): void {
	const prev = pendingRestores.getState()[threadId] ?? [];
	pendingRestores.setState({ [threadId]: [...prev, op] });
}

export function useComposerPendingRestores(
	threadId: string,
): ComposerRestoreOp[] {
	return useStore(pendingRestores, (s) => s[threadId] ?? NO_RESTORES);
}

/** 取出并清空这个线程待处理的附件恢复操作。 */
export function takeComposerRestores(threadId: string): ComposerRestoreOp[] {
	const ops = pendingRestores.getState()[threadId] ?? [];
	if (ops.length > 0) pendingRestores.setState({ [threadId]: [] });
	return ops;
}
