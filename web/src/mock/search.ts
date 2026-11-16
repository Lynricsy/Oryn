/**
 * mock 后端的对话内容搜索（`GET /api/v1/search`）：与服务端同口径的简化实现——主会话 user / assistant 消息的
 * text 块，按空白切词、全部命中（大小写与变音符折叠），按消息时间从新到旧，片段与高亮按 UTF-16 下标。
 * 不做 trigram 与 fork 去重（mock 里没有 fork 拷贝），`copies` 恒为 1，游标是偏移量。
 */
import type { Entry, Schemas, Thread } from "../api/client";
import { fold, mergeRanges, queryTerms } from "../lib/palette-match";

type Json = Record<string, unknown>;

const SNIPPET_LEAD = 32;
const SNIPPET_MAX = 160;

function json(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), {
		status,
		headers: { "content-type": "application/json" },
	});
}

/** 消息条目的可索引正文：content 为字符串时即该串，数组时取全部 text 块以换行连接。 */
function messageText(e: Entry): { role: string; text: string } | null {
	if (e.type !== "message" || e.file.includes("/")) return null;
	const msg = (e.payload as Json).message as Json | undefined;
	const role = msg?.role;
	if (role !== "user" && role !== "assistant") return null;
	const content = msg?.content;
	const text =
		typeof content === "string"
			? content
			: Array.isArray(content)
				? content
						.filter(
							(b): b is { type: "text"; text: string } =>
								typeof b === "object" &&
								b !== null &&
								(b as Json).type === "text" &&
								typeof (b as Json).text === "string",
						)
						.map((b) => b.text)
						.join("\n")
				: "";
	return text.trim() ? { role, text } : null;
}

/** 命中处附近的一段（空白折叠），与高亮区间。 */
function snippet(
	text: string,
	terms: readonly string[],
): { snippet: string; highlights: Schemas["TextRange"][] } {
	const flat = text.replace(/\s+/g, " ").trim();
	const f = fold(flat);
	const first = Math.min(
		...terms.map(
			(t) => f.starts[f.text.indexOf(t)] ?? Number.POSITIVE_INFINITY,
		),
	);
	let start = Math.max(0, (Number.isFinite(first) ? first : 0) - SNIPPET_LEAD);
	const end = Math.min(flat.length, start + SNIPPET_MAX);
	start = Math.max(0, Math.min(start, end - SNIPPET_MAX));
	const out = `${start > 0 ? "…" : ""}${flat.slice(start, end).trim()}${end < flat.length ? "…" : ""}`;
	const g = fold(out);
	const ranges: Schemas["TextRange"][] = [];
	for (const t of terms) {
		for (
			let at = g.text.indexOf(t);
			at >= 0;
			at = g.text.indexOf(t, at + t.length)
		)
			ranges.push({
				start: g.starts[at] ?? 0,
				end: g.ends[at + t.length - 1] ?? 0,
			});
	}
	return { snippet: out, highlights: mergeRanges(ranges) };
}

export function handleSearchFetch(
	p: string,
	m: string,
	url: URL,
	data: { threads: () => Thread[]; entries: (threadId: string) => Entry[] },
): Response | null {
	if (p !== "search" || m !== "GET") return null;
	const q = (url.searchParams.get("q") ?? "").trim();
	if (!q) return json({ code: "invalid_request", message: "q 不能为空" }, 400);
	const terms = queryTerms(q);
	if (terms.length > 8)
		return json({ code: "invalid_request", message: "q 至多 8 个词" }, 400);
	const limit = Math.min(Number(url.searchParams.get("limit") ?? 20) || 20, 50);
	const offset = Number(url.searchParams.get("cursor") ?? 0) || 0;
	const hits: Schemas["SearchHit"][] = [];
	for (const th of data.threads()) {
		for (const e of data.entries(th.id)) {
			const msg = messageText(e);
			if (!msg) continue;
			const folded = fold(msg.text).text;
			if (!terms.every((t) => folded.includes(t))) continue;
			hits.push({
				threadId: th.id,
				threadTitle: th.title,
				threadKind: th.kind ?? "work",
				archived: th.archivedAt !== undefined,
				entryId: e.entryId,
				seq: e.seq,
				role: msg.role as Schemas["SearchHit"]["role"],
				ts: e.ts ?? 0,
				...snippet(msg.text, terms),
				copies: 1,
			});
		}
	}
	hits.sort((a, b) => b.ts - a.ts);
	const page = hits.slice(offset, offset + limit);
	const more = offset + limit < hits.length;
	return json({
		hits: page,
		indexing: false,
		...(more ? { nextCursor: String(offset + limit) } : {}),
	} satisfies Schemas["SearchResults"]);
}
