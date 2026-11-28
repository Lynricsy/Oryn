/**
 * 写作块改稿的自动保存（C5）：编辑器每次变更调用 `scheduleDraftSave`，同一块 800 ms 防抖后 `PUT`；
 * 发消息前 `flushDraftSaves` 立刻发出并等完该线程所有待发保存，保证 orynd 拼改稿块时拿到的是最新全文。
 * - 同一块的请求按顺序串行发出（前一个在途时后一个排在它后面），避免乱序覆盖；
 * - 成功后直接改 `threadDraftsKey` 缓存；失败给出带「重试」的提示，不阻断发送。
 */
import type { QueryClient } from "@tanstack/react-query";
import { ApiError, api, type Schemas, unwrap } from "../api/client";
import {
	draftMapKey,
	type ThreadDraft,
	threadDraftsKey,
} from "../api/draft-queries";
import i18n from "../i18n";
import type { DraftKind } from "../lib/draft-blocks";
import { showToast } from "./toasts";

export interface DraftSavePayload {
	kind: DraftKind;
	title: string;
	text: string;
}

interface PendingSave {
	timer: ReturnType<typeof setTimeout>;
	payload: DraftSavePayload;
	run: () => void;
}

const SAVE_DEBOUNCE_MS = 800;

/** threadId → 块键（`entryId/block`）→ 尚未发出的保存。 */
const pending = new Map<string, Map<string, PendingSave>>();
/** threadId → 在途的请求。 */
const inflight = new Map<string, Set<Promise<void>>>();
/** `threadId\nkey` → 该块请求链的队尾（串行发出）。 */
const chains = new Map<string, Promise<void>>();
/** `threadId\nkey` → 编辑代数：失败后的「重试」只在期间没有更新的编辑时才重发。 */
const generations = new Map<string, number>();

function chainKey(threadId: string, key: string): string {
	return `${threadId}\n${key}`;
}

function bumpGeneration(ck: string): number {
	const g = (generations.get(ck) ?? 0) + 1;
	generations.set(ck, g);
	return g;
}

/** 把一个不会 reject 的请求排到该块请求链末尾，并记入线程的在途集合。 */
function enqueue(
	threadId: string,
	key: string,
	job: () => Promise<void>,
): Promise<void> {
	const ck = chainKey(threadId, key);
	const p = (chains.get(ck) ?? Promise.resolve()).then(job);
	chains.set(ck, p);
	let set = inflight.get(threadId);
	if (!set) {
		set = new Set();
		inflight.set(threadId, set);
	}
	set.add(p);
	void p.finally(() => {
		const s = inflight.get(threadId);
		s?.delete(p);
		if (s?.size === 0) inflight.delete(threadId);
		if (chains.get(ck) === p) chains.delete(ck);
	});
	return p;
}

function takePending(threadId: string, key: string): PendingSave | undefined {
	const byKey = pending.get(threadId);
	const p = byKey?.get(key);
	if (!p || !byKey) return undefined;
	clearTimeout(p.timer);
	byKey.delete(key);
	if (byKey.size === 0) pending.delete(threadId);
	return p;
}

function upsertCache(qc: QueryClient, threadId: string, saved: ThreadDraft) {
	qc.setQueryData<Schemas["ThreadDraftList"]>(
		threadDraftsKey(threadId),
		(old) => {
			if (!old) return old;
			const k = draftMapKey(saved.entryId, saved.block);
			const i = old.drafts.findIndex(
				(d) => draftMapKey(d.entryId, d.block) === k,
			);
			if (i < 0) return { drafts: [...old.drafts, saved] };
			const drafts = old.drafts.slice();
			drafts[i] = saved;
			return { drafts };
		},
	);
}

function removeFromCache(
	qc: QueryClient,
	threadId: string,
	entryId: string,
	block: string,
) {
	const k = draftMapKey(entryId, block);
	qc.setQueryData<Schemas["ThreadDraftList"]>(
		threadDraftsKey(threadId),
		(old) =>
			old && {
				drafts: old.drafts.filter((d) => draftMapKey(d.entryId, d.block) !== k),
			},
	);
}

