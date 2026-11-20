/**
 * 选区旁路提问（「解释」等）的运行态：当前的解释卡与它对应的 btw 记录。
 * 复用 Core 的 btw：同一线程同时只答一个。已有信封问题（解释卡发的）在答 → 取消它换新的；
 * 用户在 btw 对话框里手动问的在答 → AI 动作置灰（`sideAskBusy`）。关闭解释卡时仍在生成 → 取消生成。
 * 注意不要从 `components/thread/BtwTopic` 引东西：BtwTopic → registry → selection-groups → 这里已是一条依赖链。
 */
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { Entry } from "../api/client";
import i18n from "../i18n";
import type { Anchor, SelectionContext } from "../lib/context-menu/types";
import { contextPath } from "../lib/context-segments";
import { modalHosts } from "../lib/modal";
import {
	composeSideQuestion,
	parseSideQuestion,
	type SideAction,
} from "../lib/side-ask";
import type { RpcBtwHistoryRecord } from "../omp/types";
import { realtime } from "../realtime/socket";
import { isRecord } from "../tool-render/util";
import { getThreadStore, peekThreadStore } from "./stores";
import { showToast } from "./toasts";
import { activeBranch } from "./transcript";

export interface SideAskCard {
	threadId: string;
	anchor: Anchor;
	/** 非空 = 卡片正在等用户输入问题（「就此提问…」），还没发出 */
	compose: SelectionContext | null;
	/** 已发出的信封问题 */
	question: string | null;
	/** Core 应答后的记录 id；命令在途时为 null */
	recordId: string | null;
	error: unknown;
	/** 触发时已有别的模态对话框开着（例如从 btw 对话框里发起）：解释卡改用 Dialog 外壳叠在它上面 */
	overModal: boolean;
}

export const sideAskStore = createStore<{ card: SideAskCard | null }>(() => ({
	card: null,
}));

/** 每次换卡 / 关卡都加一：在途的应答据此判断自己是否已过时。 */
let seq = 0;
/** 取消后紧接着的 btw 偶尔仍会被拒（Core 还没收尾），等这么久重试一次。 */
const RETRY_MS = 150;

function isUserMessage(e: Entry): boolean {
	const p = isRecord(e.payload) ? e.payload : {};
	return (
		e.type === "message" && isRecord(p.message) && p.message.role === "user"
	);
}

/** 条目在活动分支上离现在几个用户回合、是否还在当前上下文里（早于最近一次压缩的保留段就不在了）。 */
export function locateEntry(
	threadId: string,
	entryId: string | undefined,
): { turnsAgo: number | null; inContext: boolean } {
	const s = peekThreadStore(threadId)?.getState();
	if (!s) return { turnsAgo: null, inContext: true };
	// 在途消息还没有条目
	if (entryId === undefined) return { turnsAgo: 0, inContext: true };
	const path = activeBranch(s.entries, s.leafEntryId).flat();
	const i = path.findIndex((e) => e.entryId === entryId);
	if (i < 0) return { turnsAgo: null, inContext: true };
	const turnsAgo = path.slice(i + 1).filter(isUserMessage).length;
	const ctx = contextPath(s.entries, s.leafEntryId);
	return {
		turnsAgo,
		inContext: ctx === null || ctx.some((e) => e.entryId === entryId),
	};
}

function latest(r: RpcBtwHistoryRecord) {
	return r.followUps?.at(-1) ?? r;
}

function runningRecord(
	btw: Record<string, RpcBtwHistoryRecord>,
): RpcBtwHistoryRecord | undefined {
	return Object.values(btw).find((r) => latest(r).status === "running");
}

function busyIn(btw: Record<string, RpcBtwHistoryRecord>): boolean {
	const r = runningRecord(btw);
	return r !== undefined && parseSideQuestion(r.question) === null;
}

/** 用户手动问的 btw 在答：AI 动作置灰。 */
export function sideAskBusy(threadId: string): boolean {
	const s = peekThreadStore(threadId)?.getState();
	return s ? busyIn(s.btw) : false;
}

const EMPTY = createStore(() => ({
	btw: {} as Record<string, RpcBtwHistoryRecord>,
}));

