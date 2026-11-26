/**
 * Core 注入转录的 `custom_message` / `custom` 负载解析（对齐 oryn-core `modes/interactive/messages.tsx`）。
 * 所有字段都按「可能残缺」处理——条目与流式帧来自外部 JSON。
 */
import type { JsonRecord } from "../state/message-builder";
import { isRecord } from "../tool-render/util";

export const ADVISOR_MESSAGE_TYPE = "advisor";
export const SKILL_PROMPT_MESSAGE_TYPE = "skill-prompt";
export const LSP_LATE_DIAGNOSTIC_MESSAGE_TYPE = "lsp-late-diagnostic";
export const HANDOFF_MESSAGE_TYPE = "handoff";
export const BACKGROUND_TAN_DISPATCH_MESSAGE_TYPE = "background-tan-dispatch";

/** Core 在预期内部中止（如计划模式切执行压缩）上打的哨兵标记：渲染层静默跳过。 */
export const SILENT_ABORT_MARKER = "__omp.silent_abort__";

export function isSilentAbortMessage(message: JsonRecord): boolean {
	return message.errorMessage === SILENT_ABORT_MARKER;
}

export function isSkillPromptCustomMessage(message: JsonRecord): boolean {
	return message.customType === SKILL_PROMPT_MESSAGE_TYPE;
}

/** 从字符串或 `[{ type: "text", text }]` 内容块提取纯文本。 */
export function customMessageText(content: unknown): string {
	if (typeof content === "string") return content;
	if (!Array.isArray(content)) return "";
	return content
		.map((b) =>
			isRecord(b) && b.type === "text" && typeof b.text === "string"
				? b.text
				: "",
		)
		.filter(Boolean)
		.join("\n\n");
}

/** 反转义 Core XML 模板中用到的实体（`&amp;` 放最后避免二次反转义）。 */
export function unescapeXml(s: string): string {
	return s
		.replaceAll("&lt;", "<")
		.replaceAll("&gt;", ">")
		.replaceAll("&quot;", '"')
		.replaceAll("&apos;", "'")
		.replaceAll("&#39;", "'")
		.replaceAll("&amp;", "&");
}

function nonEmptyStr(v: unknown): string | undefined {
	return typeof v === "string" && v.trim() ? v.trim() : undefined;
}

function positiveInt(v: unknown): number | undefined {
	return typeof v === "number" && Number.isFinite(v) && v > 0
		? Math.floor(v)
		: undefined;
}

// ─── Advisor ──────────────────────────────────────────────────────────────────

export type AdvisorSeverity = "nit" | "concern" | "blocker";
export type AdvisorRank = AdvisorSeverity | "note";

/** 严重度由高到低排序：卡片色调与计数顺序以此为准。 */
export const ADVISOR_RANKS: readonly AdvisorRank[] = [
	"blocker",
	"concern",
	"nit",
	"note",
];

export interface ParsedAdvisorNote {
	note: string;
	severity?: AdvisorSeverity;
	rank: AdvisorRank;
	/** 具名顾问来源；默认顾问（或 `"default"`）为 undefined。 */
	source?: string;
	/** 合并投递时距离发出已过去的回合数（仅 > 0 时存在）。 */
	turnsAgo?: number;
}

export interface ParsedAdvisorMessage {
	notes: ParsedAdvisorNote[];
	worstRank: AdvisorRank;
	/** 非默认顾问来源的去重列表（按出现顺序）。 */
	sources: string[];
	/** 所有建议都来自同一个具名顾问时为该顾问名，否则 undefined。 */
	soleSource?: string;
	/** 存在两个及以上不同来源（含默认顾问与具名顾问混排）时为真。 */
	mixedSources: boolean;
	/** 各严重度计数（按 blocker → concern → nit → note 顺序，只保留 > 0 项）。 */
	counts: Array<{ rank: AdvisorRank; count: number }>;
}

function parseSeverity(v: unknown): AdvisorSeverity | undefined {
	return v === "blocker" || v === "concern" || v === "nit" ? v : undefined;
}

function normalizeAdvisorSource(v: unknown): string | undefined {
	const s = nonEmptyStr(v);
	return s && s !== "default" ? s : undefined;
}

const ADVISORY_TAG_RE = /<advisory(\s[^>]*)?>([\s\S]*?)<\/advisory>/g;
const XML_ATTR_RE = /([\w-]+)="([^"]*)"/g;

function parseAdvisoryXml(text: string): ParsedAdvisorNote[] {
	const out: ParsedAdvisorNote[] = [];
	for (const match of text.matchAll(ADVISORY_TAG_RE)) {
		const attrsText = match[1] ?? "";
		const body = unescapeXml((match[2] ?? "").trim());
		if (!body) continue;
		const attrs: Record<string, string> = {};
		for (const a of attrsText.matchAll(XML_ATTR_RE)) {
			const k = a[1];
			const v = a[2];
			if (k && v !== undefined) attrs[k] = unescapeXml(v);
		}
		const severity = parseSeverity(attrs.severity);
		const turnsNum = attrs.turns_ago ? Number(attrs.turns_ago) : undefined;
		out.push({
			note: body,
			severity,
			rank: severity ?? "note",
			source: normalizeAdvisorSource(attrs.advisor),
			turnsAgo: positiveInt(turnsNum),
		});
	}
	return out;
}

