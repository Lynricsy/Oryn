/**
 * 上下文分段：omp `get_context_report` 的 `breakdown` 给出 5 项非消息部分的精确 token 数和消息总数；
 * 消息总数再按当前生效上下文（活动分支、最后一次压缩 / `/clear` 之后）里可见文本的字符比例粗略估算成 5 类。
 * 生效范围里只要有无法按字符计量的内容（图片、加密或 redacted 思考、未知块、压缩的原生载荷或图像归档），就不拆分，整段显示「消息」。
 */
import type { Entry } from "../api/client";
import type { ResponseData } from "../omp/types";
import { activeBranch } from "../state/transcript";
import { isRecord } from "../tool-render/util";
import { formatCount } from "./format";

export type ContextBreakdown = NonNullable<
	ResponseData<"get_context_report">["breakdown"]
>;

/** 消息拆分的 5 类，顺序即量表上的顺序（omp `CONTEXT_CATEGORIES` 的消息部分）。 */
export const MESSAGE_KINDS = [
	"userMessages",
	"assistantMessages",
	"thinking",
	"toolCalls",
	"toolOutput",
] as const;
export type MessageKind = (typeof MESSAGE_KINDS)[number];
export type MessageWeights = Record<MessageKind, number>;

/** 分段 id 同时是 i18n key（`report.ctx.<id>`）。`messages` 只在拆不出 5 类时整段出现。 */
export type ContextPartId =
	| "systemPrompt"
	| "systemContext"
	| "instructions"
	| "skills"
	| "systemTools"
	| MessageKind
	| "messages";

export interface ContextPart {
	id: ContextPartId;
	tokens: number;
	/** 按可见文本字符比例粗略分摊的估算值，不是 token 计数 */
	estimated: boolean;
}

function payloadOf(e: Entry): Record<string, unknown> {
	return isRecord(e.payload) ? e.payload : {};
}

/**
 * 当前生效的上下文条目（规则照 omp `buildSessionContext`）：当前主会话文件的活动分支；
 * 最后一个 `/clear` 边界晚于最后一次压缩时从边界之后算起；否则有压缩时依次为压缩摘要、
 * 保留段（`firstKeptEntryId` 到压缩之间）、压缩之后。保留段起点不在分支上时认不出边界，返回 null。
 */
export function contextPath(
	entries: readonly Entry[],
	leafEntryId: string | undefined,
): Entry[] | null {
	const files = activeBranch(entries, leafEntryId);
	const path = files[files.length - 1];
	if (!path || path.length === 0) return null;
	let reset = -1;
	let compaction = -1;
	path.forEach((e, i) => {
		if (e.type === "reset_boundary") reset = i;
		else if (e.type === "compaction") compaction = i;
	});
	if (reset > compaction) return path.slice(reset + 1);
	const summary = path[compaction];
	if (!summary) return path;
	const firstKept = payloadOf(summary).firstKeptEntryId;
	let start = compaction;
	if (typeof firstKept === "string" && firstKept !== "") {
		start = path.findIndex((e) => e.entryId === firstKept);
		if (start < 0 || start > compaction) return null;
	}
	return [
		summary,
		...path.slice(start, compaction),
		...path.slice(compaction + 1),
	];
}

function str(v: unknown): number {
	return typeof v === "string" ? v.length : 0;
}

/** 消息内容的字符量：字符串或纯文本块；含任何非文本块（图片等）时无法计量，返回 null。 */
function contentChars(content: unknown): number | null {
	if (typeof content === "string") return content.length;
	if (content === undefined || content === null) return 0;
	if (!Array.isArray(content)) return null;
	let n = 0;
	for (const b of content) {
		if (!isRecord(b) || b.type !== "text") return null;
		n += str(b.text);
	}
	return n;
}

/** assistant 内容按块归类；遇到 redacted 思考或未知块时无法计量，返回 null。 */
function assistantChars(
	content: unknown,
): Pick<MessageWeights, "assistantMessages" | "thinking" | "toolCalls"> | null {
	if (!Array.isArray(content)) return null;
	const w = { assistantMessages: 0, thinking: 0, toolCalls: 0 };
	for (const b of content) {
		if (!isRecord(b)) return null;
		if (b.type === "text") w.assistantMessages += str(b.text);
		else if (b.type === "thinking") w.thinking += str(b.thinking);
		else if (b.type === "toolCall")
			w.toolCalls +=
				str(b.name) + (JSON.stringify(b.arguments ?? {})?.length ?? 0);
		else return null;
	}
	return w;
}

