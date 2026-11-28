/**
 * `<oryn-draft-edits>` 块：主人在写作块里改过的草稿，由 orynd 在下一条用户消息末尾（附件块之前）附给 agent。
 * - 显示用户消息时剥掉该块，只留「附上了改过的草稿」芯片；
 * - `buildDraftEditsBlock` 与 Go `draftEditsBlock` 输出逐字一致（mock 与测试用）。
 * 调用方须先用 `parseAttachmentBlock` 剥掉末尾的附件块，再对其正文调用 `parseDraftEditsBlock`。
 */
import type { DraftKind } from "./draft-blocks";

export const DRAFT_EDITS_HEAD =
	"<oryn-draft-edits>\nThe user edited these drafts from your earlier replies in place. The text below is the current version of each; build on it rather than on your original wording.\n";

export interface DraftEditRef {
	kind: DraftKind;
	title: string;
}

const BLOCK_RE =
	/(?:\n\n)?<oryn-draft-edits>\n([\s\S]*?)<\/oryn-draft-edits>\s*$/;
const DRAFT_RE = /<draft kind="(email|message|doc)" title="([^"]*)">\n/g;

function escapeAttr(s: string): string {
	return s.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;");
}

function unescapeAttr(s: string): string {
	return s
		.replace(/&lt;/g, "<")
		.replace(/&quot;/g, '"')
		.replace(/&amp;/g, "&");
}

/** 拆出末尾的改稿块：返回去掉该块的正文与各草稿的类型、标题；没有改稿块时原样返回。 */
export function parseDraftEditsBlock(text: string): {
	text: string;
	drafts: DraftEditRef[];
} {
	const m = BLOCK_RE.exec(text);
	if (!m) return { text, drafts: [] };
	const drafts: DraftEditRef[] = [];
	for (const d of (m[1] ?? "").matchAll(DRAFT_RE))
		drafts.push({
			kind: d[1] as DraftKind,
			title: unescapeAttr(d[2] ?? ""),
		});
	return { text: text.slice(0, m.index), drafts };
}

/** 拼出改稿块（含前导空行）；没有草稿时返回空串。 */
export function buildDraftEditsBlock(
	drafts: readonly { kind: DraftKind; title: string; text: string }[],
): string {
	if (drafts.length === 0) return "";
	const parts = drafts.map(
		(d) =>
			`<draft kind="${d.kind}" title="${escapeAttr(d.title)}">\n${d.text.replace(/<\/(draft|oryn-draft-edits)/g, "<\\/$1")}\n</draft>\n`,
	);
	return `\n\n${DRAFT_EDITS_HEAD}${parts.join("")}</oryn-draft-edits>`;
}
