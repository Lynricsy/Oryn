// 文档按 Markdown 顶层块拆开，供按块编辑：每块是一段源码，拼回去与原文逐字节相同。
// 链接引用定义并入前一块（它不单独显示，单独成块点不到）。偏移取自 mdast 的 position（JS 字符串下标）。

import remarkGfm from "remark-gfm";
import remarkParse from "remark-parse";
import { unified } from "unified";

export interface Block {
	/** 第一块之前的空白（只有第一块可能非空）。 */
	lead: string;
	/** 这一块的源码。 */
	body: string;
	/** 到下一块之前的分隔（空行等）；最后一块是文末的剩余部分。 */
	tail: string;
}

const parser = unified().use(remarkParse).use(remarkGfm);

/** 拆成顶层块；空文档是一个空块（仍能点开来写）。 */
export function splitBlocks(markdown: string): Block[] {
	const spans: { start: number; end: number }[] = [];
	for (const node of parser.parse(markdown).children) {
		const start = node.position?.start.offset;
		const end = node.position?.end.offset;
		if (start === undefined || end === undefined) continue;
		const prev = spans.at(-1);
		if (node.type === "definition" && prev) {
			prev.end = end;
			continue;
		}
		spans.push({ start, end });
	}
	if (spans.length === 0) return [{ lead: "", body: markdown, tail: "" }];
	return spans.map((s, i) => ({
		lead: i === 0 ? markdown.slice(0, s.start) : "",
		body: markdown.slice(s.start, s.end),
		tail: markdown.slice(s.end, spans[i + 1]?.start ?? markdown.length),
	}));
}

export function joinBlocks(blocks: readonly Block[]): string {
	return blocks.map((b) => b.lead + b.body + b.tail).join("");
}

/**
 * 把第 `index` 块的源码换成 `body`，返回整篇新文本。新源码只剩空白时删掉这一块连同它后面的分隔；
 * 只剩这一块时保留空文档。
 */
export function replaceBlock(
	blocks: readonly Block[],
	index: number,
	body: string,
): string {
	const next = blocks.flatMap((b, i) => {
		if (i !== index) return [b];
		if (body.trim() === "" && blocks.length > 1)
			return b.lead === "" ? [] : [{ lead: b.lead, body: "", tail: "" }];
		return [{ ...b, body }];
	});
	return joinBlocks(next);
}
