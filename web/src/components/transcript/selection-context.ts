/**
 * 转录里的选区 → 旁路提问的上下文：认出选区来自哪条消息、哪一部分（正文 / 思考 / 工具卡），
 * 离现在几个用户回合、是否已被压缩出上下文，以及紧邻选区的前后文。
 */
import { anchorOf } from "../../lib/context-menu/selection";
import type {
	Anchor,
	DomSelection,
	SelectionContext,
	SelectionSource,
} from "../../lib/context-menu/types";
import { normalizeQuote } from "../../lib/quote";
import { AROUND_MAX } from "../../lib/side-ask";
import { locateEntry } from "../../state/side-ask";

export function transcriptSelection(
	sel: DomSelection,
	o: {
		storeThreadId: string;
		askThreadId: string | null;
		quote?: (text: string) => void;
		fallback: Anchor;
	},
): SelectionContext {
	const scopeOf = (el: Element) =>
		el.closest("[data-quote-scope], [data-ask-scope]");
	const start = scopeOf(sel.startEl);
	const end = scopeOf(sel.endEl);
	const spans = start !== end;
	const row = sel.startEl.closest("[data-index]");
	const entryId = row?.getAttribute("data-entry-id") ?? undefined;
	const role = row?.getAttribute("data-role");
	const live = row?.hasAttribute("data-live") ?? false;
	const tool = sel.startEl.closest("[data-tool-name]");
	let source: SelectionSource;
	if (tool)
		source = {
			kind: "tool",
			toolName: tool.getAttribute("data-tool-name") ?? "",
			...locateEntry(o.storeThreadId, entryId),
			spans,
		};
	else if (sel.startEl.closest('[data-ask-scope="thinking"]'))
		source = {
			kind: "message",
			role: "assistant",
			part: "thinking",
			...locateEntry(o.storeThreadId, entryId),
			live,
			spans,
		};
	else if (role === "user" || role === "assistant")
		source = {
			kind: "message",
			role,
			part: "text",
			...locateEntry(o.storeThreadId, entryId),
			live,
			spans,
		};
	else source = { kind: "other" };
	// 引用回复的可用条件与选区胶囊一致：起止落在同一条 agent 消息里
	const quoteScope = sel.startEl.closest("[data-quote-scope]");
	const { quote } = o;
	const quotable =
		quote !== undefined &&
		!spans &&
		quoteScope !== null &&
		quoteScope === sel.endEl.closest("[data-quote-scope]");
	return {
		threadId: o.askThreadId,
		text: sel.text,
		before: sel.before(start ?? sel.root, AROUND_MAX),
		after: sel.after(end ?? sel.root, AROUND_MAX),
		source,
		anchor: sel.rects.length ? anchorOf(sel, 0, 0) : o.fallback,
		quote: quotable ? () => quote(normalizeQuote(sel.text)) : undefined,
	};
}
