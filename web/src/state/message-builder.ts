/**
 * 流式消息重建：把 `message_start` / `message_update`（`messageUpdates:"delta"` 增量投影或完整形态）/
 * `message_end` 还原成一条完整的 AgentMessage，供叠加层渲染。纯函数，便于测试。
 */

import { isRecord } from "../tool-render/util";

export type JsonRecord = Record<string, unknown>;

/** 叠加层里的在途消息。 */
export interface LiveMessage {
	messageId?: string;
	role: string;
	timestamp?: number;
	/** 当前重建出的消息对象（结构与 AgentMessage 相同，但字段可能残缺）。 */
	message: JsonRecord;
	/** 每个 toolCall 块累计的参数 JSON 文本（按 contentIndex）。 */
	partialJson: Record<number, string>;
	ended: boolean;
}

function cloneMessage(m: unknown): JsonRecord {
	if (!isRecord(m)) return { content: [] };
	const content = Array.isArray(m.content)
		? m.content.map((b) => (isRecord(b) ? { ...b } : b))
		: m.content;
	return { ...m, content };
}

export function startLiveMessage(frame: JsonRecord): LiveMessage {
	const message = cloneMessage(frame.message);
	return {
		messageId:
			typeof frame.messageId === "string" ? frame.messageId : undefined,
		role: typeof message.role === "string" ? message.role : "assistant",
		timestamp:
			typeof message.timestamp === "number" ? message.timestamp : undefined,
		message,
		partialJson: {},
		ended: false,
	};
}

/**
 * 尽力解析残缺的 JSON（流式 toolcall 参数）：补齐未闭合的字符串与括号后再解析，失败返回 undefined。
 */
export function parsePartialJson(text: string): unknown {
	const trimmed = text.trim();
	if (!trimmed) return undefined;
	try {
		return JSON.parse(trimmed);
	} catch {
		// 继续尝试补齐
	}
	const stack: string[] = [];
	let inString = false;
	let escaped = false;
	for (const ch of trimmed) {
		if (inString) {
			if (escaped) escaped = false;
			else if (ch === "\\") escaped = true;
			else if (ch === '"') inString = false;
			continue;
		}
		if (ch === '"') inString = true;
		else if (ch === "{") stack.push("}");
		else if (ch === "[") stack.push("]");
		else if (ch === "}" || ch === "]") stack.pop();
	}
	let fixed = trimmed;
	if (escaped) fixed = fixed.slice(0, -1);
	if (inString) fixed += '"';
	const closers = [...stack].reverse().join("");
	// 依次尝试：直接补括号；去掉悬空的逗号 / 冒号；再去掉尚无值的键
	const candidates = [
		fixed,
		fixed.replace(/[,:]\s*$/, ""),
		fixed.replace(/,?\s*"(?:[^"\\]|\\.)*"\s*:?\s*$/, ""),
	];
	for (const c of candidates) {
		try {
			return JSON.parse(c + closers);
		} catch {
			// 换下一个候选
		}
	}
	return undefined;
}

function contentOf(m: JsonRecord): unknown[] {
	if (!Array.isArray(m.content)) m.content = [];
	return m.content as unknown[];
}

/** 应用一条 `message_update`，返回新的 LiveMessage（不修改入参）。 */
export function applyMessageUpdate(
	live: LiveMessage,
	frame: JsonRecord,
): LiveMessage {
	const ev = isRecord(frame.assistantMessageEvent)
		? frame.assistantMessageEvent
		: null;
	const frameMsg = isRecord(frame.message) ? frame.message : null;
	// 完整形态：帧里直接带了整条消息
	if (frameMsg && Array.isArray(frameMsg.content) && (!ev || "partial" in ev)) {
		const message = cloneMessage(frameMsg);
		return { ...live, message, role: (message.role as string) ?? live.role };
	}
	if (!ev) return live;
	const message = cloneMessage(live.message);
	const content = contentOf(message);
	const partialJson = { ...live.partialJson };
	const idx = typeof ev.contentIndex === "number" ? ev.contentIndex : -1;
	const block =
		idx >= 0 && isRecord(content[idx]) ? (content[idx] as JsonRecord) : null;
	switch (ev.type) {
		case "start":
			if (isRecord(ev.message))
				return { ...live, message: cloneMessage(ev.message), partialJson: {} };
			return live;
		case "text_start":
			content[idx] = isRecord(ev.block)
				? { ...ev.block, text: "" }
				: { type: "text", text: "" };
			break;
		case "thinking_start":
			content[idx] = isRecord(ev.block)
				? { ...ev.block, thinking: "" }
				: { type: "thinking", thinking: "" };
			break;
		case "toolcall_start":
			content[idx] = isRecord(ev.block)
				? { ...ev.block, arguments: {} }
				: { type: "toolCall", id: "", name: "", arguments: {} };
			partialJson[idx] = "";
			break;
		case "text_delta":
			if (typeof ev.delta === "string") {
				const base = block ?? { type: "text", text: "" };
				content[idx] = {
					...base,
					text: `${typeof base.text === "string" ? base.text : ""}${ev.delta}`,
				};
			}
			break;
		case "thinking_delta":
			if (typeof ev.delta === "string") {
				const base = block ?? { type: "thinking", thinking: "" };
				content[idx] = {
					...base,
					thinking: `${typeof base.thinking === "string" ? base.thinking : ""}${ev.delta}`,
				};
			}
			break;
		case "toolcall_delta":
			if (typeof ev.delta === "string") {
				const text = (partialJson[idx] ?? "") + ev.delta;
				partialJson[idx] = text;
				const parsed = parsePartialJson(text);
				const base = block ?? {
					type: "toolCall",
					id: "",
					name: "",
					arguments: {},
				};
				content[idx] = isRecord(parsed) ? { ...base, arguments: parsed } : base;
			}
			break;
		case "text_end":
			content[idx] = {
				...(block ?? { type: "text" }),
				...(isRecord(ev.block) ? ev.block : {}),
				text: typeof ev.content === "string" ? ev.content : (block?.text ?? ""),
			};
			break;
		case "thinking_end":
			content[idx] = {
				...(block ?? { type: "thinking" }),
				...(isRecord(ev.block) ? ev.block : {}),
				thinking:
					typeof ev.content === "string" ? ev.content : (block?.thinking ?? ""),
			};
			break;
		case "image_end":
			if (isRecord(ev.content)) content[idx] = { ...ev.content };
			break;
		case "toolcall_end":
			if (isRecord(ev.toolCall)) content[idx] = { ...ev.toolCall };
			delete partialJson[idx];
			break;
		case "done":
			if (isRecord(ev.message))
				return { ...live, message: cloneMessage(ev.message), partialJson: {} };
			return live;
		case "error":
			if (isRecord(ev.error))
				return { ...live, message: cloneMessage(ev.error), partialJson: {} };
			return live;
		default:
			return live;
	}
	return { ...live, message, partialJson };
}

export function endLiveMessage(
	live: LiveMessage | null,
	frame: JsonRecord,
): LiveMessage {
	const base = live ?? startLiveMessage(frame);
	const message = isRecord(frame.message)
		? cloneMessage(frame.message)
		: base.message;
	return {
		...base,
		message,
		role: typeof message.role === "string" ? message.role : base.role,
		timestamp:
			typeof message.timestamp === "number"
				? message.timestamp
				: base.timestamp,
		partialJson: {},
		ended: true,
	};
}