/** omp 中断续接消息的 customType（`INTERRUPTED_THINKING_MESSAGE_TYPE`）。 */
const INTERRUPTED_THINKING = "interrupted-thinking";

/**
 * 压缩条目 `preserveData` 里会替代或附加到摘要上一并进入模型上下文的载荷：OpenAI 远端压缩的 replacement history、
 * Anthropic 原生压缩块、snapcompact 图像归档。它们的 token 量无法按摘要文本计量。
 */
const OPAQUE_COMPACTION_KEYS = [
	"openaiRemoteCompaction",
	"anthropicCompaction",
	"snapcompact",
] as const;

function messageOf(e: Entry | undefined): Record<string, unknown> | null {
	if (e?.type !== "message") return null;
	const m = payloadOf(e).message;
	return isRecord(m) ? m : null;
}

/** 中断续接消息（隐藏的 custom 消息，紧跟在被中断的助手回合之后）。 */
function isInterruptedThinking(e: Entry | undefined): boolean {
	if (e?.type === "custom_message")
		return payloadOf(e).customType === INTERRUPTED_THINKING;
	const m = messageOf(e);
	return m?.role === "custom" && m.customType === INTERRUPTED_THINKING;
}

/**
 * 去掉末尾一段未签名的思考（照 omp `demoteInterruptedThinking`）：先跳过末尾的空白文本块，
 * 再去掉紧挨着的、非空且没有 `thinkingSignature` 的思考块。续接消息已把这段思考转述进上下文。
 */
function stripInterruptedThinking(content: unknown[]): unknown[] {
	let end = content.length;
	while (end > 0) {
		const b = content[end - 1];
		if (!isRecord(b) || b.type !== "text" || String(b.text ?? "").trim()) break;
		end--;
	}
	let start = end;
	while (start > 0) {
		const b = content[start - 1];
		if (
			!isRecord(b) ||
			b.type !== "thinking" ||
			!String(b.thinking ?? "").trim() ||
			b.thinkingSignature
		)
			break;
		start--;
	}
	return start === end ? content : content.slice(0, start);
}

/**
 * 按 omp `buildSessionContext` 的规则得出真正进入模型上下文的消息条目：去掉 retryRecovery 回合；
 * 中断（aborted）或出错（error）的助手回合连同其配对的工具结果一并移除，
 * 例外是紧跟中断续接消息的回合（保留，见 {@link stripInterruptedThinking}）。
 */
function contextMessages(path: readonly Entry[]): Entry[] {
	const msgs = path.filter((e) => {
		if (e.type === "compaction" || e.type === "custom_message") return true;
		if (e.type === "branch_summary") return !!payloadOf(e).summary;
		const m = messageOf(e);
		return m !== null && !(m.role === "assistant" && m.retryRecovery);
	});
	// 倒序逐条移除，与 omp 一致：「下一条」取的是移除后的相邻消息
	for (let i = msgs.length - 1; i >= 0; i--) {
		const m = messageOf(msgs[i]);
		if (m?.role !== "assistant") continue;
		if (m.stopReason !== "aborted" && m.stopReason !== "error") continue;
		if (isInterruptedThinking(msgs[i + 1])) continue;
		const dropped = new Set<unknown>();
		if (Array.isArray(m.content))
			for (const b of m.content)
				if (isRecord(b) && b.type === "toolCall") dropped.add(b.id);
		msgs.splice(i, 1);
		if (dropped.size === 0) continue;
		for (let j = msgs.length - 1; j >= i; j--) {
			const r = messageOf(msgs[j]);
			if (r?.role === "toolResult" && dropped.has(r.toolCallId))
				msgs.splice(j, 1);
		}
	}
	return msgs;
}

/**
 * 各类可见文本的字符量。bash / python 执行算工具输出，其余非 assistant 消息算用户消息。
 * 只计真正进入模型上下文的消息（见 {@link contextMessages}）。
 * 生效范围里出现无法按字符计量的内容时返回 null（调用方不拆分）：图片（用户消息、工具结果、执行输出、附件）、
 * redacted 思考、带 `providerPayload` 的 assistant（加密推理载荷）、带原生载荷或图像归档的压缩，以及其他非文本块。
 */