/**
 * 解析 `customType === "advisor"` 消息。
 * 优先取 `details.notes`；缺失时从 `<advisory ...>...</advisory>` XML 恢复；再退回非空纯文本。
 */
export function parseAdvisorMessage(
	message: JsonRecord,
): ParsedAdvisorMessage | null {
	if (message.customType !== ADVISOR_MESSAGE_TYPE) return null;
	const notes: ParsedAdvisorNote[] = [];
	const details = isRecord(message.details) ? message.details : null;
	if (details && Array.isArray(details.notes)) {
		for (const raw of details.notes) {
			if (!isRecord(raw) || typeof raw.note !== "string") continue;
			const body = raw.note.trim();
			if (!body) continue;
			const severity = parseSeverity(raw.severity);
			notes.push({
				note: body,
				severity,
				rank: severity ?? "note",
				source: normalizeAdvisorSource(raw.advisor),
				turnsAgo: positiveInt(raw.turnsAgo),
			});
		}
	}
	if (notes.length === 0) {
		const text = customMessageText(message.content).trim();
		if (text) {
			const xmlNotes = parseAdvisoryXml(text);
			if (xmlNotes.length > 0) {
				notes.push(...xmlNotes);
			} else {
				notes.push({ note: text, rank: "note" });
			}
		}
	}
	if (notes.length === 0) return null;

	const worstRank =
		ADVISOR_RANKS.find((r) => notes.some((n) => n.rank === r)) ?? "note";
	const rawSources = new Set<string | undefined>(notes.map((n) => n.source));
	const sources = [...rawSources].filter((s): s is string => s !== undefined);
	const soleSource =
		rawSources.size === 1 && sources.length === 1 ? sources[0] : undefined;
	const mixedSources = rawSources.size > 1;
	const counts: Array<{ rank: AdvisorRank; count: number }> = [];
	for (const rank of ADVISOR_RANKS) {
		const count = notes.filter((n) => n.rank === rank).length;
		if (count > 0) counts.push({ rank, count });
	}
	return { notes, worstRank, sources, soleSource, mixedSources, counts };
}

// ─── Late LSP Diagnostics ─────────────────────────────────────────────────────

export interface ParsedDiagnosticFile {
	path: string;
	summary: string;
	errored: boolean;
	messages: string[];
}

export interface ParsedLateDiagnostics {
	files: ParsedDiagnosticFile[];
	errored: boolean;
	totalMessages: number;
	summary: string;
	/** 无结构化 `details.files` 时的纯文本兜底（已剥离 `<system-notice>`）。 */
	fallbackText?: string;
}

function stripOuterTag(text: string, tag: string): string {
	const trimmed = text.trim();
	const openRe = new RegExp(`^<${tag}(?:\\s[^>]*)?>\\s*`);
	const closeRe = new RegExp(`\\s*</${tag}>$`);
	if (!openRe.test(trimmed)) return trimmed;
	return trimmed.replace(openRe, "").replace(closeRe, "").trim();
}

export function parseLateDiagnostics(
	message: JsonRecord,
): ParsedLateDiagnostics | null {
	if (message.customType !== LSP_LATE_DIAGNOSTIC_MESSAGE_TYPE) return null;
	const details = isRecord(message.details) ? message.details : null;
	const files: ParsedDiagnosticFile[] = [];
	if (details && Array.isArray(details.files)) {
		for (const raw of details.files) {
			if (!isRecord(raw)) continue;
			const messages = Array.isArray(raw.messages)
				? raw.messages.filter((m): m is string => typeof m === "string" && !!m)
				: [];
			const path = typeof raw.path === "string" ? raw.path : "";
			const summary = typeof raw.summary === "string" ? raw.summary : "";
			const errored = raw.errored === true;
			if (messages.length > 0 || path || summary) {
				files.push({ path, summary, errored, messages });
			}
		}
	}
	if (files.length > 0) {
		const errored = files.some((f) => f.errored);
		const totalMessages = files.reduce((acc, f) => acc + f.messages.length, 0);
		const summary = files
			.map((f) => f.summary)
			.filter(Boolean)
			.join(" · ");
		return { files, errored, totalMessages, summary };
	}
	const fallback = stripOuterTag(
		customMessageText(message.content),
		"system-notice",
	);
	if (!fallback) return null;
	return {
		files: [],
		errored: false,
		totalMessages: 0,
		summary: "",
		fallbackText: fallback,
	};
}

// ─── Skill Prompt ─────────────────────────────────────────────────────────────

export interface ParsedSkillPrompt {
	name: string;
	path?: string;
	lineCount?: number;
	/** 用户附带的指令正文（已去掉开头的 `/skill:<name>` 标记）；无附加指令时为空串。 */
	draft: string;
	/** 完整的加载后技能提示词文本（折叠展示）。 */
	promptText: string;
}

