/**
 * zustand 状态：连接、线程索引、orb 表、按线程作用域的视图 store。
 * 线程 store 只经 reducer（`thread-reducer.ts`）变更，便于测试与回放。
 */
import { useStore } from "zustand";
import { createStore, type StoreApi } from "zustand/vanilla";
import type { Orb, Thread, WsEntries, WsThreadSnapshot } from "../api/client";
import type { SessionState } from "../omp/types";
import {
	applyEntries,
	applyFrame,
	applyLiveReset,
	applyPlanResolved,
	applySnapshot,
	applyTranscriptReset,
	applyUiResolved,
	initialThreadState,
	type OmpFrame,
	type ThreadViewState,
} from "./thread-reducer";

// ---------------------------------------------------------------- 连接

export type ConnectionStatus =
	| "idle"
	| "connecting"
	| "open"
	| "reconnecting"
	| "unauthorized";

export interface ConnectionState {
	status: ConnectionStatus;
	attempts: number;
	lastError?: string;
	/** 服务端 `error` 消息（非命令引起的），供全局提示。 */
	errors: { key: number; code: string; message: string; threadId?: string }[];
}

export const connectionStore = createStore<ConnectionState>(() => ({
	status: "idle",
	attempts: 0,
	errors: [],
}));

export function useConnection<T>(selector: (s: ConnectionState) => T): T {
	return useStore(connectionStore, selector);
}

// ---------------------------------------------------------------- 线程索引

export interface IndexState {
	threads: Record<string, Thread>;
	orbs: Record<string, Orb>;
	rev: number;
	loaded: boolean;
	setAll(threads: Thread[], rev: number): void;
	patch(upsert: Thread[], remove: string[], rev: number): void;
	upsertThread(thread: Thread): void;
	upsertOrb(orb: Orb): void;
	removeThread(id: string): void;
}

export const indexStore = createStore<IndexState>((set) => ({
	threads: {},
	orbs: {},
	rev: 0,
	loaded: false,
	setAll: (threads, rev) =>
		set({
			threads: Object.fromEntries(threads.map((t) => [t.id, t])),
			rev,
			loaded: true,
		}),
	patch: (upsert, remove, rev) =>
		set((s) => {
			const threads = { ...s.threads };
			for (const t of upsert) threads[t.id] = t;
			for (const id of remove) delete threads[id];
			return { threads, rev };
		}),
	upsertThread: (thread) =>
		set((s) => ({ threads: { ...s.threads, [thread.id]: thread } })),
	upsertOrb: (orb) => set((s) => ({ orbs: { ...s.orbs, [orb.id]: orb } })),
	removeThread: (id) =>
		set((s) => {
			const threads = { ...s.threads };
			delete threads[id];
			return { threads };
		}),
}));

export function useIndex<T>(selector: (s: IndexState) => T): T {
	return useStore(indexStore, selector);
}

// ---------------------------------------------------------------- 线程视图

/** 线程 store 收到的消息（WS 消息去掉 threadId 后的子集）。 */
export type ThreadMessage =
	| { type: "thread.snapshot"; snapshot: WsThreadSnapshot }
	| {
			type: "entries";
			msg: Pick<WsEntries, "entries" | "leafEntryId">;
	  }
	| { type: "frame"; frame: OmpFrame }
	| { type: "ui.resolved"; requestId: string }
	| { type: "live.reset" }
	| { type: "transcript.reset"; transcriptRev: number }
	| { type: "plan.resolved" }
	| { type: "orb"; orb: Orb }
	| { type: "thread"; thread: Thread }
	| { type: "state"; state: SessionState };

export interface ThreadStore extends ThreadViewState {
	apply(messages: readonly ThreadMessage[]): void;
	patchLocal(patch: Partial<ThreadViewState>): void;
}

/** 纯 reducer：把一条线程消息应用到视图状态。 */
export function reduceThread(
	s: ThreadViewState,
	m: ThreadMessage,
): ThreadViewState {
	switch (m.type) {
		case "thread.snapshot":
			return applySnapshot(s, m.snapshot);
		case "entries":
			return applyEntries(s, m.msg);
		case "frame":
			return applyFrame(s, m.frame);
		case "ui.resolved":
			return applyUiResolved(s, m.requestId);
		case "live.reset":
			return applyLiveReset(s);
		case "transcript.reset":
			return applyTranscriptReset(s, m.transcriptRev);
		case "plan.resolved":
			return applyPlanResolved(s);
		case "orb":
			return { ...s, orb: m.orb };
		case "thread":
			// 叶子也随线程摘要推送（switch_leaf / navigate_tree 由控制面改写叶子后经 index.patch 广播）
			return {
				...s,
				thread: m.thread,
				leafEntryId: m.thread.leafEntryId ?? s.leafEntryId,
			};
		case "state":
			return {
				...s,
				state: m.state,
				running: m.state.isStreaming || (s.running && !m.state.isSettled),
			};
	}
}

const threadStores = new Map<string, StoreApi<ThreadStore>>();

export function getThreadStore(threadId: string): StoreApi<ThreadStore> {
	let store = threadStores.get(threadId);
	if (!store) {
		store = createStore<ThreadStore>((set, get) => ({
			...initialThreadState(threadId),
			apply(messages) {
				let s: ThreadViewState = get();
				for (const m of messages) s = reduceThread(s, m);
				set(s);
			},
			patchLocal(patch) {
				set(patch);
			},
		}));
		threadStores.set(threadId, store);
	}
	return store;
}

export function peekThreadStore(
	threadId: string,
): StoreApi<ThreadStore> | undefined {
	return threadStores.get(threadId);
}

export function useThread<T>(
	threadId: string,
	selector: (s: ThreadStore) => T,
): T {
	return useStore(getThreadStore(threadId), selector);
}

/** 仅测试用：清空所有线程 store。 */
export function resetThreadStores() {
	threadStores.clear();
}
