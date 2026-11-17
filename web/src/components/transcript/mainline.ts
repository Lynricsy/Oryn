/**
 * 主线对话（M19）的转录折叠：主线里大多是助手在幕后处理来料，读的人只关心说出口的话。
 * - 自动消息（`parseOrynMessage` 认得的 user 消息）连同其后、到该回合助手最后一条有正文的消息之前的一切
 *   （中间的助手消息、工具卡、提示行）收成一行「幕后」；最后那条有正文的助手消息照常显示。
 * - 由自动消息开头、且其中调用过 `stay_silent` 的回合整个收成一行「没有打扰你」，带上 `reason`。
 *   主人亲自发起的回合里 `stay_silent` 无效，不折叠。
 * 回合以 user 消息或会话分隔为界（与 `layoutTurns` 相同）。纯函数，只改显示，不动条目。
 */
import { parseOrynMessage } from "../../lib/oryn-message";
import type { JsonRecord } from "../../state/message-builder";
import type { TranscriptItem } from "../../state/transcript";
import { isRecord } from "../../tool-render/util";
import { textOfContent } from "./MessageView";

/** 主线专属的沉默工具名（`internal/coord/main.go` 的 `ToolStaySilent`）。 */
export const STAY_SILENT_TOOL = "stay_silent";

/** 折叠成一行的一段转录。 */
export interface MainlineFold {
	kind: "fold";
	key: string;
	seq: number;
	/** `auto`：自动消息与幕后过程；`silent`：助手选择沉默的整个回合 */
	fold: "auto" | "silent";
	/** 被收起的原始行，展开时按原样渲染 */
	items: TranscriptItem[];
	/** 开头那条自动消息的正文（行内文案据此描述来料） */
	source: string;
	/** `silent`：`stay_silent` 调用里的理由（流式中还拿不到参数时为空串） */
	reason?: string;
}

export type MainlineRow = TranscriptItem | MainlineFold;

/** 自动消息的正文；不是自动消息（含别的 agent 经 irc 发来的消息）时为 null。 */
function autoText(it: TranscriptItem): string | null {
	if (it.kind !== "message" || it.message.role !== "user") return null;
	if (isRecord(it.message.parentIrc)) return null;
	const text = textOfContent(it.message.content);
	return parseOrynMessage(text) ? text : null;
}

/** 助手消息里第一个 `stay_silent` 调用的理由；没有调用时为 undefined。 */
function silentReason(message: JsonRecord): string | undefined {
	if (!Array.isArray(message.content)) return undefined;
	for (const b of message.content) {
		if (!isRecord(b) || b.type !== "toolCall" || b.name !== STAY_SILENT_TOOL)
			continue;
		const args = isRecord(b.arguments) ? b.arguments : {};
		return typeof args.reason === "string" ? args.reason : "";
	}
	return undefined;
}

function segmentSilence(seg: readonly TranscriptItem[]): string | undefined {
	for (const it of seg) {
		if (it.kind === "tool" && it.toolName === STAY_SILENT_TOOL) return "";
		if (it.kind === "message" && it.message.role === "assistant") {
			const reason = silentReason(it.message);
			if (reason !== undefined) return reason;
		}
	}
	return undefined;
}

/** 助手消息有没有正文（非空 text 块）。 */
export function hasAssistantText(it: MainlineRow): boolean {
	return (
		it.kind === "message" &&
		it.message.role === "assistant" &&
		textOfContent(it.message.content).trim() !== ""
	);
}

export function foldMainline(items: readonly TranscriptItem[]): MainlineRow[] {
	const out: MainlineRow[] = [];
	let i = 0;
	while (i < items.length) {
		const head = items[i] as TranscriptItem;
		const source = autoText(head);
		if (source === null) {
			out.push(head);
			i++;
			continue;
		}
		let end = i + 1;
		while (end < items.length) {
			const it = items[end] as TranscriptItem;
			if (
				(it.kind === "message" && it.message.role === "user") ||
				it.kind === "session_divider"
			)
				break;
			end++;
		}
		const seg = items.slice(i, end);
		i = end;
		const fold = { key: `fold:${head.key}`, seq: head.seq, source };
		const reason = segmentSilence(seg);
		if (reason !== undefined) {
			out.push({ kind: "fold", fold: "silent", items: seg, reason, ...fold });
			continue;
		}
		let cut = seg.length;
		for (let k = seg.length - 1; k > 0; k--)
			if (hasAssistantText(seg[k] as TranscriptItem)) {
				cut = k;
				break;
			}
		out.push({ kind: "fold", fold: "auto", items: seg.slice(0, cut), ...fold });
		out.push(...seg.slice(cut));
	}
	return out;
}
