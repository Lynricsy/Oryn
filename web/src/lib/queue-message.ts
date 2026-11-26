/**
 * 排队/引导消息（QueueBar）的显示与编辑解析：
 * - 复用 Oryn 现有的附件块（`<oryn-attachments>`）、引用回复（`splitLeadingQuote`）、
 *   自动化消息头（`parseOrynMessage`）以及 artifact 选区数据块（`oryn-data-<hex>`）解析；
 * - 展示时拆出干净的正文与附件/引用/选区徽章，不向用户暴露原始标签文本；
 * - 编辑保存时通过 `rebuild(nextBody)` 无损拼回原有引用、选区块与 `<oryn-attachments>` 后缀。
 */
import { type MessageAttachment, parseAttachmentBlock } from "./attachments";
import { type OrynMessage, parseOrynMessage } from "./oryn-message";
import { composeQuoted, splitLeadingQuote } from "./quote";

export interface OptimisticAttachmentMeta {
	name: string;
	mime?: string;
	size?: number;
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
	/** 从末尾 `oryn-data-<16hex>` 块解析出的 artifact 标题（若有）。 */
	artifactTitle: string | null;
	/** 把用户编辑后的新正文拼回原始完整消息（保留前导引用、选区块与附件块）。 */
	rebuild: (nextBody: string) => string;
}

/** 匹配 `selectionPrompt` 追加在末尾的说明行 + `oryn-data-<16hex>` 数据块。 */
const ARTIFACT_DATA_RE =
	/(?:\n\n[^\n]+\n\n[^\n]+\n\n|\n\n)(oryn-data-[0-9a-f]{16})\n([\s\S]*?)\n\1\s*$/;
const ARTIFACT_TITLE_RE = /^artifact:\s*"([^"]*)"/m;

/** 用于乐观排队项与权威 `queue_update` 去重匹配的规范化键（剥离控制面追加的附件块）。 */
export function queueMatchKey(rawText: string): string {
	return parseAttachmentBlock(rawText.replace(/\r\n/g, "\n")).text.trim();
}

export function parseQueueMessage(rawText: string): ParsedQueueMessage {
	const normalized = rawText.replace(/\r\n/g, "\n");
	const attParsed = parseAttachmentBlock(normalized);
	const attachmentSuffix = normalized.slice(attParsed.text.length);
	let rest = attParsed.text;
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

	const oryn = parseOrynMessage(rest);
	if (oryn) {
		return {
			oryn,
			quote: null,
			body: oryn.body,
			attachments: attParsed.attachments,
			artifactTitle,
			rebuild: (nextBody: string) =>
				`${nextBody}${artifactSuffix}${attachmentSuffix}`,
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
		artifactTitle,
		rebuild: (nextBody: string) => {
			const withQuote = quote ? composeQuoted(quote, nextBody) : nextBody;
			const withArtifact = artifactSuffix
				? withQuote
					? `${withQuote}${artifactSuffix}`
					: artifactSuffix.replace(/^\n+/, "")
				: withQuote;
			if (!attachmentSuffix) return withArtifact;
			if (!withArtifact) return attachmentSuffix.replace(/^\n+/, "");
			return `${withArtifact}${attachmentSuffix.startsWith("\n") ? attachmentSuffix : `\n\n${attachmentSuffix}`}`;
		},
	};
}