export function parseSkillPrompt(
	message: JsonRecord,
): ParsedSkillPrompt | null {
	if (message.customType !== SKILL_PROMPT_MESSAGE_TYPE) return null;
	const details = isRecord(message.details) ? message.details : null;
	const name = nonEmptyStr(details?.name);
	const promptText = customMessageText(message.content).trim();
	if (!name && !promptText) return null;
	const skillName = name ?? "unknown";
	const token = `/skill:${skillName}`;
	const rawPrompt =
		typeof details?.prompt === "string"
			? details.prompt.trim()
			: typeof details?.args === "string" && details.args.trim()
				? `${token} ${details.args.trim()}`
				: "";
	let draft = rawPrompt;
	if (draft === token) {
		draft = "";
	} else if (
		draft.startsWith(token) &&
		/^\s/.test(draft.charAt(token.length))
	) {
		draft = draft.slice(token.length).trim();
	}
	return {
		name: skillName,
		path: nonEmptyStr(details?.path),
		lineCount: positiveInt(details?.lineCount),
		draft,
		promptText,
	};
}

// ─── Handoff ──────────────────────────────────────────────────────────────────

/** 提取 `customType === "handoff"` 的交接正文（剥离 `<handoff-context>` 标签，与 Core 一致）。 */
export function parseHandoffMessage(message: JsonRecord): string | null {
	if (message.customType !== HANDOFF_MESSAGE_TYPE) return null;
	const text = customMessageText(message.content);
	const open = "<handoff-context>";
	const at = text.indexOf(open);
	if (at === -1) return text.trim();
	const start = at + open.length;
	const end = text.indexOf("</handoff-context>", start);
	return (end === -1 ? text.slice(start) : text.slice(start, end)).trim();
}

// ─── IRC Custom Messages ──────────────────────────────────────────────────────

export type IrcMessageKind = "incoming" | "autoreply" | "relay" | "workpool";

const IRC_CUSTOM_TYPES: Readonly<Record<string, IrcMessageKind>> = {
	"irc:incoming": "incoming",
	"irc:autoreply": "autoreply",
	"irc:relay": "relay",
	"irc:workpool": "workpool",
};

export interface ParsedIrcMessage {
	kind: IrcMessageKind;
	from?: string;
	to?: string;
	body: string;
	replyTo?: string;
	pool?: string;
	mode?: string;
	progress: boolean;
	broadcast: boolean;
}

export function parseIrcCustomMessage(
	message: JsonRecord,
): ParsedIrcMessage | null {
	const customType =
		typeof message.customType === "string" ? message.customType : "";
	const kind = IRC_CUSTOM_TYPES[customType];
	if (!kind) return null;
	const details = isRecord(message.details) ? message.details : null;
	const from = nonEmptyStr(details?.fromLabel) ?? nonEmptyStr(details?.from);
	const to = nonEmptyStr(details?.to);
	const rawBody = kind === "incoming" ? details?.message : details?.body;
	const body =
		typeof rawBody === "string" && rawBody.trim()
			? rawBody.trim()
			: stripOuterTag(customMessageText(message.content), "irc");
	if (!body && !from && !to) return null;
	return {
		kind,
		from,
		to,
		body,
		replyTo: nonEmptyStr(details?.replyTo),
		pool: nonEmptyStr(details?.pool),
		mode: nonEmptyStr(details?.mode),
		progress: details?.progress === true,
		broadcast: details?.broadcast === true,
	};
}

// ─── Background Tan Dispatch ──────────────────────────────────────────────────

export interface ParsedTanDispatch {
	jobId?: string;
	work: string;
	sessionFile?: string;
}

export function parseTanDispatch(
	message: JsonRecord,
): ParsedTanDispatch | null {
	if (message.customType !== BACKGROUND_TAN_DISPATCH_MESSAGE_TYPE) return null;
	const details = isRecord(message.details) ? message.details : null;
	const work =
		nonEmptyStr(details?.work) ?? customMessageText(message.content).trim();
	const jobId = nonEmptyStr(details?.jobId);
	if (!work && !jobId) return null;
	return {
		jobId,
		work,
		sessionFile: nonEmptyStr(details?.sessionFile),
	};
}

// ─── Generic Custom Message Preview ───────────────────────────────────────────

/** 匹配独占一行的 XML 开/闭/自闭合包裹标签（含属性，如 `<system-notice id="foo">`）。 */
const XML_WRAPPER_LINE_RE = /^<\/?[\w-]+(?:\s[^>]*)?\/?>$/;

/** 通用自定义消息折叠时取第一行有意义的正文（跳过空行与 XML 包裹标签行）。 */
export function firstCustomMessagePreview(text: string): string {
	return (
		text
			.split("\n")
			.map((l) => l.trim())
			.find((l) => l && !XML_WRAPPER_LINE_RE.test(l)) ?? ""
	);
}
