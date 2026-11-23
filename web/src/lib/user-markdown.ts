/**
 * 用户消息的轻量 Markdown：不改写原文，只给原文的每一段标上样式。
 * 所有片段按顺序拼起来就是原文（一个字符都不少、不多、不换），用户能核对自己输入的是什么，选中复制得到的也是原文；
 * 语法记号（`#`、`**`、反引号、`>`、列表符号、围栏行、链接的括号与地址）照常显示，只是变淡。
 *
 * 只认常见的几种写法，认错了也只是样式不对、字符不会丢：
 * - 行级：围栏代码块（``` / ~~~，未闭合时延续到末尾）、ATX 标题、分隔线、引用、无序 / 有序列表；
 * - 行内：反斜杠转义、行内代码、粗体、删除线、斜体、`[文字](地址)`（含图片的 `!`）、裸 http(s) 地址。
 */

export interface UserMdStyle {
	/** 语法记号：淡色 */
	marker?: true;
	strong?: true;
	em?: true;
	del?: true;
	/** 行内代码 */
	code?: true;
	/** 围栏代码块里的行（含围栏行本身） */
	block?: true;
	/** 标题正文 */
	heading?: true;
	/** 引用正文 */
	quote?: true;
	link?: true;
}

export interface UserMdSegment {
	text: string;
	style: UserMdStyle;
	/** 可点的链接地址（只给 http(s) / mailto） */
	href?: string;
}

const FENCE_OPEN = /^ {0,3}(`{3,}|~{3,})/;
const FENCE_CLOSE = /^ {0,3}(`{3,}|~{3,})[ \t]*$/;
const HR = /^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/;
const HEADING = /^( {0,3})(#{1,6}(?:[ \t]+|$))(.*)$/;
const QUOTE = /^( {0,3})((?:>[ \t]?)+)(.*)$/;
const LIST = /^([ \t]*)((?:[-*+]|\d{1,9}[.)])[ \t]+)(.*)$/;
/** 不用后行断言（老 WebKit 不支持）：下划线的词内边界在匹配后另判。 */
const INLINE =
	/(?<esc>\\[!-/:-@[-`{-~])|(?<tick>`+)(?<code>.+?)\k<tick>(?!`)|(?<s>\*\*|__)(?<strong>\S(?:.*?\S)?)\k<s>|~~(?<del>\S(?:.*?\S)?)~~|(?<e>[*_])(?<em>[^\s*_](?:.*?[^\s*_])?)\k<e>|(?<img>!?)\[(?<label>[^\]\n]+)\]\((?<href>[^()\s]+)\)|(?<url>https?:\/\/[^\s<>()[\]]*[^\s<>()[\].,;:!?'"，。；：！？、）】」』])/g;
const WORD = /[A-Za-z0-9]/;
const SAFE_HREF = /^(?:https?:|mailto:)/i;

export function userMarkdown(text: string): UserMdSegment[] {
	const out: UserMdSegment[] = [];
	const push = (t: string, style: UserMdStyle, href?: string) => {
		if (t) out.push(href ? { text: t, style, href } : { text: t, style });
	};
	let fence: string | null = null;
	text.split("\n").forEach((line, i) => {
		if (i > 0) push("\n", fence !== null ? { block: true } : {});
		if (fence !== null) {
			const close = FENCE_CLOSE.exec(line)?.[1];
			if (close && close[0] === fence[0] && close.length >= fence.length) {
				fence = null;
				push(line, { block: true, marker: true });
			} else push(line, { block: true });
			return;
		}
		const open = FENCE_OPEN.exec(line)?.[1];
		// 反引号围栏的信息串里不能再有反引号（否则是行内代码）
		if (
			open &&
			!(
				open[0] === "`" &&
				line.slice(line.indexOf(open) + open.length).includes("`")
			)
		) {
			fence = open;
			push(line, { block: true, marker: true });
			return;
		}
		if (HR.test(line)) {
			push(line, { marker: true });
			return;
		}
		const block =
			lineBlock(HEADING, line, { heading: true }) ??
			lineBlock(QUOTE, line, { quote: true }) ??
			lineBlock(LIST, line, {});
		if (block) {
			push(block.lead, {});
			push(block.mark, { marker: true });
			inline(block.rest, block.style, push);
		} else inline(line, {}, push);
	});
	return out;
}

function lineBlock(
	re: RegExp,
	line: string,
	style: UserMdStyle,
): { lead: string; mark: string; rest: string; style: UserMdStyle } | null {
	const m = re.exec(line);
	if (!m) return null;
	return { lead: m[1] ?? "", mark: m[2] ?? "", rest: m[3] ?? "", style };
}

function inline(
	s: string,
	base: UserMdStyle,
	push: (t: string, style: UserMdStyle, href?: string) => void,
): void {
	// 递归调用各用各的正则状态
	const re = new RegExp(INLINE.source, "g");
	const marker: UserMdStyle = { ...base, marker: true };
	let plain = 0;
	for (let m = re.exec(s); m; m = re.exec(s)) {
		const g = m.groups ?? {};
		const at = m.index;
		const end = at + m[0].length;
		// 下划线强调只在词边界上成立：snake_case 里的下划线照常是正文
		if (
			(g.s === "__" || g.e === "_") &&
			(WORD.test(s[at - 1] ?? "") || WORD.test(s[end] ?? ""))
		) {
			re.lastIndex = at + 1;
			continue;
		}
		push(s.slice(plain, at), base);
		plain = end;
		if (g.esc !== undefined) {
			push("\\", marker);
			push(g.esc.slice(1), base);
		} else if (g.tick !== undefined && g.code !== undefined) {
			const code: UserMdStyle = { ...base, code: true };
			push(g.tick, { ...code, marker: true });
			push(g.code, code);
			push(g.tick, { ...code, marker: true });
		} else if (g.s !== undefined && g.strong !== undefined) {
			push(g.s, marker);
			inline(g.strong, { ...base, strong: true }, push);
			push(g.s, marker);
		} else if (g.del !== undefined) {
			push("~~", marker);
			inline(g.del, { ...base, del: true }, push);
			push("~~", marker);
		} else if (g.e !== undefined && g.em !== undefined) {
			push(g.e, marker);
			inline(g.em, { ...base, em: true }, push);
			push(g.e, marker);
		} else if (g.label !== undefined && g.href !== undefined) {
			const href = SAFE_HREF.test(g.href) ? g.href : undefined;
			push(`${g.img ?? ""}[`, marker);
			inline(g.label, { ...base, link: true }, (t, st, h) =>
				push(t, st, h ?? href),
			);
			push(`](${g.href})`, marker);
		} else if (g.url !== undefined) {
			push(g.url, { ...base, link: true }, g.url);
		}
	}
	push(s.slice(plain), base);
}
