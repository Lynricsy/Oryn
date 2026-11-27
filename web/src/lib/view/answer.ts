/**
 * 视图作答的线上格式（docs/plan-m33.md 的 C4）。
 *
 * - 作答正文（给模型读，也是转录里显示的内容）只由 Web 生成：`formatFormAnswer` 与按钮的 `prompt`、HTML 页传来的文本。
 * - 非阻塞：正文包在 `<oryn-view-answer>` 回执里作为主人的下一条消息发出（`buildViewReceipt` / `parseViewReceipt`）。
 * - 阻塞：`extension_ui_response.value` 是 `encodeAwaitValue` 的 JSON，扩展与 orynd 只转交。
 */
import type { FieldValue, FormBlock } from "./spec";

export type ViewAnswer =
	| { block: string; kind: "form"; values: Record<string, FieldValue> }
	/** `choice` 是按钮在 `buttons` 里的下标。 */
	| { block: string; kind: "action"; choice: number }
	/** 正文即 `sendPrompt` 的文本。 */
	| { block: "html"; kind: "prompt" };

/** HTML 视图的块路径（C2）。 */
export const HTML_BLOCK = "html";
/** `sendPrompt` 文本的上限（C9）。 */
export const MAX_PROMPT_TEXT = 4000;
/** 阻塞请求的标记：`extension_ui_request` 的 `method === "input"` 且 `placeholder` 以它开头（C5）。 */
export const VIEW_AWAIT_MARK = "oryn-view:";

const RECEIPT =
	/^<oryn-view-answer call="([^"\s<>]{1,256})" block="([0-9a-z.]{1,40})"(?: choice="(\d)")?>\n([\s\S]*)\n<\/oryn-view-answer>\s*$/;
const JSON_FENCE = /```json\n([\s\S]*?)\n```/g;
const CALL_ID = /^[^"\s<>]{1,256}$/;

/** 这次调用的 id 能否放进回执；不能时视图不可交互（C4）。 */
export function isAnswerableCallId(toolCallId: string): boolean {
	return CALL_ID.test(toolCallId);
}

/** 阻塞请求的 placeholder 对应的 toolCallId；不是视图的请求返回 null。 */
export function viewAwaitCallId(
	method: string,
	placeholder: unknown,
): string | null {
	if (method !== "input" || typeof placeholder !== "string") return null;
	if (!placeholder.startsWith(VIEW_AWAIT_MARK)) return null;
	return placeholder.slice(VIEW_AWAIT_MARK.length) || null;
}

/** 表单字段的显示值：选项显示其标签，多选用 `, ` 连接，布尔 ✓ / ✗，空值 —。 */
function displayValue(
	field: FormBlock["fields"][number],
	value: FieldValue | undefined,
): string {
	if (value === null || value === undefined || value === "") return "—";
	if (typeof value === "boolean") return value ? "✓" : "✗";
	const labelOf = (v: string) =>
		field.options?.find((o) => o.value === v)?.label ?? v;
	if (Array.isArray(value))
		return value.length ? value.map(labelOf).join(", ") : "—";
	return typeof value === "string" ? labelOf(value) : String(value);
}

/**
 * 表单的作答正文：首行是表单标题（没有则省略），随后每个字段一行 `- <label>: <显示值>`，空一行，
 * 再接 `{ <name>: <原始值> }` 的 JSON 围栏。
 */
export function formatFormAnswer(
	form: FormBlock,
	values: Record<string, FieldValue>,
): string {
	const lines: string[] = [];
	if (form.title) lines.push(form.title);
	for (const f of form.fields)
		lines.push(`- ${f.label}: ${displayValue(f, values[f.name])}`);
	const raw: Record<string, FieldValue> = {};
	for (const f of form.fields) raw[f.name] = values[f.name] ?? null;
	return `${lines.join("\n")}\n\n\`\`\`json\n${JSON.stringify(raw)}\n\`\`\``;
}

/** 非阻塞回执：作为主人的下一条消息发出（首行不以 `[Oryn ` 开头）。 */
export function buildViewReceipt(
	toolCallId: string,
	answer: ViewAnswer,
	text: string,
): string {
	const choice = answer.kind === "action" ? ` choice="${answer.choice}"` : "";
	return `<oryn-view-answer call="${toolCallId}" block="${answer.block}"${choice}>\n${text}\n</oryn-view-answer>`;
}

export interface ParsedViewReceipt {
	toolCallId: string;
	answer: ViewAnswer;
	/** 回执里的作答正文。 */
	text: string;
	/** 显示用的正文：去掉末尾的 JSON 围栏。 */
	display: string;
}

/** 正文里最后一个 ```json 围栏的对象；没有或解析失败时为 null。 */
function lastJsonFence(text: string): Record<string, unknown> | null {
	let last: string | undefined;
	for (const m of text.matchAll(JSON_FENCE)) last = m[1];
	if (last === undefined) return null;
	try {
		const v: unknown = JSON.parse(last);
		return typeof v === "object" && v !== null && !Array.isArray(v)
			? (v as Record<string, unknown>)
			: null;
	} catch {
		return null;
	}
}

function toFieldValue(v: unknown): FieldValue {
	if (typeof v === "string" || typeof v === "number" || typeof v === "boolean")
		return v;
	if (Array.isArray(v))
		return v.filter((x): x is string => typeof x === "string");
	return null;
}

/** 去掉作答正文末尾的 JSON 围栏（显示用）。 */
export function stripAnswerJson(text: string): string {
	return text.replace(/\n*```json\n[\s\S]*?\n```\s*$/, "").trimEnd();
}

/** 解析主人消息里的视图回执；不是回执时返回 null。 */
export function parseViewReceipt(message: string): ParsedViewReceipt | null {
	const m = RECEIPT.exec(message);
	if (!m) return null;
	// 正则里这三组一定匹配（只有 choice 可选）
	const [, toolCallId = "", block = "", choice, text = ""] = m;
	let answer: ViewAnswer;
	if (block === HTML_BLOCK) {
		answer = { block: HTML_BLOCK, kind: "prompt" };
	} else if (choice !== undefined) {
		answer = { block, kind: "action", choice: Number(choice) };
	} else {
		const values: Record<string, FieldValue> = {};
		for (const [k, v] of Object.entries(lastJsonFence(text) ?? {}))
			values[k] = toFieldValue(v);
		answer = { block, kind: "form", values };
	}
	return {
		toolCallId,
		answer,
		text,
		display: answer.kind === "form" ? stripAnswerJson(text) : text,
	};
}

/** 阻塞应答的 `value`（C4）。 */
export function encodeAwaitValue(answer: ViewAnswer, text: string): string {
	return JSON.stringify({ v: 1, answer, text });
}

/** 工具结果 `details.view.answer` 的宽容读取：形状不对时返回 null。 */
export function readViewAnswer(raw: unknown): ViewAnswer | null {
	if (typeof raw !== "object" || raw === null) return null;
	const a = raw as Record<string, unknown>;
	if (typeof a.block !== "string") return null;
	if (a.kind === "prompt" && a.block === HTML_BLOCK)
		return { block: HTML_BLOCK, kind: "prompt" };
	if (a.kind === "action" && typeof a.choice === "number")
		return { block: a.block, kind: "action", choice: a.choice };
	if (a.kind === "form" && typeof a.values === "object" && a.values !== null) {
		const values: Record<string, FieldValue> = {};
		for (const [k, v] of Object.entries(a.values)) values[k] = toFieldValue(v);
		return { block: a.block, kind: "form", values };
	}
	return null;
}