/** 同一判断的响应式版本（选区胶囊用）；threadId 为 null 时恒为 false */
export function useSideAskBusy(threadId: string | null): boolean {
	return useStore(threadId ? getThreadStore(threadId) : EMPTY, (s) =>
		busyIn(s.btw),
	);
}

/** 模态栈里有不是解释卡自己的对话框。 */
function overForeignModal(): boolean {
	return modalHosts().some((h) => h.querySelector("[data-side-ask]") === null);
}

function patchCard(mine: number, patch: Partial<SideAskCard>) {
	const card = sideAskStore.getState().card;
	if (seq === mine && card)
		sideAskStore.setState({ card: { ...card, ...patch } });
}

function cancelQuietly(threadId: string, recordId: string) {
	void realtime
		.cmd(threadId, { type: "btw_cancel", recordId })
		.catch(() => undefined);
}

export async function runSideAsk(
	action: SideAction,
	sel: SelectionContext,
): Promise<void> {
	const threadId = sel.threadId;
	if (!threadId) return;
	const overModal = overForeignModal();
	if (sideAskStore.getState().card) closeSideAsk();
	// 先领令牌再等取消应答：等待期间卡片被关（例如切走线程）就不再开卡、不再发问
	seq += 1;
	const mine = seq;
	const running = runningRecord(
		peekThreadStore(threadId)?.getState().btw ?? {},
	);
	if (running) {
		if (parseSideQuestion(running.question) === null) {
			showToast({ text: i18n.t("sideAsk.busy"), tone: "err" });
			return;
		}
		await realtime
			.cmd(threadId, { type: "btw_cancel", recordId: running.id })
			.catch(() => undefined);
		if (seq !== mine) return;
	}
	const question = composeSideQuestion(action, sel, i18n.language);
	sideAskStore.setState({
		card: {
			threadId,
			anchor: sel.anchor,
			compose: null,
			question,
			recordId: null,
			error: null,
			overModal,
		},
	});
	const send = () => realtime.cmd(threadId, { type: "btw", question });
	try {
		let res: Awaited<ReturnType<typeof send>>;
		try {
			res = await send();
		} catch (e) {
			if (!(e instanceof Error && e.message.includes("cancel it first")))
				throw e;
			await new Promise((r) => setTimeout(r, RETRY_MS));
			if (seq !== mine) return;
			res = await send();
		}
		if (seq === mine) patchCard(mine, { recordId: res.record.id });
		// 卡片已被关掉或换掉：这条回答没人看了
		else cancelQuietly(threadId, res.record.id);
	} catch (e) {
		patchCard(mine, { error: e });
	}
}

/** 「就此提问…」：先开一张等输入的卡片，提交后再走 `runSideAsk`。 */
export function openSideAskCompose(sel: SelectionContext): void {
	if (!sel.threadId) return;
	const overModal = overForeignModal();
	closeSideAsk();
	sideAskStore.setState({
		card: {
			threadId: sel.threadId,
			anchor: sel.anchor,
			compose: sel,
			question: null,
			recordId: null,
			error: null,
			overModal,
		},
	});
}

/** 在解释卡里追问（同一条 btw 记录追加一轮）。 */
export async function followUpSideAsk(text: string): Promise<void> {
	const card = sideAskStore.getState().card;
	if (!card?.recordId || !text.trim()) return;
	const mine = seq;
	try {
		await realtime.cmd(card.threadId, {
			type: "btw",
			question: text.trim(),
			recordId: card.recordId,
		});
	} catch (e) {
		patchCard(mine, { error: e });
	}
}

/** 关闭解释卡；仍在生成时取消（`keepRunning` 时留着，例如转去 btw 对话框继续看）。 */
export function closeSideAsk(opts?: { keepRunning?: boolean }): void {
	seq += 1;
	const card = sideAskStore.getState().card;
	if (!card) return;
	if (!opts?.keepRunning && card.recordId) {
		const r = peekThreadStore(card.threadId)?.getState().btw[card.recordId];
		if (r && latest(r).status === "running")
			cancelQuietly(card.threadId, card.recordId);
	}
	sideAskStore.setState({ card: null });
}
