/**
 * 引用回复：把转录里选中的一段 agent 输出变成 Markdown 引用块，拼在用户回复前面一起发出；
 * 渲染用户消息时再把开头的引用块拆出来单独排版。纯文本处理，协议不变（发出去的就是普通 prompt 正文）。
 */

/** 引用正文的长度上限（字符）：超出截断并以 `…` 收尾，免得一次选中整篇长文把提示词撑满。 */
export const QUOTE_MAX = 2000;

/**
 * 规整选中的文本：统一换行、去掉每行行尾空白与首尾空行，超过 `QUOTE_MAX` 截断加 `…`（按码点截，不切开 emoji）。
 * 行首缩进保留（代码块的缩进有意义）。全是空白时返回空串。
 */
export function normalizeQuote(raw: string): string {
	const lines = raw
		.replace(/\r\n?/g, "\n")
		.split("\n")
		.map((l) => l.replace(/\s+$/u, ""));
	while (lines.length > 0 && lines[0] === "") lines.shift();
	while (lines.length > 0 && lines[lines.length - 1] === "") lines.pop();
	const text = lines.join("\n");
	const chars = Array.from(text);
	if (chars.length <= QUOTE_MAX) return text;
	return `${chars.slice(0, QUOTE_MAX).join("").trimEnd()}…`;
}

/** Markdown 引用块：每行加 `> `，空行只留 `>`（引用不断开）；原文自带的 `>` 随之成为嵌套引用。 */
export function quoteBlock(text: string): string {
	return text
		.split("\n")
		.map((l) => (l ? `> ${l}` : ">"))
		.join("\n");
}

/** 发出的正文：引用块、空一行、回复；回复为空时只有引用块。`quote` 应已经过 `normalizeQuote`。 */
export function composeQuoted(quote: string, reply: string): string {
	const block = quoteBlock(quote);
	return reply ? `${block}\n\n${reply}` : block;
}

/** 一行是否是引用行：至多 3 个空格缩进后跟 `>`（CommonMark 的块引用标记）。 */
const QUOTE_LINE = /^ {0,3}>/;
/** 去掉一层引用标记（`>` 与其后至多一个空格）。 */
const QUOTE_MARK = /^ {0,3}> ?/;

/**
 * 拆出消息开头的引用块：开头连续的引用行去掉一层 `>` 作为引用，其后的空行跳过，余下是回复正文。
 * 开头不是引用、或引用里只有空白时返回 null（照原文显示）。只认开头一段：正文中间的引用按原文显示。
 */
export function splitLeadingQuote(
	text: string,
): { quote: string; rest: string } | null {
	const lines = text.replace(/\r\n?/g, "\n").split("\n");
	let i = 0;
	const quoted: string[] = [];
	while (i < lines.length && QUOTE_LINE.test(lines[i] ?? "")) {
		quoted.push((lines[i] ?? "").replace(QUOTE_MARK, ""));
		i++;
	}
	const quote = normalizeBlank(quoted);
	if (!quote.trim()) return null;
	while (i < lines.length && (lines[i] ?? "").trim() === "") i++;
	return { quote, rest: lines.slice(i).join("\n") };
}

/** 去掉首尾的空白行后拼回文本。 */
function normalizeBlank(lines: string[]): string {
	let start = 0;
	let end = lines.length;
	while (start < end && (lines[start] ?? "").trim() === "") start++;
	while (end > start && (lines[end - 1] ?? "").trim() === "") end--;
	return lines.slice(start, end).join("\n");
}
