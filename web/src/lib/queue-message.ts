/**
 * 排队/引导消息（QueueBar）的显示与编辑解析：
 * - 复用 Oryn 现有的附件块（`<oryn-attachments>`）、写作块改稿块（`<oryn-draft-edits>`）、引用回复（`splitLeadingQuote`）、
 *   自动化消息头（`parseOrynMessage`）以及 artifact 选区数据块（`oryn-data-<hex>`）解析；
 * - 展示时拆出干净的正文与附件/引用/选区徽章，不向用户暴露原始标签文本；
 * - 编辑保存时通过 `rebuild(nextBody)` 无损拼回原有引用、选区块与 `<oryn-draft-edits>` + `<oryn-attachments>` 后缀；
 * - 乐观排队去重通过「提交时刻的权威基线 ID + 正文 + 附件身份」一对一匹配，
 *   防止已排队的同正文/纯附件旧项或无关 `queue_update` 隐藏或误删新提交的在途乐观项。
 */
import type { RpcQueuedMessage } from "../omp/types";
import { type MessageAttachment, parseAttachmentBlock } from "./attachments";
import { type DraftEditRef, parseDraftEditsBlock } from "./draft-edits";
import { type OrynMessage, parseOrynMessage } from "./oryn-message";
import { composeQuoted, splitLeadingQuote } from "./quote";
import { type ParsedViewReceipt, parseViewReceipt } from "./view/answer";

export interface OptimisticAttachmentMeta {
	/** 线程附件 `att_…` 或助手库文件 `afl_…` 的 id（随 `<oryn-attachments>` 块投递）。 */
	id?: string;
	name: string;
	mime?: string;
	size?: number;
}

export interface OptimisticMatchCandidate {
	text: string;
	attachments?: OptimisticAttachmentMeta[];
	/** 提交时刻该 lane 已存在的权威条目 id（新乐观项绝不被这些旧权威项抵消）。 */
	baselineIds?: readonly string[];
	/** 提交时刻该 lane 已存在的权威条目数量（仅旧版纯字符串无 id 帧回退使用）。 */
	baselineCount?: number;
	settled?: boolean;
}

export interface ParsedQueueMessage {
	/** 控制面/agent 发来的自动化消息（定时、跨线程、报告等）。 */
	oryn: OrynMessage | null;
	/** 消息开头拆出的引用块纯文本（无前导 `>`）。 */
	quote: string | null;
	/** 去掉附件块、选区块与前导引用后的正文（供单行展示与编辑）。 */
	body: string;
	/** 从末尾 `<oryn-attachments>` 块解析出的附件列表。 */
	attachments: MessageAttachment[];
	/** 从末尾 `<oryn-draft-edits>` 块解析出的改稿（类型与标题）。 */
	draftEdits: DraftEditRef[];
	/** 从末尾 `oryn-data-<16hex>` 块解析出的 artifact 标题（若有）。 */
	artifactTitle: string | null;
	/** 视图作答的回执（M33）：正文是去掉 JSON 围栏的作答。 */
	viewReceipt: ParsedViewReceipt | null;
	/** 把用户编辑后的新正文拼回原始完整消息（保留前导引用、选区块、改稿块与附件块）。 */
	rebuild: (nextBody: string) => string;
}

/** 匹配 `selectionPrompt` 追加在末尾的说明行 + `oryn-data-<16hex>` 数据块。 */
const ARTIFACT_DATA_RE =
	/(?:\n\n[^\n]+\n\n[^\n]+\n\n|\n\n)(oryn-data-[0-9a-f]{16})\n([\s\S]*?)\n\1\s*$/;
const ARTIFACT_TITLE_RE = /^artifact:\s*"([^"]*)"/m;
const ATT_OR_FILE_ID_RE = /\/((?:att|afl)_[^/]+)\/[^/]+$/;

function attachmentsMatch(
	optAtts: readonly OptimisticAttachmentMeta[],
	authAtts: readonly MessageAttachment[],
): boolean {
	if (optAtts.length !== authAtts.length) return false;
	if (optAtts.length === 0) return true;
	const used = new Array<boolean>(authAtts.length).fill(false);
	for (const o of optAtts) {
		const idx = authAtts.findIndex((a, i) => {
			if (used[i]) return false;
			const aId = a.id ?? ATT_OR_FILE_ID_RE.exec(a.path)?.[1] ?? null;
			if (o.id && aId) return o.id === aId;
			if (o.name !== a.name) return false;
			return o.size === undefined || o.size === a.size;
		});
		if (idx < 0) return false;
		used[idx] = true;
	}
	return true;
}

/** 剥离控制面追加的 `<oryn-attachments>` 与 `<oryn-draft-edits>` 块后的规范化正文。 */
export function queueMatchKey(rawText: string): string {
	return controlBody(rawText).text.trim();
}

/** 依次剥掉末尾的附件块与改稿块（附件块总在最后）。 */
function controlBody(rawText: string): {
	text: string;
	attachments: MessageAttachment[];
} {
	const parsed = parseAttachmentBlock(rawText.replace(/\r\n/g, "\n"));
	return {
		text: parseDraftEditsBlock(parsed.text).text,
		attachments: parsed.attachments,
	};
}

