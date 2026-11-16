/**
 * 在转录里定位某条条目（分享前的密钥扫描命中 →「定位」、命令面板的对话内容命中）。线程页的 Transcript 按线程登记定位函数；
 * 调用方同步拿到是否找到：条目不在活动分支上、被折叠隐藏，或该线程没有挂载转录时为 false。
 * 工具输出（`toolResult` 条目）不是独立的转录行，定位到承载对应工具卡的助手消息，并标出那张卡。
 */
import type { Entry } from "../../api/client";
import type { JsonRecord } from "../../state/message-builder";
import { toolResultIdOf } from "../../state/thread-reducer";
import type { TranscriptItem } from "../../state/transcript";
import { isRecord } from "../../tool-render/util";

export interface RevealTarget {
	entryId: string;
	seq: number;
}

export interface RevealHit {
	/** 转录行下标 */
	index: number;
	/** 命中在工具输出里：对应工具卡的 toolCallId */
	toolCallId?: string;
}

function hasToolCall(message: JsonRecord, id: string): boolean {
	return (
		Array.isArray(message.content) &&
		message.content.some(
			(b) => isRecord(b) && b.type === "toolCall" && b.id === id,
		)
	);
}

/** 目标条目在当前转录行里的位置；找不到为 null。 */
export function findRevealHit(
	items: readonly TranscriptItem[],
	entries: readonly Entry[],
	target: RevealTarget,
): RevealHit | null {
	const entry =
		entries.find((e) => e.entryId === target.entryId) ??
		entries.find((e) => e.seq === target.seq);
	if (!entry) return null;
	const toolCallId = toolResultIdOf(entry);
	if (toolCallId) {
		const index = items.findIndex((it) =>
			it.kind === "tool"
				? it.toolCallId === toolCallId
				: it.kind === "message" && hasToolCall(it.message, toolCallId),
		);
		return index < 0 ? null : { index, toolCallId };
	}
	const index = items.findIndex((it) => it.seq === entry.seq);
	return index < 0 ? null : { index };
}

type Revealer = (target: RevealTarget) => boolean;

const revealers = new Map<string, Revealer>();

/** Transcript 挂载时登记；返回注销函数（只注销自己，不误删后来者）。 */
export function registerRevealer(threadId: string, fn: Revealer): () => void {
	revealers.set(threadId, fn);
	return () => {
		if (revealers.get(threadId) === fn) revealers.delete(threadId);
	};
}

/** 滚动到目标条目并短暂标出；返回是否找到。 */
export function revealEntry(threadId: string, target: RevealTarget): boolean {
	return revealers.get(threadId)?.(target) ?? false;
}

/** revealWhenReady 的重试间隔与默认时限 */
const RETRY_MS = 120;
const RETRY_LIMIT_MS = 10_000;

/** 正在等待的定位（同一时间只等一个：新的请求取代旧的） */
let pending: ReturnType<typeof setTimeout> | undefined;

/**
 * 跳到线程后再定位（命令面板的对话内容命中）：转录挂载、条目经 WS 到齐之前，定位函数还没登记或找不到目标，
 * 每 120ms 重试一次，直到找到或超过时限（条目不在活动分支上时永远找不到，到时限放弃）。
 */
export function revealWhenReady(
	threadId: string,
	target: RevealTarget,
	limitMs = RETRY_LIMIT_MS,
): void {
	clearTimeout(pending);
	const deadline = Date.now() + limitMs;
	const attempt = () => {
		pending = undefined;
		if (revealEntry(threadId, target) || Date.now() >= deadline) return;
		pending = setTimeout(attempt, RETRY_MS);
	};
	pending = setTimeout(attempt, 0);
}
