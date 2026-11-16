/**
 * 命令面板的本地匹配：查询按空白切成词，每个词都要命中（AND），不区分大小写、忽略变音符（NFD 后去掉组合记号）。
 * 折叠会改变长度（如 ﬁ → fi、é → e），所以折叠串的每个 UTF-16 码元都记下它在原文里的来源区间，
 * 高亮在折叠串上算出后映射回原文，落在原字符上。
 */

/** 原文上的 `[start, end)`（UTF-16 下标）。 */
export interface TextRange {
	start: number;
	end: number;
}

/** 折叠后的文本：`starts[i]` / `ends[i]` 是折叠串第 i 个码元来自的原文字符区间。 */
export interface Folded {
	text: string;
	starts: number[];
	ends: number[];
}

const MARKS = /\p{M}/gu;

export function fold(s: string): Folded {
	let text = "";
	const starts: number[] = [];
	const ends: number[] = [];
	let i = 0;
	for (const ch of s) {
		// 逐个码点折叠：NFD 分解、去组合记号、转小写
		const f = ch.normalize("NFD").replace(MARKS, "").toLowerCase();
		const end = i + ch.length;
		// 只剩组合记号（分解型输入 e + U+0301）：并入前一个字符的来源区间，高亮不会把记号切在外面
		if (f.length === 0)
			for (let k = ends.length - 1; k >= 0 && ends[k] === i; k--) ends[k] = end;
		for (let k = 0; k < f.length; k++) {
			starts.push(i);
			ends.push(end);
		}
		text += f;
		i = end;
	}
	return { text, starts, ends };
}

/** 查询切成折叠后的词（去重、去空）。 */
export function queryTerms(q: string): string[] {
	const out: string[] = [];
	for (const w of fold(q).text.split(/\s+/)) {
		if (w && !out.includes(w)) out.push(w);
	}
	return out;
}

/** 升序合并相交 / 相邻的区间。 */
export function mergeRanges(ranges: TextRange[]): TextRange[] {
	const sorted = [...ranges].sort((a, b) => a.start - b.start || a.end - b.end);
	const out: TextRange[] = [];
	for (const r of sorted) {
		const last = out[out.length - 1];
		if (last && r.start <= last.end) last.end = Math.max(last.end, r.end);
		else out.push({ ...r });
	}
	return out;
}

/** 词在折叠串里的全部出现位置，映射成原文区间。 */
function occurrences(f: Folded, term: string): TextRange[] {
	const out: TextRange[] = [];
	let at = f.text.indexOf(term);
	while (at >= 0) {
		out.push({
			start: f.starts[at] ?? 0,
			end: f.ends[at + term.length - 1] ?? 0,
		});
		at = f.text.indexOf(term, at + term.length);
	}
	return out;
}

/** 词边界：开头，或前一个字符不是字母数字（汉字各自成词，也算边界）。 */
function atWordStart(text: string, at: number): boolean {
	if (at === 0) return true;
	const prev = text[at - 1] ?? "";
	return !/[\p{L}\p{N}]/u.test(prev) || /\p{Script=Han}/u.test(prev);
}

export interface FieldMatch {
	/** 匹配得分：越大越靠前；每个词取它在各字段里最好的位置 */
	score: number;
	/** 每个字段上的高亮区间（与 fields 一一对应，升序且不重叠） */
	ranges: TextRange[][];
}

/**
 * 在多个字段里匹配全部词：每个词至少在一个字段里出现，否则返回 null。没有词时视为全部命中、不带高亮。
 * 得分：词出现在第一个字段（主标题）开头 4、词首 3、中间 2；只出现在其余字段 1。
 */
export function matchFields(
	terms: readonly string[],
	fields: readonly Folded[],
): FieldMatch | null {
	const ranges: TextRange[][] = fields.map(() => []);
	let score = 0;
	for (const term of terms) {
		let best = 0;
		fields.forEach((f, i) => {
			const at = f.text.indexOf(term);
			if (at < 0) return;
			const s = i > 0 ? 1 : at === 0 ? 4 : atWordStart(f.text, at) ? 3 : 2;
			best = Math.max(best, s);
			ranges[i]?.push(...occurrences(f, term));
		});
		if (best === 0) return null;
		score += best;
	}
	return { score, ranges: ranges.map(mergeRanges) };
}

/** 把文本按高亮区间切成交替的片段，供渲染 `<mark>`。区间须升序且不重叠。 */
export function splitHighlights(
	text: string,
	ranges: readonly TextRange[],
): { text: string; hit: boolean }[] {
	const out: { text: string; hit: boolean }[] = [];
	let at = 0;
	for (const r of ranges) {
		const start = Math.max(at, Math.min(r.start, text.length));
		const end = Math.min(r.end, text.length);
		if (end <= start) continue;
		if (start > at) out.push({ text: text.slice(at, start), hit: false });
		out.push({ text: text.slice(start, end), hit: true });
		at = end;
	}
	if (at < text.length) out.push({ text: text.slice(at), hit: false });
	return out;
}