export function messageWeights(path: readonly Entry[]): MessageWeights | null {
	const w: MessageWeights = {
		userMessages: 0,
		assistantMessages: 0,
		thinking: 0,
		toolCalls: 0,
		toolOutput: 0,
	};
	const msgs = contextMessages(path);
	for (const [i, e] of msgs.entries()) {
		const p = payloadOf(e);
		if (e.type === "compaction" || e.type === "branch_summary") {
			const keep = p.preserveData;
			if (
				e.type === "compaction" &&
				isRecord(keep) &&
				OPAQUE_COMPACTION_KEYS.some((k) => keep[k] != null)
			)
				return null;
			w.userMessages += str(p.summary);
			continue;
		}
		if (e.type === "custom_message") {
			const n = contentChars(p.content);
			if (n === null) return null;
			w.userMessages += n;
			continue;
		}
		const m = messageOf(e);
		if (!m) continue;
		switch (m.role) {
			case "assistant": {
				if (m.providerPayload != null) return null;
				const a = assistantChars(
					Array.isArray(m.content) && isInterruptedThinking(msgs[i + 1])
						? stripInterruptedThinking(m.content)
						: m.content,
				);
				if (!a) return null;
				w.assistantMessages += a.assistantMessages;
				w.thinking += a.thinking;
				w.toolCalls += a.toolCalls;
				break;
			}
			case "toolResult": {
				const n = contentChars(m.content);
				if (n === null) return null;
				w.toolOutput += n;
				break;
			}
			case "bashExecution":
			case "pythonExecution":
				if (m.excludeFromContext) break;
				if (Array.isArray(m.images) && m.images.length > 0) return null;
				w.toolOutput += str(m.command) + str(m.code) + str(m.output);
				break;
			default: {
				if (Array.isArray(m.attachments) && m.attachments.length > 0)
					return null;
				const n = contentChars(m.content);
				if (n === null) return null;
				w.userMessages += n + str(m.summary);
			}
		}
	}
	return w;
}

/** 按权重把 `total` 分给 5 类（最大余数法取整，总和恰为 `total`）；无法分摊时返回 null。 */
export function splitMessages(
	total: number,
	weights: MessageWeights,
): MessageWeights | null {
	const sum = MESSAGE_KINDS.reduce((acc, k) => acc + weights[k], 0);
	if (!(total > 0) || !(sum > 0)) return null;
	const whole = Math.round(total);
	const raw = MESSAGE_KINDS.map((k) => (whole * weights[k]) / sum);
	const out = raw.map(Math.floor);
	let rest = whole - out.reduce((a, b) => a + b, 0);
	const order = raw
		.map((r, i) => [r - Math.floor(r), i] as const)
		.sort((a, b) => b[0] - a[0]);
	for (const [, i] of order) {
		if (rest <= 0) break;
		out[i] = (out[i] ?? 0) + 1;
		rest--;
	}
	return Object.fromEntries(
		MESSAGE_KINDS.map((k, i) => [k, out[i] ?? 0]),
	) as MessageWeights;
}

/**
 * 量表分段（固定顺序：系统类 → 技能 → 工具定义 → 消息类）。`entries` 为 null（没有转录）、认不出上下文边界、
 * 生效范围里有无法按字符计量的内容，或没有可计字符时，消息部分退回一整段「消息」。
 */
export function contextParts(
	b: ContextBreakdown,
	entries: readonly Entry[] | null,
	leafEntryId?: string,
): ContextPart[] {
	const exact = (id: ContextPartId, tokens: number): ContextPart => ({
		id,
		tokens,
		estimated: false,
	});
	const parts = [
		exact("systemPrompt", b.systemPromptTokens),
		exact("systemContext", b.systemContextTokens),
		exact("instructions", b.instructionFilesTokens),
		exact("skills", b.skillsTokens),
		exact("systemTools", b.systemToolsTokens),
	];
	const path = entries ? contextPath(entries, leafEntryId) : null;
	const weights = path ? messageWeights(path) : null;
	const split = weights ? splitMessages(b.messagesTokens, weights) : null;
	if (!split) return [...parts, exact("messages", b.messagesTokens)];
	return [
		...parts,
		...MESSAGE_KINDS.map((id) => ({ id, tokens: split[id], estimated: true })),
	];
}

/** 一段占上下文窗口的百分比文字：<1% 保留一位小数。 */
export function formatShare(tokens: number, contextWindow: number): string {
	const pct = contextWindow > 0 ? (tokens / contextWindow) * 100 : 0;
	const tenth = Math.round(pct * 10) / 10;
	return `${tenth < 1 ? tenth.toFixed(1) : Math.round(pct)}%`;
}

/** 分段悬停提示：「名称 · tokens · 百分比」，估算项末尾再加 `note`。 */
export function partTitle(
	name: string,
	tokens: number,
	contextWindow: number,
	note?: string,
): string {
	const items = [name, formatCount(tokens), formatShare(tokens, contextWindow)];
	if (note) items.push(note);
	return items.join(" · ");
}