/**
 * 筛出尚未被权威队列接管的乐观排队项：
 * - 权威项带真实 `id` 时，只允许「不在该乐观项 `baselineIds` 中」的新权威项与其按「正文 + 附件身份」一对一匹配；
 *   从而保证已有旧项与无关 `queue_update` 绝不会隐藏或误删同正文/纯附件的新在途乐观项。
 * - 兼容旧版纯字符串无 `id` 帧：仅用超出 `baselineCount` 后的新增条目按签名一对一匹配。
 */
export function filterUnmatchedOptimistic<T extends OptimisticMatchCandidate>(
	optimistic: readonly T[],
	authoritativeItems: readonly RpcQueuedMessage[] | null | undefined,
	authoritativeTexts: readonly string[] | null | undefined,
	opts: { dropSettled?: boolean } = {},
): T[] {
	if (optimistic.length === 0) return [];
	const useObjects =
		authoritativeItems !== null && authoritativeItems !== undefined;
	const authList = useObjects
		? authoritativeItems.map((item) => {
				const parsed = controlBody(item.text);
				return {
					id: item.id,
					body: parsed.text.trim(),
					attachments: parsed.attachments,
				};
			})
		: (authoritativeTexts ?? []).map((text) => {
				const parsed = controlBody(text);
				return {
					id: null as string | null,
					body: parsed.text.trim(),
					attachments: parsed.attachments,
				};
			});

	const consumed = new Array<boolean>(authList.length).fill(false);
	const kept: T[] = [];

	for (const item of optimistic) {
		if (opts.dropSettled && item.settled) continue;
		const optBody = queueMatchKey(item.text);
		const optAtts = item.attachments ?? [];
		const baseIds = item.baselineIds ? new Set(item.baselineIds) : null;
		const baseCount = item.baselineCount ?? 0;

		let matchedIndex = -1;
		for (let i = 0; i < authList.length; i++) {
			if (consumed[i]) continue;
			const auth = authList[i];
			if (!auth) continue;
			if (useObjects) {
				if (auth.id && baseIds?.has(auth.id)) continue;
			} else if (i < baseCount) {
				continue;
			}
			if (auth.body !== optBody) continue;
			if (!attachmentsMatch(optAtts, auth.attachments)) continue;
			matchedIndex = i;
			break;
		}

		if (matchedIndex >= 0) {
			consumed[matchedIndex] = true;
			continue;
		}
		kept.push(item);
	}
	return kept;
}

export function parseQueueMessage(rawText: string): ParsedQueueMessage {
	const normalized = rawText.replace(/\r\n/g, "\n");
	const attParsed = parseAttachmentBlock(normalized);
	const draftParsed = parseDraftEditsBlock(attParsed.text);
	// 改稿块 + 附件块：编辑正文时原样保留在末尾
	const controlSuffix = normalized.slice(draftParsed.text.length);
	const draftEdits = draftParsed.drafts;
	let rest = draftParsed.text;
	let artifactSuffix = "";
	let artifactTitle: string | null = null;

	const artMatch = ARTIFACT_DATA_RE.exec(rest);
	if (artMatch) {
		const titleMatch = ARTIFACT_TITLE_RE.exec(artMatch[2] ?? "");
		if (titleMatch) {
			artifactTitle = titleMatch[1] || "Artifact";
			artifactSuffix = rest.slice(artMatch.index);
			rest = rest.slice(0, artMatch.index);
		}
	}

	// 视图回执：显示作答正文；编辑后不再是回执（正文没改时原样保留）
	const viewReceipt = parseViewReceipt(rest);
	if (viewReceipt) {
		return {
			oryn: null,
			quote: null,
			body: viewReceipt.display,
			attachments: attParsed.attachments,
			draftEdits,
			artifactTitle,
			viewReceipt,
			rebuild: (nextBody: string) =>
				nextBody === viewReceipt.display
					? normalized
					: `${nextBody}${artifactSuffix}${controlSuffix}`,
		};
	}

	const oryn = parseOrynMessage(rest);
	if (oryn) {
		return {
			oryn,
			quote: null,
			body: oryn.body,
			attachments: attParsed.attachments,
			draftEdits,
			artifactTitle,
			viewReceipt: null,
			rebuild: (nextBody: string) =>
				`${nextBody}${artifactSuffix}${controlSuffix}`,
		};
	}

	const quoted = splitLeadingQuote(rest);
	const quote = quoted ? quoted.quote : null;
	const body = quoted ? quoted.rest : rest;

	return {
		oryn: null,
		quote,
		body,
		attachments: attParsed.attachments,
		draftEdits,
		artifactTitle,
		viewReceipt: null,
		rebuild: (nextBody: string) => {
			const withQuote = quote ? composeQuoted(quote, nextBody) : nextBody;
			const withArtifact = artifactSuffix
				? withQuote
					? `${withQuote}${artifactSuffix}`
					: artifactSuffix.replace(/^\n+/, "")
				: withQuote;
			if (!controlSuffix) return withArtifact;
			if (!withArtifact) return controlSuffix.replace(/^\n+/, "");
			return `${withArtifact}${controlSuffix.startsWith("\n") ? controlSuffix : `\n\n${controlSuffix}`}`;
		},
	};
}