function failToast(retry: () => void) {
	showToast({
		text: i18n.t("writingBlock.saveFailed"),
		tone: "err",
		action: { label: i18n.t("writingBlock.retry"), run: retry },
	});
}

/** 立即发出一次 `PUT`（排在该块请求链末尾）。 */
function sendPut(
	qc: QueryClient,
	threadId: string,
	entryId: string,
	block: string,
	payload: DraftSavePayload,
): Promise<void> {
	const key = draftMapKey(entryId, block);
	const ck = chainKey(threadId, key);
	const gen = generations.get(ck) ?? 0;
	return enqueue(threadId, key, async () => {
		try {
			const saved = await unwrap(
				api.PUT("/api/v1/threads/{id}/drafts/{entryId}/{block}", {
					params: { path: { id: threadId, entryId, block } },
					body: payload,
				}),
			);
			upsertCache(qc, threadId, saved);
		} catch {
			failToast(() => {
				if ((generations.get(ck) ?? 0) === gen)
					void sendPut(qc, threadId, entryId, block, payload);
			});
		}
	});
}

/** 编辑器内容变了：同一块 800 ms 防抖后保存。 */
export function scheduleDraftSave(
	qc: QueryClient,
	threadId: string,
	entryId: string,
	block: string,
	payload: DraftSavePayload,
): void {
	const key = draftMapKey(entryId, block);
	takePending(threadId, key);
	bumpGeneration(chainKey(threadId, key));
	const run = () => {
		if (pending.get(threadId)?.get(key) !== entry) return;
		takePending(threadId, key);
		void sendPut(qc, threadId, entryId, block, payload);
	};
	const entry: PendingSave = {
		timer: setTimeout(run, SAVE_DEBOUNCE_MS),
		payload,
		run,
	};
	let byKey = pending.get(threadId);
	if (!byKey) {
		byKey = new Map();
		pending.set(threadId, byKey);
	}
	byKey.set(key, entry);
}

/** 立刻发出该线程所有待发保存，并等它们（连同已在途的）结束；失败已有提示，这里不抛出。 */
export async function flushDraftSaves(threadId: string): Promise<void> {
	for (const p of [...(pending.get(threadId)?.values() ?? [])]) p.run();
	const set = inflight.get(threadId);
	if (set) await Promise.allSettled([...set]);
}

/**
 * 该线程是否有待发或在途的保存。发送路径据此决定要不要 `await flushDraftSaves`：
 * 没有改稿时不多等一拍，发送保持同步。
 */
export function hasUnsavedDrafts(threadId: string): boolean {
	return pending.has(threadId) || inflight.has(threadId);
}

/** 该块是否还有没落库的保存（待发或在途）：此时不能用服务端内容覆盖编辑器。 */
export function hasPendingDraftSave(
	threadId: string,
	entryId: string,
	block: string,
): boolean {
	const key = draftMapKey(entryId, block);
	return (
		!!pending.get(threadId)?.has(key) || chains.has(chainKey(threadId, key))
	);
}

/**
 * 恢复原稿：先取消该块的待发保存。改稿还没交给过 agent 时直接删除；已交给过时把原文作为新的改稿保存，
 * 让 agent 下回合知道主人已恢复原稿。
 */
export function resetDraft(
	qc: QueryClient,
	threadId: string,
	entryId: string,
	block: string,
	original: DraftSavePayload,
	delivered: boolean,
): Promise<void> {
	const key = draftMapKey(entryId, block);
	takePending(threadId, key);
	bumpGeneration(chainKey(threadId, key));
	if (delivered) return sendPut(qc, threadId, entryId, block, original);
	return enqueue(threadId, key, async () => {
		try {
			await unwrap(
				api.DELETE("/api/v1/threads/{id}/drafts/{entryId}/{block}", {
					params: { path: { id: threadId, entryId, block } },
				}),
			);
		} catch (e) {
			// 服务端本来就没有这份改稿（还没保存过）：视为已恢复
			if (!(e instanceof ApiError && e.code === "draft_not_found")) {
				failToast(() => {
					void resetDraft(qc, threadId, entryId, block, original, delivered);
				});
				return;
			}
		}
		removeFromCache(qc, threadId, entryId, block);
	});
}
